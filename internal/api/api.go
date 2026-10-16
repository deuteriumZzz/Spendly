// Package api serves the REST API and the web panel for the head of the family.
// Per the spec the panel has no authentication; it sees one family.
package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"spendly/internal/expense"
	"spendly/web"
)

//go:embed openapi.yaml
var openapiSpec []byte

type Server struct {
	db       *expense.DB
	familyID int64
	loc      *time.Location
	hosts    []string // domain names the panel may be opened under, besides localhost and IPs
	botName  string   // Telegram username for invite links; "" when the bot is off
}

func New(db *expense.DB, familyID int64, loc *time.Location, hosts []string, botName string) *Server {
	return &Server{db: db, familyID: familyID, loc: loc, hosts: hosts, botName: botName}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/expenses", s.listExpenses)
	mux.HandleFunc("POST /api/expenses", s.createExpense)
	mux.HandleFunc("PATCH /api/expenses/{id}", s.updateExpense)
	mux.HandleFunc("DELETE /api/expenses/{id}", s.deleteExpense)
	mux.HandleFunc("GET /api/users", s.listUsers)
	mux.HandleFunc("PATCH /api/users/{id}", s.renameUser)
	mux.HandleFunc("POST /api/invites", s.createInvite)
	mux.HandleFunc("GET /api/categories", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, expense.Categories)
	})
	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapiSpec) // a failed write means the client is gone
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerPage))
	})
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /", http.FileServerFS(web.FS))
	// The panel has no auth (per spec), so other sites must not be able to use
	// the visitor's browser against it: CSRF, DNS rebinding, clickjacking.
	return logRequests(securityHeaders(s.checkHost(http.NewCrossOriginProtection().Handler(mux))))
}

func (s *Server) store() *expense.Store { return s.db.ForFamily(s.familyID) }

func (s *Server) today() string { return time.Now().In(s.loc).Format(time.DateOnly) }

type listResponse struct {
	Items   []expense.Expense   `json:"items"`
	Summary expense.Summary     `json:"summary"`
	ByUser  []expense.UserTotal `json:"by_user"`
}

func (s *Server) listExpenses(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := s.store().List(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Totals ignore paging: they describe the whole filtered set.
	f.Limit, f.Offset = 0, 0
	sum, err := s.store().Sum(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	byUser, err := s.store().TotalsByUser(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Summary: sum, ByUser: byUser})
}

func parseFilter(r *http.Request) (expense.Filter, error) {
	q := r.URL.Query()
	f := expense.Filter{From: q.Get("from"), To: q.Get("to"), Text: q.Get("text"), OrderBy: q.Get("order")}
	var err error
	if f.UserID, err = intParam(q.Get("user_id"), "user_id"); err != nil {
		return f, err
	}
	if c := q.Get("category"); c != "" {
		if f.Category, err = expense.ParseCategory(c); err != nil {
			return f, err
		}
	}
	limit, err := intParam(q.Get("limit"), "limit")
	if err != nil {
		return f, err
	}
	offset, err := intParam(q.Get("offset"), "offset")
	if err != nil {
		return f, err
	}
	f.Limit, f.Offset = int(limit), int(offset)
	return f, f.Validate()
}

func intParam(s, name string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, &expense.ValidationError{Field: name, Msg: "ожидается неотрицательное целое число"}
	}
	return n, nil
}

func (s *Server) createExpense(w http.ResponseWriter, r *http.Request) {
	var in expense.Input
	if err := decode(w, r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	if in.UserID == 0 {
		writeErr(w, r, &expense.ValidationError{Field: "user_id", Msg: "выбери участника"})
		return
	}
	if err := in.Validate(s.today()); err != nil {
		writeErr(w, r, err)
		return
	}
	e, err := s.store().Create(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) updateExpense(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var p expense.Patch
	if err := decode(w, r, &p); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := p.Validate(s.today()); err != nil {
		writeErr(w, r, err)
		return
	}
	e, err := s.store().Update(r.Context(), id, p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) deleteExpense(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := s.store().Delete(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.Users(r.Context(), s.familyID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &expense.ValidationError{Field: "id", Msg: "некорректный id"}
	}
	return id, nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var verr *expense.ValidationError
		if errors.As(err, &verr) {
			return verr
		}
		return &expense.ValidationError{Field: "body", Msg: "некорректный JSON: " + err.Error()}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

type errorResponse struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var verr *expense.ValidationError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: verr.Msg, Field: verr.Field})
	case errors.Is(err, expense.ErrNotFound), errors.Is(err, expense.ErrUserNotFound):
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "внутренняя ошибка"})
	}
}

// Swagger UI runs on the panel's origin, so the CDN files are pinned by hash:
// a tampered file is refused by the browser instead of reading the API.
const swaggerPage = `<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><title>Spendly API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css"
  integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn" crossorigin="anonymous"></head>
<body><div id="ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"
  integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep" crossorigin="anonymous"></script>
<script>SwaggerUIBundle({url: "/api/openapi.yaml", dom_id: "#ui"});</script>
</body></html>`
