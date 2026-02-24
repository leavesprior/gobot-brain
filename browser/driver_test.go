// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

package browser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/memory"
)

// Compile-time check: Driver must satisfy gobot.Device.
var _ gobot.Device = (*Driver)(nil)

// ---------------------------------------------------------------------------
// mockTransport
// ---------------------------------------------------------------------------

type mockTransport struct {
	mu           sync.Mutex
	lastMethod   string
	lastParams   map[string]interface{}
	response     json.RawMessage
	err          error
	connectCount int
	closeCount   int
	connected    bool
	connectErr   error
}

func newMockTransport() *mockTransport {
	return &mockTransport{}
}

func (m *mockTransport) Connect(endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectCount++
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	return nil
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCount++
	m.connected = false
	return nil
}

func (m *mockTransport) Send(method string, params map[string]interface{}) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMethod = method
	m.lastParams = params
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockTransport) setResponse(data json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.response = data
}

func (m *mockTransport) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *mockTransport) getLastMethod() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastMethod
}

func (m *mockTransport) getLastParams() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastParams
}

func (m *mockTransport) getConnectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connectCount
}

func (m *mockTransport) getCloseCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeCount
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestDriver(t *testing.T, opts ...Option) (*Driver, *memory.Adaptor, *mockTransport) {
	t.Helper()
	a := memory.NewAdaptor()
	if err := a.Connect(); err != nil {
		t.Fatalf("memory connect: %v", err)
	}
	mt := newMockTransport()
	allOpts := append([]Option{WithTransport(mt), WithEndpoint("ws://localhost:9222")}, opts...)
	d := NewDriver(a, allOpts...)
	if err := d.Start(); err != nil {
		t.Fatalf("driver start: %v", err)
	}
	return d, a, mt
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNameSetName(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a)

	if d.Name() != "browser" {
		t.Fatalf("expected name 'browser', got %q", d.Name())
	}
	d.SetName("custom")
	if d.Name() != "custom" {
		t.Fatalf("expected name 'custom', got %q", d.Name())
	}
}

func TestConnection(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a)

	if d.Connection() != a {
		t.Fatal("Connection() should return the memory adaptor")
	}
}

func TestStartConnectsTransport(t *testing.T) {
	d, _, mt := newTestDriver(t)
	defer d.Halt()

	if mt.getConnectCount() != 1 {
		t.Fatalf("expected 1 connect call, got %d", mt.getConnectCount())
	}
}

func TestHaltDisconnectsTransport(t *testing.T) {
	d, _, mt := newTestDriver(t)

	if err := d.Halt(); err != nil {
		t.Fatalf("Halt failed: %v", err)
	}
	if mt.getCloseCount() != 1 {
		t.Fatalf("expected 1 close call, got %d", mt.getCloseCount())
	}
}

func TestStartWithoutTransport(t *testing.T) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	d := NewDriver(a)
	if err := d.Start(); err != nil {
		t.Fatalf("Start without transport should succeed, got: %v", err)
	}
	if err := d.Halt(); err != nil {
		t.Fatalf("Halt without transport should succeed, got: %v", err)
	}
}

func TestNavigateSendsCorrectCDPMethod(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"frameId":"frame-1"}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	if err := d.Navigate("http://example.com/page"); err != nil {
		t.Fatalf("Navigate failed: %v", err)
	}

	if mt.getLastMethod() != "Page.navigate" {
		t.Fatalf("expected method 'Page.navigate', got %q", mt.getLastMethod())
	}

	params := mt.getLastParams()
	if url, ok := params["url"].(string); !ok || url != "http://example.com/page" {
		t.Fatalf("expected url 'http://example.com/page', got %v", params["url"])
	}
}

func TestEvalSendsRuntimeEvaluate(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"42"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result, err := d.Eval("1+1")
	if err != nil {
		t.Fatalf("Eval failed: %v", err)
	}
	if result != "42" {
		t.Fatalf("expected '42', got %q", result)
	}

	if mt.getLastMethod() != "Runtime.evaluate" {
		t.Fatalf("expected method 'Runtime.evaluate', got %q", mt.getLastMethod())
	}

	params := mt.getLastParams()
	if expr, ok := params["expression"].(string); !ok || expr != "1+1" {
		t.Fatalf("expected expression '1+1', got %v", params["expression"])
	}
}

func TestClickComposesCorrectJS(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"undefined"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	if err := d.Click("#submit-btn"); err != nil {
		t.Fatalf("Click failed: %v", err)
	}

	params := mt.getLastParams()
	expr, ok := params["expression"].(string)
	if !ok {
		t.Fatal("expected expression param")
	}
	expected := `document.querySelector("#submit-btn").click()`
	if expr != expected {
		t.Fatalf("expected expression %q, got %q", expected, expr)
	}
}

