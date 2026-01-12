package manager

import (
	"strings"
	"testing"
)

func TestConfigValidate_RejectsNegativeGroupConnectDelayMS(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1", ConnectDelayMS: -1},
		},
		Hosts: []Host{
			{Name: "linux1", Group: "dc1"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "connect_delay_ms") {
		t.Fatalf("expected connect_delay_ms validation error, got: %v", err)
	}
}

func TestConfigValidate_RejectsNegativeHostConnectDelayMS(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1", ConnectDelayMS: 250},
		},
		Hosts: []Host{
			{Name: "linux1", Group: "dc1", ConnectDelayMS: -5},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "connect_delay_ms") {
		t.Fatalf("expected connect_delay_ms validation error, got: %v", err)
	}
}

func TestResolveEffective_ConnectDelayMS_GroupDefault(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1", ConnectDelayMS: 500},
		},
		Hosts: []Host{
			{Name: "linux1", Group: "dc1"},
		},
	}

	h := cfg.HostByName("linux1")
	if h == nil {
		t.Fatalf("expected host linux1 to exist")
	}

	r := cfg.ResolveEffective(*h)
	if r.EffectiveConnectDelayMS != 500 {
		t.Fatalf("expected EffectiveConnectDelayMS=500, got %d", r.EffectiveConnectDelayMS)
	}
}

func TestResolveEffective_ConnectDelayMS_HostOverrideWins(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1", ConnectDelayMS: 500},
		},
		Hosts: []Host{
			{Name: "linux1", Group: "dc1", ConnectDelayMS: 1000},
		},
	}

	h := cfg.HostByName("linux1")
	if h == nil {
		t.Fatalf("expected host linux1 to exist")
	}

	r := cfg.ResolveEffective(*h)
	if r.EffectiveConnectDelayMS != 1000 {
		t.Fatalf("expected EffectiveConnectDelayMS=1000, got %d", r.EffectiveConnectDelayMS)
	}
}

func TestResolveEffective_ConnectDelayMS_DefaultZeroWhenUnset(t *testing.T) {
	cfg := &Config{
		Groups: []Group{
			{Name: "dc1"},
		},
		Hosts: []Host{
			{Name: "linux1", Group: "dc1"},
		},
	}

	h := cfg.HostByName("linux1")
	if h == nil {
		t.Fatalf("expected host linux1 to exist")
	}

	r := cfg.ResolveEffective(*h)
	if r.EffectiveConnectDelayMS != 0 {
		t.Fatalf("expected EffectiveConnectDelayMS=0 when unset, got %d", r.EffectiveConnectDelayMS)
	}
}
