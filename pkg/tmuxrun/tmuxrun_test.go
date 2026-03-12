package tmuxrun

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSSHCommandQuotesAlias(t *testing.T) {
	got := SSHCommand("prod'box")
	want := "exec ssh 'prod'\"'\"'box'"
	if got != want {
		t.Fatalf("unexpected ssh command: %s", got)
	}
}

func TestSSHCommandSimpleAlias(t *testing.T) {
	got := SSHCommand("edge1")
	want := "exec ssh 'edge1'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSSHCommandEmptyAlias(t *testing.T) {
	got := SSHCommand("")
	want := "exec ssh ''"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"", "''"},
		{"it's", "'it'\"'\"'s'"},
		{"a b c", "'a b c'"},
		{"$VAR", "'$VAR'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestInTmuxReadsEnv(t *testing.T) {
	t.Setenv("TMUX", "")
	if InTmux() {
		t.Fatal("expected false when TMUX is empty")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	if !InTmux() {
		t.Fatal("expected true when TMUX is set")
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	got := socketPath()
	if got != "/tmp/tmux-501/default" {
		t.Fatalf("expected socket path, got %q", got)
	}

	t.Setenv("TMUX", "")
	if got := socketPath(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestLoginShellFallback(t *testing.T) {
	t.Setenv("SHELL", "")
	if got := loginShell(); got != "sh" {
		t.Fatalf("expected sh fallback, got %q", got)
	}
	t.Setenv("SHELL", "/bin/zsh")
	if got := loginShell(); got != "/bin/zsh" {
		t.Fatalf("expected /bin/zsh, got %q", got)
	}
}

func TestSanitizeAlias(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"edge1", "edge1"},
		{"my-host.local", "my-host.local"},
		{"user@host", "user_host"},
		{"host with spaces", "host_with_spaces"},
		{"host/path", "host_path"},
		{"", "_"},
		{"café", "caf_"},
	}
	for _, tt := range tests {
		got := sanitizeAlias(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeAlias(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLogDirUsesXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	dir, err := LogDir("edge1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "tmux-ssh-manager", "logs", "edge1")
	if dir != want {
		t.Fatalf("expected %q, got %q", want, dir)
	}
}

func TestLogDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	dir, err := LogDir("prod")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "tmux-ssh-manager", "logs", "prod")
	if dir != want {
		t.Fatalf("expected %q, got %q", want, dir)
	}
}

func TestEnsureLogFileCreatesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	path, err := ensureLogFile("edge1")
	if err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	wantDir := filepath.Join(tmp, "tmux-ssh-manager", "logs", "edge1")
	wantPath := filepath.Join(wantDir, today+".log")
	if path != wantPath {
		t.Fatalf("expected %q, got %q", wantPath, path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("expected empty file, got size %d", info.Size())
	}
}
