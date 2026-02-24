// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

// Package watchdog provides a GoBot v2 device driver for fleet health
// monitoring with configurable checks, consecutive-failure alerting,
// and memory-backed persistence of check history and alert logs.
package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Memory adaptor namespace used for persisting check history and alerts.
const namespace = "watchdog"

// Events published by the driver.
const (
	Healthy   = "healthy"
	Unhealthy = "unhealthy"
	Recovered = "recovered"
	Error     = "error"
)

// Default values.
const (
	defaultAlertAfter = 2
	defaultTimeout    = 30 * time.Second
)

// ---------------------------------------------------------------------------
// Check definition & status
// ---------------------------------------------------------------------------

// Check defines a single health check to run periodically.
type Check struct {
	// Name uniquely identifies this check.
	Name string

	// Fn is the health check function. A nil return means healthy.
	Fn func() error

	// Interval is the time between successive check executions.
	Interval time.Duration

	// Timeout is the maximum duration a single check execution may take.
	// If zero, defaults to 30 seconds.
	Timeout time.Duration
}

// CheckStatus is the observable state of a single health check.
type CheckStatus struct {
	Name        string    `json:"name"`
	Healthy     bool      `json:"healthy"`
	LastErr     string    `json:"last_err,omitempty"`
	LastCheck   time.Time `json:"last_check"`
	Consecutive int       `json:"consecutive_failures"`
}

// AlertFunc is called when a check crosses the alert threshold or recovers.
// On recovery, err is nil and consecutive is 0.
type AlertFunc func(name string, err error, consecutive int)

// ---------------------------------------------------------------------------
// Internal per-check state
// ---------------------------------------------------------------------------

type checkState struct {
	check  Check
	status CheckStatus
	cancel context.CancelFunc
}

// ---------------------------------------------------------------------------
// Alert log record (persisted to memory)
// ---------------------------------------------------------------------------

type alertRecord struct {
	Name        string `json:"name"`
	Err         string `json:"err,omitempty"`
	Consecutive int    `json:"consecutive"`
	Recovered   bool   `json:"recovered"`
	Time        string `json:"time"`
}

// ---------------------------------------------------------------------------
// Driver option
// ---------------------------------------------------------------------------

// DriverOption configures the watchdog driver.
type DriverOption func(*Driver)

// WithAlertAfter sets the number of consecutive failures required before
// alerting. The default is 2.
func WithAlertAfter(n int) DriverOption {
	return func(d *Driver) {
		if n > 0 {
			d.alertAfter = n
		}
	}
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver is a GoBot v2 Device that runs periodic health checks, publishes
// status events, fires alert callbacks on consecutive failures, and persists
// check history and alert logs to the memory adaptor under the "watchdog"
// namespace.
type Driver struct {
	name       string
	connection gobot.Connection
	checks     map[string]*checkState
	alertFn    AlertFunc
	alertAfter int // consecutive failures before alert (default 2)
	mu         sync.Mutex
	done       chan struct{}
	running    bool
	adaptor    *memory.Adaptor

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a new watchdog driver backed by the given memory adaptor.
// The alertFn is called when a check exceeds the consecutive failure threshold
// or recovers (in which case err is nil). Pass nil for alertFn if no callback
// is needed.
//
// Options may be supplied to configure the driver:
//
//	NewDriver(adaptor, alertFn, WithAlertAfter(3))
func NewDriver(a *memory.Adaptor, alertFn AlertFunc, opts ...DriverOption) *Driver {
	d := &Driver{
		name:       "watchdog",
		connection: a,
		adaptor:    a,
		checks:     make(map[string]*checkState),
		alertFn:    alertFn,
		alertAfter: defaultAlertAfter,
		Eventer:    gobot.NewEventer(),
		Commander:  gobot.NewCommander(),
	}

	for _, opt := range opts {
		opt(d)
	}

	d.AddEvent(Healthy)
	d.AddEvent(Unhealthy)
	d.AddEvent(Recovered)
	d.AddEvent(Error)

	d.AddCommand("status", func(params map[string]interface{}) interface{} {
		return d.Status()
	})

	d.AddCommand("healthy", func(params map[string]interface{}) interface{} {
		return d.Healthy()
	})

	return d
}

// ---------------------------------------------------------------------------
// gobot.Device interface
// ---------------------------------------------------------------------------

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection { return d.connection }

// Start launches a monitoring goroutine for every registered check.
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return fmt.Errorf("watchdog: already running")
	}

	d.done = make(chan struct{})
	d.running = true

	for _, cs := range d.checks {
		d.startCheckLocked(cs)
	}
	return nil
}

// Halt gracefully stops all monitoring goroutines.
func (d *Driver) Halt() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return nil
	}

	close(d.done)

	for _, cs := range d.checks {
		if cs.cancel != nil {
			cs.cancel()
			cs.cancel = nil
		}
	}

	d.running = false
	return nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// AddCheck registers a new health check and starts it immediately if the
