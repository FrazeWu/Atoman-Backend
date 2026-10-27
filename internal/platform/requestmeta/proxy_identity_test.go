package requestmeta

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestNormalizedProxyAddressCannotBeOverriddenByClientCloudflareHeader(t *testing.T) {
	t.Setenv("GEOIP_DB_PATH", "")
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:42000"
	// Nginx always replaces X-Real-IP after trusted Cloudflare real-IP processing.
	request.Header.Set("X-Real-IP", "198.51.100.8")
	request.Header.Set("CF-Connecting-IP", "203.0.113.19")
	request.Header.Set("CF-IPCountry", "US")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = request
	got := FromGin(ctx)
	if got.IPAddress != "198.51.100.8" || got.IPPrefix != "198.51.100.0/24" || got.CountryCode != "" {
		t.Fatalf("untrusted provider header overrode normalized proxy metadata: %#v", got)
	}
}

func TestMatchingCloudflareAddressRetainsCountryFallback(t *testing.T) {
	t.Setenv("GEOIP_DB_PATH", "")
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[::1]:42000"
	request.Header.Set("X-Real-IP", "216.167.7.4")
	request.Header.Set("CF-Connecting-IP", "216.167.7.4")
	request.Header.Set("CF-IPCountry", "US")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = request
	got := FromGin(ctx)
	if got.IPAddress != "216.167.7.4" || got.CountryCode != "US" {
		t.Fatalf("unexpected metadata: %#v", got)
	}
}
