// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"sync/atomic"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time check that Driver satisfies gobot.Device.
var _ gobot.Device = (*Driver)(nil)

func newTestDriver(opts ...Option) (*Driver, *memory.Adaptor) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	d := NewDriver(a, opts...)
	return d, a
}

// ---------------------------------------------------------------------------
// Name / SetName / Connection
// ---------------------------------------------------------------------------

func TestNameSetNameConnection(t *testing.T) {
	d, a := newTestDriver()

	if d.Name() != "lifecycle" {
		t.Fatalf("expected name 'lifecycle', got %q", d.Name())
	}
	d.SetName("custom")
	if d.Name() != "custom" {
		t.Fatalf("expected name 'custom', got %q", d.Name())
	}
	if d.Connection() != a {
		t.Fatal("expected Connection() to return the adaptor")
	}
}

// ---------------------------------------------------------------------------
// Tier.String() and Tier.DefaultTTL()
// ---------------------------------------------------------------------------

func TestTierString(t *testing.T) {
	tests := []struct {
		tier Tier
		want string
	}{
		{Critical, "critical"},
		{High, "high"},
		{Medium, "medium"},
		{Low, "low"},
		{Ephemeral, "ephemeral"},
		{Telemetry, "telemetry"},
		{Tier(99), "tier(99)"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("Tier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestTierDefaultTTL(t *testing.T) {
	tests := []struct {
		tier Tier
		want time.Duration
	}{
		{Critical, 0},
		{High, 90 * 24 * time.Hour},
		{Medium, 30 * 24 * time.Hour},
		{Low, 7 * 24 * time.Hour},
		{Ephemeral, 3 * 24 * time.Hour},
		{Telemetry, 14 * 24 * time.Hour},
	}
	for _, tt := range tests {
		if got := tt.tier.DefaultTTL(); got != tt.want {
			t.Errorf("Tier(%d).DefaultTTL() = %v, want %v", tt.tier, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Track adds entry metadata
// ---------------------------------------------------------------------------

func TestTrackAddsEntryMetadata(t *testing.T) {
	d, _ := newTestDriver()

	d.Track("sensors", "temp_reading", Low)

	stats := d.Stats()
	if stats.Tracked != 1 {
		t.Fatalf("expected 1 tracked entry, got %d", stats.Tracked)
	}
	if stats.ByTier[Low] != 1 {
		t.Fatalf("expected 1 Low tier entry, got %d", stats.ByTier[Low])
	}

	// Verify the entry metadata is correct.
	d.mu.RLock()
	meta, ok := d.entries["sensors:temp_reading"]
	d.mu.RUnlock()
	if !ok {
		t.Fatal("expected entry to be tracked")
	}
	if meta.Namespace != "sensors" || meta.Key != "temp_reading" {
		t.Fatalf("unexpected namespace/key: %s/%s", meta.Namespace, meta.Key)
	}
	if meta.Tier != Low {
		t.Fatalf("expected tier Low, got %s", meta.Tier)
	}
	if meta.StoredAt.IsZero() {
		t.Fatal("expected non-zero StoredAt")
	}
	if meta.ExpiresAt.IsZero() {
		t.Fatal("expected non-zero ExpiresAt for Low tier")
	}
}

// ---------------------------------------------------------------------------
// Prune deletes expired entries from memory adaptor
// ---------------------------------------------------------------------------

func TestPruneDeletesExpiredEntries(t *testing.T) {
	d, a := newTestDriver()

	// Store a value in the memory adaptor that the prune should delete.
	_ = a.Store("logs", "old_entry", "some data")

	// Manually insert an expired entry.
	d.mu.Lock()
	d.entries["logs:old_entry"] = &entryMeta{
		Namespace: "logs",
		Key:       "old_entry",
		Tier:      Low,
		StoredAt:  time.Now().Add(-10 * 24 * time.Hour),
		ExpiresAt: time.Now().Add(-3 * 24 * time.Hour), // expired 3 days ago
	}
	d.mu.Unlock()

	count, err := d.Prune()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 pruned, got %d", count)
	}

	// Verify the entry was deleted from the memory adaptor.
	_, err = a.Retrieve("logs", "old_entry")
	if err == nil {
		t.Fatal("expected entry to be deleted from memory adaptor")
	}

	// Verify it was removed from tracking.
	stats := d.Stats()
	if stats.Tracked != 0 {
		t.Fatalf("expected 0 tracked entries, got %d", stats.Tracked)
	}
	if stats.LastCount != 1 {
		t.Fatalf("expected LastCount=1, got %d", stats.LastCount)
	}
}

// ---------------------------------------------------------------------------
// Prune does NOT delete Critical tier entries
// ---------------------------------------------------------------------------

func TestPruneSkipsCriticalEntries(t *testing.T) {
	d, a := newTestDriver()

	_ = a.Store("config", "important", "do not delete")

	// Critical entry with very old timestamps — should never be pruned.
	d.mu.Lock()
	d.entries["config:important"] = &entryMeta{
		Namespace: "config",
		Key:       "important",
		Tier:      Critical,
		StoredAt:  time.Now().Add(-1000 * 24 * time.Hour),
		ExpiresAt: time.Time{}, // zero: never expires
	}
	d.mu.Unlock()

	count, err := d.Prune()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 pruned for Critical entries, got %d", count)
	}

	// Verify the entry still exists in the adaptor.
	val, err := a.Retrieve("config", "important")
	if err != nil {
		t.Fatalf("critical entry should still exist: %v", err)
	}
	if val != "do not delete" {
		t.Fatalf("unexpected value: %v", val)
	}
}

// ---------------------------------------------------------------------------
// Prune does NOT delete non-expired entries
// ---------------------------------------------------------------------------

func TestPruneSkipsNonExpiredEntries(t *testing.T) {
	d, a := newTestDriver()

	_ = a.Store("cache", "fresh", "still good")

	// Entry that expires far in the future.
	d.mu.Lock()
	d.entries["cache:fresh"] = &entryMeta{
		Namespace: "cache",
		Key:       "fresh",
		Tier:      Medium,
		StoredAt:  time.Now(),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	d.mu.Unlock()

	count, err := d.Prune()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 pruned for non-expired entries, got %d", count)
	}

	val, err := a.Retrieve("cache", "fresh")
	if err != nil {
		t.Fatalf("non-expired entry should still exist: %v", err)
	}
	if val != "still good" {
		t.Fatalf("unexpected value: %v", val)
	}
}

// ---------------------------------------------------------------------------
// Custom TTL overrides tier default
// ---------------------------------------------------------------------------

func TestCustomTTLOverridesTierDefault(t *testing.T) {
	customTTL := 2 * time.Hour
	d, _ := newTestDriver(
		WithRule(Rule{Namespace: "sessions", Tier: Medium, TTL: customTTL}),
	)

	d.Track("sessions", "abc123", Medium)

	d.mu.RLock()
	meta := d.entries["sessions:abc123"]
	d.mu.RUnlock()

	// The ExpiresAt should be approximately StoredAt + customTTL, not the
	// default 30-day Medium TTL.
	expectedExpiry := meta.StoredAt.Add(customTTL)
	diff := meta.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("expected ExpiresAt near %v, got %v (diff %v)", expectedExpiry, meta.ExpiresAt, diff)
	}

	// Should NOT be near the default 30-day TTL.
	defaultExpiry := meta.StoredAt.Add(Medium.DefaultTTL())
	if meta.ExpiresAt.After(defaultExpiry.Add(-time.Hour)) {
		t.Fatal("ExpiresAt should be much sooner than default 30-day TTL")
	}
}

// ---------------------------------------------------------------------------
// Wildcard rule "*" applies to unmatched namespaces
// ---------------------------------------------------------------------------

func TestWildcardRuleApplies(t *testing.T) {
	wildcardTTL := 5 * time.Hour
	d, _ := newTestDriver(
		WithRule(Rule{Namespace: "specific", Tier: High, TTL: 0}),
		WithRule(Rule{Namespace: "*", Tier: Low, TTL: wildcardTTL}),
	)

	// "random" does not match "specific", so "*" should apply its TTL.
	d.Track("random", "key1", Low)

	d.mu.RLock()
	meta := d.entries["random:key1"]
	d.mu.RUnlock()

	expectedExpiry := meta.StoredAt.Add(wildcardTTL)
	diff := meta.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("expected ExpiresAt near %v (wildcard TTL), got %v", expectedExpiry, meta.ExpiresAt)
	}
}

// ---------------------------------------------------------------------------
// Stats returns correct counts
// ---------------------------------------------------------------------------

func TestStatsReturnsCorrectCounts(t *testing.T) {
	d, _ := newTestDriver()

	d.Track("ns1", "k1", Critical)
	d.Track("ns2", "k2", Low)
	d.Track("ns3", "k3", Low)
	d.Track("ns4", "k4", Telemetry)

	stats := d.Stats()
	if stats.Tracked != 4 {
		t.Fatalf("expected 4 tracked, got %d", stats.Tracked)
	}
	if stats.ByTier[Critical] != 1 {
		t.Fatalf("expected 1 Critical, got %d", stats.ByTier[Critical])
	}
	if stats.ByTier[Low] != 2 {
		t.Fatalf("expected 2 Low, got %d", stats.ByTier[Low])
	}
	if stats.ByTier[Telemetry] != 1 {
		t.Fatalf("expected 1 Telemetry, got %d", stats.ByTier[Telemetry])
	}

	// After a prune (nothing expired), LastCount should be 0.
	_, _ = d.Prune()
	stats = d.Stats()
	if stats.LastCount != 0 {
		t.Fatalf("expected LastCount=0, got %d", stats.LastCount)
	}
	if stats.LastPrune.IsZero() {
		t.Fatal("expected non-zero LastPrune after Prune()")
	}
}

// ---------------------------------------------------------------------------
// AddRule / RemoveRule
// ---------------------------------------------------------------------------

func TestAddRuleAndRemoveRule(t *testing.T) {
	d, _ := newTestDriver()

	// Start with no rules.
	if len(d.Rules()) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(d.Rules()))
	}

	d.AddRule(Rule{Namespace: "logs", Tier: Ephemeral})
	d.AddRule(Rule{Namespace: "config", Tier: Critical})

	rules := d.Rules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	// Replace an existing rule.
	d.AddRule(Rule{Namespace: "logs", Tier: Low, TTL: time.Hour})
	rules = d.Rules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules after replacement, got %d", len(rules))
	}
	// Find the logs rule and verify it was updated.
	for _, r := range rules {
		if r.Namespace == "logs" {
			if r.Tier != Low {
				t.Fatalf("expected logs rule tier Low, got %s", r.Tier)
			}
			if r.TTL != time.Hour {
				t.Fatalf("expected logs rule TTL 1h, got %v", r.TTL)
			}
		}
	}

	// Remove.
	d.RemoveRule("logs")
	rules = d.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule after removal, got %d", len(rules))
	}
	if rules[0].Namespace != "config" {
		t.Fatalf("expected remaining rule for 'config', got %q", rules[0].Namespace)
	}

	// Remove non-existent: should be a no-op.
	d.RemoveRule("nonexistent")
	if len(d.Rules()) != 1 {
		t.Fatal("removing non-existent rule should be a no-op")
	}
}

