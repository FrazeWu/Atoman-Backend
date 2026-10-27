package handlers

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTurnstileBoundsProviderResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"normal", 16, false}, {"exact-limit", 64 << 10, false}, {"oversized", (64 << 10) + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"success":true}`
			body += strings.Repeat(" ", tc.size-len(body))
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			defer response.Body.Close()
			err := checkTurnstileResponse(response)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestTurnstileRejectsInvalidProviderResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusBadGateway, `{"success":true}`},
		{http.StatusOK, `{"success":false}`},
		{http.StatusOK, `not JSON`},
	} {
		response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}
		if err := checkTurnstileResponse(response); err == nil {
			t.Fatalf("accepted invalid response status=%d", tc.status)
		}
		response.Body.Close()
	}
}
