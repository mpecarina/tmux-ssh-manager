package manager

import (
	"strings"
	"testing"
)

func TestFilterHosts_GroupAndTagsAndNameContains(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1"},
			{Name: "dc2"},
		},
		Hosts: []Host{
			{Name: "rtr1.dc1.example.com", Group: "dc1", Tags: []string{"router", "ios-xe"}},
			{Name: "sw1.dc1.example.com", Group: "dc1", Tags: []string{"switch", "nxos"}},
			{Name: "rtr2.dc2.example.com", Group: "dc2", Tags: []string{"router", "junos"}},
		},
	}

	// Match dc1 routers containing "rtr"
	got, err := cfg.FilterHosts(PaneFilter{
		Group:        "dc1",
		Tags:         []string{"router"},
		NameContains: "rtr",
	})
	if err != nil {
		t.Fatalf("FilterHosts error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(got), got)
	}
	if got[0].Name != "rtr1.dc1.example.com" {
		t.Fatalf("expected rtr1, got %q", got[0].Name)
	}

	// Tag matching should be case-insensitive and require all tags.
	got, err = cfg.FilterHosts(PaneFilter{
		Tags: []string{"ROUTER", "ios-xe"},
	})
	if err != nil {
		t.Fatalf("FilterHosts error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "rtr1.dc1.example.com" {
		t.Fatalf("expected only rtr1, got: %+v", got)
	}
}

func TestFilterHosts_NameRegex_Invalid(t *testing.T) {
	cfg := &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "rtr1", Group: "dc1"}},
	}

	_, err := cfg.FilterHosts(PaneFilter{
		NameRegex: "(",
	})
	if err == nil {
		t.Fatalf("expected invalid regex error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid") {
		t.Fatalf("expected invalid regex error, got: %v", err)
	}
}

func TestResolveDashboard_PaneHostNotFound(t *testing.T) {
	cfg := &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "rtr1", Group: "dc1"}},
		Dashboards: []Dashboard{
			{
				Name:      "bad",
				NewWindow: true,
				Panes: []DashPane{
					{Host: "does-not-exist", Commands: []string{"show version"}},
				},
			},
		},
	}

	_, err := cfg.ResolveDashboard(cfg.Dashboards[0])
	if err == nil {
		t.Fatalf("expected resolve error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestResolveDashboard_FilterChoosesFirstMatchAndMergesOnConnect(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{
				Name:      "dc1",
				OnConnect: []string{"terminal length 0"},
			},
		},
		Hosts: []Host{
			{
				Name:      "rtr1",
				Group:     "dc1",
				OnConnect: []string{"show clock"},
				Tags:      []string{"router"},
			},
			{
				Name:  "rtr2",
				Group: "dc1",
				Tags:  []string{"router"},
			},
		},
	}

	d := Dashboard{
		Name: "core",
		Panes: []DashPane{
			{
				Filter: PaneFilter{Group: "dc1", Tags: []string{"router"}},
				Commands: []string{
					"show version",
				},
			},
		},
	}

	rd, err := cfg.ResolveDashboard(d)
	if err != nil {
		t.Fatalf("ResolveDashboard error: %v", err)
	}
	if len(rd.Panes) != 1 {
		t.Fatalf("expected 1 resolved pane, got %d", len(rd.Panes))
	}

	// Filter should pick first match in config host order: rtr1 then rtr2.
	if rd.Panes[0].Target.Host.Name != "rtr1" {
		t.Fatalf("expected first matched host rtr1, got %q", rd.Panes[0].Target.Host.Name)
	}

	// EffectiveCommands should be group on_connect + host on_connect + pane commands.
	want := []string{
		"terminal length 0",
		"show clock",
		"show version",
	}
	got := rd.Panes[0].EffectiveCommands
	if len(got) != len(want) {
		t.Fatalf("expected %d commands, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cmd[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestResolveDashboard_ConnectDelayMS_Resolution(t *testing.T) {
	cfg := &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "linux1", Group: "dc1"}},
	}

	d := Dashboard{
		Name:           "noc",
		ConnectDelayMS: 500,
		Panes: []DashPane{
			{
				Host: "linux1",
				Commands: []string{
					"watch -n 2 -t -- uptime",
				},
			},
		},
	}

	rd, err := cfg.ResolveDashboard(d)
	if err != nil {
		t.Fatalf("ResolveDashboard error: %v", err)
	}
	if len(rd.Panes) != 1 {
		t.Fatalf("expected 1 resolved pane, got %d", len(rd.Panes))
	}
	if rd.Panes[0].EffectiveConnectDelayMS != 500 {
		t.Fatalf("expected EffectiveConnectDelayMS=500, got %d", rd.Panes[0].EffectiveConnectDelayMS)
	}

	// Pane override should win
	d.Panes[0].ConnectDelayMS = 1000
	rd, err = cfg.ResolveDashboard(d)
	if err != nil {
		t.Fatalf("ResolveDashboard error: %v", err)
	}
	if rd.Panes[0].EffectiveConnectDelayMS != 1000 {
		t.Fatalf("expected EffectiveConnectDelayMS=1000, got %d", rd.Panes[0].EffectiveConnectDelayMS)
	}
}

