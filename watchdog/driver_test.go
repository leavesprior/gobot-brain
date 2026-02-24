package watchdog

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time interface check.
var _ gobot.Device = (*Driver)(nil)

// helper: create a memory adaptor for tests.
func testAdaptor() *memory.Adaptor {
	return memory.NewAdaptor()
}

func TestHealthyCheck(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)
	d.AddCheck(Check{
		Name:     "ok",
		Fn:       func() error { return nil },
		Interval: 25 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	// Let a few ticks fire.
	time.Sleep(120 * time.Millisecond)

	st := d.Status()
	cs, ok := st["ok"]
	if !ok {
		t.Fatal("expected status entry for check 'ok'")
	}
	if !cs.Healthy {
		t.Errorf("expected check to be healthy, got unhealthy: %v", cs.LastErr)
	}
	if cs.Consecutive != 0 {
		t.Errorf("expected 0 consecutive failures, got %d", cs.Consecutive)
	}
	if cs.LastCheck.IsZero() {
		t.Error("expected LastCheck to be set")
	}
}

func TestUnhealthyAlertAfterDebounce(t *testing.T) {
	a := testAdaptor()

	var alertMu sync.Mutex
	var alerts []struct {
		name string
		err  error
		n    int
	}

	alertFn := func(name string, err error, n int) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alerts = append(alerts, struct {
			name string
			err  error
			n    int
		}{name, err, n})
	}

	d := NewDriver(a, alertFn, WithAlertAfter(2))

	checkErr := errors.New("broken")
	d.AddCheck(Check{
		Name:     "bad",
		Fn:       func() error { return checkErr },
		Interval: 25 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	// Wait for enough ticks to exceed debounce threshold.
	time.Sleep(200 * time.Millisecond)

	alertMu.Lock()
	numAlerts := len(alerts)
	alertMu.Unlock()

	if numAlerts == 0 {
		t.Fatal("expected alert to fire after debounce threshold")
	}

	// First alert should be at consecutive == 2.
	alertMu.Lock()
	first := alerts[0]
	alertMu.Unlock()

	if first.name != "bad" {
		t.Errorf("expected alert for 'bad', got %q", first.name)
	}
	if first.err == nil {
		t.Error("expected non-nil error in alert")
	}
	if first.n < 2 {
		t.Errorf("expected consecutive >= 2, got %d", first.n)
	}

	// Status should show unhealthy.
	st := d.Status()
	if st["bad"].Healthy {
		t.Error("expected status to be unhealthy")
	}

	// Healthy() should return false.
	if d.Healthy() {
		t.Error("expected Healthy() to return false")
	}
}

func TestRecoveryAfterFailure(t *testing.T) {
	a := testAdaptor()

	var alertMu sync.Mutex
	var recoveryAlerts []struct {
		name string
		err  error
		n    int
	}

	alertFn := func(name string, err error, n int) {
		alertMu.Lock()
		defer alertMu.Unlock()
		if err == nil {
			recoveryAlerts = append(recoveryAlerts, struct {
				name string
				err  error
				n    int
			}{name, err, n})
		}
	}

	// Use atomic to switch between failing and passing.
	var failing atomic.Int32
	failing.Store(1)

	d := NewDriver(a, alertFn, WithAlertAfter(2))

	checkErr := errors.New("fail")
	d.AddCheck(Check{
		Name: "flip",
		Fn: func() error {
			if failing.Load() == 1 {
				return checkErr
			}
			return nil
		},
		Interval: 25 * time.Millisecond,
	})

	// Listen for recovered event.
	var recoveredEvent atomic.Int32
	_ = d.On(Recovered, func(data interface{}) {
		recoveredEvent.Add(1)
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	// Let failures accumulate past debounce.
	time.Sleep(150 * time.Millisecond)

	if d.Healthy() {
		t.Fatal("expected unhealthy before recovery")
	}

	// Flip to healthy.
	failing.Store(0)

	// Wait for at least one successful check.
	time.Sleep(120 * time.Millisecond)

	if !d.Healthy() {
		t.Error("expected Healthy() after recovery")
	}

	if recoveredEvent.Load() == 0 {
		t.Error("expected recovered event to fire")
	}

	alertMu.Lock()
	numRecovery := len(recoveryAlerts)
	alertMu.Unlock()

	if numRecovery == 0 {
		t.Error("expected recovery alert (nil error) to fire")
	}
}

func TestHealthyReturnsFalseWhenAnyCheckFails(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	d.AddCheck(Check{
		Name:     "good",
		Fn:       func() error { return nil },
		Interval: 25 * time.Millisecond,
	})
	d.AddCheck(Check{
		Name:     "bad",
		Fn:       func() error { return errors.New("nope") },
		Interval: 25 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	time.Sleep(120 * time.Millisecond)

	if d.Healthy() {
		t.Error("expected Healthy() false when one check fails")
	}

	st := d.Status()
	if !st["good"].Healthy {
		t.Error("expected 'good' check to be healthy")
	}
	if st["bad"].Healthy {
		t.Error("expected 'bad' check to be unhealthy")
	}
}

func TestAddCheckWhileRunning(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	var ran atomic.Int32
	d.AddCheck(Check{
		Name: "late",
		Fn: func() error {
			ran.Add(1)
			return nil
		},
		Interval: 25 * time.Millisecond,
	})

	time.Sleep(120 * time.Millisecond)

	if ran.Load() == 0 {
		t.Error("expected dynamically added check to run")
	}

	st := d.Status()
	if _, ok := st["late"]; !ok {
		t.Error("expected 'late' in status")
	}
}

func TestRemoveCheck(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	var ran atomic.Int32
	d.AddCheck(Check{
		Name: "doomed",
		Fn: func() error {
			ran.Add(1)
			return nil
		},
		Interval: 25 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	time.Sleep(80 * time.Millisecond)
	d.RemoveCheck("doomed")

	countAfterRemove := ran.Load()
	time.Sleep(100 * time.Millisecond)
	countLater := ran.Load()

	// Allow at most one extra tick from race between remove and goroutine.
	if countLater > countAfterRemove+1 {
		t.Errorf("check kept running after removal: before=%d after=%d", countAfterRemove, countLater)
	}

	st := d.Status()
	if _, ok := st["doomed"]; ok {
		t.Error("expected 'doomed' removed from status")
	}
}

func TestTimeoutHandling(t *testing.T) {
	a := testAdaptor()

	var alertMu sync.Mutex
	var alerts []string

	alertFn := func(name string, err error, n int) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alerts = append(alerts, name)
	}

	d := NewDriver(a, alertFn, WithAlertAfter(1))

	d.AddCheck(Check{
		Name: "slow",
		Fn: func() error {
			// Block far longer than the timeout.
			time.Sleep(5 * time.Second)
			return nil
		},
		Interval: 50 * time.Millisecond,
		Timeout:  30 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	time.Sleep(200 * time.Millisecond)

	st := d.Status()
	cs, ok := st["slow"]
	if !ok {
		t.Fatal("expected status entry for 'slow'")
	}
	if cs.Healthy {
		t.Error("expected timed-out check to be unhealthy")
	}

	alertMu.Lock()
	numAlerts := len(alerts)
	alertMu.Unlock()

	if numAlerts == 0 {
		t.Error("expected alert for timed-out check")
	}
}

func TestHaltStopsGoroutines(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	var count atomic.Int32
	d.AddCheck(Check{
		Name: "counter",
		Fn: func() error {
			count.Add(1)
			return nil
		},
		Interval: 25 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)
	if err := d.Halt(); err != nil {
		t.Fatal(err)
	}

	snapshot := count.Load()
	time.Sleep(100 * time.Millisecond)

	if count.Load() > snapshot+1 {
		t.Error("goroutine continued after Halt()")
	}
}

func TestNewDriverWithOptions(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil, WithAlertAfter(5))

	// Verify the option was applied by adding a failing check and
	// confirming no alert fires before 5 consecutive failures.
	var alertCount atomic.Int32
	d.alertFn = func(name string, err error, n int) {
		alertCount.Add(1)
	}

	d.AddCheck(Check{
		Name:     "opt-test",
		Fn:       func() error { return errors.New("fail") },
		Interval: 20 * time.Millisecond,
	})

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Halt()

	// 3 ticks (60ms) should not trigger alert (threshold is 5).
	time.Sleep(70 * time.Millisecond)
	if alertCount.Load() > 0 {
		t.Error("alert fired before reaching threshold of 5")
	}

	// Wait enough for 5+ ticks.
	time.Sleep(80 * time.Millisecond)
	if alertCount.Load() == 0 {
		t.Error("expected alert after reaching threshold of 5")
	}
}

func TestDeviceInterfaceMethods(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	if d.Name() != "watchdog" {
		t.Errorf("expected name 'watchdog', got %q", d.Name())
	}

	d.SetName("wd2")
	if d.Name() != "wd2" {
		t.Errorf("expected name 'wd2', got %q", d.Name())
	}

	if d.Connection() != a {
		t.Error("expected Connection() to return the adaptor")
	}
}

func TestCommandsRegistered(t *testing.T) {
	a := testAdaptor()
	d := NewDriver(a, nil)

	d.AddCheck(Check{
		Name:     "cmd-test",
		Fn:       func() error { return nil },
		Interval: 50 * time.Millisecond,
	})

	// The "status" command should return a map.
	result := d.Command("status")(nil)
	st, ok := result.(map[string]CheckStatus)
	if !ok {
		t.Fatalf("expected map[string]CheckStatus, got %T", result)
	}
	if _, exists := st["cmd-test"]; !exists {
		t.Error("expected 'cmd-test' in status command result")
	}

	// The "healthy" command should return a bool.
	hResult := d.Command("healthy")(nil)
	h, ok := hResult.(bool)
	if !ok {
		t.Fatalf("expected bool, got %T", hResult)
	}
	if !h {
		t.Error("expected healthy command to return true before any failures")
	}
}
