// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"math/rand"
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
	// Use a deterministic RNG for reproducible tests.
	d.rng = rand.New(rand.NewSource(42))
	_ = d.Start()
	return d, a
}

func TestNameSetNameConnection(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	if d.Name() != "routing" {
		t.Fatalf("expected name 'routing', got %q", d.Name())
	}
	d.SetName("custom-router")
	if d.Name() != "custom-router" {
		t.Fatalf("expected name 'custom-router', got %q", d.Name())
	}
	if d.Connection() == nil {
		t.Fatal("expected non-nil connection")
	}
}

func TestRouteNoWorkers(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	_, err := d.Route("build")
	if err == nil {
		t.Fatal("expected error when no workers registered")
	}
}

func TestRouteNoCapableWorkers(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"deploy"}})

	_, err := d.Route("build")
	if err == nil {
		t.Fatal("expected error when no workers capable of task type")
	}
}

func TestRouteOneCapableWorker(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build", "test"}})

	worker, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if worker != "alpha" {
		t.Fatalf("expected 'alpha', got %q", worker)
	}
}

func TestRoutePrefersHigherScoringWorker(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	now := time.Now()

	// Alpha: 5 successes.
	for i := 0; i < 5; i++ {
		d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	}
	// Beta: 5 failures.
	for i := 0; i < 5; i++ {
		d.Report(Result{Worker: "beta", TaskType: "build", Success: false, Time: now})
	}

	worker, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if worker != "alpha" {
		t.Fatalf("expected 'alpha' (higher scoring), got %q", worker)
	}
}

func TestLaplaceSmoothingGivesNewWorkersFairChance(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "veteran", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "newcomer", Capabilities: []string{"build"}})

	now := time.Now()

	// Veteran has 2 successes and 2 failures: greenRate = (2+1)/(2+2+2) = 0.5
	d.Report(Result{Worker: "veteran", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "veteran", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "veteran", TaskType: "build", Success: false, Time: now})
	d.Report(Result{Worker: "veteran", TaskType: "build", Success: false, Time: now})

	// Newcomer has no history: greenRate = (0+1)/(0+0+2) = 0.5
	// But newcomer has no LastResult so recency = 0.5 (no stats entry).
	// Veteran has full recency = 1.0 since results are recent.

	scores := d.Scores("build")
	veteranScore := scores["veteran"]
	newcomerScore := scores["newcomer"]

	// Newcomer should have a nonzero score (Laplace gives 0.5 greenRate).
	if newcomerScore <= 0 {
		t.Fatalf("expected newcomer to have positive score from Laplace smoothing, got %f", newcomerScore)
	}

	// Veteran has same greenRate (0.5) but better recency (1.0 vs 0.5).
	if veteranScore <= newcomerScore {
		t.Logf("veteran=%f newcomer=%f", veteranScore, newcomerScore)
		// This is expected: veteran has recency=1.0, newcomer recency=0.5.
	}

	t.Logf("veteran score=%f, newcomer score=%f (Laplace gives newcomer a fair base)", veteranScore, newcomerScore)
}

func TestExplorationRateAlwaysExplores(t *testing.T) {
	// With exploration rate 1.0, every Route call should explore.
	d, _ := newTestDriver(WithExplorationRate(1.0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	now := time.Now()
	// Make alpha clearly the best.
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	}
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "beta", TaskType: "build", Success: false, Time: now})
	}

	// Run many routes and verify that beta gets picked at least once
	// (exploration should sometimes pick non-optimal workers).
	betaPicked := false
	for i := 0; i < 50; i++ {
		worker, err := d.Route("build")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if worker == "beta" {
			betaPicked = true
			break
		}
	}
	if !betaPicked {
		t.Fatal("expected exploration to pick beta at least once with rate=1.0")
	}
}

func TestExplorationRateZeroNeverExplores(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	now := time.Now()
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	}
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "beta", TaskType: "build", Success: false, Time: now})
	}

	// Every route should pick alpha (no exploration).
	for i := 0; i < 20; i++ {
		worker, err := d.Route("build")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if worker != "alpha" {
			t.Fatalf("expected 'alpha' with exploration=0, got %q on iteration %d", worker, i)
		}
	}
}

