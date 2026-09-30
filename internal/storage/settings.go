package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
)

// Every ID needs a label in internal/web/settings.go. tokens.css must define a
// swatch for every accent color and an accent rule for every color except the
// default, blue; breakdowns.html must render a section for every breakdown.
var (
	Themes       = []string{"light", "dark", "system"}
	AccentColors = []string{"blue", "green", "violet", "rose", "orange"}
	BreakdownIDs = []string{"provider", "model", "source", "group", "token"}
)

var (
	ErrUnknownSettingsSection = errors.New("unknown settings section")
	ErrInvalidSettings        = errors.New("invalid settings")
)

type AppearanceSettings struct {
	Theme string `json:"theme"`
	Color string `json:"color"`
}

type BreakdownSettings struct {
	Order  []string `json:"order"`
	Hidden []string `json:"hidden"`
}

type SetupSettings struct {
	Endpoint string `json:"endpoint"`
	Auth     bool   `json:"auth"`
}

type DashboardSettings struct {
	Appearance AppearanceSettings `json:"appearance"`
	Breakdowns BreakdownSettings  `json:"breakdowns"`
	Setup      SetupSettings      `json:"setup"`
}

func DefaultDashboardSettings() DashboardSettings {
	return DashboardSettings{
		Appearance: AppearanceSettings{Theme: "system", Color: "blue"},
		Breakdowns: BreakdownSettings{Order: slices.Clone(BreakdownIDs), Hidden: []string{}},
	}
}

// normalize reports whether a decoded section is valid, filling in or
// canonicalizing fields so equivalent inputs are stored the same way.
type settingsSection interface {
	normalize() bool
	apply(*DashboardSettings)
}

func newSection(name string) settingsSection {
	switch name {
	case "appearance":
		return &AppearanceSettings{}
	case "breakdowns":
		return &BreakdownSettings{}
	case "setup":
		return &SetupSettings{}
	}
	return nil
}

func (a *AppearanceSettings) apply(d *DashboardSettings) { d.Appearance = *a }
func (b *BreakdownSettings) apply(d *DashboardSettings)  { d.Breakdowns = *b }
func (s *SetupSettings) apply(d *DashboardSettings)      { d.Setup = *s }

func (a *AppearanceSettings) normalize() bool {
	return slices.Contains(Themes, a.Theme) && slices.Contains(AccentColors, a.Color)
}

// Breakdowns missing from Order are appended, so adding a breakdown keeps
// saved preferences and shows the new one last.
func (b *BreakdownSettings) normalize() bool {
	if !isSubset(b.Order) || !isSubset(b.Hidden) {
		return false
	}
	for _, id := range BreakdownIDs {
		if !slices.Contains(b.Order, id) {
			b.Order = append(b.Order, id)
		}
	}
	if b.Hidden == nil {
		b.Hidden = []string{}
	}
	return true
}

func isSubset(ids []string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if !slices.Contains(BreakdownIDs, id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// The receiver URL is inserted into quoted JSON, TOML, and Lua snippets.
// Reject literal delimiters and controls as well as URLs clients cannot use.
// Credentials are rejected because every page embeds the endpoint.
func (s *SetupSettings) normalize() bool {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	if s.Endpoint == "" {
		return true
	}
	if len(s.Endpoint) > 2048 || strings.ContainsAny(s.Endpoint, "\"\\") {
		return false
	}
	for _, r := range s.Endpoint {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	u, err := url.Parse(s.Endpoint)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

// Request bodies and stored rows share these rules, so pages only render
// values their templates and snippets are written for. Only request bodies
// reject unknown fields: a stored field that was later renamed or removed is
// dropped instead of discarding the rest of its section.
func decodeSection(data []byte, dest settingsSection, strict bool) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalidSettings
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		dec.DisallowUnknownFields()
	}
	if dec.Decode(dest) != nil || dec.Decode(new(any)) != io.EOF || !dest.normalize() {
		return ErrInvalidSettings
	}
	return nil
}

// A stored section that is no longer valid falls back to its default, so
// one bad row cannot break every page.
func ReadDashboardSettings(ctx context.Context, db *sql.DB) (DashboardSettings, error) {
	out := DefaultDashboardSettings()
	rows, err := db.QueryContext(ctx, `SELECT section, value FROM dashboard_settings`)
	if err != nil {
		return DefaultDashboardSettings(), err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var value []byte
		if err := rows.Scan(&name, &value); err != nil {
			return DefaultDashboardSettings(), err
		}
		dest := newSection(name)
		if dest == nil {
			continue
		}
		if err := decodeSection(value, dest, false); err != nil {
			slog.Warn("ignoring invalid stored dashboard settings", "section", name)
			continue
		}
		dest.apply(&out)
	}
	if err := rows.Err(); err != nil {
		return DefaultDashboardSettings(), err
	}
	return out, nil
}

// Each section is written independently so edits on different devices
// cannot overwrite unrelated preferences loaded earlier.
func SaveDashboardSettings(ctx context.Context, db *sql.DB, name string, data []byte) error {
	dest := newSection(name)
	if dest == nil {
		return ErrUnknownSettingsSection
	}
	if err := decodeSection(data, dest, true); err != nil {
		return err
	}
	normalized, err := json.Marshal(dest)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO dashboard_settings(section, value) VALUES (?, ?)
		ON CONFLICT(section) DO UPDATE SET value = excluded.value`, name, string(normalized))
	return err
}
