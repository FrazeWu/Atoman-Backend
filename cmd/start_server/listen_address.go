package main

import (
	"net"
	"os"
	"strings"
)

// HTTP_HOST is optional for development; production uses the loopback proxy.
func configuredListenAddress(port string) string {
	return net.JoinHostPort(strings.TrimSpace(os.Getenv("HTTP_HOST")), port)
}