func TestDeltaGuardPreventsSwapping(t *testing.T) {
	// Set deltaGuard high (0.99) so close scores trigger the guard.
	d, _ := newTestDriver(WithExplorationRate(0), WithDeltaGuard(0.99))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	now := time.Now()

	// Both workers have similar records: alpha slightly better.
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: false, Time: now})

	d.Report(Result{Worker: "beta", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "beta", TaskType: "build", Success: true, Time: now})

	// First route establishes alpha as last winner (sorted first by name if tied).
	first, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Now give beta a slight edge.
	d.Report(Result{Worker: "beta", TaskType: "build", Success: true, Time: now})

	// Second route: beta might have a slightly higher score, but delta guard
	// should keep the previous winner since scores are close.
	second, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With high delta guard (0.99), swapping should be prevented.
	if first != second {
		t.Logf("first=%q second=%q", first, second)
		// Delta guard should have kept the same winner.
		scores := d.Scores("build")
		t.Logf("scores: alpha=%f beta=%f", scores["alpha"], scores["beta"])
		t.Fatal("expected delta guard to prevent swapping between close scores")
	}
}

func TestDeltaGuardAllowsSwapOnLargeGap(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0), WithDeltaGuard(0.6))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	now := time.Now()

	// Start with alpha as winner.
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})

	first, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != "alpha" {
		t.Fatalf("expected first winner to be alpha, got %q", first)
	}

	// Now give beta a huge lead.
	for i := 0; i < 20; i++ {
		d.Report(Result{Worker: "beta", TaskType: "build", Success: true, Time: now})
	}
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "alpha", TaskType: "build", Success: false, Time: now})
	}

	second, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second != "beta" {
		t.Fatalf("expected beta to win with large score gap, got %q", second)
	}
}

func TestRegisterUnregister(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	workers := d.Workers()
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}

	d.Unregister("alpha")

	workers = d.Workers()
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker after unregister, got %d", len(workers))
	}
	if workers[0] != "beta" {
		t.Fatalf("expected remaining worker to be 'beta', got %q", workers[0])
	}

	// Unregistering a non-existent worker should not panic.
	d.Unregister("nonexistent")
}

func TestWorkersReturnsSorted(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "charlie", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	workers := d.Workers()
	if len(workers) != 3 {
		t.Fatalf("expected 3 workers, got %d", len(workers))
	}
	if workers[0] != "alpha" || workers[1] != "beta" || workers[2] != "charlie" {
		t.Fatalf("expected sorted order [alpha beta charlie], got %v", workers)
	}
}

func TestScoresReturnsCorrectValues(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build", "test"}})
	d.Register(Worker{Name: "gamma", Capabilities: []string{"test"}})

	now := time.Now()
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "beta", TaskType: "build", Success: false, Time: now})

	scores := d.Scores("build")

	// Only alpha and beta should appear (gamma has no "build" capability).
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d: %v", len(scores), scores)
	}
	if _, ok := scores["gamma"]; ok {
		t.Fatal("gamma should not appear in build scores")
	}

	// Alpha: greenRate = (1+1)/(1+0+2) = 2/3 ~= 0.667
	// Beta: greenRate = (0+1)/(0+1+2) = 1/3 ~= 0.333
	alphaScore := scores["alpha"]
	betaScore := scores["beta"]
	if alphaScore <= betaScore {
		t.Fatalf("expected alpha score > beta score, got alpha=%f beta=%f", alphaScore, betaScore)
	}

	// Test scores for task type with no capable workers.
	scores = d.Scores("deploy")
	if len(scores) != 0 {
		t.Fatalf("expected 0 scores for 'deploy', got %d", len(scores))
	}
}

func TestReportPersistsToMemory(t *testing.T) {
	d, a := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: time.Now()})

	// Check that stats were persisted to the memory adaptor.
	val, err := a.Retrieve(namespace, "alpha:build")
	if err != nil {
		t.Fatalf("expected persisted stats, got error: %v", err)
	}
	if val == nil {
		t.Fatal("expected non-nil persisted value")
	}

	// The value should be a map with successes = 1.
	m, ok := val.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", val)
	}
	if m["successes"] != 1 {
		t.Fatalf("expected successes=1, got %v", m["successes"])
	}
}

func TestWithWorkerOption(t *testing.T) {
	d, _ := newTestDriver(
		WithWorker(Worker{Name: "pre-registered", Capabilities: []string{"build"}}),
		WithExplorationRate(0),
	)
	defer d.Halt()

	workers := d.Workers()
	if len(workers) != 1 {
		t.Fatalf("expected 1 pre-registered worker, got %d", len(workers))
	}
	if workers[0] != "pre-registered" {
		t.Fatalf("expected 'pre-registered', got %q", workers[0])
	}

	worker, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if worker != "pre-registered" {
		t.Fatalf("expected 'pre-registered', got %q", worker)
	}
}

