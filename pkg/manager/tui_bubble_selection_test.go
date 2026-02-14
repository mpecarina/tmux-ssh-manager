package manager

import (
	"testing"
)

func TestSelectionStableAcrossFiltering(t *testing.T) {
	cfg := &Config{
		Hosts: []Host{{Name: "alpha"}, {Name: "bravo"}, {Name: "charlie"}, {Name: "delta"}},
	}

	m := newModel(cfg, UIOptions{MaxResults: 50})

	// Start with no query: all visible.
	m.input.SetValue("")
	m.recomputeFilter()

	// Select a specific host.
	m.selected = 1
	if cur := m.current(); cur == nil || cur.Resolved.Host.Name != "bravo" {
		t.Fatalf("expected current to be bravo, got %#v", cur)
	}
	m.toggleCurrentSelection()

	if _, ok := m.selectedSet["bravo"]; !ok {
		t.Fatalf("expected bravo to be selected")
	}

	// Change query so list changes; selection must remain by identity.
	m.input.SetValue("del")
	m.recomputeFilter()
	if _, ok := m.selectedSet["bravo"]; !ok {
		t.Fatalf("expected bravo to remain selected after filtering")
	}

	// Clear query; host becomes visible again and still selected.
	m.input.SetValue("")
	m.recomputeFilter()
	if _, ok := m.selectedSet["bravo"]; !ok {
		t.Fatalf("expected bravo to remain selected after clearing filter")
	}
}

func TestSelectedResolvedUsesIdentityNotFilteredIndex(t *testing.T) {
	cfg := &Config{
		Hosts: []Host{{Name: "alpha"}, {Name: "bravo"}, {Name: "charlie"}, {Name: "delta"}},
	}

	m := newModel(cfg, UIOptions{MaxResults: 50})
	m.input.SetValue("")
	m.recomputeFilter()

	// Select bravo and delta.
	m.selected = 1
	m.toggleCurrentSelection()
	m.selected = 3
	m.toggleCurrentSelection()

	// Filter so delta disappears.
	m.input.SetValue("br")
	m.recomputeFilter()

	targets := m.selectedResolved()
	got := map[string]struct{}{}
	for _, r := range targets {
		got[r.Host.Name] = struct{}{}
	}
	if _, ok := got["bravo"]; !ok {
		t.Fatalf("expected selectedResolved to include bravo, got=%v", got)
	}
	if _, ok := got["delta"]; !ok {
		t.Fatalf("expected selectedResolved to include delta even when filtered out, got=%v", got)
	}
}
