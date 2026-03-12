package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadIncludesAndLastWins(t *testing.T) {
	root := t.TempDir()
	sshDir := filepath.Join(root, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "conf.d"), 0o700); err != nil {
		t.Fatalf("mkdir ssh dir: %v", err)
	}

	primary := filepath.Join(sshDir, "config")
	if err := os.WriteFile(primary, []byte("Include conf.d/*.conf\n\nHost app\n  HostName app-primary\n  User alice\n\nHost wildcard-*\n  HostName ignored\n"), 0o600); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "conf.d", "base.conf"), []byte("Host db\n  HostName db.internal\n  User bob\n  Port 2222\n"), 0o600); err != nil {
		t.Fatalf("write include: %v", err)
	}

	hosts, err := Load(primary)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 literal hosts, got %d", len(hosts))
	}
	if hosts[0].Alias != "app" || hosts[0].HostName != "app-primary" {
		t.Fatalf("unexpected app host: %+v", hosts[0])
	}
	if hosts[1].Alias != "db" || hosts[1].Port != 2222 || hosts[1].User != "bob" {
		t.Fatalf("unexpected db host: %+v", hosts[1])
	}
}

func TestAddHostAppendsBlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config")
	if err := os.WriteFile(path, []byte("Host old\n  HostName old.example\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := AddHost(path, AddHostInput{
		Alias:        "newbox",
		HostName:     "10.0.0.10",
		User:         "matt",
		Port:         2201,
		ProxyJump:    "bastion",
		IdentityFile: "~/.ssh/id_ed25519",
	})
	if err != nil {
		t.Fatalf("add host: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := string(data)
	for _, want := range []string{"Host newbox", "HostName 10.0.0.10", "User matt", "Port 2201", "ProxyJump bastion", "IdentityFile ~/.ssh/id_ed25519"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in:\n%s", want, content)
		}
	}
}

func TestAddHostRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config")
	if err := os.WriteFile(path, []byte("Host existing\n  HostName 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AddHost(path, AddHostInput{Alias: "existing", HostName: "5.6.7.8"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestAddHostRequiresAlias(t *testing.T) {
	err := AddHost(filepath.Join(t.TempDir(), "config"), AddHostInput{HostName: "1.2.3.4"})
	if err == nil || !strings.Contains(err.Error(), "alias is required") {
		t.Fatalf("expected alias required error, got %v", err)
	}
}

func TestInlineCommentStripping(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"HostName 1.2.3.4 # production", "HostName 1.2.3.4"},
		{"HostName 1.2.3.4", "HostName 1.2.3.4"},
		{`HostName "foo#bar" # real comment`, `HostName "foo#bar"`},
		{"# full line comment", ""},
		{"HostName 'has#hash'", "HostName 'has#hash'"},
	}
	for _, tt := range tests {
		got := stripInlineComment(tt.input)
		if got != tt.want {
			t.Errorf("stripInlineComment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParsePort(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"22", 22},
		{"2222", 2222},
		{"", 0},
		{"abc", 0},
		{"-1", 0},
		{"0", 0},
		{"  3000  ", 3000},
	}
	for _, tt := range tests {
		got := parsePort(tt.input)
		if got != tt.want {
			t.Errorf("parsePort(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestIsLiteralPattern(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"myhost", true},
		{"my-host.example.com", true},
		{"*", false},
		{"web-*", false},
		{"!negated", false},
		{"host[1-3]", false},
		{"host?", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isLiteralPattern(tt.input)
		if got != tt.want {
			t.Errorf("isLiteralPattern(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestMultipleIdentityFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config")
	if err := os.WriteFile(path, []byte("Host multi\n  HostName 1.2.3.4\n  IdentityFile ~/.ssh/id_rsa\n  IdentityFile ~/.ssh/id_ed25519\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if len(hosts[0].IdentityFiles) != 2 {
		t.Fatalf("expected 2 identity files, got %d: %v", len(hosts[0].IdentityFiles), hosts[0].IdentityFiles)
	}
}

func TestLoadMissingFile(t *testing.T) {
	hosts, err := Load(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("expected 0 hosts, got %d", len(hosts))
	}
}

func TestSplitDirective(t *testing.T) {
	tests := []struct {
		input    string
		key, val string
		ok       bool
	}{
		{"HostName 1.2.3.4", "HostName", "1.2.3.4", true},
		{"Port=2222", "Port", "2222", true},
		{"User\tadmin", "User", "admin", true},
		{"", "", "", false},
		{"single", "", "", false},
	}
	for _, tt := range tests {
		key, val, ok := splitDirective(tt.input)
		if ok != tt.ok || key != tt.key || val != tt.val {
			t.Errorf("splitDirective(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.input, key, val, ok, tt.key, tt.val, tt.ok)
		}
	}
}
