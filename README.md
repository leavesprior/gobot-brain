<p align="center">
  <h1 align="center">gobot-brain</h1>
  <p align="center">
    The missing operational layer for <a href="https://github.com/hybridgroup/gobot">GoBot</a> robots.
    <br/>
    Memory. Intelligence. Autonomy. Safety.
  </p>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/leavesprior/gobot-brain"><img src="https://pkg.go.dev/badge/github.com/leavesprior/gobot-brain.svg" alt="Go Reference"></a>
  <a href="https://github.com/leavesprior/gobot-brain/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
  <a href="https://goreportcard.com/report/github.com/leavesprior/gobot-brain"><img src="https://goreportcard.com/badge/github.com/leavesprior/gobot-brain" alt="Go Report Card"></a>
</p>

---

## The Problem

GoBot gives you hardware abstraction for 35+ robotics platforms. That's the easy part.

The hard part is everything else: your robot forgets its state on every restart. It can't reason about sensor data. It has no concept of "check this every 5 minutes and escalate if it keeps failing." There's no health monitoring across a fleet. No safety gate before a dangerous action. No way to route tasks to the best worker. No cleanup of old data. No browser automation for dashboards.

You end up building the same operational plumbing for every project.

**gobot-brain** is that plumbing — packaged as standard GoBot Adaptors and Drivers that plug in like any other component.

## How It Works

```
┌─────────────────────────────────────────────────────────────────┐
│                         Your Robot                              │
│                                                                 │
│  ┌──────────┐  "arm overheating"  ┌───────────┐                │
│  │ Watchdog ├────────────────────►│ Inference  │ ◄── LLM chain  │
│  │ (health) │                     │ (diagnose) │     Ollama →   │
│  └────┬─────┘                     └─────┬──────┘     OpenAI     │
│       │ unhealthy                       │ "check coolant"       │
│       ▼                                 ▼                       │
│  ┌──────────┐  "notify operator"  ┌──────────┐                 │
│  │Scheduler ├────────────────────►│   HITL   │ ◄── webhook/    │
│  │ (timers) │  escalate after 3x  │ (approve) │     Telegram    │
│  └──────────┘                     └─────┬──────┘                │
│                                         │ approved              │
│                                         ▼                       │
│  ┌──────────┐  "safe to move?"    ┌──────────┐                 │
│  │ Guardian ├◄───────────────────►│ Routing  │ ◄── pick best   │
│  │ (policy) │  enforce policies   │ (assign) │     worker      │
│  └──────────┘                     └──────────┘                  │
│       │                                                         │
│       ▼                                                         │
│  ┌──────────┐    prune expired    ┌──────────┐                 │
│  │Lifecycle ├────────────────────►│ Browser  │ ◄── dashboard   │
│  │ (retain) │    telemetry data   │  (CDP)   │     automation  │
│  └──────────┘                     └──────────┘                  │
│       │               │               │              │          │
│       └───────────────┴───────────────┴──────────────┘          │
│                               │                                 │
│                        ┌──────┴──────┐                          │
│                        │   Memory    │ ◄── persists everything  │
│                        │  (state)    │     file / HTTP / RAM    │
│                        └─────────────┘                          │
└─────────────────────────────────────────────────────────────────┘
```

**Memory** is the foundation — every driver persists its state through it. The other eight drivers build on each other: **Watchdog** detects a problem, **Inference** diagnoses it, **Scheduler** escalates if it persists, **HITL** gates the fix behind human approval, **Guardian** checks safety policies before acting, **Routing** picks the best worker, **Lifecycle** cleans up old data, and **Browser** automates dashboard interactions.

## Install

```bash
go get github.com/leavesprior/gobot-brain
```

Requires Go 1.22+ and GoBot v2.

## Real-World Example: Warehouse Pick Fleet

Here's how all nine components work together in a warehouse with multiple robot arms:

