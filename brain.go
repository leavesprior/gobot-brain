// Package brain provides a GoBot v2 plugin that adds persistent memory,
// LLM inference, proactive scheduling, fleet health monitoring,
// human-in-the-loop confirmation flows, security monitoring, confidence-aware
// routing, data lifecycle management, and browser automation to any GoBot robot.
//
// See the sub-packages for individual component documentation:
//   - memory: namespace key-value store (Connection/Adaptor)
//   - inference: multi-model LLM fallback chain (Driver)
//   - scheduler: proactive timers with escalation (Driver)
//   - watchdog: fleet health monitoring (Driver)
//   - hitl: human-in-the-loop confirmation (Driver)
//   - guardian: security monitoring with policy enforcement (Driver)
//   - routing: confidence-aware worker selection (Driver)
//   - lifecycle: data retention and pruning (Driver)
//   - browser: Chrome DevTools Protocol automation (Driver)
package brain

import (
	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/browser"
	"github.com/leavesprior/gobot-brain/guardian"
	"github.com/leavesprior/gobot-brain/hitl"
	"github.com/leavesprior/gobot-brain/inference"
	"github.com/leavesprior/gobot-brain/lifecycle"
	"github.com/leavesprior/gobot-brain/memory"
	"github.com/leavesprior/gobot-brain/routing"
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
	Guardian  *guardian.Driver
	Routing   *routing.Driver
	Lifecycle *lifecycle.Driver
	Browser   *browser.Driver
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
	guardianOpts  []guardian.Option
	routingOpts   []routing.Option
	lifecycleOpts []lifecycle.Option
	browserOpts   []browser.Option
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

// WithGuardianOptions sets options for the guardian driver.
func WithGuardianOptions(opts ...guardian.Option) BrainOption {
	return func(c *brainConfig) {
		c.guardianOpts = append(c.guardianOpts, opts...)
	}
}

// WithRoutingOptions sets options for the routing driver.
func WithRoutingOptions(opts ...routing.Option) BrainOption {
	return func(c *brainConfig) {
		c.routingOpts = append(c.routingOpts, opts...)
	}
}

// WithLifecycleOptions sets options for the lifecycle driver.
func WithLifecycleOptions(opts ...lifecycle.Option) BrainOption {
	return func(c *brainConfig) {
		c.lifecycleOpts = append(c.lifecycleOpts, opts...)
	}
}

// WithBrowserOptions sets options for the browser driver.
func WithBrowserOptions(opts ...browser.Option) BrainOption {
	return func(c *brainConfig) {
		c.browserOpts = append(c.browserOpts, opts...)
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

	g := guardian.NewDriver(mem, cfg.guardianOpts...)
	r := routing.NewDriver(mem, cfg.routingOpts...)
	lc := lifecycle.NewDriver(mem, cfg.lifecycleOpts...)
	br := browser.NewDriver(mem, cfg.browserOpts...)

	connections := []gobot.Connection{mem}
	connections = append(connections, cfg.extraConns...)

	devices := []gobot.Device{inf, sched, wd, h, g, r, lc, br}
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
		Guardian:  g,
		Routing:   r,
		Lifecycle: lc,
		Browser:   br,
		Robot:     robot,
	}
}
