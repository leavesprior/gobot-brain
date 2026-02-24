package scheduler

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time interface check.
var _ gobot.Device = (*Driver)(nil)

// helper: create a driver with a fresh memory adaptor.
func newTestDriver(opts ...Option) (*Driver, *memory.Adaptor) {
	a := memory.NewAdaptor()
	d := NewDriver(a, opts...)
	return d, a
}

func TestTaskExecution(t *testing.T) {
	var count int64

	d, _ := newTestDriver(
		WithTask(Task{
			Name:     "counter",
			Interval: 20 * time.Millisecond,
			Level:    Silent,
			Fn: func() error {
				atomic.AddInt64(&count, 1)
				return nil
			},
		}),
	)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait enough time for several ticks.
	time.Sleep(120 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	got := atomic.LoadInt64(&count)
	if got < 2 {
		t.Errorf("expected at least 2 ticks, got %d", got)
	}
}

func TestEscalationAfterFailures(t *testing.T) {
	threshold := 3

	d, _ := newTestDriver(
		WithEscalationThreshold(threshold),
		WithTask(Task{
			Name:     "failing",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn: func() error {
				return errors.New("boom")
			},
		}),
	)

	escalations := make(chan string, 20)
	_ = d.On(EventEscalation, func(data interface{}) {
		escalations <- data.(string)
	})

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for at least threshold ticks to trigger first escalation.
	time.Sleep(time.Duration(threshold+2) * 20 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	if len(escalations) == 0 {
		t.Fatal("expected at least one escalation event, got none")
	}

	msg := <-escalations
	if msg == "" {
		t.Error("escalation message was empty")
	}
}

func TestRecoveryResetsFailures(t *testing.T) {
	var calls int64
	threshold := 2

	d, _ := newTestDriver(
		WithEscalationThreshold(threshold),
		WithTask(Task{
			Name:     "flaky",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn: func() error {
				n := atomic.AddInt64(&calls, 1)
				// Fail for the first few calls, then succeed.
				if n <= int64(threshold)+1 {
					return errors.New("temporary")
				}
				return nil
			},
		}),
	)

	recovered := make(chan string, 10)
	_ = d.On(EventRecovered, func(data interface{}) {
		recovered <- data.(string)
	})

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for failures + recovery.
	time.Sleep(time.Duration(threshold+4) * 20 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	if len(recovered) == 0 {
		t.Fatal("expected a recovered event, got none")
	}
}

func TestPauseResume(t *testing.T) {
	var count int64

	d, _ := newTestDriver(
		WithTask(Task{
			Name:     "pausable",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn: func() error {
				atomic.AddInt64(&count, 1)
				return nil
			},
		}),
	)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let it tick a few times.
	time.Sleep(60 * time.Millisecond)
	beforePause := atomic.LoadInt64(&count)

	d.Pause("pausable")

	// Wait while paused.
	time.Sleep(60 * time.Millisecond)
	afterPause := atomic.LoadInt64(&count)

	// Count should not have increased (or at most by 1 due to race).
	if afterPause-beforePause > 1 {
		t.Errorf("task ran %d times while paused (expected 0-1)", afterPause-beforePause)
	}

	d.Resume("pausable")

	// Let it tick again.
	time.Sleep(60 * time.Millisecond)
	afterResume := atomic.LoadInt64(&count)

	if afterResume <= afterPause {
		t.Error("task did not resume after Resume()")
	}

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}
}

func TestAddRemove(t *testing.T) {
	d, _ := newTestDriver()

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if len(d.Tasks()) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(d.Tasks()))
	}

	var count int64
	d.Add(Task{
		Name:     "dynamic",
		Interval: 15 * time.Millisecond,
		Level:    Silent,
		Fn: func() error {
			atomic.AddInt64(&count, 1)
			return nil
		},
	})

	tasks := d.Tasks()
	if len(tasks) != 1 || tasks[0] != "dynamic" {
		t.Fatalf("expected [dynamic], got %v", tasks)
	}

	time.Sleep(60 * time.Millisecond)

	if atomic.LoadInt64(&count) < 1 {
		t.Error("dynamically added task did not execute")
	}

	d.Remove("dynamic")

	if len(d.Tasks()) != 0 {
		t.Errorf("expected 0 tasks after removal, got %d", len(d.Tasks()))
	}

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}
}

