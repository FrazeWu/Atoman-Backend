package requestmeta

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFromGinUsesForwardedClientOnlyFromLoopbackProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:42000"
	request.Header.Set("CF-Connecting-IP", "203.0.113.19")
	request.Header.Set("User-Agent", "Mozilla/5.0 test")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	info := FromGin(context)
	if info.IPAddress != "203.0.113.19" || info.IPPrefix != "203.0.113.0/24" || info.UserAgent != "Mozilla/5.0 test" {
		t.Fatalf("unexpected request metadata: %#v", info)
	}
}

func TestFromGinIgnoresForwardedClientFromUntrustedPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "198.51.100.8:42000"
	request.Header.Set("CF-Connecting-IP", "203.0.113.19")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	info := FromGin(context)
	if info.IPAddress != "198.51.100.8" || info.IPPrefix != "198.51.100.0/24" {
		t.Fatalf("unexpected request metadata: %#v", info)
	}
}

func TestFromGinLabelsPrivateAddressesAsLocalNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.4.21:42000"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	if info := FromGin(context); info.City != "本地网络" || info.Location() != "本地网络" {
		t.Fatalf("unexpected local metadata: %#v", info)
	}
}

func TestFromGinUsesCloudflareCountryWhenGeoIPDatabaseIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GEOIP_DB_PATH", "")
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:42000"
	request.Header.Set("CF-Connecting-IP", "216.167.7.4")
	request.Header.Set("CF-IPCountry", "US")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	info := FromGin(context)
	if info.IPAddress != "216.167.7.4" || info.CountryCode != "US" || info.Location() != "US" {
		t.Fatalf("unexpected request metadata: %#v", info)
	}
}

func TestFromIPAddressLabelsPrivateAddressesAsLocalNetwork(t *testing.T) {
	info := FromIPAddress("10.0.4.21")
	if info.IPPrefix != "10.0.4.0/24" || info.Location() != "本地网络" {
		t.Fatalf("unexpected IP metadata: %#v", info)
	}
}