func TestRecencyWindowAffectsScore(t *testing.T) {
	d, _ := newTestDriver(
		WithExplorationRate(0),
		WithRecencyWindow(100*time.Millisecond),
	)
	defer d.Halt()

	d.Register(Worker{Name: "old-worker", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "new-worker", Capabilities: []string{"build"}})

	// Old worker reported in the past.
	oldTime := time.Now().Add(-5 * time.Second)
	d.Report(Result{Worker: "old-worker", TaskType: "build", Success: true, Time: oldTime})

	// New worker reported just now.
	d.Report(Result{Worker: "new-worker", TaskType: "build", Success: true, Time: time.Now()})

	scores := d.Scores("build")
	if scores["new-worker"] <= scores["old-worker"] {
		t.Fatalf("expected new-worker score (%f) > old-worker score (%f) due to recency",
			scores["new-worker"], scores["old-worker"])
	}
}

func TestStartClearsStats(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true, Time: time.Now()})

	scores := d.Scores("build")
	if scores["alpha"] <= 0 {
		t.Fatal("expected positive score before restart")
	}

	// Start clears stats.
	_ = d.Start()

	scores = d.Scores("build")
	// After clearing, alpha should have the default no-history score.
	initialScore := 0.5 * 0.5 // greenRate=0.5, recency=0.5 for no stats
	if scores["alpha"] != initialScore {
		t.Fatalf("expected score=%f after restart, got %f", initialScore, scores["alpha"])
	}
}

func TestCommandRoute(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})

	result := d.Command("route")(map[string]interface{}{"task_type": "build"})
	worker, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T: %v", result, result)
	}
	if worker != "alpha" {
		t.Fatalf("expected 'alpha', got %q", worker)
	}

	// Missing param should return error.
	result = d.Command("route")(nil)
	if _, ok := result.(error); !ok {
		t.Fatalf("expected error for missing param, got %T", result)
	}
}

func TestCommandScores(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})

	result := d.Command("scores")(map[string]interface{}{"task_type": "build"})
	scores, ok := result.(map[string]float64)
	if !ok {
		t.Fatalf("expected map[string]float64, got %T", result)
	}
	if _, ok := scores["alpha"]; !ok {
		t.Fatal("expected alpha in scores")
	}
}

func TestCommandWorkers(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "beta", Capabilities: []string{"build"}})

	result := d.Command("workers")(nil)
	workers, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}
}

func TestReportDefaultTime(t *testing.T) {
	d, _ := newTestDriver()
	defer d.Halt()

	d.Register(Worker{Name: "alpha", Capabilities: []string{"build"}})

	before := time.Now()
	d.Report(Result{Worker: "alpha", TaskType: "build", Success: true})
	after := time.Now()

	d.mu.RLock()
	s := d.stats[statsKey("alpha", "build")]
	d.mu.RUnlock()

	if s.LastResult.Before(before) || s.LastResult.After(after) {
		t.Fatal("expected LastResult to be set to approximately now when Time is zero")
	}
}

func TestMultipleTaskTypes(t *testing.T) {
	d, _ := newTestDriver(WithExplorationRate(0))
	defer d.Halt()

	d.Register(Worker{Name: "builder", Capabilities: []string{"build"}})
	d.Register(Worker{Name: "tester", Capabilities: []string{"test"}})
	d.Register(Worker{Name: "generalist", Capabilities: []string{"build", "test"}})

	now := time.Now()

	// Builder is great at building.
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "builder", TaskType: "build", Success: true, Time: now})
	}
	// Generalist is mediocre at building.
	d.Report(Result{Worker: "generalist", TaskType: "build", Success: true, Time: now})
	d.Report(Result{Worker: "generalist", TaskType: "build", Success: false, Time: now})

	// For build tasks, builder should win.
	worker, err := d.Route("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if worker != "builder" {
		t.Fatalf("expected 'builder' for build tasks, got %q", worker)
	}

	// Tester is great at testing.
	for i := 0; i < 10; i++ {
		d.Report(Result{Worker: "tester", TaskType: "test", Success: true, Time: now})
	}

	// For test tasks, tester should win.
	worker, err = d.Route("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if worker != "tester" {
		t.Fatalf("expected 'tester' for test tasks, got %q", worker)
	}
}
