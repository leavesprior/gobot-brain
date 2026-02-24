// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

// Package browser provides a GoBot v2 device driver for browser automation
// via the Chrome DevTools Protocol.
//
// The driver uses a pluggable Transport interface for the CDP connection,
// keeping this package free of websocket library dependencies. Users supply
// their own Transport implementation (or use the mock for testing).
package browser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

const (
	namespace  = "browser"
	historyKey = "history"
	maxHistory = 100
)

// Event names published by the browser driver.
const (
	EventNavigated = "navigated"
	EventEvaluated = "evaluated"
	EventClicked   = "clicked"
	EventScreenshot = "screenshot"
	EventError     = "error"
)

// Transport is the pluggable CDP connection interface. Implementations
// handle the actual websocket (or other protocol) communication.
type Transport interface {
	Connect(endpoint string) error
	Close() error
	Send(method string, params map[string]interface{}) (json.RawMessage, error)
}

// PageInfo holds the current page URL and title.
type PageInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Option configures the browser driver.
type Option func(*Driver)

// WithTransport sets the CDP transport implementation.
func WithTransport(t Transport) Option {
	return func(d *Driver) {
		d.transport = t
	}
}

// WithEndpoint sets the CDP websocket endpoint URL
// (e.g. "ws://localhost:9222").
func WithEndpoint(endpoint string) Option {
	return func(d *Driver) {
		d.endpoint = endpoint
	}
}

// Driver is a GoBot v2 device that provides browser automation via the
// Chrome DevTools Protocol through a pluggable Transport.
type Driver struct {
	name      string
	adaptor   *memory.Adaptor
	transport Transport
	endpoint  string
	connected bool
	mu        sync.Mutex

	gobot.Eventer
	gobot.Commander
}

// NewDriver creates a browser automation driver backed by the given
// memory adaptor for persistence.
func NewDriver(a *memory.Adaptor, opts ...Option) *Driver {
	d := &Driver{
		name:      "browser",
		adaptor:   a,
		Eventer:   gobot.NewEventer(),
		Commander: gobot.NewCommander(),
	}
	for _, opt := range opts {
		opt(d)
	}

	d.AddEvent(EventNavigated)
	d.AddEvent(EventEvaluated)
	d.AddEvent(EventClicked)
	d.AddEvent(EventScreenshot)
	d.AddEvent(EventError)

	d.AddCommand("navigate", func(params map[string]interface{}) interface{} {
		url, _ := params["url"].(string)
		if url == "" {
			return fmt.Errorf("navigate: missing 'url' parameter")
		}
		return d.Navigate(url)
	})
	d.AddCommand("eval", func(params map[string]interface{}) interface{} {
		expr, _ := params["expression"].(string)
		if expr == "" {
			return fmt.Errorf("eval: missing 'expression' parameter")
		}
		result, err := d.Eval(expr)
		if err != nil {
			return err
		}
		return result
	})
	d.AddCommand("click", func(params map[string]interface{}) interface{} {
		selector, _ := params["selector"].(string)
		if selector == "" {
			return fmt.Errorf("click: missing 'selector' parameter")
		}
		return d.Click(selector)
	})
	d.AddCommand("type", func(params map[string]interface{}) interface{} {
		selector, _ := params["selector"].(string)
		text, _ := params["text"].(string)
		if selector == "" || text == "" {
			return fmt.Errorf("type: missing 'selector' or 'text' parameter")
		}
		return d.Type(selector, text)
	})
	d.AddCommand("screenshot", func(params map[string]interface{}) interface{} {
		data, err := d.Screenshot()
		if err != nil {
			return err
		}
		return data
	})
	d.AddCommand("pageinfo", func(params map[string]interface{}) interface{} {
		info, err := d.PageInfo()
		if err != nil {
			return err
		}
		return info
	})

	return d
}

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection { return d.adaptor }

// Start initializes the browser driver. If a transport and endpoint are
// configured, it connects to the CDP endpoint.
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.transport != nil && d.endpoint != "" {
		if err := d.transport.Connect(d.endpoint); err != nil {
			return fmt.Errorf("browser start: %w", err)
		}
		d.connected = true
	}
	return nil
}

// Halt disconnects from the CDP endpoint if connected.
func (d *Driver) Halt() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.transport != nil && d.connected {
		d.connected = false
		return d.transport.Close()
	}
	return nil
}

// send serializes a CDP call through the transport under the mutex.
func (d *Driver) send(method string, params map[string]interface{}) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.transport == nil {
		return nil, fmt.Errorf("browser: no transport configured")
	}
	if !d.connected {
		return nil, fmt.Errorf("browser: transport not connected")
	}
	return d.transport.Send(method, params)
}

