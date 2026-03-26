package tmuxui

import (
	"os/exec"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tmux-ssh-manager/pkg/sshconfig"
	"tmux-ssh-manager/pkg/state"
)

func TestNewModelDefaultSearchMode(t *testing.T) {
	m := newModel(App{
		Hosts:         []sshconfig.Host{{Alias: "test1", HostName: "10.0.0.1"}},
		StartInSearch: true,
	})
	if !m.input.Focused() {
		t.Fatal("expected search input to be focused by default")
	}
}

func TestNewModelNormalModeOverride(t *testing.T) {
	m := newModel(App{
		Hosts: []sshconfig.Host{{Alias: "test1", HostName: "10.0.0.1"}},
	})
	if m.input.Focused() {
		t.Fatal("expected search input to be blurred in normal mode")
	}
}

func TestSelectAllWithCtrlA(t *testing.T) {
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
			{Alias: "h3", HostName: "10.0.0.3"},
		},
	})

	if len(m.selectedAliases) != 0 {
		t.Fatal("expected no selections initially")
	}

	// Press ctrl+a to select all.
	msg := tea.KeyMsg{Type: tea.KeyCtrlA}
	updated, _ := m.Update(msg)
	m = updated.(model)
	if len(m.selectedAliases) != 3 {
		t.Fatalf("expected 3 selected, got %d", len(m.selectedAliases))
	}
}

func TestSelectAllInSearchMode(t *testing.T) {
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "alpha", HostName: "10.0.0.1"},
			{Alias: "beta", HostName: "10.0.0.2"},
			{Alias: "gamma", HostName: "10.0.0.3"},
		},
		StartInSearch: true,
	})

	// Type "al" to filter down to "alpha" only.
	for _, r := range "al" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	if len(m.filtered) != 1 {
		t.Fatalf("expected 1 filtered host, got %d", len(m.filtered))
	}

	// ctrl+a should select only the filtered host.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(model)
	if len(m.selectedAliases) != 1 {
		t.Fatalf("expected 1 selected, got %d", len(m.selectedAliases))
	}
	if _, ok := m.selectedAliases["alpha"]; !ok {
		t.Fatal("expected 'alpha' to be selected")
	}
}

func TestEnterWithMultiSelectUsesNewWindow(t *testing.T) {
	var opened []string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
			{Alias: "h3", HostName: "10.0.0.3"},
		},
		State:     &state.Store{},
		StatePath: t.TempDir() + "/state.json",
		InTmux:    func() bool { return true },
		NewWindow: func(alias string) error {
			opened = append(opened, alias)
			return nil
		},
	})

	// Select h1 and h3 via space toggle.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // selects h1 (cursor=0)
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // move down
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // move to h3
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // selects h3
	m = updated.(model)

	if len(m.selectedAliases) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(m.selectedAliases))
	}

	// Enter should dispatch NewWindow for selected hosts (not connect in-place).
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command from enter")
	}
	// Execute the command to trigger the actions.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if len(opened) != 2 {
		t.Fatalf("expected 2 windows opened, got %d: %v", len(opened), opened)
	}
}

func TestArrowKeysInSearchMode(t *testing.T) {
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
			{Alias: "h3", HostName: "10.0.0.3"},
		},
		StartInSearch: true,
	})

	if m.selected != 0 {
		t.Fatalf("expected cursor at 0, got %d", m.selected)
	}

	// Down arrow should move cursor while still in search mode.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.selected != 1 {
		t.Fatalf("expected cursor at 1 after down, got %d", m.selected)
	}
	if !m.input.Focused() {
		t.Fatal("expected search to remain focused after arrow key")
	}

	// Up arrow should move back.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.selected != 0 {
		t.Fatalf("expected cursor at 0 after up, got %d", m.selected)
	}
}

func TestImplicitSelectEnterInSearchMode(t *testing.T) {
	var connected string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
		},
		StartInSearch:  true,
		ImplicitSelect: true,
		State:          &state.Store{},
		StatePath:      t.TempDir() + "/state.json",
		Connect: func(alias string) *exec.Cmd {
			connected = alias
			return exec.Command("echo", alias)
		},
	})

	// Move to h2.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)

	// Enter in search mode with ImplicitSelect should dispatch highlighted host.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command from enter in search mode with implicit select")
	}
	if connected != "h2" {
		t.Fatalf("expected connect to h2, got %q", connected)
	}
}

func TestImplicitSelectOff(t *testing.T) {
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
		},
		StartInSearch:  true,
		ImplicitSelect: false,
	})

	// Enter with ImplicitSelect=false should just blur (not dispatch).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.input.Focused() {
		t.Fatal("expected search to be blurred after enter")
	}
}

