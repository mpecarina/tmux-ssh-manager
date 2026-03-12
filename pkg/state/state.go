package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const recentsLimit = 100

type Store struct {
	Version   int      `json:"version"`
	Favorites []string `json:"favorites,omitempty"`
	Recents   []string `json:"recents,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

func DefaultPath() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "tmux-ssh-manager", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "tmux-ssh-manager", "state.json"), nil
}

func Load(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{Version: 1}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if store.Version == 0 {
		store.Version = 1
	}
	store.normalize()
	return &store, nil
}

func Save(path string, store *Store) error {
	if store == nil {
		return fmt.Errorf("nil state store")
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	store.normalize()
	store.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func (s *Store) ToggleFavorite(alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return false
	}
	if s.IsFavorite(alias) {
		next := s.Favorites[:0]
		for _, item := range s.Favorites {
			if item != alias {
				next = append(next, item)
			}
		}
		s.Favorites = next
		return false
	}
	s.Favorites = append(s.Favorites, alias)
	s.normalize()
	return true
}

func (s *Store) IsFavorite(alias string) bool {
	alias = strings.TrimSpace(alias)
	for _, item := range s.Favorites {
		if item == alias {
			return true
		}
	}
	return false
}

func (s *Store) AddRecent(alias string) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return
	}
	next := make([]string, 0, len(s.Recents)+1)
	next = append(next, alias)
	for _, item := range s.Recents {
		if item != alias {
			next = append(next, item)
		}
	}
	if len(next) > recentsLimit {
		next = next[:recentsLimit]
	}
	s.Recents = next
}

func (s *Store) normalize() {
	if s.Version == 0 {
		s.Version = 1
	}
	s.Favorites = uniqueNonEmpty(s.Favorites)
	s.Recents = uniqueNonEmpty(s.Recents)
	if len(s.Recents) > recentsLimit {
		s.Recents = s.Recents[:recentsLimit]
	}
}

func uniqueNonEmpty(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
