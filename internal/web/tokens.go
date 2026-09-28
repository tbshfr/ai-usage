package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// tokensView is the OTLP tokens page: tokens grouped by their group label with
// all-time usage. Created is set only on the response to a
// successful create, the one time the plaintext is shown.
type tokensView struct {
	CanManage  bool
	Created    *createdToken
	Groups     []tokenGroupView
	Unauth     *storage.Breakdown // usage without a token, nil when none
	UnauthURL  string
	GroupNames []string // datalist suggestions for the group input
	Form       tokenForm
}

type createdToken struct {
	Plain       string
	Label       string
	Regenerated bool // an existing token got a new secret
}

// tokenForm echoes the create form's values after a validation error.
type tokenForm struct {
	Name  string
	Group string
}

type tokenGroupView struct {
	Name   string
	URL    string
	Total  storage.Breakdown
	Tokens []tokenRowView
}

type tokenRowView struct {
	storage.Token
	URL   string
	Usage storage.Breakdown
}

// requireTokenStore requires the same store used by the ingestion listeners.
func (s *server) requireTokenStore(w http.ResponseWriter) bool {
	if s.tokens == nil {
		http.Error(w, "token management is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *server) tokensPage(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, http.StatusOK, nil, tokenForm{}, "")
}

// renderTokens renders the OTLP tokens page. Usage is all-time: the page is for
// managing tokens, and the dashboard's token/group filters cover ranges.
func (s *server) renderTokens(w http.ResponseWriter, r *http.Request, status int, created *createdToken, form tokenForm, errMsg string) {
	ctx := r.Context()
	d := &pageData{
		Title: "OTLP tokens", Active: "tokens", Error: errMsg,
		ShowLogout: s.dash != nil, Version: s.version,
		Tokens: tokensView{CanManage: s.tokens != nil, Created: created, Form: form},
	}
	tokens, err := storage.ListTokens(ctx, s.db)
	if err != nil {
		renderTokensError(w, status, d, err)
		return
	}
	usage, err := storage.ByToken(ctx, s.db, storage.Filter{})
	if err != nil {
		renderTokensError(w, status, d, err)
		return
	}
	var pending map[int64]time.Time
	if s.tokens != nil {
		pending = s.tokens.PendingUse()
	}
	byKey := make(map[string]storage.Breakdown, len(usage))
	for _, b := range usage {
		byKey[b.Key] = b
	}
	v := tokensView{CanManage: s.tokens != nil, Created: created, Form: form}
	if b, ok := byKey[""]; ok {
		v.Unauth = &b
		v.UnauthURL = dashboardURL("token", storage.TokenNone)
	}
	groupIdx := map[string]int{}
	for _, t := range tokens {
		if p, ok := pending[t.ID]; ok && (t.LastUsed == nil || p.After(*t.LastUsed)) {
			p = p.UTC()
			t.LastUsed = &p
		}
		i, ok := groupIdx[t.Group]
		if !ok {
			i = len(v.Groups)
			groupIdx[t.Group] = i
			g := tokenGroupView{Name: t.Group}
			if t.Group != "" {
				g.URL = dashboardURL("group", t.Group)
				v.GroupNames = append(v.GroupNames, t.Group)
			} else {
				g.URL = dashboardURL("ungrouped", "true")
			}
			v.Groups = append(v.Groups, g)
		}
		id := strconv.FormatInt(t.ID, 10)
		row := tokenRowView{Token: t, URL: dashboardURL("token", id), Usage: byKey[id]}
		v.Groups[i].Tokens = append(v.Groups[i].Tokens, row)
	}
	for i := range v.Groups {
		rows := make([]storage.Breakdown, len(v.Groups[i].Tokens))
		for j, t := range v.Groups[i].Tokens {
			rows[j] = t.Usage
		}
		v.Groups[i].Total = totalBreakdown(rows)
	}
	d.Tokens = v
	renderTemplate(w, pageTmpls["tokens"], "layout", status, d)
}

// Once a secret is committed, a failure to load the rest of the page must
// not prevent its one-time display.
func renderTokensError(w http.ResponseWriter, status int, d *pageData, err error) {
	if d.Tokens.Created == nil {
		writeErr(w, err)
		return
	}
	slog.Error("load token page after saving secret", "error", err)
	d.Error = "Token saved, but its usage details could not be loaded. Copy the secret below."
	renderTemplate(w, pageTmpls["tokens"], "layout", status, d)
}

// dashboardURL links to the dashboard filtered by one dimension.
func dashboardURL(key, value string) string {
	return "/?" + url.Values{key: {value}}.Encode()
}

func (s *server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTokenStore(w) {
		return
	}
	form := tokenForm{Name: r.PostFormValue("name"), Group: r.PostFormValue("group")}
	name, err := storage.CleanTokenField("name", form.Name, true)
	if err == nil {
		form.Group, err = storage.CleanTokenField("group", form.Group, false)
	}
	if err != nil {
		s.renderTokens(w, r, http.StatusBadRequest, nil, form, err.Error())
		return
	}
	plain, _, err := s.tokens.Create(r.Context(), name, form.Group)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The plaintext appears only in this response; never cache it.
	w.Header().Set("Cache-Control", "no-store")
	label := storage.Token{Name: name, Group: form.Group}.Label()
	s.renderTokens(w, r, http.StatusCreated, &createdToken{Plain: plain, Label: label}, tokenForm{}, "")
}

func (s *server) tokenUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := tokenIDParam(w, r)
	if !ok {
		return
	}
	if !s.requireTokenStore(w) {
		return
	}
	name, err := storage.CleanTokenField("name", r.PostFormValue("name"), true)
	var group string
	if err == nil {
		group, err = storage.CleanTokenField("group", r.PostFormValue("group"), false)
	}
	if err != nil {
		s.renderTokens(w, r, http.StatusBadRequest, nil, tokenForm{}, err.Error())
		return
	}
	if err := storage.UpdateToken(r.Context(), s.db, id, name, group); err != nil {
		tokenErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/tokens", http.StatusSeeOther)
}

func (s *server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := tokenIDParam(w, r)
	if !ok {
		return
	}
	if !s.requireTokenStore(w) {
		return
	}
	if err := s.tokens.Revoke(r.Context(), id); err != nil {
		tokenErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/tokens", http.StatusSeeOther)
}

func (s *server) tokenRegenerate(w http.ResponseWriter, r *http.Request) {
	id, ok := tokenIDParam(w, r)
	if !ok {
		return
	}
	if !s.requireTokenStore(w) {
		return
	}
	tokens, err := storage.ListTokens(r.Context(), s.db)
	if err != nil {
		writeErr(w, err)
		return
	}
	var label string
	for _, t := range tokens {
		if t.ID == id {
			label = t.Label()
		}
	}
	plain, err := s.tokens.Regenerate(r.Context(), id)
	if err != nil {
		tokenErr(w, r, err)
		return
	}
	// The plaintext appears only in this response; never cache it.
	w.Header().Set("Cache-Control", "no-store")
	s.renderTokens(w, r, http.StatusOK, &createdToken{Plain: plain, Label: label, Regenerated: true}, tokenForm{}, "")
}

func tokenIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func tokenErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storage.ErrTokenNotFound) {
		http.NotFound(w, r)
		return
	}
	writeErr(w, err)
}
