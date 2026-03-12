package credentials

import "testing"

func TestServiceNameIncludesHostAndKind(t *testing.T) {
	got := serviceName("edge1", "password")
	want := "tmux-ssh-manager:edge1:password"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNormalizeUserFallsBackToHost(t *testing.T) {
	if got := normalizeUser("edge1", ""); got != "edge1" {
		t.Fatalf("expected host fallback, got %q", got)
	}
	if got := normalizeUser("edge1", "matt"); got != "matt" {
		t.Fatalf("expected explicit user, got %q", got)
	}
}

func TestItemLabelIncludesUserWhenPresent(t *testing.T) {
	if got := itemLabel("edge1", "matt", "password"); got != "password for matt@edge1" {
		t.Fatalf("unexpected label %q", got)
	}
	if got := itemLabel("edge1", "edge1", "password"); got != "password for edge1" {
		t.Fatalf("unexpected fallback label %q", got)
	}
}

func TestNormalizeKind(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "password"},
		{"password", "password"},
		{"Password", "password"},
		{"passphrase", "passphrase"},
		{"otp", "otp"},
		{"TOTP", "otp"},
		{"custom", "custom"},
	}
	for _, tt := range tests {
		got := normalizeKind(tt.input)
		if got != tt.want {
			t.Errorf("normalizeKind(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeHostRejectsEmpty(t *testing.T) {
	_, err := normalizeHost("")
	if err == nil {
		t.Fatal("expected error for empty host")
	}
	_, err = normalizeHost("  ")
	if err == nil {
		t.Fatal("expected error for whitespace host")
	}
}

func TestServiceNameVariousKinds(t *testing.T) {
	got := serviceName("myhost", "passphrase")
	if got != "tmux-ssh-manager:myhost:passphrase" {
		t.Fatalf("unexpected: %q", got)
	}
}
