package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCredSetParsesFlags(t *testing.T) {
	originalSet := credSet
	originalGet := credGet
	originalDelete := credDelete
	t.Cleanup(func() {
		credSet = originalSet
		credGet = originalGet
		credDelete = originalDelete
	})

	called := false
	credSet = func(host, user, kind string) error {
		called = true
		if host != "edge1" || user != "matt" || kind != "password" {
			t.Fatalf("unexpected args: %q %q %q", host, user, kind)
		}
		return nil
	}

	var stdout bytes.Buffer
	if err := runCred([]string{"set", "--host", "edge1", "--user", "matt"}, &stdout); err != nil {
		t.Fatalf("runCred returned error: %v", err)
	}
	if !called {
		t.Fatalf("expected credSet to be called")
	}
	if got := stdout.String(); !strings.Contains(got, "stored password for matt@edge1") {
		t.Fatalf("unexpected stdout %q", got)
	}
}

func TestRunCredDeleteUsesKind(t *testing.T) {
	originalSet := credSet
	originalGet := credGet
	originalDelete := credDelete
	t.Cleanup(func() {
		credSet = originalSet
		credGet = originalGet
		credDelete = originalDelete
	})

	credDelete = func(host, user, kind string) error {
		if host != "edge1" || user != "" || kind != "passphrase" {
			t.Fatalf("unexpected args: %q %q %q", host, user, kind)
		}
		return nil
	}

	if err := runCred([]string{"delete", "--host", "edge1", "--kind", "passphrase"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runCred returned error: %v", err)
	}
}

func TestRunCredRequiresHost(t *testing.T) {
	if err := runCred([]string{"get"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "missing required --host") {
		t.Fatalf("expected missing host error, got %v", err)
	}
}

func TestCredentialCommandForPathIncludesOptionalUser(t *testing.T) {
	cmd := credentialCommandForPath("/tmp/tmux-ssh-manager", "set", "edge1", "matt", "password")
	got := strings.Join(cmd.Args, " ")
	want := "/tmp/tmux-ssh-manager cred set --host edge1 --kind password --user matt"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCredentialCommandForPathOmitsEmptyUser(t *testing.T) {
	cmd := credentialCommandForPath("/tmp/tmux-ssh-manager", "delete", "edge1", "", "password")
	got := strings.Join(cmd.Args, " ")
	want := "/tmp/tmux-ssh-manager cred delete --host edge1 --kind password"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRunListOutputsAliases(t *testing.T) {
	tmp := t.TempDir()
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Host alpha\n  HostName 1.2.3.4\n\nHost beta\n  HostName 5.6.7.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)

	var stdout bytes.Buffer
	if err := runList(nil, &stdout); err != nil {
		t.Fatalf("runList error: %v", err)
	}
	lines := strings.TrimSpace(stdout.String())
	if !strings.Contains(lines, "alpha") || !strings.Contains(lines, "beta") {
		t.Fatalf("expected alpha and beta in output, got %q", lines)
	}
}

func TestRunConnectDryRun(t *testing.T) {
	var stdout bytes.Buffer
	err := runConnect([]string{"--dry-run", "myhost"}, nil, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "ssh myhost" {
		t.Fatalf("expected %q, got %q", "ssh myhost", got)
	}
}

func TestRunConnectRequiresAlias(t *testing.T) {
	err := runConnect(nil, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestRunAddCreatesHostBlock(t *testing.T) {
	tmp := t.TempDir()
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)

	var stdout bytes.Buffer
	err := runAdd([]string{"--alias", "newbox", "--hostname", "10.0.0.5", "--user", "admin"}, &stdout)
	if err != nil {
		t.Fatalf("runAdd error: %v", err)
	}
	if !strings.Contains(stdout.String(), "added host newbox") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}

	data, err := os.ReadFile(filepath.Join(sshDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Host newbox") || !strings.Contains(string(data), "User admin") {
		t.Fatalf("expected host block in config, got:\n%s", data)
	}
}

func TestRunCredUnknownAction(t *testing.T) {
	err := runCred([]string{"bogus", "--host", "x"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown cred action") {
		t.Fatalf("expected unknown action error, got %v", err)
	}
}

func TestRunListJSON(t *testing.T) {
	tmp := t.TempDir()
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Host gamma\n  HostName 9.8.7.6\n  User root\n  Port 2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)

	var stdout bytes.Buffer
	if err := runList([]string{"--json"}, &stdout); err != nil {
		t.Fatalf("runList --json error: %v", err)
	}
	var entries []listEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Alias != "gamma" || entries[0].HostName != "9.8.7.6" || entries[0].Port != 2222 || entries[0].User != "root" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestRunVersionOutputsVersion(t *testing.T) {
	var stdout bytes.Buffer
	err := Run([]string{"--version"}, nil, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got == "" {
		t.Fatal("expected version output")
	}
}

func TestNormalizeEnterMode(t *testing.T) {
	tests := []struct{ input, want string }{
		{"p", "p"}, {"pane", "p"}, {"P", "p"},
		{"w", "w"}, {"window", "w"}, {"W", "w"},
		{"s", "s"}, {"split", "s"}, {"split-h", "s"},
		{"v", "v"}, {"split-v", "v"},
		{"", "p"}, {"junk", "p"},
	}
	for _, tc := range tests {
		if got := normalizeEnterMode(tc.input); got != tc.want {
			t.Errorf("normalizeEnterMode(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRunConnectSplitRequiresTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	err := runConnectSplit("edge1", 3, "v", "tiled")
	if err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Fatalf("expected tmux error, got: %v", err)
	}
}

func TestRunConnectSplitInvalidMode(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	err := runConnectSplit("edge1", 3, "bad", "tiled")
	if err == nil || !strings.Contains(err.Error(), "split-mode") {
		t.Fatalf("expected split-mode error, got: %v", err)
	}
}

func TestRunConnectParsesLayoutFlags(t *testing.T) {
	var stdout bytes.Buffer
	err := runConnect([]string{"--dry-run", "--split-count", "4", "--split-mode", "v", "--layout", "tiled", "edge1"}, nil, &stdout, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "ssh edge1" {
		t.Fatalf("dry-run output = %q, want %q", got, "ssh edge1")
	}
}