func TestValidateDashboards_RejectsNegativeConnectDelayMS(t *testing.T) {
	cfg := &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "linux1", Group: "dc1"}},
		Dashboards: []Dashboard{
			{
				Name:           "bad",
				ConnectDelayMS: -1,
				Panes: []DashPane{
					{Host: "linux1", Commands: []string{"uptime"}},
				},
			},
		},
	}

	err := cfg.ValidateDashboards()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "connect_delay_ms") {
		t.Fatalf("expected connect_delay_ms error, got: %v", err)
	}

	cfg = &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "linux1", Group: "dc1"}},
		Dashboards: []Dashboard{
			{
				Name:           "bad2",
				ConnectDelayMS: 500,
				Panes: []DashPane{
					{Host: "linux1", ConnectDelayMS: -5, Commands: []string{"uptime"}},
				},
			},
		},
	}
	err = cfg.ValidateDashboards()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "connect_delay_ms") {
		t.Fatalf("expected connect_delay_ms error, got: %v", err)
	}
}

func TestValidateDashboards_RejectsMissingPaneTarget(t *testing.T) {
	cfg := &Config{
		Groups: []Group{{Name: "dc1"}},
		Hosts:  []Host{{Name: "rtr1", Group: "dc1"}},
		Dashboards: []Dashboard{
			{
				Name: "oops",
				Panes: []DashPane{
					// Neither Host nor Filter criteria
					{Commands: []string{"show version"}},
				},
			},
		},
	}

	err := cfg.ValidateDashboards()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "either host or filter") {
		t.Fatalf("expected 'either host or filter' error, got: %v", err)
	}
}

func TestRecordedDashboardUpsert_SanitizesAndSetsTimestamps(t *testing.T) {
	st := &State{Version: 1}

	rd := RecordedDashboard{
		Name:        "  my-dash  ",
		Description: " test ",
		Created:     "",
		Updated:     "",
		Panes: []RecordedPane{
			{
				Title: "  RTR1  ",
				Host:  " rtr1 ",
				Commands: []string{
					"  terminal length 0  ",
					"   ",
					"\tshow version\t",
					"",
				},
			},
		},
	}

	changed := st.UpsertRecordedDashboard(rd)
	if !changed {
		t.Fatalf("expected changed=true on first upsert")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected 1 recorded dashboard, got %d", len(st.RecordedDashboards))
	}

	got := st.RecordedDashboards[0]
	// Name used as key should be trimmed by UpsertRecordedDashboard input processing (it trims local var),
	// and stored as provided in rd (note: implementation keeps rd.Name as-is unless caller trimmed).
	// We assert semantic parts that the code guarantees: timestamps and pane sanitization.
	if strings.TrimSpace(got.Name) != "my-dash" {
		t.Fatalf("expected name to trim to %q, got %q", "my-dash", got.Name)
	}
	if got.Created == "" || got.Updated == "" {
		t.Fatalf("expected Created/Updated to be set, got Created=%q Updated=%q", got.Created, got.Updated)
	}
	if got.Panes[0].Title != "RTR1" {
		t.Fatalf("expected pane title %q, got %q", "RTR1", got.Panes[0].Title)
	}
	if got.Panes[0].Host != "rtr1" {
		t.Fatalf("expected pane host %q, got %q", "rtr1", got.Panes[0].Host)
	}
	if len(got.Panes[0].Commands) != 2 {
		t.Fatalf("expected 2 sanitized commands, got %d: %#v", len(got.Panes[0].Commands), got.Panes[0].Commands)
	}
	if got.Panes[0].Commands[0] != "terminal length 0" || got.Panes[0].Commands[1] != "show version" {
		t.Fatalf("unexpected sanitized commands: %#v", got.Panes[0].Commands)
	}

	// Upsert same name should update (not append)
	rd2 := RecordedDashboard{
		Name: "my-dash",
		Panes: []RecordedPane{
			{Host: "rtr2", Commands: []string{"show ip int br"}},
		},
	}
	changed = st.UpsertRecordedDashboard(rd2)
	if !changed {
		t.Fatalf("expected changed=true on update upsert")
	}
	if len(st.RecordedDashboards) != 1 {
		t.Fatalf("expected still 1 recorded dashboard after update, got %d", len(st.RecordedDashboards))
	}
	if st.RecordedDashboards[0].Panes[0].Host != "rtr2" {
		t.Fatalf("expected updated pane host rtr2, got %q", st.RecordedDashboards[0].Panes[0].Host)
	}
}
