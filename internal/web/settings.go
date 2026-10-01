package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/tbshfr/ai-usage/internal/backup"
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
	section := r.PathValue("section")
	err = storage.SaveDashboardSettings(r.Context(), s.db, section, body)
	if err == nil && section == "backup" && s.backupReschedule != nil {
		s.backupReschedule()
	}
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

func (s *server) startBackup(w http.ResponseWriter, r *http.Request) {
	if s.backupStart == nil || !s.currentBackupStatus().Enabled {
		http.NotFound(w, r)
		return
	}
	s.backupStart()
	http.Redirect(w, r, "/settings#backups", http.StatusSeeOther)
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

type backupListView struct {
	Newest *backup.Object
	Older  []backup.Object
	Count  int
	Size   int64
	Denied bool
	Failed bool
}

// The list is a separate fragment so the Settings page never waits on the
// bucket, and a listing error leaves the rest of the section working.
func (s *server) fragBackupList(w http.ResponseWriter, r *http.Request) {
	if s.backupList == nil || !s.currentBackupStatus().Enabled {
		http.NotFound(w, r)
		return
	}
	var view backupListView
	objects, err := s.backupList(r.Context())
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, backup.ErrListDenied):
		view.Denied = true
	case err != nil:
		view.Failed = true
	}
	if len(objects) > 0 {
		view.Newest, view.Older = &objects[0], objects[1:]
	}
	view.Count = len(objects)
	for _, o := range objects {
		view.Size += o.Size
	}
	s.renderFrag(w, r, "backup-list", &pageData{BackupList: view})
}

// Backups can take longer to download than the server's write timeout allows.
const backupDownloadTimeout = time.Hour

// The bucket is private, so downloads go through the server rather than to the
// storage endpoint, which the browser may not be able to reach.
func (s *server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if s.backupDownload == nil || !s.currentBackupStatus().Enabled {
		http.NotFound(w, r)
		return
	}
	// The GET route also matches HEAD, whose discarded body would still be
	// fetched from the bucket in full.
	if r.Method == http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backupDownloadTimeout)
	defer cancel()
	// Shutdown would otherwise wait out its grace period on a long download.
	if s.hub != nil {
		defer s.hub.TrackStream(cancel)()
	}
	name := r.PathValue("name")
	body, size, err := s.backupDownload(ctx, name)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, backup.ErrNotFound):
		http.Error(w, "Backup not found.", http.StatusNotFound)
		return
	case errors.Is(err, backup.ErrDownloadDenied):
		http.Error(w, "The access key can't download backups. Allow it to read objects under the prefix; see the backup guide.", http.StatusForbidden)
		return
	case err != nil:
		http.Error(w, "The backup couldn't be downloaded. Try again later.", http.StatusBadGateway)
		return
	}
	defer body.Close()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(backupDownloadTimeout))
	// Cancellation alone does not interrupt a write blocked on a client that
	// stopped reading, which would hold up shutdown. The controller must not be
	// used after the handler returns, so a callback already running is waited for.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		_ = rc.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if _, err := io.Copy(w, body); err != nil && ctx.Err() == nil {
		slog.Warn("backup download interrupted")
	}
}
