// Package memory provides a generic namespace/key-value memory adaptor
// that implements the gobot.Connection interface with pluggable backends.
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gobot.io/x/gobot/v2"
)

// Event names published by the Adaptor on each operation.
const (
	EventStored    = "stored"
	EventRetrieved = "retrieved"
	EventDeleted   = "deleted"
	EventError     = "error"
)

// Backend is the pluggable storage interface that every memory backend must
// implement.
type Backend interface {
	Init() error
	Close() error
	Store(namespace, key string, value interface{}) error
	Retrieve(namespace, key string) (interface{}, error)
	Delete(namespace, key string) error
	List(namespace string) ([]string, error)
}

// Adaptor is a gobot.Connection that delegates to a Backend for persistence.
type Adaptor struct {
	name    string
	backend Backend
	gobot.Eventer
}

// Option configures the Adaptor during construction.
type Option func(*Adaptor)

// WithBackend injects an arbitrary Backend implementation.
func WithBackend(b Backend) Option {
	return func(a *Adaptor) {
		a.backend = b
	}
}

// WithFileStore configures the Adaptor to persist namespaces as JSON files
// inside dir.
func WithFileStore(dir string) Option {
	return func(a *Adaptor) {
		a.backend = &fileStore{dir: dir}
	}
}

// WithHTTPStore configures the Adaptor to use an HTTP key-value API at
// baseURL.
func WithHTTPStore(baseURL string) Option {
	return func(a *Adaptor) {
		a.backend = &httpStore{baseURL: baseURL, client: &http.Client{}}
	}
}

// NewAdaptor returns a new memory Adaptor. Without options an in-memory
// backend is used.
func NewAdaptor(opts ...Option) *Adaptor {
	a := &Adaptor{
		name:    "Memory",
		backend: newInMemory(),
		Eventer: gobot.NewEventer(),
	}
	for _, o := range opts {
		o(a)
	}
	a.AddEvent(EventStored)
	a.AddEvent(EventRetrieved)
	a.AddEvent(EventDeleted)
	a.AddEvent(EventError)
	return a
}

// Name returns the adaptor name (gobot.Connection).
func (a *Adaptor) Name() string { return a.name }

// SetName sets the adaptor name (gobot.Connection).
func (a *Adaptor) SetName(n string) { a.name = n }

// Connect initialises the backend (gobot.Connection).
func (a *Adaptor) Connect() error { return a.backend.Init() }

// Finalize flushes and closes the backend (gobot.Connection).
func (a *Adaptor) Finalize() error { return a.backend.Close() }

// Store persists a value and publishes an EventStored event.
func (a *Adaptor) Store(namespace, key string, value interface{}) error {
	if err := a.backend.Store(namespace, key, value); err != nil {
		a.Publish(EventError, err)
		return err
	}
	a.Publish(EventStored, map[string]string{"namespace": namespace, "key": key})
	return nil
}

// Retrieve fetches a value and publishes an EventRetrieved event.
func (a *Adaptor) Retrieve(namespace, key string) (interface{}, error) {
	v, err := a.backend.Retrieve(namespace, key)
	if err != nil {
		a.Publish(EventError, err)
		return nil, err
	}
	a.Publish(EventRetrieved, map[string]string{"namespace": namespace, "key": key})
	return v, nil
}

// Delete removes a key and publishes an EventDeleted event.
func (a *Adaptor) Delete(namespace, key string) error {
	if err := a.backend.Delete(namespace, key); err != nil {
		a.Publish(EventError, err)
		return err
	}
	a.Publish(EventDeleted, map[string]string{"namespace": namespace, "key": key})
	return nil
}

// List returns all keys in a namespace.
func (a *Adaptor) List(namespace string) ([]string, error) {
	keys, err := a.backend.List(namespace)
	if err != nil {
		a.Publish(EventError, err)
		return nil, err
	}
	return keys, nil
}

// ---------------------------------------------------------------------------
// InMemory backend
// ---------------------------------------------------------------------------

