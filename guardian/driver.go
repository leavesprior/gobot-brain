// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

// Package guardian provides security monitoring with policy enforcement for
// GoBot robots. It evaluates actions against configurable security policies
// before allowing them to execute, maintains an audit log, and can block
// dangerous operations.
package guardian

import (
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Memory adaptor namespace used for persisting audit data.
const namespace = "guardian"

// Event names published by the guardian driver.
const (
	EventEvaluated = "evaluated"
	EventBlocked   = "blocked"
	EventViolation = "violation"
	EventError     = "error"
)

// Default maximum number of audit log entries retained.
const defaultMaxAuditLog = 1000

// ---------------------------------------------------------------------------
// Severity
// ---------------------------------------------------------------------------

// Severity classifies the seriousness of a policy decision.
type Severity int

const (
	Info     Severity = iota
	Warning
	Critical
	Blocked
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Critical:
		return "critical"
	case Blocked:
		return "blocked"
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Policy, Action, Decision
// ---------------------------------------------------------------------------

// Policy defines a security rule that evaluates actions and returns decisions.
type Policy struct {
	Name        string
	Description string
	Severity    Severity
	Check       func(action Action) Decision
}

// Action represents an operation being evaluated by the guardian.
type Action struct {
	Name       string
	Source     string // who/what initiated the action
	Parameters map[string]interface{}
	Timestamp  time.Time
}

// Decision is the result of evaluating an action against a policy.
type Decision struct {
	Allowed  bool
	Reason   string
	Severity Severity
}

// ---------------------------------------------------------------------------
// AuditEntry
// ---------------------------------------------------------------------------

// AuditEntry records the outcome of evaluating an action.
type AuditEntry struct {
	Action    Action
	Decision  Decision
	Timestamp time.Time
}

// ---------------------------------------------------------------------------
// Option
// ---------------------------------------------------------------------------

// Option configures the guardian driver.
type Option func(*Driver)

// WithPolicy adds a security policy to the driver at construction time.
func WithPolicy(p Policy) Option {
	return func(d *Driver) {
		d.policies = append(d.policies, p)
	}
}

// WithMaxAuditLog sets the maximum number of audit log entries retained.
// Default is 1000.
func WithMaxAuditLog(n int) Option {
	return func(d *Driver) {
		if n > 0 {
			d.maxAuditLog = n
		}
	}
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver is a GoBot v2 Device that provides security monitoring: policy
// enforcement, action evaluation, audit logging, and event publishing.
type Driver struct {
	name    string
	adaptor *memory.Adaptor

	mu          sync.RWMutex
	policies    []Policy
	auditLog    []AuditEntry
	maxAuditLog int

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a guardian driver attached to the given memory adaptor.
func NewDriver(a *memory.Adaptor, opts ...Option) *Driver {
	d := &Driver{
		name:        "guardian",
		adaptor:     a,
		maxAuditLog: defaultMaxAuditLog,
		Eventer:     gobot.NewEventer(),
		Commander:   gobot.NewCommander(),
	}
	for _, opt := range opts {
		opt(d)
	}

	d.AddEvent(EventEvaluated)
	d.AddEvent(EventBlocked)
	d.AddEvent(EventViolation)
	d.AddEvent(EventError)

	d.AddCommand("evaluate", func(params map[string]interface{}) interface{} {
		name, _ := params["name"].(string)
		source, _ := params["source"].(string)
		action := Action{
			Name:       name,
			Source:     source,
			Parameters: params,
			Timestamp:  time.Now(),
		}
		return d.Evaluate(action)
	})
	d.AddCommand("policies", func(params map[string]interface{}) interface{} {
		return d.Policies()
	})
	d.AddCommand("audit", func(params map[string]interface{}) interface{} {
		return d.AuditLog()
	})

	return d
}

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start initializes the guardian driver.
func (d *Driver) Start() error {
	return nil
}

// Halt stops the guardian driver and persists the audit log.
func (d *Driver) Halt() error {
	d.persistAuditLog()
	return nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// AddPolicy registers a new security policy.
func (d *Driver) AddPolicy(p Policy) {
	d.mu.Lock()
	d.policies = append(d.policies, p)
	d.mu.Unlock()
}

// RemovePolicy removes the first policy with the given name.
func (d *Driver) RemovePolicy(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, p := range d.policies {
		if p.Name == name {
			d.policies = append(d.policies[:i], d.policies[i+1:]...)
			return
		}
	}
}

// Evaluate checks all policies against the action and returns the most severe
// Decision. If any policy returns Blocked severity, the decision is not
// allowed. If no policies are registered, the action is allowed with Info
// severity.
func (d *Driver) Evaluate(action Action) Decision {
	if action.Timestamp.IsZero() {
		action.Timestamp = time.Now()
	}

	d.mu.RLock()
	policies := make([]Policy, len(d.policies))
	copy(policies, d.policies)
	d.mu.RUnlock()

	// No policies means allowed.
	if len(policies) == 0 {
		dec := Decision{
			Allowed:  true,
			Reason:   "no policies registered",
			Severity: Info,
		}
		d.recordAudit(action, dec)
		d.Publish(EventEvaluated, dec)
		return dec
	}

	// Evaluate all policies and keep the worst decision.
	worst := Decision{
		Allowed:  true,
		Reason:   "all policies passed",
		Severity: Info,
	}

	for _, p := range policies {
		if p.Check == nil {
			continue
		}
		result := p.Check(action)
		if result.Severity > worst.Severity {
			worst = result
		}
	}

	// If the worst severity is Blocked, the action is not allowed.
	if worst.Severity == Blocked {
		worst.Allowed = false
	}

	d.recordAudit(action, worst)

	// Publish events based on outcome.
	d.Publish(EventEvaluated, worst)
	if !worst.Allowed {
		d.Publish(EventBlocked, worst)
	}
	if worst.Severity >= Critical {
		d.Publish(EventViolation, worst)
	}

	return worst
}

// Guard is a convenience method that evaluates the action, executes fn if
// allowed, and logs everything to the audit trail. If blocked, it returns
// an error without executing fn.
func (d *Driver) Guard(action Action, fn func() error) error {
	dec := d.Evaluate(action)
	if !dec.Allowed {
		return fmt.Errorf("guardian: action %q blocked: %s", action.Name, dec.Reason)
	}
	if err := fn(); err != nil {
		d.Publish(EventError, fmt.Sprintf("action %q failed: %v", action.Name, err))
		return err
	}
	return nil
}

// AuditLog returns a copy of the recent audit entries.
func (d *Driver) AuditLog() []AuditEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	out := make([]AuditEntry, len(d.auditLog))
	copy(out, d.auditLog)
	return out
}

// Policies returns a list of registered policy names.
func (d *Driver) Policies() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	names := make([]string, len(d.policies))
	for i, p := range d.policies {
		names[i] = p.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

// recordAudit appends an entry to the audit log, truncating if needed, and
// persists to the memory adaptor.
func (d *Driver) recordAudit(action Action, dec Decision) {
	entry := AuditEntry{
		Action:    action,
		Decision:  dec,
		Timestamp: time.Now(),
	}

	d.mu.Lock()
	d.auditLog = append(d.auditLog, entry)
	if len(d.auditLog) > d.maxAuditLog {
		// Drop the oldest entries to stay within the limit.
		excess := len(d.auditLog) - d.maxAuditLog
		d.auditLog = d.auditLog[excess:]
	}
	d.mu.Unlock()

	d.persistAuditLog()
}

// persistAuditLog writes the audit log to the memory adaptor (best-effort).
func (d *Driver) persistAuditLog() {
	d.mu.RLock()
	logCopy := make([]AuditEntry, len(d.auditLog))
	copy(logCopy, d.auditLog)
	d.mu.RUnlock()

	if d.adaptor != nil {
		_ = d.adaptor.Store(namespace, "audit_log", logCopy)
	}
}