// Navigate directs the browser to the given URL.
func (d *Driver) Navigate(url string) error {
	_, err := d.send("Page.navigate", map[string]interface{}{"url": url})
	if err != nil {
		d.Publish(EventError, fmt.Sprintf("navigate %s: %v", url, err))
		return err
	}

	d.Publish(EventNavigated, url)
	d.persistHistory(url)
	return nil
}

// Eval evaluates a JavaScript expression in the browser and returns the
// string result.
func (d *Driver) Eval(expr string) (string, error) {
	result, err := d.send("Runtime.evaluate", map[string]interface{}{
		"expression":    expr,
		"returnByValue": true,
	})
	if err != nil {
		d.Publish(EventError, fmt.Sprintf("eval: %v", err))
		return "", err
	}

	var evalResult struct {
		Result struct {
			Value interface{} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &evalResult); err != nil {
		return "", fmt.Errorf("eval: parse response: %w", err)
	}

	d.Publish(EventEvaluated, expr)
	return fmt.Sprintf("%v", evalResult.Result.Value), nil
}

// Click clicks an element matching the CSS selector.
func (d *Driver) Click(selector string) error {
	expr := fmt.Sprintf(`document.querySelector(%q).click()`, selector)
	_, err := d.Eval(expr)
	if err != nil {
		d.Publish(EventError, fmt.Sprintf("click %s: %v", selector, err))
		return fmt.Errorf("click %q: %w", selector, err)
	}

	d.Publish(EventClicked, selector)
	return nil
}

// Type types text into an element matching the CSS selector. It focuses
// the element, sets its value, and dispatches an input event.
func (d *Driver) Type(selector, text string) error {
	expr := fmt.Sprintf(
		`(function(){`+
			`var el=document.querySelector(%q);`+
			`el.focus();`+
			`el.value=%q;`+
			`el.dispatchEvent(new Event('input',{bubbles:true}));`+
			`})()`,
		selector, text,
	)
	_, err := d.Eval(expr)
	if err != nil {
		return fmt.Errorf("type %q: %w", selector, err)
	}
	return nil
}

// Screenshot captures a PNG screenshot and returns the decoded bytes.
func (d *Driver) Screenshot() ([]byte, error) {
	result, err := d.send("Page.captureScreenshot", map[string]interface{}{
		"format": "png",
	})
	if err != nil {
		d.Publish(EventError, fmt.Sprintf("screenshot: %v", err))
		return nil, err
	}

	var ssResult struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &ssResult); err != nil {
		return nil, fmt.Errorf("screenshot: parse response: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(ssResult.Data)
	if err != nil {
		return nil, fmt.Errorf("screenshot: decode base64: %w", err)
	}

	d.Publish(EventScreenshot, nil)
	return decoded, nil
}

// PageInfo returns the current page URL and title.
func (d *Driver) PageInfo() (*PageInfo, error) {
	expr := `JSON.stringify({url: window.location.href, title: document.title})`
	result, err := d.Eval(expr)
	if err != nil {
		return nil, fmt.Errorf("pageinfo: %w", err)
	}

	var info PageInfo
	if err := json.Unmarshal([]byte(result), &info); err != nil {
		return nil, fmt.Errorf("pageinfo: parse response: %w", err)
	}
	return &info, nil
}

// WaitFor polls for an element matching the CSS selector until it appears
// or the timeout expires.
func (d *Driver) WaitFor(selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	expr := fmt.Sprintf(`document.querySelector(%q) !== null`, selector)

	for time.Now().Before(deadline) {
		result, err := d.Eval(expr)
		if err == nil && result == "true" {
			return nil
		}
		time.Sleep(pollInterval)
	}

	return fmt.Errorf("waitfor: selector %q not found after %v", selector, timeout)
}

// persistHistory appends a URL to the navigation history in the memory
// adaptor, keeping the most recent maxHistory entries.
func (d *Driver) persistHistory(url string) {
	var history []string

	raw, err := d.adaptor.Retrieve(namespace, historyKey)
	if err == nil {
		if h, ok := raw.([]string); ok {
			history = h
		} else if h, ok := raw.([]interface{}); ok {
			// Handle JSON-deserialized form.
			for _, v := range h {
				if s, ok := v.(string); ok {
					history = append(history, s)
				}
			}
		}
	}

	history = append(history, url)
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}

	_ = d.adaptor.Store(namespace, historyKey, history)
}
