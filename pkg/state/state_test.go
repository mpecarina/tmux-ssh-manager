package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToggleFavoriteAndRecents(t *testing.T) {
	store := &Store{}
	if on := store.ToggleFavorite("edge1"); !on {
		t.Fatalf("expected favorite to turn on")
	}
	if !store.IsFavorite("edge1") {
		t.Fatalf("favorite missing")
	}
	if on := store.ToggleFavorite("edge1"); on {
		t.Fatalf("expected favorite to turn off")
	}
	store.AddRecent("one")
	store.AddRecent("two")
	store.AddRecent("one")
	if got := store.Recents[0]; got != "one" {
		t.Fatalf("expected one to be most recent, got %s", got)
	}
	if len(store.Recents) != 2 {
		t.Fatalf("expected 2 recents, got %d", len(store.Recents))
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := &Store{Version: 1}
	store.ToggleFavorite("host-a")
	store.ToggleFavorite("host-b")
	store.AddRecent("host-c")
	store.AddRecent("host-a")

	if err := Save(path, store); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.IsFavorite("host-a") || !loaded.IsFavorite("host-b") {
		t.Fatalf("favorites not preserved: %v", loaded.Favorites)
	}
	if len(loaded.Recents) != 2 || loaded.Recents[0] != "host-a" {
		t.Fatalf("recents not preserved: %v", loaded.Recents)
	}
	if loaded.UpdatedAt == "" {
		t.Fatal("expected UpdatedAt to be set")
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if store.Version != 1 {
		t.Fatalf("expected version 1, got %d", store.Version)
	}
	if len(store.Favorites) != 0 || len(store.Recents) != 0 {
		t.Fatal("expected empty favorites and recents")
	}
}

func TestRecentsCapAt100(t *testing.T) {
	store := &Store{}
	for i := 0; i < 150; i++ {
		store.AddRecent(filepath.Join("host", string(rune('a'+i%26)), string(rune('0'+i/26))))
	}
	if len(store.Recents) > recentsLimit {
		t.Fatalf("expected recents capped at %d, got %d", recentsLimit, len(store.Recents))
	}
}

func TestToggleFavoriteIgnoresEmpty(t *testing.T) {
	store := &Store{}
	if on := store.ToggleFavorite(""); on {
		t.Fatal("empty alias should be ignored")
	}
	if on := store.ToggleFavorite("  "); on {
		t.Fatal("whitespace alias should be ignored")
	}
}

func TestDefaultPathUsesXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected absolute path, got %q", path)
	}
	if filepath.Dir(filepath.Dir(path)) != tmp {
		t.Fatalf("expected path under XDG_CONFIG_HOME %q, got %q", tmp, path)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "state.json")
	if err := Save(path, &Store{Version: 1}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}
