package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"
)

// Compile-time check: Adaptor must satisfy gobot.Connection.
var _ gobot.Connection = (*Adaptor)(nil)

// ---------------------------------------------------------------------------
// InMemory backend tests
// ---------------------------------------------------------------------------

func TestInMemory_StoreRetrieve(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	if err := a.Store("ns1", "key1", "value1"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	v, err := a.Retrieve("ns1", "key1")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if v != "value1" {
		t.Errorf("got %v, want %q", v, "value1")
	}
}

func TestInMemory_RetrieveMissing(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	_, err := a.Retrieve("ns1", "nope")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestInMemory_Delete(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	if err := a.Store("ns1", "key1", "value1"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := a.Delete("ns1", "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := a.Retrieve("ns1", "key1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestInMemory_DeleteMissing(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	err := a.Delete("ns1", "nope")
	if err == nil {
		t.Fatal("expected error deleting missing key")
	}
}

func TestInMemory_List(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	// Empty namespace returns empty slice.
	keys, err := a.List("ns1")
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}

	a.Store("ns1", "b", 2)
	a.Store("ns1", "a", 1)
	a.Store("ns1", "c", 3)

	keys, err = a.List("ns1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	// Keys must be sorted.
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("keys not sorted: %v", keys)
	}
}

func TestInMemory_MultipleNamespaces(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	a.Store("ns1", "k", "v1")
	a.Store("ns2", "k", "v2")

	v1, _ := a.Retrieve("ns1", "k")
	v2, _ := a.Retrieve("ns2", "k")
	if v1 != "v1" || v2 != "v2" {
		t.Errorf("namespace isolation failed: ns1=%v ns2=%v", v1, v2)
	}
}

func TestInMemory_ConcurrentAccess(t *testing.T) {
	a := NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	var wg sync.WaitGroup
	n := 100
	wg.Add(n * 3) // store + retrieve + list goroutines

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("key%d", i)
		go func(k string, idx int) {
			defer wg.Done()
			a.Store("concurrent", k, idx)
		}(key, i)

		go func(k string) {
			defer wg.Done()
			a.Retrieve("concurrent", k) // may or may not exist yet
		}(key)

		go func() {
			defer wg.Done()
			a.List("concurrent")
		}()
	}

	wg.Wait()

	// After all stores complete, every key must be present.
	keys, err := a.List("concurrent")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != n {
		t.Errorf("expected %d keys, got %d", n, len(keys))
	}
}

// ---------------------------------------------------------------------------
// Name / SetName tests
// ---------------------------------------------------------------------------

func TestAdaptor_NameSetName(t *testing.T) {
	a := NewAdaptor()
	if a.Name() != "Memory" {
		t.Errorf("default name = %q, want %q", a.Name(), "Memory")
	}
	a.SetName("CustomName")
	if a.Name() != "CustomName" {
		t.Errorf("after SetName = %q, want %q", a.Name(), "CustomName")
	}
}

// ---------------------------------------------------------------------------
// FileStore backend tests
// ---------------------------------------------------------------------------

func TestFileStore_StoreRetrieve(t *testing.T) {
	dir := t.TempDir()
	a := NewAdaptor(WithFileStore(dir))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	if err := a.Store("ns1", "key1", "value1"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	v, err := a.Retrieve("ns1", "key1")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if v != "value1" {
		t.Errorf("got %v, want %q", v, "value1")
	}

	// Verify JSON file was created.
	fp := filepath.Join(dir, "ns1.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m["key1"] != "value1" {
		t.Errorf("file content: key1 = %v, want %q", m["key1"], "value1")
	}
}

func TestFileStore_RetrieveMissing(t *testing.T) {
	dir := t.TempDir()
	a := NewAdaptor(WithFileStore(dir))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	_, err := a.Retrieve("ns1", "nope")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestFileStore_Delete(t *testing.T) {
	dir := t.TempDir()
	a := NewAdaptor(WithFileStore(dir))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	a.Store("ns1", "key1", "value1")
	if err := a.Delete("ns1", "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := a.Retrieve("ns1", "key1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestFileStore_DeleteMissing(t *testing.T) {
	dir := t.TempDir()
	a := NewAdaptor(WithFileStore(dir))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	err := a.Delete("ns1", "nope")
	if err == nil {
		t.Fatal("expected error deleting missing key")
	}
}

func TestFileStore_List(t *testing.T) {
	dir := t.TempDir()
	a := NewAdaptor(WithFileStore(dir))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	// Empty namespace returns empty slice.
	keys, err := a.List("ns1")
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}

	a.Store("ns1", "b", 2)
	a.Store("ns1", "a", 1)
	a.Store("ns1", "c", 3)

	keys, err = a.List("ns1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("keys not sorted: %v", keys)
	}
}

func TestFileStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	// First adaptor writes data.
	a1 := NewAdaptor(WithFileStore(dir))
	a1.Connect()
	a1.Store("persist", "greeting", "hello")
	a1.Finalize()

	// Second adaptor reads it back.
	a2 := NewAdaptor(WithFileStore(dir))
	a2.Connect()
	defer a2.Finalize()

	v, err := a2.Retrieve("persist", "greeting")
	if err != nil {
		t.Fatalf("Retrieve after reopen: %v", err)
	}
	if v != "hello" {
		t.Errorf("got %v, want %q", v, "hello")
	}
}

// ---------------------------------------------------------------------------
// WithBackend option test
// ---------------------------------------------------------------------------

func TestWithBackend(t *testing.T) {
	mem := newInMemory()
	a := NewAdaptor(WithBackend(mem))
	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer a.Finalize()

	a.Store("ns", "k", "v")
	v, err := a.Retrieve("ns", "k")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if v != "v" {
		t.Errorf("got %v, want %q", v, "v")
	}
}

// ---------------------------------------------------------------------------
// Events test
// ---------------------------------------------------------------------------

func TestAdaptor_Events(t *testing.T) {
	a := NewAdaptor()
	a.Connect()
	defer a.Finalize()

	stored := make(chan interface{}, 1)
	retrieved := make(chan interface{}, 1)
	deleted := make(chan interface{}, 1)
	errCh := make(chan interface{}, 1)

	_ = a.On(EventStored, func(data interface{}) {
		stored <- data
	})
	_ = a.On(EventRetrieved, func(data interface{}) {
		retrieved <- data
	})
	_ = a.On(EventDeleted, func(data interface{}) {
		deleted <- data
	})
	_ = a.On(EventError, func(data interface{}) {
		errCh <- data
	})

	timeout := 500 * time.Millisecond

	a.Store("ns", "k", "v")
	select {
	case <-stored:
	case <-time.After(timeout):
		t.Error("expected stored event")
	}

	a.Retrieve("ns", "k")
	select {
	case <-retrieved:
	case <-time.After(timeout):
		t.Error("expected retrieved event")
	}

	a.Delete("ns", "k")
	select {
	case <-deleted:
	case <-time.After(timeout):
		t.Error("expected deleted event")
	}

	// Trigger an error event.
	a.Retrieve("ns", "missing")
	select {
	case <-errCh:
	case <-time.After(timeout):
		t.Error("expected error event")
	}
}

// ---------------------------------------------------------------------------
// InMemory backend: complex value types
// ---------------------------------------------------------------------------

func TestInMemory_ComplexValues(t *testing.T) {
	a := NewAdaptor()
	a.Connect()
	defer a.Finalize()

	// Map value.
	m := map[string]interface{}{"nested": true, "count": 42}
	a.Store("ns", "map", m)
	v, err := a.Retrieve("ns", "map")
	if err != nil {
		t.Fatalf("Retrieve map: %v", err)
	}
	vm, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	if vm["nested"] != true {
		t.Errorf("nested = %v, want true", vm["nested"])
	}

	// Slice value.
	s := []string{"a", "b", "c"}
	a.Store("ns", "slice", s)
	v2, _ := a.Retrieve("ns", "slice")
	vs, ok := v2.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", v2)
	}
	if len(vs) != 3 {
		t.Errorf("slice len = %d, want 3", len(vs))
	}
}

// ---------------------------------------------------------------------------
// InMemory backend: delete cleans up empty namespace
// ---------------------------------------------------------------------------

func TestInMemory_DeleteCleansNamespace(t *testing.T) {
	a := NewAdaptor()
	a.Connect()
	defer a.Finalize()

	a.Store("ephemeral", "only", "value")
	a.Delete("ephemeral", "only")

	// After deleting the only key the namespace list should be empty.
	keys, _ := a.List("ephemeral")
	if len(keys) != 0 {
		t.Errorf("expected empty namespace, got %v", keys)
	}
}