func TestTypeComposesCorrectJS(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"undefined"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	if err := d.Type("#input-field", "hello world"); err != nil {
		t.Fatalf("Type failed: %v", err)
	}

	params := mt.getLastParams()
	expr, ok := params["expression"].(string)
	if !ok {
		t.Fatal("expected expression param")
	}
	// The expression should contain the selector, the text, focus, value
	// assignment, and input event dispatch.
	if len(expr) == 0 {
		t.Fatal("expected non-empty expression")
	}
	// Verify key components are present in the composed JS.
	for _, substr := range []string{
		`document.querySelector("#input-field")`,
		`"hello world"`,
		`.focus()`,
		`.value=`,
		`new Event('input'`,
	} {
		found := false
		if containsSubstring(expr, substr) {
			found = true
		}
		if !found {
			t.Fatalf("expected expression to contain %q, got %q", substr, expr)
		}
	}
}

func TestScreenshotReturnsDecodedBytes(t *testing.T) {
	mt := newMockTransport()
	// Encode some fake PNG data as base64.
	fakeData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}
	encoded := base64.StdEncoding.EncodeToString(fakeData)
	mt.response = json.RawMessage(fmt.Sprintf(`{"data":%q}`, encoded))

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	data, err := d.Screenshot()
	if err != nil {
		t.Fatalf("Screenshot failed: %v", err)
	}

	if len(data) != len(fakeData) {
		t.Fatalf("expected %d bytes, got %d", len(fakeData), len(data))
	}
	for i, b := range data {
		if b != fakeData[i] {
			t.Fatalf("byte %d: expected %02x, got %02x", i, fakeData[i], b)
		}
	}

	if mt.getLastMethod() != "Page.captureScreenshot" {
		t.Fatalf("expected method 'Page.captureScreenshot', got %q", mt.getLastMethod())
	}
}

func TestPageInfoParsesResponse(t *testing.T) {
	mt := newMockTransport()
	// PageInfo calls Eval, which sends Runtime.evaluate.
	infoJSON := `{"url":"http://example.com","title":"Test Page"}`
	mt.response = json.RawMessage(fmt.Sprintf(`{"result":{"value":%q}}`, infoJSON))

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	info, err := d.PageInfo()
	if err != nil {
		t.Fatalf("PageInfo failed: %v", err)
	}
	if info.URL != "http://example.com" {
		t.Fatalf("expected URL 'http://example.com', got %q", info.URL)
	}
	if info.Title != "Test Page" {
		t.Fatalf("expected Title 'Test Page', got %q", info.Title)
	}
}

func TestWaitForSucceeds(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":true}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	err := d.WaitFor("#loaded", 1*time.Second)
	if err != nil {
		t.Fatalf("WaitFor should succeed when element exists: %v", err)
	}
}

func TestWaitForTimesOut(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":false}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	err := d.WaitFor("#missing", 300*time.Millisecond)
	if err == nil {
		t.Fatal("WaitFor should time out when element is never found")
	}
}

func TestNavigatePersistsHistory(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"frameId":"frame-1"}`)

	d, a, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	urls := []string{
		"http://example.com/page1",
		"http://example.com/page2",
		"http://example.com/page3",
	}
	for _, u := range urls {
		if err := d.Navigate(u); err != nil {
			t.Fatalf("Navigate failed: %v", err)
		}
	}

	raw, err := a.Retrieve(namespace, historyKey)
	if err != nil {
		t.Fatalf("failed to retrieve history: %v", err)
	}
	history, ok := raw.([]string)
	if !ok {
		t.Fatalf("expected []string history, got %T", raw)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}
	for i, u := range urls {
		if history[i] != u {
			t.Fatalf("history[%d]: expected %q, got %q", i, u, history[i])
		}
	}
}

func TestHistoryTruncatesToMax(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"frameId":"frame-1"}`)

	d, a, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	// Navigate more than maxHistory times.
	for i := 0; i < maxHistory+10; i++ {
		if err := d.Navigate(fmt.Sprintf("http://example.com/%d", i)); err != nil {
			t.Fatalf("Navigate %d failed: %v", i, err)
		}
	}

	raw, err := a.Retrieve(namespace, historyKey)
	if err != nil {
		t.Fatalf("failed to retrieve history: %v", err)
	}
	history, ok := raw.([]string)
	if !ok {
		t.Fatalf("expected []string history, got %T", raw)
	}
	if len(history) != maxHistory {
		t.Fatalf("expected %d history entries, got %d", maxHistory, len(history))
	}
	// The first entry should be the 11th navigation (index 10).
	expected := "http://example.com/10"
	if history[0] != expected {
		t.Fatalf("expected first history entry %q, got %q", expected, history[0])
	}
}