// ---------------------------------------------------------------------------
// Background pruning runs on interval
// ---------------------------------------------------------------------------

func TestBackgroundPruningRunsOnInterval(t *testing.T) {
	d, a := newTestDriver(WithPruneInterval(50 * time.Millisecond))

	// Store a value that should be pruned.
	_ = a.Store("temp", "expired_key", "data")

	// Insert an already-expired entry.
	d.mu.Lock()
	d.entries["temp:expired_key"] = &entryMeta{
		Namespace: "temp",
		Key:       "expired_key",
		Tier:      Ephemeral,
		StoredAt:  time.Now().Add(-5 * 24 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour), // expired 1 hour ago
	}
	d.mu.Unlock()

	var pruneCompleteCount atomic.Int32
	_ = d.On(EventPruneComplete, func(data interface{}) {
		pruneCompleteCount.Add(1)
	})

	// Start the driver (launches background goroutine).
	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for at least one prune cycle.
	time.Sleep(200 * time.Millisecond)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt() error: %v", err)
	}

	// The background loop should have pruned the expired entry.
	stats := d.Stats()
	if stats.Tracked != 0 {
		t.Fatalf("expected 0 tracked after background prune, got %d", stats.Tracked)
	}

	// At least one prune_complete event should have been published.
	if pruneCompleteCount.Load() < 1 {
		t.Fatalf("expected at least 1 prune_complete event, got %d", pruneCompleteCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Pruned event carries namespace and key
// ---------------------------------------------------------------------------

func TestPrunedEventCarriesData(t *testing.T) {
	d, a := newTestDriver()

	_ = a.Store("telemetry", "metric1", "value")

	d.mu.Lock()
	d.entries["telemetry:metric1"] = &entryMeta{
		Namespace: "telemetry",
		Key:       "metric1",
		Tier:      Telemetry,
		StoredAt:  time.Now().Add(-20 * 24 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	d.mu.Unlock()

	var prunedData map[string]string
	_ = d.On(EventPruned, func(data interface{}) {
		if m, ok := data.(map[string]string); ok {
			prunedData = m
		}
	})

	count, err := d.Prune()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 pruned, got %d", count)
	}

	// Give event handler time to fire.
	time.Sleep(50 * time.Millisecond)

	if prunedData == nil {
		t.Fatal("expected pruned event data")
	}
	if prunedData["namespace"] != "telemetry" {
		t.Fatalf("expected namespace 'telemetry', got %q", prunedData["namespace"])
	}
	if prunedData["key"] != "metric1" {
		t.Fatalf("expected key 'metric1', got %q", prunedData["key"])
	}
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func TestCommands(t *testing.T) {
	d, _ := newTestDriver()

	d.Track("ns", "k", Medium)

	// stats command.
	result := d.Command("stats")(nil)
	stats, ok := result.(Stats)
	if !ok {
		t.Fatalf("expected Stats, got %T", result)
	}
	if stats.Tracked != 1 {
		t.Fatalf("expected 1 tracked, got %d", stats.Tracked)
	}

	// prune command.
	result = d.Command("prune")(nil)
	count, ok := result.(int)
	if !ok {
		t.Fatalf("expected int, got %T", result)
	}
	if count != 0 {
		t.Fatalf("expected 0 pruned, got %d", count)
	}

	// rules command.
	d.AddRule(Rule{Namespace: "test", Tier: Low})
	result = d.Command("rules")(nil)
	rules, ok := result.([]Rule)
	if !ok {
		t.Fatalf("expected []Rule, got %T", result)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
}

// ---------------------------------------------------------------------------
// Halt stops the background goroutine (no panic on double halt)
// ---------------------------------------------------------------------------

func TestHaltIsIdempotent(t *testing.T) {
	d, _ := newTestDriver(WithPruneInterval(50 * time.Millisecond))
	_ = d.Start()
	time.Sleep(100 * time.Millisecond)

	// Should not panic.
	if err := d.Halt(); err != nil {
		t.Fatalf("first Halt() error: %v", err)
	}
	if err := d.Halt(); err != nil {
		t.Fatalf("second Halt() error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle metadata persisted to memory adaptor
// ---------------------------------------------------------------------------

func TestLifecycleMetadataPersistedToAdaptor(t *testing.T) {
	d, a := newTestDriver()

	d.Track("sensors", "temp", Low)

	// The metadata should be stored under the "lifecycle" namespace.
	val, err := a.Retrieve(metaNamespace, "sensors:temp")
	if err != nil {
		t.Fatalf("expected lifecycle metadata to be persisted: %v", err)
	}
	if val == nil {
		t.Fatal("expected non-nil metadata value")
	}
}

// ---------------------------------------------------------------------------
// WithDefaultTier option
// ---------------------------------------------------------------------------

func TestWithDefaultTier(t *testing.T) {
	d, _ := newTestDriver(WithDefaultTier(Ephemeral))

	// Track without any rules — should use the custom default tier.
	d.Track("anything", "key", Ephemeral)

	d.mu.RLock()
	meta := d.entries["anything:key"]
	d.mu.RUnlock()

	if meta.Tier != Ephemeral {
		t.Fatalf("expected Ephemeral tier, got %s", meta.Tier)
	}
	// Verify the TTL is Ephemeral's default (3 days).
	expectedExpiry := meta.StoredAt.Add(Ephemeral.DefaultTTL())
	diff := meta.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("expected ExpiresAt near %v, got %v", expectedExpiry, meta.ExpiresAt)
	}
}

// ---------------------------------------------------------------------------
// Multiple entries, mixed tiers, selective pruning
// ---------------------------------------------------------------------------

func TestMixedTierSelectivePruning(t *testing.T) {
	d, a := newTestDriver()

	// Store values in the adaptor.
	_ = a.Store("critical_ns", "key1", "vital")
	_ = a.Store("logs", "key2", "old log")
	_ = a.Store("cache", "key3", "fresh cache")

	now := time.Now()

	d.mu.Lock()
	d.entries["critical_ns:key1"] = &entryMeta{
		Namespace: "critical_ns",
		Key:       "key1",
		Tier:      Critical,
		StoredAt:  now.Add(-500 * 24 * time.Hour),
		ExpiresAt: time.Time{}, // never
	}
	d.entries["logs:key2"] = &entryMeta{
		Namespace: "logs",
		Key:       "key2",
		Tier:      Low,
		StoredAt:  now.Add(-10 * 24 * time.Hour),
		ExpiresAt: now.Add(-3 * 24 * time.Hour), // expired
	}
	d.entries["cache:key3"] = &entryMeta{
		Namespace: "cache",
		Key:       "key3",
		Tier:      Medium,
		StoredAt:  now,
		ExpiresAt: now.Add(30 * 24 * time.Hour), // far future
	}
	d.mu.Unlock()

	count, err := d.Prune()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 pruned (only the expired low tier), got %d", count)
	}

	stats := d.Stats()
	if stats.Tracked != 2 {
		t.Fatalf("expected 2 remaining, got %d", stats.Tracked)
	}
	if stats.ByTier[Critical] != 1 {
		t.Fatalf("expected 1 Critical, got %d", stats.ByTier[Critical])
	}
	if stats.ByTier[Medium] != 1 {
		t.Fatalf("expected 1 Medium, got %d", stats.ByTier[Medium])
	}

	// Verify adaptor state.
	_, err = a.Retrieve("critical_ns", "key1")
	if err != nil {
		t.Fatal("critical entry should still exist in adaptor")
	}
	_, err = a.Retrieve("logs", "key2")
	if err == nil {
		t.Fatal("expired entry should have been deleted from adaptor")
	}
	_, err = a.Retrieve("cache", "key3")
	if err != nil {
		t.Fatal("non-expired entry should still exist in adaptor")
	}
}
