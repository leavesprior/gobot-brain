// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

package guardian

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time check: Driver must satisfy gobot.Device.
var _ gobot.Device = (*Driver)(nil)

func newTestDriver(opts ...Option) (*Driver, *memory.Adaptor) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	d := NewDriver(a, opts...)
	_ = d.Start()
	return d, a
}

func blockingPolicy(name string) Policy {
	return Policy{
		Name:        name,
		Description: "blocks everything",
		Severity:    Blocked,
		Check: func(action Action) Decision {
			return Decision{
				Allowed:  false,
				Reason:   "blocked by " + name,
				Severity: Blocked,
			}
		},
	}
}

func allowingPolicy(name string, sev Severity) Policy {
	return Policy{
		Name:        name,
		Description: "allows with severity",
		Severity:    sev,
		Check: func(action Action) Decision {
			return Decision{
				Allowed:  true,
				Reason:   "allowed by " + name,
				Severity: sev,
			}
		},
	}
}

func testAction(name string) Action {
	return Action{
		Name:       name,
		Source:     "test",
		Parameters: map[string]interface{}{"key": "value"},
		Timestamp:  time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNameSetNameConnection(t *testing.T) {
	d, a := newTestDriver()
	defer d.Halt()

	if d.Name() != "guardian" {
		t.Fatalf("expected name 'guardian', got %q", d.Name())
	}
	d.SetName("custom-guardian")
	if d.Name() != "custom-guardian" {
		t.Fatalf("expected name 'custom-guardian', got %q", d.Name())
	}
	if d.Connection() != a {
		t.Fatal("expected Connection() to return the memory adaptor")
	}
}

func TestEvaluateNoPolicies(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	dec := d.Evaluate(testAction("do-something"))
	if !dec.Allowed {
		t.Fatal("expected allowed with no policies")
	}
	if dec.Severity != Info {
		t.Fatalf("expected Info severity, got %v", dec.Severity)
	}
}

func TestEvaluateBlockingPolicy(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(blockingPolicy("deny-all")))
	defer d.Halt()

	dec := d.Evaluate(testAction("rm -rf /"))
	if dec.Allowed {
		t.Fatal("expected action to be blocked")
	}
	if dec.Severity != Blocked {
		t.Fatalf("expected Blocked severity, got %v", dec.Severity)
	}
	if dec.Reason != "blocked by deny-all" {
		t.Fatalf("unexpected reason: %q", dec.Reason)
	}
}

func TestEvaluatePicksWorstSeverity(t *testing.T) {
	d, _ := newTestDriver(
		WithPolicy(allowingPolicy("info-policy", Info)),
		WithPolicy(allowingPolicy("warning-policy", Warning)),
		WithPolicy(allowingPolicy("critical-policy", Critical)),
	)
	defer d.Halt()

	dec := d.Evaluate(testAction("something"))
	// Critical is the worst non-Blocked severity; action is still allowed.
	if !dec.Allowed {
		t.Fatal("expected allowed (no Blocked policy)")
	}
	if dec.Severity != Critical {
		t.Fatalf("expected Critical severity, got %v", dec.Severity)
	}
}

func TestEvaluateWorstSeverityBlocked(t *testing.T) {
	d, _ := newTestDriver(
		WithPolicy(allowingPolicy("info-policy", Info)),
		WithPolicy(blockingPolicy("block-policy")),
		WithPolicy(allowingPolicy("warning-policy", Warning)),
	)
	defer d.Halt()

	dec := d.Evaluate(testAction("something"))
	if dec.Allowed {
		t.Fatal("expected blocked (Blocked severity in mix)")
	}
	if dec.Severity != Blocked {
		t.Fatalf("expected Blocked severity, got %v", dec.Severity)
	}
}

func TestGuardExecutesWhenAllowed(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("allow-all", Info)))
	defer d.Halt()

	executed := false
	err := d.Guard(testAction("safe-action"), func() error {
		executed = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Fatal("expected fn to be executed")
	}
}

