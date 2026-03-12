//go:build !darwin

package credentials

import (
	"errors"
	"testing"
)

func TestStubSet(t *testing.T) {
	err := Set("host1", "user1", "password")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Set() = %v, want ErrUnsupported", err)
	}
}

func TestStubGet(t *testing.T) {
	err := Get("host1", "user1", "password")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Get() = %v, want ErrUnsupported", err)
	}
}

func TestStubDelete(t *testing.T) {
	err := Delete("host1", "user1", "password")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Delete() = %v, want ErrUnsupported", err)
	}
}
