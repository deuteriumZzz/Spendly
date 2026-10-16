package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
			"tool_calls": [{"id": "c1", "type": "function", "function": {"name": "add_expense", "arguments": "{\"amount\": 350}"}}]}}]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1/", APIKey: "key", Model: "test-model", HTTP: srv.Client()}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "кофе 350"}}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "add_expense" {
		t.Errorf("attempts %d, msg %+v", attempts, msg)
	}
}

func TestClientDoesNotRetryBadRequest(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, `{"error": "bad model"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Model: "x", HTTP: srv.Client()}
	if _, err := c.Chat(context.Background(), nil, tools); err == nil || attempts != 1 {
		t.Errorf("err %v, attempts %d; want an error after one attempt", err, attempts)
	}
}
