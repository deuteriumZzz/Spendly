package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"spendly/internal/api"
	"spendly/internal/expense"
	"spendly/internal/testdb"
)

func TestExpensesCRUD(t *testing.T) {
	db := expense.NewDB(testdb.New(t))
	if _, err := db.EnsureUser(context.Background(), 101, expense.DefaultFamilyID, "Алиса"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(db, expense.DefaultFamilyID, time.UTC, nil).Handler())
	defer srv.Close()

	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out) // 204 has no body; callers check the status
		return res.StatusCode, out
	}

	today := time.Now().UTC().Format(time.DateOnly)
	code, created := do("POST", "/api/expenses",
		`{"user_id": 101, "amount": "1 200,5", "category": "Транспорт", "spent_at": "`+today+`", "comment": "такси"}`)
	if code != http.StatusCreated || created["amount"] != 1200.5 || created["category"] != "транспорт" {
		t.Fatalf("create: %d %v", code, created)
	}
	path := "/api/expenses/" + strconv.FormatInt(int64(created["id"].(float64)), 10)

	bad := map[string]string{
		"unknown category": `{"user_id": 101, "amount": 1, "category": "кофе", "spent_at": "` + today + `"}`,
		"future date":      `{"user_id": 101, "amount": 1, "category": "еда", "spent_at": "2999-01-01"}`,
		"unknown member":   `{"user_id": 999, "amount": 1, "category": "еда", "spent_at": "` + today + `"}`,
		"unknown field":    `{"user_id": 101, "amount": 1, "category": "еда", "spent_at": "` + today + `", "x": 1}`,
	}
	for name, body := range bad {
		if code, out := do("POST", "/api/expenses", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v, want 400", name, code, out)
		}
	}

	if code, out := do("PATCH", path, `{"amount": 450}`); code != http.StatusOK || out["amount"] != 450.0 {
		t.Errorf("patch: %d %v", code, out)
	}
	code, list := do("GET", "/api/expenses?category=транспорт&user_id=101", "")
	summary, _ := list["summary"].(map[string]any)
	if code != http.StatusOK || summary["count"] != 1.0 || summary["total"] != 450.0 {
		t.Errorf("list: %d %v", code, list)
	}
	if byUser, _ := list["by_user"].([]any); len(byUser) != 1 || byUser[0].(map[string]any)["name"] != "Алиса" {
		t.Errorf("by_user = %v", list["by_user"])
	}
	if code, _ := do("GET", "/api/expenses?from=yesterday", ""); code != http.StatusBadRequest {
		t.Errorf("bad filter date: %d, want 400", code)
	}
	if code, _ := do("DELETE", path, ""); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do("DELETE", path, ""); code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", code)
	}
}

// The panel has no auth, so other websites must not reach it through the visitor's browser.
func TestPanelRejectsForeignSites(t *testing.T) {
	db := expense.NewDB(testdb.New(t))
	if _, err := db.EnsureUser(context.Background(), 101, expense.DefaultFamilyID, "Алиса"); err != nil {
		t.Fatal(err)
	}
	h := api.New(db, expense.DefaultFamilyID, time.UTC, []string{"panel.example"}).Handler()
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		if req.Host == "example.com" { // httptest default; the panel's real host is localhost
			req.Host = "localhost:8080"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	hosts := map[string]int{
		"localhost:8080":      http.StatusOK,
		"127.0.0.1:8080":      http.StatusOK,
		"[::1]:8080":          http.StatusOK,
		"192.168.1.10":        http.StatusOK,
		"panel.example":       http.StatusOK,
		"evil.example:8080":   http.StatusMisdirectedRequest, // DNS rebinding
		"localhost.evil.test": http.StatusMisdirectedRequest,
	}
	for host, want := range hosts {
		req := httptest.NewRequest("GET", "/api/expenses", nil)
		req.Host = host
		if got := serve(req).Code; got != want {
			t.Errorf("Host %s: %d, want %d", host, got, want)
		}
	}

	body := `{"user_id": 101, "amount": 1, "category": "еда", "spent_at": "2026-01-01"}`
	req := httptest.NewRequest("POST", "/api/expenses", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain") // a "simple" request needs no CORS preflight
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if got := serve(req).Code; got != http.StatusForbidden {
		t.Errorf("cross-site POST: %d, want 403", got)
	}
	req = httptest.NewRequest("POST", "/api/expenses", strings.NewReader(body))
	req.Header.Set("Origin", "https://evil.example")
	if got := serve(req).Code; got != http.StatusForbidden {
		t.Errorf("POST with foreign Origin: %d, want 403", got)
	}

	rec := serve(httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("security headers missing: %v", rec.Header())
	}
}
