package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestCollectAdminInputReadsPasswordThroughHiddenReader(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("alice\nalice@example.com\nvisible-password\n"))
	called := false

	input, err := collectAdminInput(scanner, 42, func(fd int) (string, error) {
		called = true
		if fd != 42 {
			t.Fatalf("expected fd 42, got %d", fd)
		}
		return "hidden-password", nil
	})
	if err != nil {
		t.Fatalf("collect admin input: %v", err)
	}

	if !called {
		t.Fatal("expected password to be read through hidden reader")
	}
	if input.username != "alice" {
		t.Fatalf("username = %q, want alice", input.username)
	}
	if input.email != "alice@example.com" {
		t.Fatalf("email = %q, want alice@example.com", input.email)
	}
	if input.password != "hidden-password" {
		t.Fatalf("password = %q, want hidden-password", input.password)
	}
}

func TestReadPasswordFromTerminalRejectsNonInteractiveInput(t *testing.T) {
	_, err := readPasswordFromTerminal(-1)
	if err == nil {
		t.Fatal("expected non-interactive password input to fail")
	}

	message := err.Error()
	if !strings.Contains(message, "interactive terminal") {
		t.Fatalf("expected diagnostic to mention interactive terminal, got %q", message)
	}
	if !strings.Contains(message, "init_owner") {
		t.Fatalf("expected diagnostic to guide non-interactive bootstrap, got %q", message)
	}
}