// driver is already running. If a check with the same name exists it is
// stopped and replaced.
func (d *Driver) AddCheck(check Check) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Stop any existing check with the same name.
	if old, ok := d.checks[check.Name]; ok && old.cancel != nil {
		old.cancel()
	}

	cs := &checkState{
		check:  check,
		status: CheckStatus{Name: check.Name, Healthy: true},
	}
	d.checks[check.Name] = cs

	if d.running {
		d.startCheckLocked(cs)
	}
}

// RemoveCheck stops and removes the check with the given name.
func (d *Driver) RemoveCheck(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if cs, ok := d.checks[name]; ok {
		if cs.cancel != nil {
			cs.cancel()
		}
		delete(d.checks, name)
	}
}

// Status returns a snapshot of every registered check's status.
func (d *Driver) Status() map[string]CheckStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make(map[string]CheckStatus, len(d.checks))
	for name, cs := range d.checks {
		out[name] = cs.status
	}
	return out
}

// Healthy returns true if every registered check is currently passing.
func (d *Driver) Healthy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, cs := range d.checks {
		if !cs.status.Healthy {
			return false
		}
	}
	return true
}

// SetAlertAfter sets the consecutive failure threshold at runtime.
func (d *Driver) SetAlertAfter(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n > 0 {
		d.alertAfter = n
	}
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

// startCheckLocked launches the goroutine for a single check.
// Caller must hold d.mu.
func (d *Driver) startCheckLocked(cs *checkState) {
	ctx, cancel := context.WithCancel(context.Background())
	cs.cancel = cancel

	timeout := cs.check.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	go d.runCheck(ctx, cs, timeout)
}

// runCheck is the per-check goroutine loop.
func (d *Driver) runCheck(ctx context.Context, cs *checkState, timeout time.Duration) {
	ticker := time.NewTicker(cs.check.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-ticker.C:
			err := d.executeCheck(ctx, cs, timeout)
			d.recordResult(cs, err)
		}
	}
}

// executeCheck runs the check function with a timeout.
func (d *Driver) executeCheck(ctx context.Context, cs *checkState, timeout time.Duration) error {
	ch := make(chan error, 1)
	go func() {
		if cs.check.Fn != nil {
			ch <- cs.check.Fn()
		} else {
			ch <- nil
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-ch:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("check %q timed out after %s", cs.check.Name, timeout)
	}
}

// recordResult updates the check state, fires events, calls alertFn, and
// persists to the memory adaptor.
func (d *Driver) recordResult(cs *checkState, err error) {
	d.mu.Lock()

	now := time.Now()
	name := cs.check.Name
	wasHealthy := cs.status.Healthy

	if err == nil {
		recovered := !wasHealthy && cs.status.Consecutive > 0
		prevConsecutive := cs.status.Consecutive

		cs.status.Healthy = true
		cs.status.LastErr = ""
		cs.status.LastCheck = now
		cs.status.Consecutive = 0
		statusCopy := cs.status
		alertFn := d.alertFn
		d.mu.Unlock()

		d.Publish(Healthy, name)

		if recovered {
			d.Publish(Recovered, name)
			if alertFn != nil {
				alertFn(name, nil, 0)
			}
			d.persistAlert(name, nil, prevConsecutive, true)
		}

		d.persistStatus(name, statusCopy)
	} else {
		cs.status.Healthy = false
		cs.status.LastErr = err.Error()
		cs.status.LastCheck = now
		cs.status.Consecutive++
		consecutive := cs.status.Consecutive
		alertAfter := d.alertAfter
		statusCopy := cs.status
		alertFn := d.alertFn
		d.mu.Unlock()

		// Fire alert and unhealthy event once the threshold is reached,
		// then on every subsequent failure.
		if consecutive >= alertAfter {
			d.Publish(Unhealthy, name)
			if alertFn != nil {
				alertFn(name, err, consecutive)
			}
			d.persistAlert(name, err, consecutive, false)
		}

		d.persistStatus(name, statusCopy)
	}
}

// persistStatus writes the check status to the memory adaptor.
func (d *Driver) persistStatus(name string, s CheckStatus) {
	if d.adaptor == nil {
		return
	}
	if err := d.adaptor.Store(namespace, "status:"+name, s); err != nil {
		d.Publish(Error, fmt.Errorf("watchdog: persist status %q: %w", name, err))
	}
}

// persistAlert writes an alert record to the memory adaptor.
func (d *Driver) persistAlert(name string, err error, consecutive int, recovered bool) {
	if d.adaptor == nil {
		return
	}

	rec := alertRecord{
		Name:        name,
		Consecutive: consecutive,
		Recovered:   recovered,
		Time:        time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		rec.Err = err.Error()
	}

	// Store latest alert per check name.
	_ = d.adaptor.Store(namespace, "alert:"+name, rec)

	// Store a global latest-alert record for quick retrieval.
	raw, _ := json.Marshal(rec)
	_ = d.adaptor.Store(namespace, "alert_latest", json.RawMessage(raw))
}
