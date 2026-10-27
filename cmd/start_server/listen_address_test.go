package main

import "testing"

func TestConfiguredListenAddress(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"", ":8080"}, {"127.0.0.1", "127.0.0.1:8080"}, {"::1", "[::1]:8080"},
	} {
		t.Setenv("HTTP_HOST", tc.host)
		if got := configuredListenAddress("8080"); got != tc.want {
			t.Fatalf("got %s, want %s", got, tc.want)
		}
	}
}