func TestGuardBlocksExecution(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(blockingPolicy("deny-all")))
	defer d.Halt()

	executed := false
	err := d.Guard(testAction("dangerous-action"), func() error {
		executed = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error for blocked action")
	}
	if executed {
		t.Fatal("fn should not have been executed when blocked")
	}
}

func TestGuardPropagatesFnError(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("allow-all", Info)))
	defer d.Halt()

	fnErr := errors.New("operation failed")
	err := d.Guard(testAction("failing-action"), func() error {
		return fnErr
	})
	if !errors.Is(err, fnErr) {
		t.Fatalf("expected fn error, got: %v", err)
	}
}

func TestAddPolicyRemovePolicy(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.AddPolicy(blockingPolicy("temp-policy"))
	policies := d.Policies()
	if len(policies) != 1 || policies[0] != "temp-policy" {
		t.Fatalf("expected [temp-policy], got %v", policies)
	}

	d.RemovePolicy("temp-policy")
	policies = d.Policies()
	if len(policies) != 0 {
		t.Fatalf("expected empty policies after removal, got %v", policies)
	}
}

func TestRemovePolicyNonexistent(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	// Removing a non-existent policy should not panic.
	d.RemovePolicy("does-not-exist")
	if len(d.Policies()) != 0 {
		t.Fatal("expected no policies")
	}
}

func TestAuditLogRecordsEntries(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("log-policy", Info)))
	defer d.Halt()

	d.Evaluate(testAction("action-1"))
	d.Evaluate(testAction("action-2"))

	log := d.AuditLog()
	if len(log) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(log))
	}
	if log[0].Action.Name != "action-1" {
		t.Fatalf("expected first action 'action-1', got %q", log[0].Action.Name)
	}
	if log[1].Action.Name != "action-2" {
		t.Fatalf("expected second action 'action-2', got %q", log[1].Action.Name)
	}
	if log[0].Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp on audit entry")
	}
}

func TestMaxAuditLogTruncation(t *testing.T) {
	d, _ := newTestDriver(
		WithPolicy(allowingPolicy("allow", Info)),
		WithMaxAuditLog(3),
	)
	defer d.Halt()

	for i := 0; i < 5; i++ {
		d.Evaluate(testAction("action"))
	}

	log := d.AuditLog()
	if len(log) != 3 {
		t.Fatalf("expected 3 audit entries (max), got %d", len(log))
	}
}

func TestEventPublishedOnBlock(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(blockingPolicy("block")))
	defer d.Halt()

	var blockedCount atomic.Int32
	_ = d.On(EventBlocked, func(data interface{}) {
		blockedCount.Add(1)
	})

	var evaluatedCount atomic.Int32
	_ = d.On(EventEvaluated, func(data interface{}) {
		evaluatedCount.Add(1)
	})

	d.Evaluate(testAction("blocked-action"))
	time.Sleep(50 * time.Millisecond)

	if blockedCount.Load() != 1 {
		t.Fatalf("expected 1 blocked event, got %d", blockedCount.Load())
	}
	if evaluatedCount.Load() != 1 {
		t.Fatalf("expected 1 evaluated event, got %d", evaluatedCount.Load())
	}
}

func TestEventPublishedOnEvaluation(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("allow", Info)))
	defer d.Halt()

	var evaluatedCount atomic.Int32
	_ = d.On(EventEvaluated, func(data interface{}) {
		evaluatedCount.Add(1)
	})

	var blockedCount atomic.Int32
	_ = d.On(EventBlocked, func(data interface{}) {
		blockedCount.Add(1)
	})

	d.Evaluate(testAction("safe-action"))
	time.Sleep(50 * time.Millisecond)

	if evaluatedCount.Load() != 1 {
		t.Fatalf("expected 1 evaluated event, got %d", evaluatedCount.Load())
	}
	if blockedCount.Load() != 0 {
		t.Fatalf("expected 0 blocked events, got %d", blockedCount.Load())
	}
}

