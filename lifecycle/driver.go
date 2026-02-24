// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle provides data retention policies and automatic pruning
// for the memory adaptor. It tracks when data was stored, assigns retention
// tiers, and periodically prunes expired entries. This prevents unbounded
// memory growth in long-running robots.
package lifecycle

import (
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Memory adaptor namespace used for persisting lifecycle metadata.
const metaNamespace = "lifecycle"

// Event names published by the lifecycle driver.
const (
	EventPruned        = "pruned"
	EventPruneComplete = "prune_complete"
	EventError         = "error"
)

// ---------------------------------------------------------------------------
// Retention tiers
// ---------------------------------------------------------------------------

// Tier represents a data retention tier with an associated default TTL.
type Tier int

const (
	Critical  Tier = iota // never expires
	High                  // 90 days
	Medium                // 30 days
	Low                   // 7 days
	Ephemeral             // 3 days
	Telemetry             // 14 days
)

// String returns a human-readable label for the tier.
func (t Tier) String() string {
	switch t {
	case Critical:
		return "critical"
	case High:
		return "high"
	case Medium:
		return "medium"
	case Low:
		return "low"
	case Ephemeral:
		return "ephemeral"
	case Telemetry:
		return "telemetry"
	}
	return fmt.Sprintf("tier(%d)", int(t))
}

// DefaultTTL returns the default time-to-live for this tier. Critical returns
// 0, meaning the entry never expires.
func (t Tier) DefaultTTL() time.Duration {
	switch t {
	case High:
		return 90 * 24 * time.Hour
	case Medium:
		return 30 * 24 * time.Hour
	case Low:
		return 7 * 24 * time.Hour
	case Ephemeral:
		return 3 * 24 * time.Hour
	case Telemetry:
		return 14 * 24 * time.Hour
	}
	return 0 // Critical: never expires
}

// ---------------------------------------------------------------------------
// Rule
// ---------------------------------------------------------------------------

// Rule maps a namespace to a retention tier with an optional TTL override.
type Rule struct {
	Namespace string        // which memory namespace this applies to (or "*" for all)
	Tier      Tier
	TTL       time.Duration // override tier default; 0 means use tier default
}

// ---------------------------------------------------------------------------
// Entry metadata (tracked internally)
// ---------------------------------------------------------------------------

type entryMeta struct {
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	Tier      Tier      `json:"tier"`
	StoredAt  time.Time `json:"stored_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero for Critical
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

// Stats contains current lifecycle statistics.
type Stats struct {
	Tracked   int          // total entries being tracked
	ByTier    map[Tier]int // count per tier
	LastPrune time.Time
	LastCount int // entries pruned in last run
}

// ---------------------------------------------------------------------------
// Option
// ---------------------------------------------------------------------------

// Option configures the lifecycle driver.
type Option func(*Driver)

// WithRule adds a retention rule at construction time.
func WithRule(r Rule) Option {
	return func(d *Driver) {
		d.mu.Lock()
		d.rules = append(d.rules, r)
		d.mu.Unlock()
	}
}

// WithPruneInterval sets the interval between automatic prune runs.
// The default is 1 hour.
func WithPruneInterval(interval time.Duration) Option {
	return func(d *Driver) {
		d.pruneInterval = interval
	}
}

// WithDefaultTier sets the tier applied to namespaces that match no rule.
// The default is Medium.
func WithDefaultTier(t Tier) Option {
	return func(d *Driver) {
		d.defaultTier = t
	}
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver is a GoBot v2 Device that manages data lifecycle: it classifies
// entries into retention tiers, tracks their age, and periodically prunes
// expired entries from the memory adaptor.
type Driver struct {
	name    string
	adaptor *memory.Adaptor

	mu            sync.RWMutex
	entries       map[string]*entryMeta // key: "namespace:key"
	rules         []Rule
	defaultTier   Tier
	pruneInterval time.Duration
	stats         Stats

	done chan struct{}

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a lifecycle driver attached to the given memory adaptor.
func NewDriver(a *memory.Adaptor, opts ...Option) *Driver {
	d := &Driver{
		name:          "lifecycle",
		adaptor:       a,
		entries:       make(map[string]*entryMeta),
		defaultTier:   Medium,
		pruneInterval: time.Hour,
		stats: Stats{
			ByTier: make(map[Tier]int),
		},
		Eventer:   gobot.NewEventer(),
		Commander: gobot.NewCommander(),
	}
	for _, opt := range opts {
		opt(d)
	}

	d.AddEvent(EventPruned)
	d.AddEvent(EventPruneComplete)
	d.AddEvent(EventError)

	d.AddCommand("prune", func(params map[string]interface{}) interface{} {
		count, err := d.Prune()
		if err != nil {
			return err
		}
		return count
	})
	d.AddCommand("stats", func(params map[string]interface{}) interface{} {
		return d.Stats()
	})
	d.AddCommand("rules", func(params map[string]interface{}) interface{} {
		return d.Rules()
	})

	return d
}

// ---------------------------------------------------------------------------
// gobot.Device interface
// ---------------------------------------------------------------------------

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start launches a background goroutine that runs Prune() at the configured
// interval (default every 1 hour).
func (d *Driver) Start() error {
	d.mu.Lock()
	d.done = make(chan struct{})
	d.mu.Unlock()

	go d.pruneLoop()
	return nil
}

// Halt stops the background pruning goroutine.
func (d *Driver) Halt() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done != nil {
		select {
		case <-d.done:
			// already closed
		default:
			close(d.done)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// AddRule adds a retention rule. If a rule for the same namespace already
// exists, it is replaced.
func (d *Driver) AddRule(r Rule) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, existing := range d.rules {
		if existing.Namespace == r.Namespace {
			d.rules[i] = r
			return
		}
	}
	d.rules = append(d.rules, r)
}

// RemoveRule removes the rule for the given namespace.
func (d *Driver) RemoveRule(namespace string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, r := range d.rules {
		if r.Namespace == namespace {
			d.rules = append(d.rules[:i], d.rules[i+1:]...)
			return
		}
	}
}

// Track registers an entry for lifecycle tracking. The entry's expiration is
// computed from its tier's TTL (or a rule's custom TTL). If a rule for the
// namespace exists (or a wildcard "*" rule), its tier and TTL are used;
// otherwise the default tier applies.
func (d *Driver) Track(namespace, key string, tier Tier) {
	now := time.Now()
	ek := namespace + ":" + key

	// Find a rule for this namespace to get any TTL override.
	ttl := tier.DefaultTTL()

	d.mu.RLock()
	for _, r := range d.rules {
		if r.Namespace == namespace || r.Namespace == "*" {
			if r.TTL > 0 {
				ttl = r.TTL
			}
			break
		}
	}
	d.mu.RUnlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	meta := &entryMeta{
		Namespace: namespace,
		Key:       key,
		Tier:      tier,
		StoredAt:  now,
		ExpiresAt: expiresAt,
	}

	d.mu.Lock()
	d.entries[ek] = meta
	d.mu.Unlock()

	// Persist metadata to memory adaptor.
	_ = d.adaptor.Store(metaNamespace, ek, meta)
}

// Prune iterates all tracked entries and deletes those whose ExpiresAt has
// passed (and is non-zero). Returns the count of deleted entries.
func (d *Driver) Prune() (int, error) {
	now := time.Now()

	d.mu.Lock()

	var toDelete []string
	var metas []*entryMeta

	for ek, e := range d.entries {
		if e.ExpiresAt.IsZero() {
			continue // Critical: never prune
		}
		if now.After(e.ExpiresAt) {
			toDelete = append(toDelete, ek)
			metas = append(metas, e)
		}
	}

	for _, ek := range toDelete {
		delete(d.entries, ek)
	}

	// Update stats.
	d.stats.LastPrune = now
	d.stats.LastCount = len(toDelete)
	d.mu.Unlock()

	// Perform adaptor deletions and publish events outside the lock.
	var firstErr error
	for i, meta := range metas {
		if err := d.adaptor.Delete(meta.Namespace, meta.Key); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			d.Publish(EventError, err)
		}
		// Also clean up lifecycle metadata.
		_ = d.adaptor.Delete(metaNamespace, toDelete[i])

		d.Publish(EventPruned, map[string]string{
			"namespace": meta.Namespace,
			"key":       meta.Key,
		})
	}

	d.Publish(EventPruneComplete, len(toDelete))

	return len(toDelete), firstErr
}

// Stats returns current lifecycle statistics.
func (d *Driver) Stats() Stats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	byTier := make(map[Tier]int)
	for _, e := range d.entries {
		byTier[e.Tier]++
	}

	return Stats{
		Tracked:   len(d.entries),
		ByTier:    byTier,
		LastPrune: d.stats.LastPrune,
		LastCount: d.stats.LastCount,
	}
}

// Rules returns a copy of the current rule list.
func (d *Driver) Rules() []Rule {
	d.mu.RLock()
	defer d.mu.RUnlock()

	out := make([]Rule, len(d.rules))
	copy(out, d.rules)
	return out
}

// ---------------------------------------------------------------------------
// Background pruning loop
// ---------------------------------------------------------------------------

func (d *Driver) pruneLoop() {
	ticker := time.NewTicker(d.pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			if _, err := d.Prune(); err != nil {
				d.Publish(EventError, err)
			}
		}
	}
}