type inMemory struct {
	mu   sync.RWMutex
	data map[string]map[string]interface{}
}

func newInMemory() *inMemory {
	return &inMemory{data: make(map[string]map[string]interface{})}
}

func (m *inMemory) Init() error  { return nil }
func (m *inMemory) Close() error { return nil }

func (m *inMemory) Store(namespace, key string, value interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, ok := m.data[namespace]
	if !ok {
		ns = make(map[string]interface{})
		m.data[namespace] = ns
	}
	ns[key] = value
	return nil
}

func (m *inMemory) Retrieve(namespace, key string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ns, ok := m.data[namespace]
	if !ok {
		return nil, fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	v, ok := ns[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	return v, nil
}

func (m *inMemory) Delete(namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, ok := m.data[namespace]
	if !ok {
		return fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	if _, ok := ns[key]; !ok {
		return fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	delete(ns, key)
	if len(ns) == 0 {
		delete(m.data, namespace)
	}
	return nil
}

func (m *inMemory) List(namespace string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ns, ok := m.data[namespace]
	if !ok {
		return []string{}, nil
	}
	keys := make([]string, 0, len(ns))
	for k := range ns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// ---------------------------------------------------------------------------
// FileStore backend
// ---------------------------------------------------------------------------

type fileStore struct {
	dir string
	mu  sync.Mutex
}

func (f *fileStore) Init() error {
	return os.MkdirAll(f.dir, 0o755)
}

func (f *fileStore) Close() error { return nil }

func (f *fileStore) path(namespace string) string {
	return filepath.Join(f.dir, namespace+".json")
}

func (f *fileStore) load(namespace string) (map[string]interface{}, error) {
	data, err := os.ReadFile(f.path(namespace))
	if os.IsNotExist(err) {
		return make(map[string]interface{}), nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// save writes the namespace map atomically via temp file + rename.
func (f *fileStore) save(namespace string, m map[string]interface{}) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, namespace+"*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, f.path(namespace))
}

func (f *fileStore) Store(namespace, key string, value interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load(namespace)
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(namespace, m)
}

func (f *fileStore) Retrieve(namespace, key string) (interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load(namespace)
	if err != nil {
		return nil, err
	}
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	return v, nil
}

func (f *fileStore) Delete(namespace, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load(namespace)
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	delete(m, key)
	return f.save(namespace, m)
}

func (f *fileStore) List(namespace string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load(namespace)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// ---------------------------------------------------------------------------
// HTTPStore backend
// ---------------------------------------------------------------------------

type httpStore struct {
	baseURL string
	client  *http.Client
}

func (h *httpStore) Init() error  { return nil }
func (h *httpStore) Close() error { return nil }

func (h *httpStore) Store(namespace, key string, value interface{}) error {
	body := map[string]interface{}{
		"namespace": namespace,
		"key":       key,
		"value":     value,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := h.client.Post(h.baseURL+"/store", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http store failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func (h *httpStore) Retrieve(namespace, key string) (interface{}, error) {
	u := fmt.Sprintf("%s/retrieve?namespace=%s&key=%s",
		h.baseURL, url.QueryEscape(namespace), url.QueryEscape(key))
	resp, err := h.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("key not found: %s/%s", namespace, key)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http retrieve failed (%d): %s", resp.StatusCode, string(b))
	}
	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *httpStore) Delete(namespace, key string) error {
	u := fmt.Sprintf("%s/delete?namespace=%s&key=%s",
		h.baseURL, url.QueryEscape(namespace), url.QueryEscape(key))
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http delete failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func (h *httpStore) List(namespace string) ([]string, error) {
	u := fmt.Sprintf("%s/list?namespace=%s",
		h.baseURL, url.QueryEscape(namespace))
	resp, err := h.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http list failed (%d): %s", resp.StatusCode, string(b))
	}
	var keys []string
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		return nil, err
	}
	return keys, nil
}
