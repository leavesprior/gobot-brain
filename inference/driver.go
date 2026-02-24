// Copyright 2026 leavesprior contributors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package inference provides a multi-model LLM fallback chain driver for GoBot v2.
//
// The driver tries each registered Provider in order. When a provider fails,
// a "fallback" event is published and the next provider is attempted. If all
// providers fail, an aggregated error is returned.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leavesprior/gobot-brain/memory"
	"gobot.io/x/gobot/v2"
)

// ---------------------------------------------------------------------------
// Provider interface & options
// ---------------------------------------------------------------------------

// Provider is a pluggable LLM inference backend.
type Provider interface {
	Name() string
	Infer(ctx context.Context, prompt string, opts ...InferOption) (string, error)
}

// inferConfig holds optional parameters for a single inference call.
type inferConfig struct {
	Temperature  float64
	MaxTokens    int
	SystemPrompt string
}

// InferOption configures an inference call.
type InferOption func(*inferConfig)

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) InferOption {
	return func(c *inferConfig) { c.Temperature = t }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(n int) InferOption {
	return func(c *inferConfig) { c.MaxTokens = n }
}

// WithSystemPrompt sets a system-level instruction for the inference call.
func WithSystemPrompt(s string) InferOption {
	return func(c *inferConfig) { c.SystemPrompt = s }
}

func applyOpts(opts []InferOption) inferConfig {
	cfg := inferConfig{Temperature: 0.7, MaxTokens: 2048}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// Reasoning frameworks
// ---------------------------------------------------------------------------

// Framework selects a reasoning prompt wrapper.
type Framework int

const (
	// NoFramework passes the prompt through without modification.
	NoFramework Framework = iota
	// ChainOfThought asks the model to reason step by step.
	ChainOfThought
	// TreeOfThought asks the model to explore branching solution paths.
	TreeOfThought
	// Adversarial asks the model to find weaknesses and edge cases.
	Adversarial
	// ReAct asks the model to use the Reason-Act-Observe loop.
	ReAct
)

var frameworkPreambles = map[Framework]string{
	ChainOfThought: "Think step by step. Show your reasoning at each stage before " +
		"providing your final answer.\n\n",
	TreeOfThought: "You are an expert problem solver. Break this problem into branches, " +
		"explore each branch, evaluate which is most promising, and synthesize " +
		"the best answer.\n\n",
	Adversarial: "Assume this will fail. Find every weakness and edge case. " +
		"Then provide the most robust solution that addresses them all.\n\n",
	ReAct: "Use the Reason-Act-Observe loop. First reason about what to do, " +
		"then describe the action you would take, then describe what you " +
		"would observe. Repeat until you reach a conclusion.\n\n",
}

func wrapPrompt(fw Framework, prompt string) string {
	if preamble, ok := frameworkPreambles[fw]; ok {
		return preamble + prompt
	}
	return prompt
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

const (
	// InferenceEvent is published when an inference completes successfully.
	InferenceEvent = "inference"
	// FallbackEvent is published when a provider fails and the next is tried.
	FallbackEvent = "fallback"
	// ErrorEvent is published when all providers fail.
	ErrorEvent = "error"
)

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// Driver is a GoBot v2 Device that provides multi-model LLM inference with
// automatic fallback, reasoning framework wrappers, and memory persistence.
type Driver struct {
	name       string
	connection gobot.Connection
	providers  []Provider

	gobot.Eventer
	gobot.Commander
}

// inferResult is persisted to the memory adaptor after each successful call.
type inferResult struct {
	Prompt   string `json:"prompt"`
	Result   string `json:"result"`
	Provider string `json:"provider"`
	Time     string `json:"time"`
}

// NewDriver creates a new inference driver backed by the given memory adaptor
// and provider chain. Providers are tried in the order supplied.
func NewDriver(a *memory.Adaptor, providers ...Provider) *Driver {
	d := &Driver{
		name:       "inference",
		connection: a,
		providers:  providers,
		Eventer:    gobot.NewEventer(),
		Commander:  gobot.NewCommander(),
	}

	d.AddEvent(InferenceEvent)
	d.AddEvent(FallbackEvent)
	d.AddEvent(ErrorEvent)

	d.AddCommand("infer", func(params map[string]interface{}) interface{} {
		prompt, ok := params["prompt"].(string)
		if !ok {
			return fmt.Errorf("infer command: missing string 'prompt' parameter")
		}
		result, err := d.Infer(prompt)
		if err != nil {
			return err
		}
		return result
	})

	return d
}

// ---------------------------------------------------------------------------
// gobot.Driver interface
// ---------------------------------------------------------------------------

// Name returns the driver name.
func (d *Driver) Name() string { return d.name }

// SetName sets the driver name.
func (d *Driver) SetName(name string) { d.name = name }

// Start validates that at least one provider is registered.
func (d *Driver) Start() error {
	if len(d.providers) == 0 {
		return errors.New("inference: no providers configured")
	}
	return nil
}

// Halt performs cleanup.
func (d *Driver) Halt() error { return nil }

// Connection returns the underlying memory adaptor as a gobot.Connection.
func (d *Driver) Connection() gobot.Connection {
	return d.connection
}

// ---------------------------------------------------------------------------
// Inference
// ---------------------------------------------------------------------------

// Infer sends the prompt through the provider fallback chain and returns the
// first successful result. Each failed provider triggers a "fallback" event.
// If all providers fail, an "error" event is published and an aggregated
// error is returned.
func (d *Driver) Infer(prompt string) (string, error) {
	return d.infer(prompt)
}

// InferWithFramework wraps the prompt with the given reasoning framework
// preamble and then runs the fallback chain.
func (d *Driver) InferWithFramework(fw Framework, prompt string) (string, error) {
	return d.infer(wrapPrompt(fw, prompt))
}

func (d *Driver) infer(prompt string) (string, error) {
	ctx := context.Background()
	var errs []string

	for i, p := range d.providers {
		result, err := p.Infer(ctx, prompt)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
			if i < len(d.providers)-1 {
				d.Publish(FallbackEvent, p.Name())
			}
			continue
		}

		// Publish success event.
		d.Publish(InferenceEvent, result)

		// Persist to memory adaptor, best-effort.
		if a, ok := d.connection.(*memory.Adaptor); ok {
			_ = a.Store("inference", "last", inferResult{
				Prompt:   prompt,
				Result:   result,
				Provider: p.Name(),
				Time:     time.Now().UTC().Format(time.RFC3339),
			})
		}

		return result, nil
	}

	aggErr := fmt.Errorf("inference: all providers failed: %s", strings.Join(errs, "; "))
	d.Publish(ErrorEvent, aggErr.Error())
	return "", aggErr
}

// ---------------------------------------------------------------------------
// OllamaProvider
// ---------------------------------------------------------------------------

// OllamaProvider calls a local Ollama instance.
type OllamaProvider struct {
	host   string
	model  string
	client *http.Client
}

// OllamaOption configures the Ollama provider.
type OllamaOption func(*OllamaProvider)

// WithOllamaHost sets the Ollama API base URL.
func WithOllamaHost(host string) OllamaOption {
	return func(p *OllamaProvider) { p.host = host }
}

// WithOllamaModel sets the Ollama model name.
func WithOllamaModel(model string) OllamaOption {
	return func(p *OllamaProvider) { p.model = model }
}

// NewOllamaProvider creates a provider backed by Ollama.
func NewOllamaProvider(opts ...OllamaOption) *OllamaProvider {
	p := &OllamaProvider{
		host:   "http://localhost:11434",
		model:  "llama3",
		client: &http.Client{Timeout: 120 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns "ollama".
func (p *OllamaProvider) Name() string { return "ollama" }

// ollamaRequest is the JSON body sent to the Ollama generate endpoint.
type ollamaRequest struct {
	Model       string  `json:"model"`
	Prompt      string  `json:"prompt"`
	System      string  `json:"system,omitempty"`
	Stream      bool    `json:"stream"`
	Temperature float64 `json:"temperature,omitempty"`
}

// ollamaResponse is the JSON body returned by the Ollama generate endpoint.
type ollamaResponse struct {
	Response string `json:"response"`
}

// Infer sends a prompt to Ollama and returns the generated text.
func (p *OllamaProvider) Infer(ctx context.Context, prompt string, opts ...InferOption) (string, error) {
	cfg := applyOpts(opts)

	body, err := json.Marshal(ollamaRequest{
		Model:       p.model,
		Prompt:      prompt,
		System:      cfg.SystemPrompt,
		Stream:      false,
		Temperature: cfg.Temperature,
	})
	if err != nil {
		return "", fmt.Errorf("ollama: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama: decode: %w", err)
	}

	return result.Response, nil
}

// ---------------------------------------------------------------------------
// OpenAIProvider
// ---------------------------------------------------------------------------

// OpenAIProvider calls an OpenAI-compatible chat completions endpoint.
type OpenAIProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// OpenAIOption configures the OpenAI provider.
type OpenAIOption func(*OpenAIProvider)

// WithOpenAIBaseURL sets the API base URL.
func WithOpenAIBaseURL(url string) OpenAIOption {
	return func(p *OpenAIProvider) { p.baseURL = url }
}

// WithOpenAIModel sets the model name.
func WithOpenAIModel(model string) OpenAIOption {
	return func(p *OpenAIProvider) { p.model = model }
}

// NewOpenAIProvider creates a provider backed by an OpenAI-compatible API.
func NewOpenAIProvider(apiKey string, opts ...OpenAIOption) *OpenAIProvider {
	p := &OpenAIProvider{
		baseURL: "https://api.openai.com",
		apiKey:  apiKey,
		model:   "gpt-4",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns "openai".
func (p *OpenAIProvider) Name() string { return "openai" }

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Infer sends a prompt to the OpenAI-compatible endpoint and returns the
// generated text.
func (p *OpenAIProvider) Infer(ctx context.Context, prompt string, opts ...InferOption) (string, error) {
	cfg := applyOpts(opts)

	messages := make([]openAIMessage, 0, 2)
	if cfg.SystemPrompt != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: cfg.SystemPrompt})
	}
	messages = append(messages, openAIMessage{Role: "user", Content: prompt})

	body, err := json.Marshal(openAIRequest{
		Model:       p.model,
		Messages:    messages,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("openai: marshal: %w", err)
	}

	url := strings.TrimRight(p.baseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("openai: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("openai: decode: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", errors.New("openai: empty choices")
	}

	return result.Choices[0].Message.Content, nil
}
