package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/storage"
)

// tokensView is the Tokens page: tokens grouped by their group label with
// all-time usage. Created is set only on the response to a
// successful create, the one time the plaintext is shown.
type tokensView struct {
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

// tokenStore returns the shared token store, loading one from the
// database when the dashboard was built without WithTokens.
func (s *server) tokenStore(ctx context.Context) (*auth.TokenStore, error) {
	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	if s.tokens != nil {
		return s.tokens, nil
	}
	t, err := auth.NewTokenStore(ctx, s.db, nil)
	if err != nil {
		return nil, err
	}
	s.tokens = t
	return t, nil
}

func (s *server) tokensPage(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, http.StatusOK, nil, tokenForm{}, "")
}

// renderTokens renders the Tokens page. Usage is all-time: the page is for
// managing tokens, and the dashboard's token/group filters cover ranges.
func (s *server) renderTokens(w http.ResponseWriter, r *http.Request, status int, created *createdToken, form tokenForm, errMsg string) {
	ctx := r.Context()
	d := &pageData{Title: "Tokens", Active: "tokens", Error: errMsg}
	tokens, err := storage.ListTokens(ctx, s.db)
	if err != nil {
		writeErr(w, err)
		return
	}
	usage, err := storage.ByToken(ctx, s.db, storage.Filter{})
	if err != nil {
		writeErr(w, err)
		return
	}
	var pending map[int64]time.Time
	if store, err := s.tokenStore(ctx); err == nil {
		pending = store.PendingUse()
	}
	byKey := make(map[string]storage.Breakdown, len(usage))
	for _, b := range usage {
		byKey[b.Key] = b
	}
	v := tokensView{Created: created, Form: form}
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
	d.ShowLogout = s.dash != nil
	d.Version = s.version
	renderTemplate(w, pageTmpls["tokens"], "layout", status, d)
}

// dashboardURL links to the dashboard filtered by one dimension.
func dashboardURL(key, value string) string {
	return "/?" + url.Values{key: {value}}.Encode()
}

func (s *server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	form := tokenForm{Name: r.PostFormValue("name"), Group: r.PostFormValue("group")}
	name, err := storage.CleanTokenField("name", form.Name, true)
	if err == nil {
		form.Group, err = storage.CleanTokenField("group", form.Group, false)
	}
	if err != nil {
		s.renderTokens(w, r, http.StatusBadRequest, nil, form, err.Error())
		return
	}
	store, err := s.tokenStore(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	plain, _, err := store.Create(r.Context(), name, form.Group)
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
	store, err := s.tokenStore(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := store.Revoke(r.Context(), id); err != nil {
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
	store, err := s.tokenStore(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	plain, err := store.Regenerate(r.Context(), id)
	if err != nil {
		tokenErr(w, r, err)
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
