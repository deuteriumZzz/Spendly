package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Wire types of the OpenAI-compatible chat completions API.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Model that wrote this reply; set by Client, not sent over the wire.
	Model string `json:"-"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	// Gemini puts its thought signature here and rejects the next turn without it.
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// LLM is a chat model with tool calling: Client in production, a fake in tests.
type LLM interface {
	Chat(ctx context.Context, msgs []Message, tools []Tool) (Message, error)
}

// Client talks to any OpenAI-compatible /chat/completions endpoint: OpenAI,
// OpenRouter, Ollama, vLLM, Anthropic's OpenAI compatibility layer.
type Client struct {
	BaseURL string
	APIKey  string
	Models  []string // the first one is the main model, the rest are fallbacks
	HTTP    *http.Client
}

const maxAttempts = 3

// Chat moves on to the next model when one stays overloaded or hangs.
// A conversation that already has a reply from some model stays with it:
// Gemini's thought signatures in the history are valid only for that model.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool) (Message, error) {
	models := c.Models
	for _, m := range msgs {
		if m.Model != "" {
			models = []string{m.Model}
			break
		}
	}
	var lastErr error
	for i, model := range models {
		if i > 0 {
			slog.Warn("llm fallback", "from", models[i-1], "to", model, "err", lastErr)
		}
		msg, next, err := c.chat(ctx, model, msgs, tools)
		if err == nil {
			msg.Model = model
			return msg, nil
		}
		lastErr = err
		if !next || ctx.Err() != nil {
			break
		}
	}
	return Message{}, lastErr
}

// chat retries rate limits, 5xx and network errors with a short backoff.
// next reports whether another model is worth a try.
func (c *Client) chat(ctx context.Context, model string, msgs []Message, tools []Tool) (msg Message, next bool, err error) {
	// No temperature: newer models (Claude Opus/Sonnet 5.5, OpenAI reasoning
	// models) reject non-default sampling params; tool arguments are validated anyway.
	body, err := json.Marshal(map[string]any{"model": model, "messages": msgs, "tools": tools})
	if err != nil {
		return Message{}, false, fmt.Errorf("encode llm request: %w", err)
	}
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return Message{}, false, ctx.Err()
			}
		}
		msg, retry, err := c.do(ctx, model, body)
		if err == nil {
			return msg, false, nil
		}
		lastErr = err
		// A model that hung for the whole HTTP timeout will likely hang again,
		// and the reply deadline has no room for a second wait.
		if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
			return Message{}, true, err
		}
		if !retry {
			return Message{}, false, err
		}
	}
	return Message{}, true, lastErr
}

func (c *Client) do(ctx context.Context, model string, body []byte) (msg Message, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	start := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, ctx.Err() == nil, fmt.Errorf("llm request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Message{}, true, fmt.Errorf("read llm response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		retry := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		return Message{}, retry, fmt.Errorf("llm status %d: %s", res.StatusCode, clip(raw))
	}

	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Message{}, false, fmt.Errorf("unexpected llm response: %s", clip(raw))
	}
	msg = out.Choices[0].Message
	slog.Info("llm", "model", model, "dur", time.Since(start),
		"prompt_tokens", out.Usage.PromptTokens, "completion_tokens", out.Usage.CompletionTokens,
		"tool_calls", len(msg.ToolCalls))
	return msg, false, nil
}

func clip(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
