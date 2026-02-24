// Package hitl provides a GoBot v2 device driver for human-in-the-loop
// confirmation of risky robot actions.
package hitl

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

const (
	defaultTimeout = 2 * time.Hour
	namespace      = "hitl"
)

// Event names published by the driver.
const (
	RequestedEvent = "requested"
	ApprovedEvent  = "approved"
	DeniedEvent    = "denied"
	ExpiredEvent   = "expired"
	ExecutedEvent  = "executed"
	ErrorEvent     = "error"
)

// Decision represents the current state of a HITL request.
type Decision string

const (
	Pending  Decision = "pending"
	Approved Decision = "approved"
	Denied   Decision = "denied"
	Expired  Decision = "expired"
)

// Request represents a pending human-in-the-loop approval request.
type Request struct {
	ID          string
	Description string
	Action      func() error  `json:"-"` // executed only if approved
	Timeout     time.Duration             // auto-expire; defaults to 2h
	CreatedAt   time.Time
	Decision    Decision
}

// requestRecord is the JSON-safe subset of Request for persistence.
type requestRecord struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Timeout     string   `json:"timeout"`
	CreatedAt   string   `json:"created_at"`
	Decision    Decision `json:"decision"`
}

func toRecord(r *Request) requestRecord {
	return requestRecord{
		ID:          r.ID,
		Description: r.Description,
		Timeout:     r.Timeout.String(),
		CreatedAt:   r.CreatedAt.Format(time.RFC3339),
		Decision:    r.Decision,
	}
}

// NotifyFunc is called to alert a human about a new approval request.
type NotifyFunc func(req Request) error

// Driver implements gobot.Device for human-in-the-loop confirmation.
type Driver struct {
	name    string
	adaptor *memory.Adaptor
	notify  NotifyFunc

	mu       sync.RWMutex
	requests map[string]*Request

	done chan struct{} // closed by Halt to cancel pending expiry goroutines
	wg   sync.WaitGroup

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a new HITL driver backed by the given memory adaptor.
// The notify function is called each time a new approval request is created;
// it may be nil if no notification is desired.
func NewDriver(a *memory.Adaptor, notify NotifyFunc) *Driver {
	d := &Driver{
		name:      "hitl",
		adaptor:   a,
		notify:    notify,
		requests:  make(map[string]*Request),
		Eventer:   gobot.NewEventer(),
		Commander: gobot.NewCommander(),
	}

	d.AddEvent(RequestedEvent)
	d.AddEvent(ApprovedEvent)
	d.AddEvent(DeniedEvent)
	d.AddEvent(ExpiredEvent)
	d.AddEvent(ExecutedEvent)
	d.AddEvent(ErrorEvent)

	d.AddCommand("approve", func(params map[string]interface{}) interface{} {
		id, ok := params["id"].(string)
		if !ok {
			return fmt.Errorf("approve: missing or invalid 'id' parameter")
		}
		return d.Approve(id)
	})

	d.AddCommand("deny", func(params map[string]interface{}) interface{} {
		id, ok := params["id"].(string)
		if !ok {
			return fmt.Errorf("deny: missing or invalid 'id' parameter")
		}
		return d.Deny(id)
	})

	d.AddCommand("pending", func(params map[string]interface{}) interface{} {
		return d.Pending()
	})

	return d
}

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start initializes the driver's internal state.
func (d *Driver) Start() error {
	d.mu.Lock()
	d.requests = make(map[string]*Request)
	d.done = make(chan struct{})
	d.mu.Unlock()
	return nil
}

// Halt cancels all pending expiry goroutines and waits for them to finish.
func (d *Driver) Halt() error {
	close(d.done)
	d.wg.Wait()
	return nil
}

// RequestApproval submits a new request for human approval. If req.ID is
// empty, a random 32-character hex ID is generated. Returns the request ID.
func (d *Driver) RequestApproval(req Request) (string, error) {
	if req.ID == "" {
		id, err := generateID()
		if err != nil {
			return "", fmt.Errorf("hitl: generate id: %w", err)
		}
		req.ID = id
	}
	if req.Timeout == 0 {
		req.Timeout = defaultTimeout
	}
	req.CreatedAt = time.Now()
	req.Decision = Pending

	d.mu.Lock()
	d.requests[req.ID] = &req
	d.mu.Unlock()

	if d.notify != nil {
		if err := d.notify(req); err != nil {
			return req.ID, fmt.Errorf("hitl: notify: %w", err)
		}
	}

	// Start a goroutine that expires the request after its timeout.
	d.wg.Add(1)
	go d.expireAfter(req.ID, req.Timeout)

	d.Publish(RequestedEvent, req)

	if err := d.persist(&req); err != nil {
		return req.ID, fmt.Errorf("hitl: persist: %w", err)
	}

	return req.ID, nil
}

// Approve approves a pending request and executes its action. Publishes
// "approved" then "executed" events. If the action returns an error, an
// "error" event is published and the error is returned, but the decision
// remains Approved.
func (d *Driver) Approve(id string) error {
	d.mu.Lock()
	req, ok := d.requests[id]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("hitl: request %q not found", id)
	}
	if req.Decision != Pending {
		d.mu.Unlock()
		return fmt.Errorf("hitl: request %q is %s, not pending", id, req.Decision)
	}
	req.Decision = Approved
	d.mu.Unlock()

	d.Publish(ApprovedEvent, *req)

	if req.Action != nil {
		if err := req.Action(); err != nil {
			d.Publish(ErrorEvent, err)
			_ = d.persist(req)
			return fmt.Errorf("hitl: action: %w", err)
		}
	}

	d.Publish(ExecutedEvent, *req)

	if err := d.persist(req); err != nil {
		return fmt.Errorf("hitl: persist: %w", err)
	}
	return nil
}

