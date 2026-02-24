package inference

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leavesprior/gobot-brain/memory"
	"gobot.io/x/gobot/v2"
)

// Compile-time check: Driver must satisfy gobot.Device.
var _ gobot.Device = (*Driver)(nil)

// ---------------------------------------------------------------------------
// Mock provider
// ---------------------------------------------------------------------------

type mockProvider struct {
	name      string
	response  string
	err       error
	callCount int
	mu        sync.Mutex
}

func newMockProvider(name, response string, err error) *mockProvider {
	return &mockProvider{name: name, response: response, err: err}
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Infer(_ context.Context, prompt string, _ ...InferOption) (string, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func (m *mockProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDriverStartNoProviders(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a)

	if err := d.Start(); err == nil {
		t.Fatal("expected error when starting with no providers")
	}
}

func TestDriverStartWithProviders(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock", "ok", nil)
	d := NewDriver(a, p)

	if err := d.Start(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDriverHalt(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a, newMockProvider("mock", "ok", nil))

	if err := d.Halt(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDriverConnection(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a, newMockProvider("mock", "ok", nil))

	conn := d.Connection()
	if conn != a {
		t.Fatal("Connection() should return the memory adaptor")
	}
}

func TestDriverNameGetSet(t *testing.T) {
	a := memory.NewAdaptor()
	d := NewDriver(a)

	if d.Name() != "inference" {
		t.Fatalf("expected default name 'inference', got %q", d.Name())
	}

	d.SetName("custom")
	if d.Name() != "custom" {
		t.Fatalf("expected name 'custom', got %q", d.Name())
	}
}

func TestInferSingleProviderSuccess(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock", "hello world", nil)
	d := NewDriver(a, p)

	result, err := d.Infer("test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("expected 'hello world', got %q", result)
	}
	if p.calls() != 1 {
		t.Fatalf("expected 1 call, got %d", p.calls())
	}
}

func TestInferFallbackChain(t *testing.T) {
	a := memory.NewAdaptor()
	failing := newMockProvider("failing", "", errors.New("model offline"))
	succeeding := newMockProvider("backup", "fallback response", nil)
	d := NewDriver(a, failing, succeeding)

	// Track fallback events.
	var fallbackEvents []string
	var mu sync.Mutex
	_ = d.On(FallbackEvent, func(data interface{}) {
		mu.Lock()
		fallbackEvents = append(fallbackEvents, data.(string))
		mu.Unlock()
	})

	result, err := d.Infer("test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "fallback response" {
		t.Fatalf("expected 'fallback response', got %q", result)
	}

	if failing.calls() != 1 {
		t.Fatalf("expected failing provider called once, got %d", failing.calls())
	}
	if succeeding.calls() != 1 {
		t.Fatalf("expected succeeding provider called once, got %d", succeeding.calls())
	}

	// GoBot publishes events asynchronously.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fallbackEvents) != 1 || fallbackEvents[0] != "failing" {
		t.Fatalf("expected fallback event for 'failing', got %v", fallbackEvents)
	}
}

func TestInferAllProvidersFail(t *testing.T) {
	a := memory.NewAdaptor()
	p1 := newMockProvider("p1", "", errors.New("err1"))
	p2 := newMockProvider("p2", "", errors.New("err2"))
	d := NewDriver(a, p1, p2)

	// Track error events.
	var errorEvents []string
	var mu sync.Mutex
	_ = d.On(ErrorEvent, func(data interface{}) {
		mu.Lock()
		errorEvents = append(errorEvents, data.(string))
		mu.Unlock()
	})

	_, err := d.Infer("test prompt")
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}

	if !strings.Contains(err.Error(), "p1: err1") || !strings.Contains(err.Error(), "p2: err2") {
		t.Fatalf("error should contain both provider errors, got: %v", err)
	}

	// GoBot publishes events asynchronously.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(errorEvents) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(errorEvents))
	}
}

func TestInferWithFrameworkWrapsPrompt(t *testing.T) {
	a := memory.NewAdaptor()

	// Capture the prompt that reaches the provider.
	var capturedPrompt string
	capture := &promptCapture{response: "ok"}
	d := NewDriver(a, capture)

	_, err := d.InferWithFramework(ChainOfThought, "solve 2+2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	capturedPrompt = capture.lastPrompt()
	if !strings.HasPrefix(capturedPrompt, "Think step by step") {
		t.Fatalf("expected ChainOfThought preamble, got %q", capturedPrompt)
	}
	if !strings.HasSuffix(capturedPrompt, "solve 2+2") {
		t.Fatalf("expected prompt suffix 'solve 2+2', got %q", capturedPrompt)
	}
}

func TestInferWithFrameworkNoFramework(t *testing.T) {
	a := memory.NewAdaptor()
	capture := &promptCapture{response: "ok"}
	d := NewDriver(a, capture)

	_, err := d.InferWithFramework(NoFramework, "raw prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.lastPrompt() != "raw prompt" {
		t.Fatalf("NoFramework should not modify prompt, got %q", capture.lastPrompt())
	}
}

func TestInferWithFrameworkAllTypes(t *testing.T) {
	frameworks := []struct {
		fw     Framework
		prefix string
	}{
		{TreeOfThought, "You are an expert problem solver"},
		{ChainOfThought, "Think step by step"},
		{Adversarial, "Assume this will fail"},
		{ReAct, "Use the Reason-Act-Observe loop"},
	}

	for _, tc := range frameworks {
		a := memory.NewAdaptor()
		capture := &promptCapture{response: "ok"}
		d := NewDriver(a, capture)

		_, err := d.InferWithFramework(tc.fw, "test")
		if err != nil {
			t.Fatalf("framework %d: unexpected error: %v", tc.fw, err)
		}

		if !strings.Contains(capture.lastPrompt(), tc.prefix) {
			t.Fatalf("framework %d: expected prefix %q in prompt, got %q",
				tc.fw, tc.prefix, capture.lastPrompt())
		}
	}
}

func TestInferenceEventPublished(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock", "result-value", nil)
	d := NewDriver(a, p)

	var inferenceResult string
	var mu sync.Mutex
	_ = d.On(InferenceEvent, func(data interface{}) {
		mu.Lock()
		inferenceResult = data.(string)
		mu.Unlock()
	})

	result, err := d.Infer("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// GoBot publishes events asynchronously.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if inferenceResult != result {
		t.Fatalf("inference event data %q should match result %q", inferenceResult, result)
	}
}

func TestInferPersistsToMemory(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock-provider", "persisted-result", nil)
	d := NewDriver(a, p)

	_, err := d.Infer("persist test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, err2 := a.Retrieve("inference", "last")
	if err2 != nil {
		t.Fatalf("failed to retrieve from memory: %v", err2)
	}
	// The value is stored as inferResult; access it via type assertion on the map.
	m, ok := val.(inferResult)
	if !ok {
		t.Fatalf("stored value is not inferResult, got %T", val)
	}
	if m.Result != "persisted-result" {
		t.Fatalf("expected stored result 'persisted-result', got %q", m.Result)
	}
	if m.Provider != "mock-provider" {
		t.Fatalf("expected stored provider 'mock-provider', got %q", m.Provider)
	}
	if m.Prompt != "persist test" {
		t.Fatalf("expected stored prompt 'persist test', got %q", m.Prompt)
	}
}

func TestInferCommand(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock", "command-result", nil)
	d := NewDriver(a, p)

	cmd := d.Command("infer")
	if cmd == nil {
		t.Fatal("expected 'infer' command to be registered")
	}

	result := cmd(map[string]interface{}{"prompt": "hello"})
	if s, ok := result.(string); !ok || s != "command-result" {
		t.Fatalf("expected 'command-result', got %v", result)
	}
}

func TestInferCommandMissingPrompt(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("mock", "ok", nil)
	d := NewDriver(a, p)

	cmd := d.Command("infer")
	result := cmd(map[string]interface{}{})

	if _, ok := result.(error); !ok {
		t.Fatal("expected error when prompt is missing")
	}
}

func TestWithTemperatureAndMaxTokens(t *testing.T) {
	cfg := applyOpts([]InferOption{
		WithTemperature(0.3),
		WithMaxTokens(512),
	})
	if cfg.Temperature != 0.3 {
		t.Fatalf("expected temperature 0.3, got %f", cfg.Temperature)
	}
	if cfg.MaxTokens != 512 {
		t.Fatalf("expected max tokens 512, got %d", cfg.MaxTokens)
	}
}

func TestDefaultInferConfig(t *testing.T) {
	cfg := applyOpts(nil)
	if cfg.Temperature != 0.7 {
		t.Fatalf("expected default temperature 0.7, got %f", cfg.Temperature)
	}
	if cfg.MaxTokens != 2048 {
		t.Fatalf("expected default max tokens 2048, got %d", cfg.MaxTokens)
	}
}

func TestFallbackEventNotPublishedForLastProvider(t *testing.T) {
	a := memory.NewAdaptor()
	p := newMockProvider("only", "", errors.New("fail"))
	d := NewDriver(a, p)

	var fallbackCount int
	var mu sync.Mutex
	_ = d.On(FallbackEvent, func(data interface{}) {
		mu.Lock()
		fallbackCount++
		mu.Unlock()
	})

	_, _ = d.Infer("test")

	mu.Lock()
	defer mu.Unlock()
	if fallbackCount != 0 {
		t.Fatalf("no fallback event should fire for the last (only) provider, got %d", fallbackCount)
	}
}

func TestWrapPromptUnknownFramework(t *testing.T) {
	result := wrapPrompt(Framework(99), "raw")
	if result != "raw" {
		t.Fatalf("unknown framework should return raw prompt, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// promptCapture: a provider that records the prompt it receives
// ---------------------------------------------------------------------------

type promptCapture struct {
	response string
	prompt   string
	mu       sync.Mutex
}

func (p *promptCapture) Name() string { return "capture" }

func (p *promptCapture) Infer(_ context.Context, prompt string, _ ...InferOption) (string, error) {
	p.mu.Lock()
	p.prompt = prompt
	p.mu.Unlock()
	return p.response, nil
}

func (p *promptCapture) lastPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prompt
}