func TestDeviceInterface(t *testing.T) {
	d, a := newTestDriver()

	if d.Name() != "scheduler" {
		t.Errorf("Name() = %q, want %q", d.Name(), "scheduler")
	}

	d.SetName("custom")
	if d.Name() != "custom" {
		t.Errorf("after SetName, Name() = %q, want %q", d.Name(), "custom")
	}

	conn := d.Connection()
	if conn != a {
		t.Error("Connection() did not return the adaptor")
	}
}

func TestPersistence(t *testing.T) {
	d, a := newTestDriver(
		WithTask(Task{
			Name:     "persisted",
			Interval: 15 * time.Millisecond,
			Level:    Notify,
			Fn:       func() error { return nil },
		}),
	)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	// Check that the state was persisted to the memory adaptor.
	val, err := a.Retrieve("scheduler", "persisted")
	if err != nil {
		t.Fatalf("Retrieve persisted state: %v", err)
	}

	// The persisted value is a taskRecord stored as interface{};
	// marshal/unmarshal to get a typed struct.
	raw, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rec taskRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if rec.Name != "persisted" {
		t.Errorf("persisted name = %q, want %q", rec.Name, "persisted")
	}
	if rec.CurrentLevel != int(Notify) {
		t.Errorf("persisted level = %d, want %d", rec.CurrentLevel, int(Notify))
	}
	if rec.LastRun.IsZero() {
		t.Error("persisted lastRun is zero")
	}
}

func TestEscalationCapsAtCritical(t *testing.T) {
	// With threshold=1, every failure escalates. Start at Escalate (L4)
	// to verify it caps at Critical (L5) and does not overflow.
	var ticks int64

	d, _ := newTestDriver(
		WithEscalationThreshold(1),
		WithTask(Task{
			Name:     "capped",
			Interval: 15 * time.Millisecond,
			Level:    Escalate, // starts at L4
			Fn: func() error {
				atomic.AddInt64(&ticks, 1)
				return errors.New("always fails")
			},
		}),
	)

	escalations := make(chan string, 20)
	_ = d.On(EventEscalation, func(data interface{}) {
		escalations <- data.(string)
	})

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for several ticks.
	time.Sleep(80 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	// Should have exactly 1 escalation (Escalate -> Critical), then no more.
	count := len(escalations)
	if count != 1 {
		t.Errorf("expected exactly 1 escalation (to Critical), got %d", count)
	}
}

func TestSnapshotJSON(t *testing.T) {
	d, _ := newTestDriver(
		WithTask(Task{
			Name:     "snap",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn:       func() error { return nil },
		}),
	)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(40 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	raw, err := d.Snapshot("snap")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if m["name"] != "snap" {
		t.Errorf("snapshot name = %v, want %q", m["name"], "snap")
	}
}

func TestCommands(t *testing.T) {
	d, _ := newTestDriver()

	// Verify that commands are registered.
	for _, cmd := range []string{"add", "remove", "pause", "resume"} {
		if d.Command(cmd) == nil {
			t.Errorf("command %q not registered", cmd)
		}
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{Silent, "silent"},
		{Notify, "notify"},
		{Urgent, "urgent"},
		{Escalate, "escalate"},
		{Critical, "critical"},
		{Level(99), "level(99)"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", int(tt.level), got, tt.want)
		}
	}
}

func TestWithTaskOption(t *testing.T) {
	var count int64

	d, _ := newTestDriver(
		WithTask(Task{
			Name:     "opt-a",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn: func() error {
				atomic.AddInt64(&count, 1)
				return nil
			},
		}),
		WithTask(Task{
			Name:     "opt-b",
			Interval: 15 * time.Millisecond,
			Level:    Silent,
			Fn:       func() error { return nil },
		}),
	)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tasks := d.Tasks()
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d: %v", len(tasks), tasks)
	}

	time.Sleep(50 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}

	if atomic.LoadInt64(&count) < 1 {
		t.Error("WithTask task did not execute")
	}
}

func TestHaltIsIdempotent(t *testing.T) {
	d, _ := newTestDriver()

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Halt(); err != nil {
		t.Fatalf("first Halt: %v", err)
	}
	// Second halt on an empty driver should not panic.
	if err := d.Halt(); err != nil {
		t.Fatalf("second Halt: %v", err)
	}
}
