package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/tbshfr/ai-usage/internal/storage"
)

// Pages embed the settings so they render without a follow-up request; this
// endpoint only resyncs pages restored from the back/forward cache.
func (s *server) readSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	settings, err := storage.ReadDashboardSettings(r.Context(), s.db)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(settings)
}

func (s *server) saveSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		http.Error(w, "invalid settings", http.StatusBadRequest)
		return
	}
	err = storage.SaveDashboardSettings(r.Context(), s.db, r.PathValue("section"), body)
	switch {
	case errors.Is(err, storage.ErrUnknownSettingsSection):
		http.NotFound(w, r)
	case errors.Is(err, storage.ErrInvalidSettings):
		http.Error(w, "invalid settings", http.StatusBadRequest)
	case err != nil:
		writeErr(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Pages must not fail because preferences are unavailable, so a read error
// falls back to the defaults.
func (s *server) pageSettings(ctx context.Context, w http.ResponseWriter) storage.DashboardSettings {
	w.Header().Set("Cache-Control", "no-store")
	settings, err := storage.ReadDashboardSettings(ctx, s.db)
	if err != nil {
		slog.Error("load dashboard settings", "error", err)
	}
	return settings
}

// Every ID in storage.Themes, storage.AccentColors, and storage.BreakdownIDs
// needs an entry here.
var (
	themeLabels     = map[string]settingOption{"light": {Label: "Light", Icon: "sun"}, "dark": {Label: "Dark", Icon: "moon"}, "system": {Label: "System", Icon: "monitor"}}
	colorLabels     = map[string]string{"blue": "Blue", "green": "Green", "violet": "Violet", "rose": "Rose", "orange": "Orange"}
	breakdownLabels = map[string]string{"provider": "Provider", "model": "Model", "source": "Source", "group": "Token group", "token": "Token"}
)

type settingOption struct {
	ID, Label, Icon string
	Selected        bool
}

func (d *pageData) ThemeOptions() []settingOption {
	var out []settingOption
	for _, id := range storage.Themes {
		option := themeLabels[id]
		option.ID, option.Selected = id, id == d.Settings.Appearance.Theme
		out = append(out, option)
	}
	return out
}

func (d *pageData) ColorOptions() []settingOption {
	var out []settingOption
	for _, id := range storage.AccentColors {
		out = append(out, settingOption{ID: id, Label: colorLabels[id], Selected: id == d.Settings.Appearance.Color})
	}
	return out
}

func (d *pageData) BreakdownPreferences() []breakdownPreferenceView {
	var out []breakdownPreferenceView
	for i, id := range d.Settings.Breakdowns.Order {
		out = append(out, breakdownPreferenceView{
			ID: id, Label: breakdownLabels[id], Hidden: slices.Contains(d.Settings.Breakdowns.Hidden, id),
			First: i == 0, Last: i == len(d.Settings.Breakdowns.Order)-1,
		})
	}
	return out
}

type breakdownPreferenceView struct {
	ID, Label           string
	Hidden, First, Last bool
}
