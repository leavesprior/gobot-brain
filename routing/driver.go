// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

// Package routing provides confidence-aware task routing for robot fleets.
// Given a task type and a pool of registered workers, it selects the best
// worker based on historical success rates, recency weighting, and Laplace
// smoothing. Inspired by multi-armed bandit / exploration-exploitation
// patterns.
package routing

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

const namespace = "routing"

// Event names published by the routing driver.
const (
	EventRouted      = "routed"
	EventReported    = "reported"
	EventExploration = "exploration"
	EventError       = "error"
)

// Worker describes a registered worker and the task types it can handle.
type Worker struct {
	Name         string
	Capabilities []string // task types this worker handles
}

// Result captures the outcome of a single task execution.
type Result struct {
	Worker   string
	TaskType string
	Success  bool
	Time     time.Time
}

// workerStats tracks per-worker, per-task-type outcome history.
type workerStats struct {
	Successes  int
	Failures   int
	LastResult time.Time
}

// Option configures the routing driver.
type Option func(*Driver)

// WithWorker registers a worker at construction time.
func WithWorker(w Worker) Option {
	return func(d *Driver) {
		d.workers[w.Name] = w
	}
}

// WithExplorationRate sets the probability (0.0-1.0) of selecting a random
// capable worker instead of the highest-scoring one. Default is 0.1.
func WithExplorationRate(rate float64) Option {
	return func(d *Driver) {
		if rate >= 0 && rate <= 1 {
			d.explorationRate = rate
		}
	}
}

// WithRecencyWindow sets the duration within which a worker's last result
// receives full recency weighting (1.0). Results older than the window
// decay toward 0.5. Default is 1 hour.
func WithRecencyWindow(dur time.Duration) Option {
	return func(d *Driver) {
		if dur > 0 {
			d.recencyWindow = dur
		}
	}
}

// WithDeltaGuard sets the threshold for preventing pathological swapping
// between close-scoring workers. A new winner only replaces the previous
// winner if its score exceeds the runner-up's score multiplied by this
// threshold. Default is 0.6.
func WithDeltaGuard(threshold float64) Option {
	return func(d *Driver) {
		if threshold >= 0 && threshold <= 1 {
			d.deltaGuard = threshold
		}
	}
}

// Driver is a GoBot v2 device that provides confidence-aware routing of
// tasks to workers based on historical success/failure outcomes.
type Driver struct {
	name    string
	adaptor *memory.Adaptor

	mu              sync.RWMutex
	workers         map[string]Worker
	stats           map[string]*workerStats // keyed by "worker:taskType"
	explorationRate float64
	recencyWindow   time.Duration
	deltaGuard      float64
	lastWinner      map[string]string // taskType -> last routed worker
	rng             *rand.Rand

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a routing driver attached to the given memory adaptor.
func NewDriver(a *memory.Adaptor, opts ...Option) *Driver {
	d := &Driver{
		name:            "routing",
		adaptor:         a,
		workers:         make(map[string]Worker),
		stats:           make(map[string]*workerStats),
		explorationRate: 0.1,
		recencyWindow:   time.Hour,
		deltaGuard:      0.6,
		lastWinner:      make(map[string]string),
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
		Eventer:         gobot.NewEventer(),
		Commander:       gobot.NewCommander(),
	}
	for _, opt := range opts {
		opt(d)
	}

	d.AddEvent(EventRouted)
	d.AddEvent(EventReported)
	d.AddEvent(EventExploration)
	d.AddEvent(EventError)

	d.AddCommand("route", func(params map[string]interface{}) interface{} {
		taskType, ok := params["task_type"].(string)
		if !ok {
			return fmt.Errorf("route command: missing string 'task_type' parameter")
		}
		worker, err := d.Route(taskType)
		if err != nil {
			return err
		}
		return worker
	})
	d.AddCommand("report", func(params map[string]interface{}) interface{} {
		worker, _ := params["worker"].(string)
		taskType, _ := params["task_type"].(string)
		success, _ := params["success"].(bool)
		if worker == "" || taskType == "" {
			return fmt.Errorf("report command: missing 'worker' or 'task_type' parameter")
		}
		d.Report(Result{Worker: worker, TaskType: taskType, Success: success, Time: time.Now()})
		return nil
	})
	d.AddCommand("scores", func(params map[string]interface{}) interface{} {
		taskType, ok := params["task_type"].(string)
		if !ok {
			return fmt.Errorf("scores command: missing string 'task_type' parameter")
		}
		return d.Scores(taskType)
	})
	d.AddCommand("workers", func(params map[string]interface{}) interface{} {
		return d.Workers()
	})

	return d
}

func (d *Driver) Name() string                 { return d.name }
func (d *Driver) SetName(name string)          { d.name = name }
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start initializes the routing driver, clearing any in-memory state.
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stats = make(map[string]*workerStats)
	d.lastWinner = make(map[string]string)
	return nil
}

// Halt stops the routing driver.
func (d *Driver) Halt() error {
	return nil
}

// Register adds a worker to the pool. If a worker with the same name
// already exists, it is replaced.
func (d *Driver) Register(w Worker) {
	d.mu.Lock()
	d.workers[w.Name] = w
	d.mu.Unlock()
}

// Unregister removes a worker from the pool by name.
func (d *Driver) Unregister(name string) {
	d.mu.Lock()
	delete(d.workers, name)
	d.mu.Unlock()
}