func TestMethodsFailWithoutTransport(t *testing.T) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	d := NewDriver(a)
	_ = d.Start()
	defer d.Halt()

	if err := d.Navigate("http://example.com"); err == nil {
		t.Fatal("Navigate should fail without transport")
	}
	if _, err := d.Eval("1+1"); err == nil {
		t.Fatal("Eval should fail without transport")
	}
	if err := d.Click("#btn"); err == nil {
		t.Fatal("Click should fail without transport")
	}
	if err := d.Type("#input", "text"); err == nil {
		t.Fatal("Type should fail without transport")
	}
	if _, err := d.Screenshot(); err == nil {
		t.Fatal("Screenshot should fail without transport")
	}
	if _, err := d.PageInfo(); err == nil {
		t.Fatal("PageInfo should fail without transport")
	}
}

func TestMethodsFailWhenNotConnected(t *testing.T) {
	a := memory.NewAdaptor()
	_ = a.Connect()
	mt := newMockTransport()
	// Create driver with transport but no endpoint, so Start won't connect.
	d := NewDriver(a, WithTransport(mt))
	_ = d.Start()
	defer d.Halt()

	if err := d.Navigate("http://example.com"); err == nil {
		t.Fatal("Navigate should fail when transport is not connected")
	}
}

func TestCommandNavigate(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"frameId":"frame-1"}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	cmd := d.Command("navigate")
	result := cmd(map[string]interface{}{"url": "http://example.com"})
	if err, ok := result.(error); ok {
		t.Fatalf("navigate command failed: %v", err)
	}
}

func TestCommandNavigateMissingURL(t *testing.T) {
	d, _, _ := newTestDriver(t)
	defer d.Halt()

	result := d.Command("navigate")(map[string]interface{}{})
	if _, ok := result.(error); !ok {
		t.Fatal("expected error for missing URL")
	}
}

func TestCommandEval(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"hello"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result := d.Command("eval")(map[string]interface{}{"expression": "document.title"})
	s, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T: %v", result, result)
	}
	if s != "hello" {
		t.Fatalf("expected 'hello', got %q", s)
	}
}

func TestCommandEvalMissingExpression(t *testing.T) {
	d, _, _ := newTestDriver(t)
	defer d.Halt()

	result := d.Command("eval")(map[string]interface{}{})
	if _, ok := result.(error); !ok {
		t.Fatal("expected error for missing expression")
	}
}

func TestCommandClick(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"undefined"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result := d.Command("click")(map[string]interface{}{"selector": "#btn"})
	if err, ok := result.(error); ok {
		t.Fatalf("click command failed: %v", err)
	}
}

func TestCommandType(t *testing.T) {
	mt := newMockTransport()
	mt.response = json.RawMessage(`{"result":{"value":"undefined"}}`)

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result := d.Command("type")(map[string]interface{}{
		"selector": "#input",
		"text":     "hello",
	})
	if err, ok := result.(error); ok {
		t.Fatalf("type command failed: %v", err)
	}
}

func TestCommandScreenshot(t *testing.T) {
	mt := newMockTransport()
	fakeData := []byte{0x89, 0x50}
	encoded := base64.StdEncoding.EncodeToString(fakeData)
	mt.response = json.RawMessage(fmt.Sprintf(`{"data":%q}`, encoded))

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result := d.Command("screenshot")(nil)
	data, ok := result.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T: %v", result, result)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(data))
	}
}

func TestCommandPageInfo(t *testing.T) {
	mt := newMockTransport()
	infoJSON := `{"url":"http://test.com","title":"Title"}`
	mt.response = json.RawMessage(fmt.Sprintf(`{"result":{"value":%q}}`, infoJSON))

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	result := d.Command("pageinfo")(nil)
	info, ok := result.(*PageInfo)
	if !ok {
		t.Fatalf("expected *PageInfo, got %T: %v", result, result)
	}
	if info.URL != "http://test.com" {
		t.Fatalf("expected URL 'http://test.com', got %q", info.URL)
	}
}

func TestTransportSendError(t *testing.T) {
	mt := newMockTransport()
	mt.err = fmt.Errorf("connection lost")

	d, _, _ := newTestDriver(t, WithTransport(mt), WithEndpoint("ws://test:9222"))
	defer d.Halt()

	if err := d.Navigate("http://example.com"); err == nil {
		t.Fatal("expected error when transport Send fails")
	}
}

func TestHaltIdempotent(t *testing.T) {
	d, _, _ := newTestDriver(t)

	if err := d.Halt(); err != nil {
		t.Fatalf("first Halt failed: %v", err)
	}
	// Second halt should not fail even though already disconnected.
	if err := d.Halt(); err != nil {
		t.Fatalf("second Halt failed: %v", err)
	}
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