func TestImplicitSelectSplitInSearchMode(t *testing.T) {
	splitCalled := false
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
		},
		StartInSearch:  true,
		ImplicitSelect: true,
		State:          &state.Store{},
		StatePath:      t.TempDir() + "/state.json",
		InTmux:         func() bool { return true },
		SplitVert: func(alias string) error {
			splitCalled = true
			return nil
		},
	})

	// Press 'v' in search mode should type into search, not trigger split.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = updated.(model)
	if splitCalled {
		t.Fatal("pressing 'v' in search mode should not trigger split")
	}
	if m.input.Value() != "v" {
		t.Fatalf("expected search input 'v', got %q", m.input.Value())
	}
}

func TestSearchModeTypingDoesNotTriggerActions(t *testing.T) {
	splitCalled := false
	windowCalled := false
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "staging", HostName: "10.0.0.1"},
		},
		StartInSearch:  true,
		ImplicitSelect: true,
		State:          &state.Store{},
		StatePath:      t.TempDir() + "/state.json",
		InTmux:         func() bool { return true },
		SplitHoriz: func(alias string) error {
			splitCalled = true
			return nil
		},
		SplitVert: func(alias string) error {
			splitCalled = true
			return nil
		},
		NewWindow: func(alias string) error {
			windowCalled = true
			return nil
		},
	})

	// Type "stag" — the 's' and 't' should go into search, not trigger split/tiled.
	for _, r := range "stag" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}

	if splitCalled {
		t.Fatal("typing 's' in search should not trigger a split")
	}
	if windowCalled {
		t.Fatal("typing 'w' in search should not trigger a window")
	}
	if m.input.Value() != "stag" {
		t.Fatalf("expected search input 'stag', got %q", m.input.Value())
	}
}

func TestEnterModeWindow(t *testing.T) {
	var opened []string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
		},
		EnterMode: "w",
		State:     &state.Store{},
		StatePath: t.TempDir() + "/state.json",
		InTmux:    func() bool { return true },
		NewWindow: func(alias string) error {
			opened = append(opened, alias)
			return nil
		},
	})

	// Enter with EnterMode="w" should open a tmux window, not connect in pane.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command from enter with enter-mode=w")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if len(opened) != 1 || opened[0] != "h1" {
		t.Fatalf("expected window opened for h1, got %v", opened)
	}
}

func TestEnterModePaneConnectsInPlace(t *testing.T) {
	var connected string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
		},
		EnterMode: "p",
		State:     &state.Store{},
		StatePath: t.TempDir() + "/state.json",
		Connect: func(alias string) *exec.Cmd {
			connected = alias
			return exec.Command("echo", alias)
		},
	})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command from enter with enter-mode=p")
	}
	if connected != "h1" {
		t.Fatalf("expected connect to h1, got %q", connected)
	}
}

func TestTiledMultiSelect(t *testing.T) {
	var tiledAliases []string
	var tiledLayout string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
			{Alias: "h3", HostName: "10.0.0.3"},
		},
		State:     &state.Store{},
		StatePath: t.TempDir() + "/state.json",
		InTmux:    func() bool { return true },
		Tiled: func(aliases []string, layout string) error {
			tiledAliases = aliases
			tiledLayout = layout
			return nil
		},
		NewWindow: func(alias string) error { return nil },
	})

	// Select h1, h2, h3 with ctrl+a then press t
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(model)
	if len(m.selectedAliases) != 3 {
		t.Fatalf("expected 3 selected, got %d", len(m.selectedAliases))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd == nil {
		t.Fatal("expected a command from t")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if len(tiledAliases) != 3 {
		t.Fatalf("expected 3 tiled aliases, got %d: %v", len(tiledAliases), tiledAliases)
	}
	if tiledLayout != "tiled" {
		t.Fatalf("expected layout 'tiled', got %q", tiledLayout)
	}
}

func TestTiledSingleHostFallsBackToWindow(t *testing.T) {
	var windowedAlias string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
		},
		State:     &state.Store{},
		StatePath: t.TempDir() + "/state.json",
		InTmux:    func() bool { return true },
		NewWindow: func(alias string) error {
			windowedAlias = alias
			return nil
		},
	})

	// Press t with no multi-select (single highlighted host)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd == nil {
		t.Fatal("expected a command from t")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if windowedAlias != "h1" {
		t.Fatalf("expected h1, got %q", windowedAlias)
	}
}

func TestTiledInSearchMode(t *testing.T) {
	var tiledAliases []string
	m := newModel(App{
		Hosts: []sshconfig.Host{
			{Alias: "h1", HostName: "10.0.0.1"},
			{Alias: "h2", HostName: "10.0.0.2"},
		},
		StartInSearch:  true,
		ImplicitSelect: true,
		State:          &state.Store{},
		StatePath:      t.TempDir() + "/state.json",
		InTmux:         func() bool { return true },
		Tiled: func(aliases []string, layout string) error {
			tiledAliases = aliases
			return nil
		},
		NewWindow: func(alias string) error { return nil },
	})

	// Select all, then Esc to normal mode, then press t.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if cmd == nil {
		t.Fatal("expected a command from t after esc to normal mode")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
	if len(tiledAliases) != 2 {
		t.Fatalf("expected 2 tiled aliases, got %d", len(tiledAliases))
	}
}