// Workers returns the names of all registered workers, sorted alphabetically.
func (d *Driver) Workers() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	names := make([]string, 0, len(d.workers))
	for name := range d.workers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// statsKey returns the map key for a worker/taskType pair.
func statsKey(worker, taskType string) string {
	return worker + ":" + taskType
}

// Report records the outcome of a task execution and persists it to memory.
func (d *Driver) Report(result Result) {
	if result.Time.IsZero() {
		result.Time = time.Now()
	}

	d.mu.Lock()
	key := statsKey(result.Worker, result.TaskType)
	s, ok := d.stats[key]
	if !ok {
		s = &workerStats{}
		d.stats[key] = s
	}
	if result.Success {
		s.Successes++
	} else {
		s.Failures++
	}
	s.LastResult = result.Time
	d.mu.Unlock()

	d.Publish(EventReported, result)

	// Persist stats to memory adaptor (best-effort).
	_ = d.adaptor.Store(namespace, key, map[string]interface{}{
		"worker":      result.Worker,
		"task_type":   result.TaskType,
		"successes":   s.Successes,
		"failures":    s.Failures,
		"last_result": s.LastResult,
	})
}

// score computes the confidence score for a worker on a task type.
// greenRate = (successes + 1) / (successes + failures + 2)   [Laplace smoothing]
// recency = 1.0 if lastResult within RecencyWindow, decaying toward 0.5
// score = greenRate * recency
func (d *Driver) score(worker, taskType string, now time.Time) float64 {
	key := statsKey(worker, taskType)
	s, ok := d.stats[key]
	if !ok {
		// No history: Laplace gives (0+1)/(0+0+2) = 0.5, recency = 0.5
		return 0.5 * 0.5
	}

	greenRate := float64(s.Successes+1) / float64(s.Successes+s.Failures+2)

	age := now.Sub(s.LastResult)
	var recency float64
	if age <= d.recencyWindow {
		recency = 1.0
	} else {
		// Decay from 1.0 toward 0.5 for results older than the window.
		// Use an exponential decay: 0.5 + 0.5 * exp(-overflow/window)
		overflow := age - d.recencyWindow
		decay := float64(overflow) / float64(d.recencyWindow)
		recency = 0.5 + 0.5*math.Exp(-decay)
	}

	return greenRate * recency
}



// Route selects the best worker for a given task type using confidence
// scoring with optional exploration. Returns the worker name or an error
// if no workers are capable.
func (d *Driver) Route(taskType string) (string, error) {
	d.mu.RLock()

	// Filter workers that have taskType in their Capabilities.
	type candidate struct {
		name  string
		score float64
	}
	var candidates []candidate
	now := time.Now()

	for _, w := range d.workers {
		if hasCapability(w.Capabilities, taskType) {
			s := d.score(w.Name, taskType, now)
			candidates = append(candidates, candidate{name: w.Name, score: s})
		}
	}

	if len(candidates) == 0 {
		d.mu.RUnlock()
		err := errors.New("no workers capable of task type: " + taskType)
		d.Publish(EventError, err)
		return "", err
	}

	// Sort by score descending, then by name for determinism.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].name < candidates[j].name
	})

	explorationRate := d.explorationRate
	lastWinner := d.lastWinner[taskType]
	d.mu.RUnlock()

	// Exploration: with probability explorationRate, pick a random capable worker.
	if len(candidates) > 1 && explorationRate > 0 {
		d.mu.Lock()
		roll := d.rng.Float64()
		d.mu.Unlock()

		if roll < explorationRate {
			d.mu.Lock()
			idx := d.rng.Intn(len(candidates))
			d.mu.Unlock()
			picked := candidates[idx].name
			d.Publish(EventExploration, map[string]string{
				"worker":    picked,
				"task_type": taskType,
			})
			d.mu.Lock()
			d.lastWinner[taskType] = picked
			d.mu.Unlock()
			return picked, nil
		}
	}

	// Exploitation: pick the highest scoring worker, subject to delta guard.
	best := candidates[0]
	winner := best.name

	if len(candidates) > 1 && lastWinner != "" && lastWinner != winner {
		second := candidates[1]
		// Delta guard: when the top scorer has changed from the
		// previous winner, only accept the swap if the new best
		// sufficiently dominates the runner-up. Specifically, the
		// runner-up's score must NOT exceed best * deltaGuard. If it
		// does, scores are too close and we stick with the previous
		// winner to prevent pathological swapping.
		if best.score > 0 && second.score > best.score*d.deltaGuard {
			// Scores are too close. Keep previous winner if it is
			// among the candidates.
			for _, c := range candidates {
				if c.name == lastWinner {
					winner = lastWinner
					break
				}
			}
		}
	}

	d.Publish(EventRouted, map[string]string{
		"worker":    winner,
		"task_type": taskType,
	})

	d.mu.Lock()
	d.lastWinner[taskType] = winner
	d.mu.Unlock()

	return winner, nil
}

// Scores returns the current confidence scores for all capable workers
// for the given task type.
func (d *Driver) Scores(taskType string) map[string]float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()

	now := time.Now()
	result := make(map[string]float64)
	for _, w := range d.workers {
		if hasCapability(w.Capabilities, taskType) {
			result[w.Name] = d.score(w.Name, taskType, now)
		}
	}
	return result
}

// hasCapability checks if a capability list contains the given task type.
func hasCapability(caps []string, taskType string) bool {
	for _, c := range caps {
		if c == taskType {
			return true
		}
	}
	return false
}