```go
b := brain.NewBrain("warehouse-fleet",
    // State survives restarts.
    brain.WithMemoryOptions(memory.WithFileStore("/var/lib/robot/state")),

    // Local LLM for diagnostics when things go wrong.
    brain.WithProviders(inference.NewOllamaProvider()),

    // Inventory check every 5 min. Auto-escalates after 3 consecutive failures.
    brain.WithSchedulerOptions(
        scheduler.WithEscalationThreshold(3),
        scheduler.WithTask(scheduler.Task{
            Name: "inventory-check", Interval: 5 * time.Minute,
            Level: scheduler.Silent, Fn: checkInventory,
        }),
    ),

    // Fleet health monitoring. Alert on 2+ consecutive failures.
    brain.WithWatchdogAlert(sendToPagerDuty),
    brain.WithWatchdogOptions(watchdog.WithAlertAfter(2)),

    // Firmware updates need human approval.
    brain.WithHITLNotify(sendToTelegram),

    // Safety: no arm movement while charging, warn on heavy payloads.
    brain.WithGuardianOptions(
        guardian.WithPolicy(noMoveWhileCharging),
        guardian.WithPolicy(heavyPayloadWarning),
    ),

    // Three arms. Route tasks to the most reliable one.
    brain.WithRoutingOptions(
        routing.WithWorker(routing.Worker{Name: "arm-alpha", Capabilities: []string{"pick", "place"}}),
        routing.WithWorker(routing.Worker{Name: "arm-beta",  Capabilities: []string{"pick", "place"}}),
        routing.WithWorker(routing.Worker{Name: "arm-gamma", Capabilities: []string{"pick", "place", "weld"}}),
        routing.WithExplorationRate(0.1),
    ),

    // Telemetry expires after 14 days. Config never expires.
    brain.WithLifecycleOptions(
        lifecycle.WithRule(lifecycle.Rule{Namespace: "telemetry", Tier: lifecycle.Telemetry}),
        lifecycle.WithRule(lifecycle.Rule{Namespace: "config", Tier: lifecycle.Critical}),
    ),
)
```

Then in the work loop, the components interact naturally:

```go
brain.WithWork(func(r *gobot.Robot) {
    gobot.Every(15*time.Second, func() {
        // Route: pick the best arm for this task.
        worker, _ := b.Routing.Route("pick")

        // Guard: check safety policies before moving.
        err := b.Guardian.Guard(
            guardian.Action{Name: "pick", Source: worker, Parameters: params},
            func() error { return executePickOn(worker) },
        )

        // Report: feed the result back to improve future routing.
        b.Routing.Report(routing.Result{
            Worker: worker, TaskType: "pick",
            Success: err == nil, Time: time.Now(),
        })

        // Track: register telemetry for lifecycle pruning.
        b.Lifecycle.Track("telemetry", entryKey, lifecycle.Telemetry)
    })

    // When watchdog detects a problem, ask the LLM to diagnose it.
    b.Watchdog.On("unhealthy", func(data interface{}) {
        diagnosis, _ := b.Inference.InferWithFramework(
            inference.ChainOfThought,
            fmt.Sprintf("Robot arm health check failing: %v. Likely causes?", data),
        )
        log.Println(diagnosis)
    })
})
```

See [`examples/warehouse/main.go`](examples/warehouse/main.go) for the complete runnable version.

## Components at a Glance

