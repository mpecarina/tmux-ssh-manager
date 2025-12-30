package manager

import "testing"

func TestRecorder_StateDeleteRecordedDashboard(t *testing.T) {
	st := &State{Version: 1}
	st.RecordedDashboards = []RecordedDashboard{
		{Name: "a", Panes: []RecordedPane{{Host: "rtr1", Commands: []string{"show version"}}}},
		{Name: "b", Panes: []RecordedPane{{Host: "rtr2", Commands: []string{"show clock"}}}},
	}

	if !st.DeleteRecordedDashboard("a") {
		t.Fatalf("expected delete to return true")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected 1 remaining dashboard, got %d", len(st.RecordedDashboards))
	}
	if st.RecordedDashboards[0].Name != "b" {
		t.Fatalf("expected remaining dashboard to be %q, got %q", "b", st.RecordedDashboards[0].Name)
	}

	// Deleting missing should be a no-op.
	if st.DeleteRecordedDashboard("nope") {
		t.Fatalf("expected delete missing to return false")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected still 1 remaining dashboard, got %d", len(st.RecordedDashboards))
	}

	// Empty name should be a no-op.
	if st.DeleteRecordedDashboard("") {
		t.Fatalf("expected delete empty name to return false")
	}
}

func TestRecorder_StateUpsertRecordedDashboard_DedupByTrimmedName(t *testing.T) {
	st := &State{Version: 1}

	// First insert with whitespace in name; implementation normalizes stored name.
	rd1 := RecordedDashboard{
		Name:        "  core-status  ",
		Description: "first",
		Panes: []RecordedPane{
			{Host: "rtr1", Commands: []string{"  terminal length 0  ", " ", "\tshow version\t"}},
		},
	}

	if !st.UpsertRecordedDashboard(rd1) {
		t.Fatalf("expected first upsert to change state")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected 1 dashboard after insert, got %d", len(st.RecordedDashboards))
	}
	if st.RecordedDashboards[0].Name != "core-status" {
		t.Fatalf("expected normalized name %q, got %q", "core-status", st.RecordedDashboards[0].Name)
	}
	if st.RecordedDashboards[0].Created == "" || st.RecordedDashboards[0].Updated == "" {
		t.Fatalf("expected Created/Updated to be set, got Created=%q Updated=%q", st.RecordedDashboards[0].Created, st.RecordedDashboards[0].Updated)
	}
	if len(st.RecordedDashboards[0].Panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(st.RecordedDashboards[0].Panes))
	}
	if st.RecordedDashboards[0].Panes[0].Host != "rtr1" {
		t.Fatalf("expected host %q, got %q", "rtr1", st.RecordedDashboards[0].Panes[0].Host)
	}
	if got := st.RecordedDashboards[0].Panes[0].Commands; len(got) != 2 || got[0] != "terminal length 0" || got[1] != "show version" {
		t.Fatalf("expected sanitized commands [terminal length 0, show version], got %#v", got)
	}

	// Update with trimmed name should overwrite, not append.
	rd2 := RecordedDashboard{
		Name:        "core-status",
		Description: "second",
		Panes: []RecordedPane{
			{Host: "rtr2", Commands: []string{"show clock"}},
		},
	}
	if !st.UpsertRecordedDashboard(rd2) {
		t.Fatalf("expected update upsert to change state")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected 1 dashboard after update, got %d", len(st.RecordedDashboards))
	}
	if st.RecordedDashboards[0].Name != "core-status" {
		t.Fatalf("expected name to remain %q, got %q", "core-status", st.RecordedDashboards[0].Name)
	}
	if st.RecordedDashboards[0].Description != "second" {
		t.Fatalf("expected description to update to %q, got %q", "second", st.RecordedDashboards[0].Description)
	}
	if len(st.RecordedDashboards[0].Panes) != 1 || st.RecordedDashboards[0].Panes[0].Host != "rtr2" {
		t.Fatalf("expected panes to update to rtr2, got %#v", st.RecordedDashboards[0].Panes)
	}
}

func TestRecorder_ToConfigDashboard_DefaultsNewWindow(t *testing.T) {
	rd := RecordedDashboard{
		Name:        "saved",
		Description: "desc",
		Panes: []RecordedPane{
			{Title: "RTR1", Host: "rtr1", Commands: []string{"show version"}},
			{Title: "RTR2", Host: "rtr2", Commands: []string{"show clock"}},
		},
	}

	d := rd.ToConfigDashboard()
	if d.Name != "saved" {
		t.Fatalf("expected name %q, got %q", "saved", d.Name)
	}
	if d.Description != "desc" {
		t.Fatalf("expected description %q, got %q", "desc", d.Description)
	}
	if !d.NewWindow {
		t.Fatalf("expected recorded dashboards to default to NewWindow=true")
	}
	if len(d.Panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(d.Panes))
	}
	if d.Panes[0].Host != "rtr1" || d.Panes[1].Host != "rtr2" {
		t.Fatalf("unexpected pane hosts: %#v", d.Panes)
	}
}

func TestRecorder_ToConfigDashboard_PreservesLayoutWhenPresent(t *testing.T) {
	rd := RecordedDashboard{
		Name:        "wallboard",
		Description: "noc layout",
		Layout:      "c3e5,200x60,0,0[100x60,0,0,0,100x60,100,0,1]",
		Panes: []RecordedPane{
			{Title: "A", Host: "linux-a", Commands: []string{"watch -n 2 -t -- uptime"}},
		},
	}

	d := rd.ToConfigDashboard()
	if d.Layout != "c3e5,200x60,0,0[100x60,0,0,0,100x60,100,0,1]" {
		t.Fatalf("expected layout to be preserved, got %q", d.Layout)
	}
}

func TestRecorder_ToConfigDashboard_LayoutTrimmed(t *testing.T) {
	rd := RecordedDashboard{
		Name:   "wallboard2",
		Layout: "  tiled  ",
		Panes: []RecordedPane{
			{Host: "linux-a", Commands: []string{"uptime"}},
		},
	}

	d := rd.ToConfigDashboard()
	if d.Layout != "tiled" {
		t.Fatalf("expected trimmed layout %q, got %q", "tiled", d.Layout)
	}
}
