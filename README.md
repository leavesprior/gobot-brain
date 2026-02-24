# gobot-brain

A community plugin for [GoBot v2](https://github.com/hybridgroup/gobot) that adds persistent memory, LLM reasoning, proactive scheduling, fleet health monitoring, and human-in-the-loop confirmation flows.

GoBot provides hardware abstraction for 35+ robotics platforms. **gobot-brain** fills the gaps: state that survives restarts, AI-powered decision making, scheduled tasks with automatic escalation, health checks with alerting, and safe confirmation gates for risky actions.

## Architecture

```
Robot
├── Connection: memory.Adaptor    (namespace key-value store)
└── Devices:
    ├── inference.Driver           (LLM fallback chain)
    ├── scheduler.Driver           (proactive timers + escalation)
    ├── watchdog.Driver            (fleet health monitoring)
    └── hitl.Driver                (human-in-the-loop confirmation)
```

The Memory Adaptor is the **Connection** (foundation). All four Drivers use it for state persistence. Each component can be used independently or together via the `NewBrain` convenience constructor.

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

## Events

All drivers publish events via GoBot's `Eventer` interface:

```go
llm.On("inference", func(data interface{}) {
    fmt.Println("Got result:", data)
})

llm.On("fallback", func(data interface{}) {
    fmt.Println("Provider failed, trying next:", data)
})
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
