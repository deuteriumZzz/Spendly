package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientRetriesAndParsesToolCalls(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model string `json:"model"`
			Tools []Tool `json:"tools"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		if body.Model != "test-model" || len(body.Tools) != len(tools) {
			t.Errorf("model %q, %d tools", body.Model, len(body.Tools))
		}
		if attempts == 1 {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": null,
			"tool_calls": [{"id": "c1", "type": "function", "function": {"name": "add_expense", "arguments": "{\"amount\": 350}"},
			"extra_content": {"google": {"thought_signature": "sig"}}}]}}]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1/", APIKey: "key", Models: []string{"test-model"}, HTTP: srv.Client()}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "кофе 350"}}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "add_expense" {
		t.Errorf("attempts %d, msg %+v", attempts, msg)
	}
	// The signature must go back to the model verbatim with the history.
	if out, _ := json.Marshal(msg); !json.Valid(out) || !bytes.Contains(out, []byte(`"thought_signature":"sig"`)) {
		t.Errorf("extra_content lost: %s", out)
	}
}

func TestClientDoesNotRetryBadRequest(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, `{"error": "bad model"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Models: []string{"x", "y"}, HTTP: srv.Client()}
	if _, err := c.Chat(context.Background(), nil, tools); err == nil || attempts != 1 {
		t.Errorf("err %v, attempts %d; want an error after one attempt, no fallback", err, attempts)
	}
}

func TestClientFallsBackToNextModel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hang      bool
		mainCalls int
	}{
		{"overloaded", false, maxAttempts},
		{"hangs", true, 1}, // a timeout is not retried: the reply deadline has no room for it
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				calls[body.Model]++
				if body.Model == "main" {
					if tc.hang {
						<-r.Context().Done()
						return
					}
					http.Error(w, "This model is currently experiencing high demand", http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "ok"}}]}`))
			}))
			defer srv.Close()

			c := &Client{BaseURL: srv.URL, Models: []string{"main", "backup"}, HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
			msgs := []Message{{Role: "user", Content: "кофе 350"}}
			msg, err := c.Chat(context.Background(), msgs, tools)
			if err != nil || msg.Content != "ok" || msg.Model != "backup" || calls["main"] != tc.mainCalls {
				t.Fatalf("msg %+v, err %v, calls %v", msg, err, calls)
			}
			// The rest of the reply stays with the model that started it.
			msgs = append(msgs, msg, Message{Role: "tool", ToolCallID: "c1", Content: "{}"})
			if _, err := c.Chat(context.Background(), msgs, tools); err != nil || calls["main"] != tc.mainCalls || calls["backup"] != 2 {
				t.Errorf("pinned call: err %v, calls %v", err, calls)
			}
		})
	}
}
