package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

func newFullTextSafeHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialPublicFullTextTarget
	return transport
}

func dialPublicFullTextTarget(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return nil, errors.New(FullTextErrorSSRFBlocked)
	}

	addresses := make([]netip.Addr, 0, 2)
	if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = append(addresses, parsed)
	} else {
		resolved, resolveErr := resolveFullTextHostname(host)
		if resolveErr != nil {
			return nil, errors.New(FullTextErrorSSRFBlocked)
		}
		for _, ip := range resolved {
			parsed, ok := netip.AddrFromSlice(ip)
			if ok {
				addresses = append(addresses, parsed.Unmap())
			}
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New(FullTextErrorSSRFBlocked)
	}
	for _, address := range addresses {
		if isBlockedFullTextIP(address) {
			return nil, errors.New(FullTextErrorSSRFBlocked)
		}
	}

	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, target := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(target.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}
