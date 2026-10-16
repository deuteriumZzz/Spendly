package api

import (
	"fmt"
	"net/http"
	"strings"

	"rsc.io/qr"

	"spendly/internal/expense"
)

type nameInput struct {
	Name string `json:"name"`
}

type inviteResponse struct {
	expense.Invite
	URL   string `json:"url"`
	QRSVG string `json:"qr_svg"`
}

// createInvite issues a one-time link into the family. The head forwards it
// (or shows the QR); the bot gets the code as "/start <code>".
func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	if s.botName == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "бот не запущен: задайте TELEGRAM_TOKEN"})
		return
	}
	var in nameInput
	if err := decode(w, r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	name, err := expense.ValidateName(in.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	inv, err := s.db.CreateInvite(r.Context(), s.familyID, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	url := "https://t.me/" + s.botName + "?start=" + inv.Code
	svg, err := qrSVG(url)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inviteResponse{Invite: inv, URL: url, QRSVG: svg})
}

func (s *Server) renameUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var in nameInput
	if err := decode(w, r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	name, err := expense.ValidateName(in.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.db.RenameUser(r.Context(), s.familyID, id, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// qrSVG draws the code as one path, black on white with the standard
// 4-module quiet zone: scanners need that contrast whatever the panel theme.
func qrSVG(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", fmt.Errorf("qr: %w", err)
	}
	var b strings.Builder
	n := c.Size + 8
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, n, n)
	for y := range c.Size {
		for x := range c.Size {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}
