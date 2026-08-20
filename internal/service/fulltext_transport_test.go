package service

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestDialPublicFullTextTargetRejectsPrivateResolvedAddress(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })

	_, err := dialPublicFullTextTarget(context.Background(), "tcp", "rebind.example:80")
	if err == nil || !strings.Contains(err.Error(), FullTextErrorSSRFBlocked) {
		t.Fatalf("error=%v", err)
	}
}
