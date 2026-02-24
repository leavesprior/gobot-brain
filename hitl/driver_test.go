package hitl

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time check that Driver satisfies the gobot.Device interface.
var _ gobot.Device = (*Driver)(nil)

func newTestDriver(notify NotifyFunc) (*Driver, *memory.Adaptor) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	d := NewDriver(a, notify)
	_ = d.Start()
	return d, a
}

func TestRequestApprovalCreatesPending(t *testing.T) {
	var notified atomic.Int32
	d, _ := newTestDriver(func(req Request) error {
		notified.Add(1)
		return nil
	})
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		Description: "deploy to prod",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if notified.Load() != 1 {
		t.Fatalf("expected notify called once, got %d", notified.Load())
	}

	req, err := d.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Decision != Pending {
		t.Fatalf("expected Pending, got %s", req.Decision)
	}
	if req.Description != "deploy to prod" {
		t.Fatalf("unexpected description: %s", req.Description)
	}
}

func TestApproveExecutesAction(t *testing.T) {
	var executed atomic.Int32
	d, _ := newTestDriver(nil)
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		Description: "restart service",
		Action: func() error {
			executed.Add(1)
			return nil
		},
		Timeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	if err := d.Approve(id); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if executed.Load() != 1 {
		t.Fatalf("expected action executed once, got %d", executed.Load())
	}

	req, err := d.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Decision != Approved {
		t.Fatalf("expected approved, got %s", req.Decision)
	}
}

func TestDenyDoesNotExecuteAction(t *testing.T) {
	var executed atomic.Int32
	d, _ := newTestDriver(nil)
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		Description: "dangerous op",
		Action: func() error {
			executed.Add(1)
			return nil
		},
		Timeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	if err := d.Deny(id); err != nil {
		t.Fatalf("Deny: %v", err)
	}

	if executed.Load() != 0 {
		t.Fatal("action should not have been executed after deny")
	}

	req, err := d.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Decision != Denied {
		t.Fatalf("expected denied, got %s", req.Decision)
	}
}

func TestExpiryAfterTimeout(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		Description: "will expire",
		Action:      func() error { return nil },
		Timeout:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// Wait for the expiry goroutine to fire.
	time.Sleep(300 * time.Millisecond)

	req, err := d.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Decision != Expired {
		t.Fatalf("expected expired, got %s", req.Decision)
	}
}

func TestApproveAlreadyDenied(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		Description: "test conflict",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	if err := d.Deny(id); err != nil {
		t.Fatalf("Deny: %v", err)
	}

	err = d.Approve(id)
	if err == nil {
		t.Fatal("expected error when approving a denied request")
	}
}

func TestPendingReturnsOnlyPending(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	// Create three requests.
	id1, _ := d.RequestApproval(Request{
		Description: "one",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})
	_, _ = d.RequestApproval(Request{
		Description: "two",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})
	id3, _ := d.RequestApproval(Request{
		Description: "three",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})

	// Approve one, deny another.
	_ = d.Approve(id1)
	_ = d.Deny(id3)

	pending := d.Pending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(pending))
	}
	if pending[0].Description != "two" {
		t.Fatalf("expected pending request 'two', got %q", pending[0].Description)
	}
}

func TestApproveActionError(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	actionErr := errors.New("boom")
	id, err := d.RequestApproval(Request{
		Description: "will fail",
		Action:      func() error { return actionErr },
		Timeout:     time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	err = d.Approve(id)
	if err == nil {
		t.Fatal("expected error from failing action")
	}
	if !errors.Is(err, actionErr) {
		t.Fatalf("expected wrapped actionErr, got: %v", err)
	}

	// Decision should still be approved even though action failed.
	req, err := d.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.Decision != Approved {
		t.Fatalf("expected approved, got %s", req.Decision)
	}
}

func TestHaltCancelsExpiry(t *testing.T) {
	d, _ := newTestDriver(nil)

	id, err := d.RequestApproval(Request{
		Description: "should not expire",
		Action:      func() error { return nil },
		Timeout:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// Halt immediately, before the expiry fires.
	_ = d.Halt()

	// Give the expiry goroutine time it would have needed.
	time.Sleep(400 * time.Millisecond)

	// The request should still be pending because Halt cancelled the goroutine.
	req, ok := d.requests[id]
	if !ok {
		t.Fatal("request not found in map")
	}
	if req.Decision != Pending {
		t.Fatalf("expected pending after halt, got %s", req.Decision)
	}
}

func TestGetNotFound(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	_, err := d.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}

func TestCustomID(t *testing.T) {
	d, _ := newTestDriver(nil)
	defer d.Halt()

	id, err := d.RequestApproval(Request{
		ID:          "my-custom-id",
		Description: "custom",
		Action:      func() error { return nil },
		Timeout:     time.Hour,
	})
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if id != "my-custom-id" {
		t.Fatalf("expected custom ID, got %q", id)
	}
}
