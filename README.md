# gobot-brain

A community plugin for [GoBot v2](https://github.com/hybridgroup/gobot) that adds persistent memory, LLM reasoning, proactive scheduling, fleet health monitoring, human-in-the-loop confirmation flows, security monitoring, confidence-aware routing, data lifecycle management, and browser automation.

GoBot provides hardware abstraction for 35+ robotics platforms. **gobot-brain** fills the gaps: state that survives restarts, AI-powered decision making, scheduled tasks with automatic escalation, health checks with alerting, safe confirmation gates for risky actions, security policy enforcement, intelligent task routing, data retention management, and browser control via Chrome DevTools Protocol.

## Architecture

```
Robot
├── Connection: memory.Adaptor    (namespace key-value store)
└── Devices:
    ├── inference.Driver           (LLM fallback chain)
    ├── scheduler.Driver           (proactive timers + escalation)
    ├── watchdog.Driver            (fleet health monitoring)
    ├── hitl.Driver                (human-in-the-loop confirmation)
    ├── guardian.Driver            (security monitoring + policy enforcement)
    ├── routing.Driver             (confidence-aware worker selection)
    ├── lifecycle.Driver           (data retention + pruning)
    └── browser.Driver             (Chrome DevTools Protocol automation)
```

The Memory Adaptor is the **Connection** (foundation). All eight Drivers use it for state persistence. Each component can be used independently or together via the `NewBrain` convenience constructor.

## Install

```bash
go get github.com/leavesprior/gobot-brain
```

Requires Go 1.22+ and GoBot v2.

## Quick Start

### Memory + Inference (minimal)

```go
package main

import (
    "fmt"
    "log"

    "gobot.io/x/gobot/v2"

    "github.com/leavesprior/gobot-brain/inference"
    "github.com/leavesprior/gobot-brain/memory"
)

func main() {
    mem := memory.NewAdaptor()
    llm := inference.NewDriver(mem,
        inference.NewOllamaProvider(inference.WithOllamaModel("llama3")),
    )

    robot := gobot.NewRobot("my-robot",
        []gobot.Connection{mem},
        []gobot.Device{llm},
        func(r *gobot.Robot) {
            mem.Store("config", "version", "1.0.0")

            result, err := llm.Infer("What are the three laws of robotics?")
            if err != nil {
                log.Printf("inference error: %v", err)
                return
            }
            fmt.Println(result)
        },
    )

    if err := robot.Start(); err != nil {
        log.Fatal(err)
    }
}
```

### Full Brain (all components)

```go
b := brain.NewBrain("smart-robot",
    brain.WithMemoryOptions(memory.WithFileStore("/tmp/brain-data")),
    brain.WithProviders(inference.NewOllamaProvider()),
    brain.WithSchedulerOptions(
        scheduler.WithTask(scheduler.Task{
            Name:     "heartbeat",
            Interval: 30 * time.Second,
            Level:    scheduler.Silent,
            Fn:       func() error { return nil },
        }),
    ),
    brain.WithWatchdogAlert(func(name string, err error, n int) {
        log.Printf("ALERT: %s failed %d times: %v", name, n, err)
    }),
    brain.WithHITLNotify(func(req hitl.Request) error {
        log.Printf("Approval needed: %s", req.Description)
        return nil
    }),
    brain.WithGuardianOptions(
        guardian.WithPatternThreshold(3),
    ),
    brain.WithRoutingOptions(
        routing.WithHalfLife(14 * 24 * time.Hour),
    ),
    brain.WithLifecycleOptions(
        lifecycle.WithRule(lifecycle.Rule{Pattern: "cache_*", Tier: lifecycle.TierEphemeral}),
    ),
    brain.WithBrowserOptions(
        browser.WithDebugURL("http://localhost:9222"),
    ),
)

if err := b.Robot.Start(); err != nil {
    log.Fatal(err)
}
```

## Components

### Memory Adaptor (`memory.Adaptor`)

Namespace key-value store implementing `gobot.Connection`.

**Backends:**
- `InMemory` (default) — `sync.RWMutex`-protected map
- `FileStore` — JSON files in a configurable directory
- `HTTPStore` — any HTTP key-value API

```go
mem := memory.NewAdaptor()                              // in-memory
mem := memory.NewAdaptor(memory.WithFileStore("./data")) // file-backed
mem := memory.NewAdaptor(memory.WithHTTPStore("http://localhost:8080")) // HTTP API

mem.Store("robots", "arm-1", map[string]any{"status": "active"})
val, _ := mem.Retrieve("robots", "arm-1")
keys, _ := mem.List("robots")
mem.Delete("robots", "arm-1")
```

**Events:** `"stored"`, `"retrieved"`, `"deleted"`, `"error"`

### Inference Driver (`inference.Driver`)

Multi-model LLM fallback chain. Tries providers in order until one succeeds.

```go
llm := inference.NewDriver(mem,
    inference.NewOllamaProvider(inference.WithOllamaModel("llama3")),
    inference.NewOpenAIProvider("sk-...", inference.WithOpenAIModel("gpt-4")),
)

result, err := llm.Infer("Analyze sensor readings")
result, err = llm.InferWithFramework(inference.ChainOfThought, "Diagnose motor failure")
```

**Built-in providers:** Ollama (local), OpenAI-compatible (any API).

**Reasoning frameworks:** `TreeOfThought`, `ChainOfThought`, `Adversarial`, `ReAct`.

**Events:** `"inference"`, `"fallback"`, `"error"`

### Scheduler Driver (`scheduler.Driver`)

Proactive timer system with automatic escalation on consecutive failures.

```go
sched := scheduler.NewDriver(mem,
    scheduler.WithEscalationThreshold(3),
    scheduler.WithTask(scheduler.Task{
        Name:     "battery-check",
        Interval: 1 * time.Minute,
        Level:    scheduler.Silent,
        Fn: func() error {
            // check battery level
            return nil
        },
    }),
)

sched.Add(scheduler.Task{Name: "cleanup", Interval: 5 * time.Minute, Fn: cleanupFn})
sched.Pause("cleanup")
sched.Resume("cleanup")
sched.Remove("cleanup")
```

**Escalation levels:** `Silent` → `Notify` → `Urgent` → `Escalate` → `Critical`

If a task fails N consecutive times, the level auto-escalates one step.

**Events:** `"tick"`, `"escalation"`, `"error"`, `"recovered"`

### Watchdog Driver (`watchdog.Driver`)

Fleet health monitoring with debounced alerting.

```go
wd := watchdog.NewDriver(mem,
    func(name string, err error, consecutive int) {
        log.Printf("ALERT: %s (failures: %d): %v", name, consecutive, err)
    },
    watchdog.WithAlertAfter(2),
)

wd.AddCheck(watchdog.Check{
    Name:     "motor-temp",
    Interval: 10 * time.Second,
    Timeout:  5 * time.Second,
    Fn: func() error {
        // read motor temperature
        return nil
    },
})

status := wd.Status()     // map[string]CheckStatus
allGood := wd.Healthy()   // true if all checks pass
```

Alerts fire only after N consecutive failures (configurable debounce). Auto-recovers and publishes recovery events.

**Events:** `"healthy"`, `"unhealthy"`, `"recovered"`, `"error"`

### HITL Driver (`hitl.Driver`)

Confirmation flow for risky robot actions.

```go
h := hitl.NewDriver(mem, func(req hitl.Request) error {
    // Send to Telegram, webhook, etc.
    return sendNotification(req.Description)
})

id, _ := h.RequestApproval(hitl.Request{
    Description: "Deploy firmware update",
    Timeout:     2 * time.Hour,
    Action: func() error {
        return deployFirmware()
    },
})

// Later, from a webhook callback:
h.Approve(id)   // executes the action
// or: h.Deny(id)

pending := h.Pending() // list pending requests
```

Requests auto-expire after their timeout. Actions execute only on approval.

**Events:** `"requested"`, `"approved"`, `"denied"`, `"expired"`, `"executed"`, `"error"`

### Guardian Driver (`guardian.Driver`)

Security monitoring with policy-based action filtering, pluggable probes, finding storage with pattern detection, and threat scoring.

```go
g := guardian.NewDriver(mem,
    guardian.WithPatternThreshold(3),
    guardian.WithEscalationThreshold(5),
)

// Add a policy to block dangerous actions.
g.AddPolicy(guardian.Policy{
    Name:        "no-force-push",
    Pattern:     `git\s+push\s+--force`,
    Risk:        guardian.RiskCritical,
    Alternative: "use --force-with-lease instead",
})

// Check if an action is allowed before executing.
allowed, denial := g.CheckAction("git push --force main")
if !allowed {
    log.Printf("Blocked: %s (risk: %s, try: %s)",
        denial.Policy, denial.Risk, denial.Alternative)
}

// Add pluggable security probes.
g.AddProbe(myPortScanner)
g.AddProbe(myFilePermChecker)

// Run all probes and detect patterns.
findings, _ := g.Scan()

// Set a baseline for drift detection.
g.SetBaseline()

// Get aggregate threat level (0-100).
level := g.ThreatLevel()
```

Findings are scored by risk: critical +40, high +25, medium +10, low +5. Pattern detection triggers at 3+ identical findings; escalation at 5+.

**Events:** `"finding"`, `"pattern_detected"`, `"escalation"`, `"policy_denied"`, `"scan_complete"`, `"baseline_drift"`, `"error"`

### Routing Driver (`routing.Driver`)

Confidence-aware worker/agent selection with colored flags, exponential decay scoring, Laplace-smoothed confidence, and a delta guard to prevent pathological swaps.

```go
r := routing.NewDriver(mem,
    routing.WithHalfLife(14 * 24 * time.Hour),
    routing.WithDeltaGuard(0.6),
    routing.WithExplorationRate(0.1),
)

// Record outcomes.
r.AddFlag(routing.Flag{Worker: "worker-a", TaskType: "build", Color: routing.Green})
r.AddFlag(routing.Flag{Worker: "worker-b", TaskType: "build", Color: routing.Red})

// Get the best worker for a task type.
decision := r.BestWorker("build")
fmt.Printf("Route to: %s (score: %.2f, confidence: %.2f, explored: %v)\n",
    decision.Worker, decision.Score, decision.Confidence, decision.Explored)

// Get full ranking.
ranking := r.Ranking("build")

// Check confidence for a specific worker.
conf := r.Confidence("worker-a", "build")
```

**Scoring:** Green flags +2, red -2, yellow 0 — all with exponential decay (14-day half-life default). Confidence uses Laplace smoothing: `(greens+1)/(total+2)`. Delta guard prevents swapping to a 2nd-place worker unless its score exceeds 1st × 0.6.

**Events:** `"routed"`, `"explored"`, `"flag_added"`, `"error"`

### Lifecycle Driver (`lifecycle.Driver`)

Data retention management with tiered classification, age tracking, stale detection, and automatic pruning.

```go
lc := lifecycle.NewDriver(mem,
    lifecycle.WithRule(lifecycle.Rule{Pattern: "critical_*", Tier: lifecycle.TierCritical}),
    lifecycle.WithRule(lifecycle.Rule{Pattern: "cache_*",    Tier: lifecycle.TierEphemeral}),
    lifecycle.WithRule(lifecycle.Rule{Pattern: "logs_*",     Tier: lifecycle.TierLow}),
    lifecycle.WithDefaultTier(lifecycle.TierMedium),
)

// Track data entries (can also auto-track via memory events).
lc.Track("cache_sessions", "user-123")
lc.Track("critical_config", "db-url")

// Check how old an entry is.
age := lc.Age("cache_sessions", "user-123")

// Audit current state.
report := lc.Audit()
fmt.Printf("Total: %d, Stale: %d\n", report.TotalEntries, report.StaleCount)

// Prune expired entries.
result := lc.Prune()
fmt.Printf("Pruned %d entries\n", result.Removed)
```

**Tiers:** Critical (never expires), High (90 days), Medium (30 days), Low (7 days), Ephemeral (3 days).

**Events:** `"pruned"`, `"stale_detected"`, `"audit_complete"`, `"error"`

### Browser Driver (`browser.Driver`)

Browser automation via Chrome DevTools Protocol: tab management, navigation, element interaction with human-like timing, content inspection, and JavaScript evaluation.

```go
br := browser.NewDriver(mem,
    browser.WithDebugURL("http://localhost:9222"),
    browser.WithCommandTimeout(10 * time.Second),
    browser.WithHumanDelay(50*time.Millisecond, 150*time.Millisecond),
)

// List open tabs.
tabs, _ := br.ListTabs()

// Navigate to a URL.
br.Navigate(tabs[0].ID, "http://example.com")

// Click an element (with human-like delay).
br.Click(tabs[0].ID, "#submit-button")

// Type text character by character.
br.Type(tabs[0].ID, "#search-input", "hello world")

// Press a key with modifiers.
br.PressKey(tabs[0].ID, "Enter")
br.PressKey(tabs[0].ID, "j", "ctrl")

// Wait for an element to appear.
br.WaitForElement(tabs[0].ID, ".results", browser.WaitVisible, 5*time.Second)

// Wait for text content.
br.WaitForElementWithText(tabs[0].ID, "#status", browser.WaitTextContains, "complete", 10*time.Second)

// Get accessibility tree snapshot.
snap, _ := br.Snapshot(tabs[0].ID)

// Take a screenshot.
png, _ := br.Screenshot(tabs[0].ID)

// Evaluate JavaScript.
result, _ := br.Eval(tabs[0].ID, "document.title")
```

Requires Chrome/Chromium running with `--remote-debugging-port=9222`. Human-like delays are added between interactions to avoid detection by anti-automation systems.

**Events:** `"navigated"`, `"clicked"`, `"typed"`, `"waited"`, `"error"`

## Custom Backends

Implement the `memory.Backend` interface to add your own storage:

```go
type Backend interface {
    Init() error
    Close() error
    Store(namespace, key string, value interface{}) error
    Retrieve(namespace, key string) (interface{}, error)
    Delete(namespace, key string) error
    List(namespace string) ([]string, error)
}

mem := memory.NewAdaptor(memory.WithBackend(myCustomBackend))
```

## Custom LLM Providers

Implement the `inference.Provider` interface:

```go
type Provider interface {
    Name() string
    Infer(ctx context.Context, prompt string, opts ...InferOption) (string, error)
}
```

## Custom Security Probes

Implement the `guardian.Probe` interface:

```go
type Probe interface {
    Name() string
    Run() ([]guardian.Finding, error)
}
```

## Events

All drivers publish events via GoBot's `Eventer` interface:

```go
llm.On("inference", func(data interface{}) {
    fmt.Println("Got result:", data)
})

llm.On("fallback", func(data interface{}) {
    fmt.Println("Provider failed, trying next:", data)
})

g.On("policy_denied", func(data interface{}) {
    denial := data.(guardian.PolicyDenial)
    fmt.Printf("Blocked: %s\n", denial.Action)
})

r.On("routed", func(data interface{}) {
    decision := data.(routing.Decision)
    fmt.Printf("Routed to: %s\n", decision.Worker)
})
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
