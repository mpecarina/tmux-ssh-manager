package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Persistent state for tmux-ssh-manager.
// Stores favorites and recents in a JSON file under the user's config dir:
//
//   ~/.config/tmux-ssh-manager/state.json
//
// On systems honoring XDG, $XDG_CONFIG_HOME is used instead of ~/.config.
//
// This file provides a small API to load/save the state and modify favorites/recents
// with sensible semantics (unique, move-to-front for recents, size limits, etc).

const (
	defaultConfigDirName = "tmux-ssh-manager"
	defaultStateFilename = "state.json"

	// Default limits/caps
	defaultRecentsLimit = 100
)

// State represents the on-disk JSON structure.
// Keep fields stable for backward compatibility.
type State struct {
	// Version allows future migrations.
	Version int `json:"version,omitempty"`

	// Favorites is a list of host names marked as favorites.
	// Names should match Host.Name (or SSH alias) used in the UI.
	Favorites []string `json:"favorites,omitempty"`

	// Recents stores a most-recently-used list of host names.
	// The first element is the most recent.
	Recents []string `json:"recents,omitempty"`

	// RecordedDashboards holds ad-hoc dashboards captured from live sessions for later replay.
	RecordedDashboards []RecordedDashboard `json:"recorded_dashboards,omitempty"`

	// Updated tracks the last update time in RFC3339.
	Updated string `json:"updated,omitempty"`
}

// DefaultConfigDir returns the directory path for this application's config.
// Precedence:
//  1. $XDG_CONFIG_HOME/tmux-ssh-manager
//  2. ~/.config/tmux-ssh-manager
func DefaultConfigDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, defaultConfigDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", defaultConfigDirName), nil
}

// DefaultStatePath returns the full path to the state.json file.
func DefaultStatePath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, defaultStateFilename), nil
}

// LoadState reads the state JSON from path. If path is empty, the default path is used.
// If the file does not exist, it returns an empty state and nil error.
func LoadState(path string) (*State, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultStatePath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Missing state is not an error; return empty state
			return &State{Version: 1}, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}

	// Ensure sane defaults
	if st.Version == 0 {
		st.Version = 1
	}
	st.ensureUnique()

	return &st, nil
}

// SaveState writes the state JSON to path atomically.
// If path is empty, the default path is used.
// The parent directory is created with 0700 permissions if missing.
func SaveState(path string, st *State) error {
	if st == nil {
		return errors.New("nil state")
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultStatePath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}

	// Prepare JSON with stable formatting
	st2 := *st
	st2.Updated = time.Now().UTC().Format(time.RFC3339)
	st2.ensureUnique()
	payload, err := json.MarshalIndent(st2, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	payload = append(payload, '\n')

	tmp := path + fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write temp state %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic rename to %s: %w", path, err)
	}
	return nil
}

// IsFavorite reports whether name is present in Favorites.
func (s *State) IsFavorite(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(s.Favorites) == 0 {
		return false
	}
	for _, n := range s.Favorites {
		if n == name {
			return true
		}
	}
	return false
}

// AddFavorite inserts name into Favorites if not already present.
// Returns true if the state was modified.
func (s *State) AddFavorite(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if s.IsFavorite(name) {
		return false
	}
	s.Favorites = append(s.Favorites, name)
	return true
}

// RemoveFavorite deletes name from Favorites.
// Returns true if the state was modified.
func (s *State) RemoveFavorite(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(s.Favorites) == 0 {
		return false
	}
	out := s.Favorites[:0]
	removed := false
	for _, n := range s.Favorites {
		if n == name {
			removed = true
			continue
		}
		out = append(out, n)
	}
	s.Favorites = out
	return removed
}

// SetFavorite sets or clears favorite for name.
// Returns true if the state was modified.
func (s *State) SetFavorite(name string, on bool) bool {
	if on {
		return s.AddFavorite(name)
	}
	return s.RemoveFavorite(name)
}

// FavoritesSet returns a set for quick lookup, derived from Favorites.
func (s *State) FavoritesSet() map[string]struct{} {
	m := make(map[string]struct{}, len(s.Favorites))
	for _, n := range s.Favorites {
		if n = strings.TrimSpace(n); n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}

// SetFavoritesFromSet replaces Favorites from the provided set of names.
// Returns true if the state was modified.
func (s *State) SetFavoritesFromSet(set map[string]struct{}) bool {
	if set == nil {
		return false
	}
	newFavs := make([]string, 0, len(set))
	for n := range set {
		n = strings.TrimSpace(n)
		if n != "" {
			newFavs = append(newFavs, n)
		}
	}
	// Quick check for equality (order-insensitive)
	if equalStringSets(s.Favorites, newFavs) {
		return false
	}
	s.Favorites = newFavs
	return true
}

// AddRecent moves name to the front of Recents (if already present) or inserts it.
// Caps the list to defaultRecentsLimit.
// Returns true if the state was modified.
func (s *State) AddRecent(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	changed := false
	out := make([]string, 0, len(s.Recents)+1)
	out = append(out, name)
	for _, n := range s.Recents {
		if n == name {
			changed = true
			continue
		}
		out = append(out, n)
	}
	if len(out) > defaultRecentsLimit {
		out = out[:defaultRecentsLimit]
	}
	// If name wasn't already present, we still changed by prepending
	if !changed {
		changed = true
	}
	s.Recents = out
	return changed
}

// RemoveRecent removes name from Recents. Returns true if modified.
func (s *State) RemoveRecent(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(s.Recents) == 0 {
		return false
	}
	out := s.Recents[:0]
	removed := false
	for _, n := range s.Recents {
		if n == name {
			removed = true
			continue
		}
		out = append(out, n)
	}
	s.Recents = out
	return removed
}

// PruneRecents caps the Recents list to the given limit (or default if <= 0).
// Returns true if the state was modified.
func (s *State) PruneRecents(limit int) bool {
	if limit <= 0 {
		limit = defaultRecentsLimit
	}
	if len(s.Recents) <= limit {
		return false
	}
	s.Recents = s.Recents[:limit]
	return true
}

// ensureUnique de-duplicates entries and cleans empty strings.
func (s *State) ensureUnique() {
	// Favorites
	if len(s.Favorites) > 0 {
		seen := map[string]struct{}{}
		out := s.Favorites[:0]
		for _, n := range s.Favorites {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
		s.Favorites = out
	}
	// Recents
	if len(s.Recents) > 0 {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(s.Recents))
		for _, n := range s.Recents {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
		if len(out) > defaultRecentsLimit {
			out = out[:defaultRecentsLimit]
		}
		s.Recents = out
	}
	// RecordedDashboards
	if len(s.RecordedDashboards) > 0 {
		seen := map[string]struct{}{}
		out := make([]RecordedDashboard, 0, len(s.RecordedDashboards))
		for _, d := range s.RecordedDashboards {
			name := strings.TrimSpace(d.Name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, d)
		}
		s.RecordedDashboards = out
	}
}

// equalStringSets compares two string slices as sets (order-insensitive).
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]int, len(a))
	for _, x := range a {
		ma[x]++
	}
	for _, y := range b {
		if ma[y] == 0 {
			return false
		}
		ma[y]--
		if ma[y] == 0 {
			delete(ma, y)
		}
	}
	return len(ma) == 0
}

