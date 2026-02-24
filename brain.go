// Package brain provides a GoBot v2 plugin that adds persistent memory,
// LLM inference, proactive scheduling, fleet health monitoring, and
// human-in-the-loop confirmation flows to any GoBot robot.
//
// See the sub-packages for individual component documentation:
//   - memory: namespace key-value store (Connection/Adaptor)
//   - inference: multi-model LLM fallback chain (Driver)
//   - scheduler: proactive timers with escalation (Driver)
//   - watchdog: fleet health monitoring (Driver)
//   - hitl: human-in-the-loop confirmation (Driver)
package brain

import (
	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/hitl"
	"github.com/leavesprior/gobot-brain/inference"
	"github.com/leavesprior/gobot-brain/memory"
	"github.com/leavesprior/gobot-brain/scheduler"
	"github.com/leavesprior/gobot-brain/watchdog"
)

// Brain holds references to all gobot-brain components for easy access
// after construction.
type Brain struct {
	Memory    *memory.Adaptor
	Inference *inference.Driver
	Scheduler *scheduler.Driver
	Watchdog  *watchdog.Driver
	HITL      *hitl.Driver
	Robot     *gobot.Robot
}

// BrainOption configures a Brain during construction.
type BrainOption func(*brainConfig)

type brainConfig struct {
	memoryOpts    []memory.Option
	providers     []inference.Provider
	schedulerOpts []scheduler.Option
	watchdogAlert watchdog.AlertFunc
	watchdogOpts  []watchdog.DriverOption
	hitlNotify    hitl.NotifyFunc
	extraDevices  []gobot.Device
	extraConns    []gobot.Connection
	work          func(*gobot.Robot)
}

// WithMemoryOptions sets options for the memory adaptor (e.g. WithFileStore).
func WithMemoryOptions(opts ...memory.Option) BrainOption {
	return func(c *brainConfig) {
		c.memoryOpts = append(c.memoryOpts, opts...)
	}
}

// WithProviders sets the LLM providers for the inference driver.
func WithProviders(providers ...inference.Provider) BrainOption {
	return func(c *brainConfig) {
		c.providers = append(c.providers, providers...)
	}
}

// WithSchedulerOptions sets options for the scheduler driver.
func WithSchedulerOptions(opts ...scheduler.Option) BrainOption {
	return func(c *brainConfig) {
		c.schedulerOpts = append(c.schedulerOpts, opts...)
	}
}

// WithWatchdogAlert sets the alert function for the watchdog driver.
func WithWatchdogAlert(fn watchdog.AlertFunc) BrainOption {
	return func(c *brainConfig) {
		c.watchdogAlert = fn
	}
}

// WithWatchdogOptions sets options for the watchdog driver.
func WithWatchdogOptions(opts ...watchdog.DriverOption) BrainOption {
	return func(c *brainConfig) {
		c.watchdogOpts = append(c.watchdogOpts, opts...)
	}
}

// WithHITLNotify sets the notification function for the HITL driver.
func WithHITLNotify(fn hitl.NotifyFunc) BrainOption {
	return func(c *brainConfig) {
		c.hitlNotify = fn
	}
}

// WithDevices adds extra gobot devices to the robot.
func WithDevices(devices ...gobot.Device) BrainOption {
	return func(c *brainConfig) {
		c.extraDevices = append(c.extraDevices, devices...)
	}
}

// WithConnections adds extra gobot connections to the robot.
func WithConnections(conns ...gobot.Connection) BrainOption {
	return func(c *brainConfig) {
		c.extraConns = append(c.extraConns, conns...)
	}
}

// WithWork sets the robot's work function.
func WithWork(fn func(*gobot.Robot)) BrainOption {
	return func(c *brainConfig) {
		c.work = fn
	}
}

// NewBrain creates a fully-wired gobot.Robot with all gobot-brain components.
// The returned Brain struct provides direct access to each component.
func NewBrain(name string, opts ...BrainOption) *Brain {
	cfg := &brainConfig{}
	for _, o := range opts {
		o(cfg)
	}

	mem := memory.NewAdaptor(cfg.memoryOpts...)

	inf := inference.NewDriver(mem, cfg.providers...)

	sched := scheduler.NewDriver(mem, cfg.schedulerOpts...)

	alertFn := cfg.watchdogAlert
	if alertFn == nil {
		alertFn = func(string, error, int) {} // no-op default
	}
	wd := watchdog.NewDriver(mem, alertFn, cfg.watchdogOpts...)

	notifyFn := cfg.hitlNotify
	if notifyFn == nil {
		notifyFn = func(hitl.Request) error { return nil } // no-op default
	}
	h := hitl.NewDriver(mem, notifyFn)

	connections := []gobot.Connection{mem}
	connections = append(connections, cfg.extraConns...)

	devices := []gobot.Device{inf, sched, wd, h}
	devices = append(devices, cfg.extraDevices...)

	robot := gobot.NewRobot(name,
		connections,
		devices,
		cfg.work,
	)

	return &Brain{
		Memory:    mem,
		Inference: inf,
		Scheduler: sched,
		Watchdog:  wd,
		HITL:      h,
		Robot:     robot,
	}
}