// Deny denies a pending request. The action is never executed. A "denied"
// event is published.
func (d *Driver) Deny(id string) error {
	d.mu.Lock()
	req, ok := d.requests[id]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("hitl: request %q not found", id)
	}
	if req.Decision != Pending {
		d.mu.Unlock()
		return fmt.Errorf("hitl: request %q is %s, not pending", id, req.Decision)
	}
	req.Decision = Denied
	d.mu.Unlock()

	d.Publish(DeniedEvent, *req)

	if err := d.persist(req); err != nil {
		return fmt.Errorf("hitl: persist: %w", err)
	}
	return nil
}

// Pending returns all requests that are still awaiting a human decision.
func (d *Driver) Pending() []Request {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var out []Request
	for _, req := range d.requests {
		if req.Decision == Pending {
			out = append(out, *req)
		}
	}
	return out
}

// Get returns a copy of the request with the given ID, or an error if not found.
func (d *Driver) Get(id string) (*Request, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	req, ok := d.requests[id]
	if !ok {
		return nil, fmt.Errorf("hitl: request %q not found", id)
	}
	cp := *req
	return &cp, nil
}

// expireAfter waits for the timeout to elapse, then expires the request if it
// is still pending. It returns early if the done channel is closed (by Halt).
func (d *Driver) expireAfter(id string, timeout time.Duration) {
	defer d.wg.Done()

	select {
	case <-time.After(timeout):
	case <-d.done:
		return
	}

	d.mu.Lock()
	req, ok := d.requests[id]
	if !ok || req.Decision != Pending {
		d.mu.Unlock()
		return
	}
	req.Decision = Expired
	d.mu.Unlock()

	d.Publish(ExpiredEvent, *req)
	_ = d.persist(req)
}

// persist stores the request record in the memory adaptor.
func (d *Driver) persist(req *Request) error {
	return d.adaptor.Store(namespace, req.ID, toRecord(req))
}

// generateID returns a 32-character hex string from 16 cryptographically
// random bytes.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