| Component | What it does | GoBot type |
|-----------|-------------|------------|
| [**Memory**](#memory) | Namespace key-value store (in-memory, file, HTTP) | Connection |
| [**Inference**](#inference) | LLM fallback chain with reasoning frameworks | Device |
| [**Scheduler**](#scheduler) | Periodic tasks with 5-level auto-escalation | Device |
| [**Watchdog**](#watchdog) | Fleet health checks with debounced alerting | Device |
| [**HITL**](#hitl) | Human approval gates with auto-expiry | Device |
| [**Guardian**](#guardian) | Security policy enforcement with audit trail | Device |
| [**Routing**](#routing) | Confidence-aware task assignment (bandit algorithm) | Device |
| [**Lifecycle**](#lifecycle) | Data retention tiers with automatic pruning | Device |
| [**Browser**](#browser) | Chrome DevTools Protocol automation | Device |

Every component publishes [GoBot events](#events), registers [commands](#commands), persists state through Memory, and can be used independently or wired together with `NewBrain()`.

---

## Memory

Namespace key-value store. The foundation — all other drivers persist through it.

```go
mem := memory.NewAdaptor()                                        // in-memory
mem := memory.NewAdaptor(memory.WithFileStore("./robot-state"))   // JSON files on disk
mem := memory.NewAdaptor(memory.WithHTTPStore("http://kv:8080"))  // any HTTP API

mem.Store("fleet", "arm-1", map[string]any{"status": "active", "temp": 42.3})
val, _ := mem.Retrieve("fleet", "arm-1")
keys, _ := mem.List("fleet")
mem.Delete("fleet", "arm-1")
```

**Backends are pluggable** — implement `memory.Backend` to add Redis, etcd, or anything else.

Events: `stored` `retrieved` `deleted` `error`

## Inference

Multi-model LLM fallback chain. First provider fails? Tries the next. Includes reasoning framework wrappers.

```go
llm := inference.NewDriver(mem,
    inference.NewOllamaProvider(inference.WithOllamaModel("llama3")),
    inference.NewOpenAIProvider("sk-...", inference.WithOpenAIModel("gpt-4")),
)

result, _ := llm.Infer("Sensor reads 95C on motor 3. Is this normal?")
result, _ = llm.InferWithFramework(inference.Adversarial, "Review safety of proposed arm trajectory")
```

**Frameworks:** `ChainOfThought` (step-by-step), `TreeOfThought` (branch and evaluate), `Adversarial` (find weaknesses), `ReAct` (reason-act-observe loop).

Events: `inference` `fallback` `error`

## Scheduler

Proactive timers that auto-escalate when tasks fail repeatedly.

```go
sched := scheduler.NewDriver(mem, scheduler.WithEscalationThreshold(3))
sched.Add(scheduler.Task{
    Name: "battery-check", Interval: time.Minute, Level: scheduler.Silent,
    Fn: func() error { return checkBattery() },
})
```

Fails 3 times in a row? Escalates: **Silent** → **Notify** → **Urgent** → **Escalate** → **Critical**. Recovers automatically when the task starts passing again.

Events: `tick` `escalation` `recovered` `error`

## Watchdog

Fleet health monitoring with debounced alerting. No alert spam — fires only after N consecutive failures.

```go
wd := watchdog.NewDriver(mem, alertFn, watchdog.WithAlertAfter(2))
wd.AddCheck(watchdog.Check{
    Name: "motor-temp", Interval: 10 * time.Second, Timeout: 5 * time.Second,
    Fn: func() error { return readMotorTemp() },
})

wd.Healthy()  // true if all checks passing
wd.Status()   // per-check details
```

Events: `healthy` `unhealthy` `recovered` `error`

## HITL

Human-in-the-loop confirmation. Risky action? Ask a human first. Auto-expires if nobody responds.

```go
h := hitl.NewDriver(mem, sendToTelegram)
id, _ := h.RequestApproval(hitl.Request{
    Description: "Deploy firmware v2.4.1 to all arms",
    Timeout:     2 * time.Hour,
    Action:      func() error { return deployFirmware() },
})

// Later, from webhook callback:
h.Approve(id)  // runs the action
h.Deny(id)     // blocks it
```

Events: `requested` `approved` `denied` `expired` `executed` `error`

## Guardian

Security policy enforcement. Define rules, gate actions, keep an audit trail.

```go
g := guardian.NewDriver(mem,
    guardian.WithPolicy(guardian.Policy{
        Name: "no-move-while-charging", Severity: guardian.Blocked,
        Check: func(a guardian.Action) guardian.Decision {
            if a.Name == "move_arm" && a.Parameters["charging"].(bool) {
                return guardian.Decision{Allowed: false, Reason: "charging"}
            }
            return guardian.Decision{Allowed: true}
        },
    }),
)

// Gate execution behind policy check:
err := g.Guard(action, func() error { return moveArm() })
// Blocked actions never execute. Full audit trail in g.AuditLog().
```

Severity levels: `Info` → `Warning` → `Critical` → `Blocked`. Worst policy wins.

Events: `evaluated` `blocked` `violation` `error`

## Routing

Confidence-aware task assignment. Tracks success rates per worker, uses Laplace smoothing and recency weighting. Includes exploration (try new workers) and a delta guard (prevent oscillation).

```go
r := routing.NewDriver(mem, routing.WithExplorationRate(0.1))
r.Register(routing.Worker{Name: "arm-1", Capabilities: []string{"pick", "place"}})
r.Register(routing.Worker{Name: "arm-2", Capabilities: []string{"pick", "place"}})

worker, _ := r.Route("pick")  // picks the best arm
r.Report(routing.Result{Worker: worker, TaskType: "pick", Success: true})
r.Scores("pick")              // {"arm-1": 0.72, "arm-2": 0.58}
```

**Scoring:** `(wins+1)/(wins+losses+2)` * recency decay. New workers get a fair 0.5 base score. 10% exploration rate means occasionally trying non-optimal workers to discover improvements.

Events: `routed` `reported` `exploration` `error`

## Lifecycle

Data retention with automatic pruning. Six tiers, background cleanup, prevents unbounded growth.

```go
lc := lifecycle.NewDriver(mem,
    lifecycle.WithRule(lifecycle.Rule{Namespace: "telemetry", Tier: lifecycle.Telemetry}),
    lifecycle.WithRule(lifecycle.Rule{Namespace: "config",    Tier: lifecycle.Critical}),
    lifecycle.WithPruneInterval(10 * time.Minute),
)
lc.Track("telemetry", "reading-001", lifecycle.Telemetry)
```

| Tier | TTL | Use case |
|------|-----|----------|
| Critical | Never | Configuration, calibration data |
| High | 90 days | Task history, performance logs |
| Medium | 30 days | General operational data |
| Low | 7 days | Debug logs, temporary state |
| Ephemeral | 3 days | Session data, scratch values |
| Telemetry | 14 days | Sensor readings, metrics |

Events: `pruned` `prune_complete` `error`

## Browser

Chrome DevTools Protocol automation via a pluggable Transport. No websocket dependency — bring your own.

```go
br := browser.NewDriver(mem,
    browser.WithTransport(myTransport),
    browser.WithEndpoint("ws://localhost:9222"),
)
br.Navigate("http://warehouse-dashboard.local")
br.WaitFor(".order-table", 5*time.Second)
br.Click("#refresh-button")
title, _ := br.Eval("document.title")
png, _ := br.Screenshot()
```

```go
// Implement this interface to connect to Chrome:
type Transport interface {
    Connect(endpoint string) error
    Close() error
    Send(method string, params map[string]interface{}) (json.RawMessage, error)
}
```

Events: `navigated` `evaluated` `clicked` `screenshot` `error`

---

## Events

All drivers publish events through GoBot's standard `Eventer` interface. Wire them together:

```go
// Watchdog detects failure → Inference diagnoses → HITL requests approval
b.Watchdog.On("unhealthy", func(data interface{}) {
    diagnosis, _ := b.Inference.Infer(fmt.Sprintf("Motor failing: %v. Cause?", data))
    b.HITL.RequestApproval(hitl.Request{
        Description: fmt.Sprintf("Motor repair needed: %s", diagnosis),
        Action:      func() error { return repairMotor() },
    })
})

// Guardian blocks something → log to audit
b.Guardian.On("blocked", func(data interface{}) {
    log.Printf("Security policy blocked an action: %v", data)
})

// Routing explores → track for analysis
b.Routing.On("exploration", func(data interface{}) {
    log.Printf("Exploration pick: %v", data)
})
```

## Commands

All drivers register commands via GoBot's `Commander` interface, making them accessible through GoBot's REST API:

```go
// Each driver exposes commands:
b.Inference.Command("infer")     // run inference
b.Guardian.Command("evaluate")   // check a policy
b.Routing.Command("route")       // route a task
b.Lifecycle.Command("prune")     // trigger manual prune
b.Watchdog.Command("status")     // get health status
```

## Extensibility

**Custom memory backend:**
```go
type Backend interface {
    Init() error
    Close() error
    Store(namespace, key string, value interface{}) error
    Retrieve(namespace, key string) (interface{}, error)
    Delete(namespace, key string) error
    List(namespace string) ([]string, error)
}
mem := memory.NewAdaptor(memory.WithBackend(myRedisBackend))
```

**Custom LLM provider:**
```go
type Provider interface {
    Name() string
    Infer(ctx context.Context, prompt string, opts ...InferOption) (string, error)
}
llm := inference.NewDriver(mem, myCustomProvider)
```

**Custom browser transport:**
```go
type Transport interface {
    Connect(endpoint string) error
    Close() error
    Send(method string, params map[string]interface{}) (json.RawMessage, error)
}
br := browser.NewDriver(mem, browser.WithTransport(myWSTransport))
```

## Project Status

- 9 components, all implementing standard GoBot v2 interfaces
- 120+ unit tests, all passing
- `go vet` and `go build` clean
- Zero external dependencies beyond GoBot v2 and Go stdlib
- Apache 2.0 licensed

## License

[Apache License 2.0](LICENSE)
