package redisx

import "testing"

func TestNewClientFromURLRejectsInvalidURL(t *testing.T) {
	if _, err := newClientFromURL("://invalid"); err == nil {
		t.Fatal("expected invalid Redis URL to be rejected")
	}
}