// ----- Recording & Replay (Dashboards captured in state.json) -----

// RecordedDashboard is a persisted, ad-hoc dashboard captured from a live session.
type RecordedDashboard struct {
	// Name is the unique identifier for the recorded dashboard.
	Name string `json:"name"`
	// Description is optional metadata to describe the dashboard's purpose.
	Description string `json:"description,omitempty"`
	// Created/Updated timestamps in RFC3339.
	Created string `json:"created,omitempty"`
	Updated string `json:"updated,omitempty"`

	// Layout is an optional tmux window layout string (the output of #{window_layout}).
	// If present, a replay can apply it via `tmux select-layout` after panes exist.
	Layout string `json:"layout,omitempty"`

	// Panes holds the recorded pane definitions (title/host/commands).
	Panes []RecordedPane `json:"panes"`
}

// RecordedPane captures a single pane's target and the commands that were executed.
type RecordedPane struct {
	Title    string   `json:"title,omitempty"`
	Host     string   `json:"host,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// UpsertRecordedDashboard inserts or updates a recorded dashboard by name.
// Returns true if the state was modified.
func (s *State) UpsertRecordedDashboard(rd RecordedDashboard) bool {
	name := strings.TrimSpace(rd.Name)
	if name == "" {
		return false
	}

	// Normalize the stored name to match the lookup/update key.
	rd.Name = name
	rd.Description = strings.TrimSpace(rd.Description)
	rd.Layout = strings.TrimSpace(rd.Layout)

	now := time.Now().UTC().Format(time.RFC3339)
	if rd.Created == "" {
		rd.Created = now
	}
	rd.Updated = now

	// sanitize commands
	for i := range rd.Panes {
		rd.Panes[i].Title = strings.TrimSpace(rd.Panes[i].Title)
		rd.Panes[i].Host = strings.TrimSpace(rd.Panes[i].Host)
		cmds := rd.Panes[i].Commands[:0]
		for _, c := range rd.Panes[i].Commands {
			c = strings.TrimSpace(c)
			if c != "" {
				cmds = append(cmds, c)
			}
		}
		rd.Panes[i].Commands = cmds
	}

	// update if exists
	for i := range s.RecordedDashboards {
		if s.RecordedDashboards[i].Name == name {
			s.RecordedDashboards[i] = rd
			return true
		}
	}
	// insert
	s.RecordedDashboards = append(s.RecordedDashboards, rd)
	return true
}

// DeleteRecordedDashboard removes a recorded dashboard by name.
// Returns true if the state was modified.
func (s *State) DeleteRecordedDashboard(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(s.RecordedDashboards) == 0 {
		return false
	}
	out := s.RecordedDashboards[:0]
	removed := false
	for _, d := range s.RecordedDashboards {
		if d.Name == name {
			removed = true
			continue
		}
		out = append(out, d)
	}
	s.RecordedDashboards = out
	return removed
}

// FindRecordedDashboard looks up a recording by name. Returns nil if not found.
func (s *State) FindRecordedDashboard(name string) *RecordedDashboard {
	name = strings.TrimSpace(name)
	for i := range s.RecordedDashboards {
		if s.RecordedDashboards[i].Name == name {
			return &s.RecordedDashboards[i]
		}
	}
	return nil
}

// ToConfigDashboard converts a RecordedDashboard to a Config Dashboard definition
// which can be materialized by the TUI. NewWindow is set true by default so
// the dashboard opens in a separate window.
func (rd RecordedDashboard) ToConfigDashboard() Dashboard {
	panes := make([]DashPane, 0, len(rd.Panes))
	for _, rp := range rd.Panes {
		panes = append(panes, DashPane{
			Title:    rp.Title,
			Host:     rp.Host,
			Commands: append([]string(nil), rp.Commands...),
		})
	}
	return Dashboard{
		Name:           rd.Name,
		Description:    rd.Description,
		NewWindow:      true,
		Layout:         strings.TrimSpace(rd.Layout),
		ConnectDelayMS: 0,
		Panes:          panes,
	}
}

// duplicate ensureUnique removed; behavior merged in the primary ensureUnique method above