func TestViolationEventOnCritical(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("critical-warn", Critical)))
	defer d.Halt()

	var violationCount atomic.Int32
	_ = d.On(EventViolation, func(data interface{}) {
		violationCount.Add(1)
	})

	d.Evaluate(testAction("suspicious"))
	time.Sleep(50 * time.Millisecond)

	if violationCount.Load() != 1 {
		t.Fatalf("expected 1 violation event, got %d", violationCount.Load())
	}
}

func TestAuditLogPersistedToMemory(t *testing.T) {
	d, a := newTestDriver(WithPolicy(allowingPolicy("allow", Info)))

	d.Evaluate(testAction("persisted-action"))

	val, err := a.Retrieve(namespace, "audit_log")
	if err != nil {
		t.Fatalf("expected audit_log in memory: %v", err)
	}
	entries, ok := val.([]AuditEntry)
	if !ok {
		t.Fatalf("expected []AuditEntry, got %T", val)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 persisted entry, got %d", len(entries))
	}

	d.Halt()
}

func TestCommandEvaluate(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("cmd-policy", Info)))
	defer d.Halt()

	result := d.Command("evaluate")(map[string]interface{}{
		"name":   "test-action",
		"source": "command",
	})
	dec, ok := result.(Decision)
	if !ok {
		t.Fatalf("expected Decision, got %T", result)
	}
	if !dec.Allowed {
		t.Fatal("expected allowed")
	}
}

func TestCommandPolicies(t *testing.T) {
	d, _ := newTestDriver(
		WithPolicy(allowingPolicy("p1", Info)),
		WithPolicy(allowingPolicy("p2", Warning)),
	)
	defer d.Halt()

	result := d.Command("policies")(nil)
	names, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 policy names, got %d", len(names))
	}
}

func TestCommandAudit(t *testing.T) {
	d, _ := newTestDriver(WithPolicy(allowingPolicy("allow", Info)))
	defer d.Halt()

	d.Evaluate(testAction("audited"))
	result := d.Command("audit")(nil)
	entries, ok := result.([]AuditEntry)
	if !ok {
		t.Fatalf("expected []AuditEntry, got %T", result)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
}

func TestSeverityString(t *testing.T) {
	tests := []struct {
		sev  Severity
		want string
	}{
		{Info, "info"},
		{Warning, "warning"},
		{Critical, "critical"},
		{Blocked, "blocked"},
		{Severity(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.sev.String(); got != tt.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tt.sev, got, tt.want)
		}
	}
}

func TestNilCheckPolicySkipped(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	// A policy with nil Check should be skipped without error.
	d.AddPolicy(Policy{
		Name:     "nil-check",
		Severity: Blocked,
		Check:    nil,
	})

	dec := d.Evaluate(testAction("anything"))
	if !dec.Allowed {
		t.Fatal("expected allowed when only policy has nil Check")
	}
}

func TestWithPolicyOption(t *testing.T) {
	p := blockingPolicy("via-option")
	d, _ := newTestDriver(WithPolicy(p))
	defer d.Halt()

	names := d.Policies()
	if len(names) != 1 || names[0] != "via-option" {
		t.Fatalf("expected [via-option], got %v", names)
	}
}

func TestEvaluateSetsTimestamp(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	action := Action{
		Name:   "no-timestamp",
		Source: "test",
	}
	d.Evaluate(action)

	log := d.AuditLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(log))
	}
	if log[0].Action.Timestamp.IsZero() {
		t.Fatal("expected Evaluate to set missing timestamp on action")
	}
}

func TestAuditLogIsACopy(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Evaluate(testAction("first"))
	log1 := d.AuditLog()

	d.Evaluate(testAction("second"))
	log2 := d.AuditLog()

	if len(log1) != 1 {
		t.Fatalf("first snapshot should have 1 entry, got %d", len(log1))
	}
	if len(log2) != 2 {
		t.Fatalf("second snapshot should have 2 entries, got %d", len(log2))
	}
}
