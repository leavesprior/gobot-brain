// Copyright 2024 The GoBot Brain Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package scheduler provides a proactive timer system with escalation levels,
// implemented as a GoBot v2 Device driver. Tasks run on configurable intervals
// and automatically escalate through severity levels on consecutive failures.
package scheduler

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// ---------------------------------------------------------------------------
// Escalation levels
// ---------------------------------------------------------------------------

// Level represents an escalation severity tier.
type Level int

const (
	Silent   Level = iota // L1: log only
	Notify                // L2: webhook/callback
	Urgent                // L3: repeated notify
	Escalate              // L4: escalation chain
	Critical              // L5: all channels
)

// String returns a human-readable label for the escalation level.
func (l Level) String() string {
	switch l {
	case Silent:
		return "silent"
	case Notify:
		return "notify"
	case Urgent:
		return "urgent"
	case Escalate:
		return "escalate"
	case Critical:
		return "critical"
	default:
		return fmt.Sprintf("level(%d)", int(l))
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// Event names published by the scheduler driver.
const (
	EventTick       = "tick"
	EventEscalation = "escalation"
	EventError      = "error"
	EventRecovered  = "recovered"
)

// ---------------------------------------------------------------------------
// Task definition
// ---------------------------------------------------------------------------

// Task defines a periodic unit of work managed by the scheduler.
type Task struct {
	// Name uniquely identifies the task within the scheduler.
	Name string
	// Interval is the tick period between successive executions.
	Interval time.Duration
	// Level is the starting (and recovery-reset) escalation level.
	Level Level
	// Fn is the function executed on each tick. A non-nil error is treated
	// as a failure and contributes to the escalation counter.
	Fn func() error
}

// ---------------------------------------------------------------------------
// Internal task state
// ---------------------------------------------------------------------------

// taskState tracks runtime state for an active task.
type taskState struct {
	Task
	timer        *time.Ticker
	done         chan struct{}
	paused       bool
	failures     int
	currentLevel Level
	lastRun      time.Time
	lastErr      error
}

// ---------------------------------------------------------------------------
// Persistence records
// ---------------------------------------------------------------------------

// taskRecord is the JSON-serializable form persisted to the memory adaptor
// under the "scheduler" namespace.
type taskRecord struct {
	Name         string    `json:"name"`
	Interval     string    `json:"interval"`
	BaseLevel    int       `json:"base_level"`
	CurrentLevel int       `json:"current_level"`
	Failures     int       `json:"failures"`
	Paused       bool      `json:"paused"`
	LastRun      time.Time `json:"last_run"`
	LastErr      string    `json:"last_error,omitempty"`
}

const defaultEscalationThreshold = 3
const memoryNamespace = "scheduler"

// ---------------------------------------------------------------------------
// Driver options
// ---------------------------------------------------------------------------

// Option configures the Driver.
type Option func(*Driver)

// WithEscalationThreshold sets the number of consecutive failures required
// to escalate one level. Escalation occurs at every multiple of the threshold
// (e.g. at failures 3, 6, 9 with a threshold of 3), capped at Critical.
// The default is 3.
func WithEscalationThreshold(n int) Option {
	return func(d *Driver) {
		if n > 0 {
			d.threshold = n
		}
	}
}

// WithTask adds a task to the driver at construction time. The task begins
// ticking when Start is called.
func WithTask(t Task) Option {
	return func(d *Driver) {
		d.pending = append(d.pending, t)
	}
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver is a GoBot v2 Device that runs periodic tasks with automatic
// escalation on consecutive failures. Execution history and escalation state
// are persisted to the memory adaptor under the "scheduler" namespace.
type Driver struct {
	name    string
	adaptor *memory.Adaptor

	mu        sync.RWMutex
	tasks     map[string]*taskState
	threshold int
	running   bool
	wg        sync.WaitGroup
	pending   []Task // tasks queued before Start

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a new scheduler Driver bound to the given memory adaptor.
func NewDriver(a *memory.Adaptor, opts ...Option) *Driver {
	d := &Driver{
		name:      "scheduler",
		adaptor:   a,
		tasks:     make(map[string]*taskState),
		threshold: defaultEscalationThreshold,
		Eventer:   gobot.NewEventer(),
		Commander: gobot.NewCommander(),
	}

	for _, opt := range opts {
		opt(d)
	}

	// Register events.
	d.AddEvent(EventTick)
	d.AddEvent(EventEscalation)
	d.AddEvent(EventError)
	d.AddEvent(EventRecovered)

	// Register commands.
	d.AddCommand("add", func(params map[string]interface{}) interface{} {
		name, _ := params["name"].(string)
		if name == "" {
			return fmt.Errorf("add: name required")
		}
		// Commands cannot carry func values; callers must use Driver.Add().
		return fmt.Sprintf("use Driver.Add() to add task %q with a function", name)
	})
	d.AddCommand("remove", func(params map[string]interface{}) interface{} {
		name, _ := params["name"].(string)
		if name == "" {
			return fmt.Errorf("remove: name required")
		}
		d.Remove(name)
		return nil
	})
	d.AddCommand("pause", func(params map[string]interface{}) interface{} {
		name, _ := params["name"].(string)
		if name == "" {
			return fmt.Errorf("pause: name required")
		}
		d.Pause(name)
		return nil
	})
	d.AddCommand("resume", func(params map[string]interface{}) interface{} {
		name, _ := params["name"].(string)
		if name == "" {
			return fmt.Errorf("resume: name required")
		}
		d.Resume(name)
		return nil
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
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start launches goroutines for all registered tasks. Tasks added via
// WithTask or Add (before Start) begin ticking immediately.
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Absorb any tasks queued via WithTask before Start was called.
	for _, t := range d.pending {
		if _, exists := d.tasks[t.Name]; !exists {
			d.tasks[t.Name] = &taskState{
				Task:         t,
				currentLevel: t.Level,
				done:         make(chan struct{}),
			}
		}
	}
	d.pending = nil

	d.running = true

	for _, ts := range d.tasks {
		d.startTask(ts)
	}
	return nil
}

// Halt stops all task goroutines, waits for them to finish, and clears the
// task map so a subsequent Start begins clean.
func (d *Driver) Halt() error {
	d.mu.Lock()
	for _, ts := range d.tasks {
		close(ts.done)
	}
	d.running = false
	d.mu.Unlock()

	// Wait outside the lock so goroutines can finish.
	d.wg.Wait()

	// Clear the map so a subsequent Start is clean.
	d.mu.Lock()
	d.tasks = make(map[string]*taskState)
	d.mu.Unlock()

	return nil
}

// Compile-time interface check.
var _ gobot.Device = (*Driver)(nil)

// ---------------------------------------------------------------------------
// Public task management
// ---------------------------------------------------------------------------

// Add registers a new task. If the driver is already running the task starts
// ticking immediately. If a task with the same name already exists, it is
// stopped and replaced.
func (d *Driver) Add(task Task) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// If a task with this name already exists, stop it first.
	if existing, ok := d.tasks[task.Name]; ok {
		close(existing.done)
		// The old goroutine will observe the closed channel and exit.
	}

	ts := &taskState{
		Task:         task,
		currentLevel: task.Level,
		done:         make(chan struct{}),
	}
	d.tasks[task.Name] = ts

	if d.running {
		d.startTask(ts)
	}
}

// Remove stops and unregisters the named task.
func (d *Driver) Remove(name string) {
	d.mu.Lock()
	ts, ok := d.tasks[name]
	if ok {
		close(ts.done)
		delete(d.tasks, name)
	}
	d.mu.Unlock()
}

// Pause temporarily suspends execution of the named task. The goroutine
// remains alive but skips Fn invocations until Resume is called. Escalation
// state is preserved.
func (d *Driver) Pause(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ts, ok := d.tasks[name]; ok {
		ts.paused = true
	}
}

// Resume unpauses a previously paused task, allowing it to execute on its
// next tick.
func (d *Driver) Resume(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ts, ok := d.tasks[name]; ok {
		ts.paused = false
	}
}

// Tasks returns the names of all registered tasks.
func (d *Driver) Tasks() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, 0, len(d.tasks))
	for name := range d.tasks {
		names = append(names, name)
	}
	return names
}

// ---------------------------------------------------------------------------
// Core tick loop
// ---------------------------------------------------------------------------

// startTask launches the ticker goroutine for ts. Must be called with d.mu
// held (write lock).
func (d *Driver) startTask(ts *taskState) {
	ts.timer = time.NewTicker(ts.Interval)
	d.wg.Add(1)

	go func() {
		defer d.wg.Done()
		defer ts.timer.Stop()

		for {
			select {
			case <-ts.done:
				return
			case <-ts.timer.C:
				d.runOnce(ts)
			}
		}
	}()
}

// runOnce executes a single tick of the task, handling errors, escalation,
// recovery, event publishing, and persistence.
func (d *Driver) runOnce(ts *taskState) {
	d.mu.RLock()
	paused := ts.paused
	d.mu.RUnlock()

	if paused {
		return
	}

	err := ts.Fn()
	now := time.Now()

	d.mu.Lock()
	ts.lastRun = now
	ts.lastErr = err

	if err != nil {
		ts.failures++

		// Escalate one level at every multiple of the threshold, capped
		// at Critical. For threshold=3 this means escalation at failures
		// 3, 6, 9, ... up to the Critical ceiling.
		escalated := false
		if ts.failures%d.threshold == 0 && ts.currentLevel < Critical {
			ts.currentLevel++
			escalated = true
		}
		level := ts.currentLevel
		failures := ts.failures
		d.mu.Unlock()

		d.Publish(EventError, ts.Name+": "+err.Error())
		if escalated {
			d.Publish(EventEscalation, fmt.Sprintf(
				"%s escalated to %s (failures: %d)", ts.Name, level, failures))
		}
	} else {
		// Success: reset failure count and restore base escalation level.
		hadFailures := ts.failures > 0
		ts.failures = 0
		ts.currentLevel = ts.Level
		d.mu.Unlock()

		if hadFailures {
			d.Publish(EventRecovered, ts.Name)
		}
	}

	d.Publish(EventTick, ts.Name)
	d.persist(ts)
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// persist saves the task state to the memory adaptor. Errors are silently
// discarded (best-effort).
func (d *Driver) persist(ts *taskState) {
	d.mu.RLock()
	rec := taskRecord{
		Name:         ts.Name,
		Interval:     ts.Interval.String(),
		BaseLevel:    int(ts.Level),
		CurrentLevel: int(ts.currentLevel),
		Failures:     ts.failures,
		Paused:       ts.paused,
		LastRun:      ts.lastRun,
	}
	if ts.lastErr != nil {
		rec.LastErr = ts.lastErr.Error()
	}
	d.mu.RUnlock()

	// Best-effort persistence; ignore errors.
	_ = d.adaptor.Store(memoryNamespace, ts.Name, rec)
}

// Snapshot returns the persisted record for the named task, if any.
// This is primarily useful for testing and introspection.
func (d *Driver) Snapshot(name string) (json.RawMessage, error) {
	val, err := d.adaptor.Retrieve(memoryNamespace, name)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}
	return json.RawMessage(raw), nil
}
