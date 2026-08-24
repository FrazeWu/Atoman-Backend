package requestmeta

import (
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/geoip2-golang"
)

type Info struct {
	IPAddress   string
	IPPrefix    string
	UserAgent   string
	CountryCode string
	Region      string
	City        string
}

type geoIPReaderState struct {
	path    string
	modTime time.Time
	size    int64
	reader  *geoip2.Reader
}

var geoIPReaderCache struct {
	sync.RWMutex
	state geoIPReaderState
}

func FromGin(c *gin.Context) Info {
	address, fromCloudflare, ok := clientAddress(c)
	if !ok {
		return Info{UserAgent: truncate(strings.TrimSpace(c.Request.UserAgent()), 512)}
	}
	info := fromAddress(address)
	info.UserAgent = truncate(strings.TrimSpace(c.Request.UserAgent()), 512)
	if info.CountryCode == "" && fromCloudflare && !address.IsLoopback() && !address.IsPrivate() && !address.IsLinkLocalUnicast() {
		info.CountryCode = cloudflareCountryCode(c.GetHeader("CF-IPCountry"))
	}
	return info
}

// FromIPAddress resolves an IP address without request-specific metadata.
func FromIPAddress(raw string) Info {
	address, ok := parseAddress(raw)
	if !ok {
		return Info{}
	}
	return fromAddress(address)
}

func fromAddress(address netip.Addr) Info {
	info := Info{
		IPAddress: address.String(),
		IPPrefix:  ipPrefix(address),
	}
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() {
		info.City = "本地网络"
		return info
	}
	info.CountryCode, info.Region, info.City = lookupGeoIP(address)
	return info
}

func (info Info) Location() string {
	parts := make([]string, 0, 3)
	for _, value := range []string{info.City, info.Region, strings.ToUpper(info.CountryCode)} {
		value = strings.TrimSpace(value)
		if value != "" && (len(parts) == 0 || parts[len(parts)-1] != value) {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

func clientAddress(c *gin.Context) (netip.Addr, bool, bool) {
	remote := strings.TrimSpace(c.Request.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	peer, peerOK := parseAddress(remote)
	if peerOK && peer.IsLoopback() {
		for index, raw := range []string{
			c.GetHeader("CF-Connecting-IP"),
			c.GetHeader("X-Real-IP"),
			firstForwardedAddress(c.GetHeader("X-Forwarded-For")),
		} {
			if address, ok := parseAddress(raw); ok {
				return address, index == 0, true
			}
		}
	}
	if address, ok := parseAddress(c.ClientIP()); ok {
		return address, false, true
	}
	return peer, false, peerOK
}

func parseAddress(raw string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func firstForwardedAddress(raw string) string {
	if index := strings.IndexByte(raw, ','); index >= 0 {
		return strings.TrimSpace(raw[:index])
	}
	return strings.TrimSpace(raw)
}

func ipPrefix(address netip.Addr) string {
	bits := 64
	if address.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(address, bits).Masked().String()
}

func lookupGeoIP(address netip.Addr) (string, string, string) {
	path := strings.TrimSpace(os.Getenv("GEOIP_DB_PATH"))
	if path == "" {
		return "", "", ""
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		return "", "", ""
	}

	geoIPReaderCache.RLock()
	if geoIPReaderMatches(path, fileInfo) {
		country, region, city := lookupGeoIPReader(geoIPReaderCache.state.reader, address)
		geoIPReaderCache.RUnlock()
		return country, region, city
	}
	geoIPReaderCache.RUnlock()

	geoIPReaderCache.Lock()
	defer geoIPReaderCache.Unlock()
	if !geoIPReaderMatches(path, fileInfo) {
		reader, err := geoip2.Open(path)
		if err != nil {
			return "", "", ""
		}
		if geoIPReaderCache.state.reader != nil {
			_ = geoIPReaderCache.state.reader.Close()
		}
		geoIPReaderCache.state = geoIPReaderState{
			path: path, modTime: fileInfo.ModTime(), size: fileInfo.Size(), reader: reader,
		}
	}
	return lookupGeoIPReader(geoIPReaderCache.state.reader, address)
}

func geoIPReaderMatches(path string, fileInfo os.FileInfo) bool {
	state := geoIPReaderCache.state
	return state.reader != nil && state.path == path && state.size == fileInfo.Size() && state.modTime.Equal(fileInfo.ModTime())
}

func lookupGeoIPReader(reader *geoip2.Reader, address netip.Addr) (string, string, string) {
	record, err := reader.City(net.IP(address.AsSlice()))
	if err != nil {
		return "", "", ""
	}
	region := ""
	if len(record.Subdivisions) > 0 {
		region = localizedName(record.Subdivisions[0].Names)
	}
	return strings.ToUpper(record.Country.IsoCode), region, localizedName(record.City.Names)
}

func cloudflareCountryCode(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	if len(value) != 2 {
		return ""
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return ""
		}
	}
	return value
}

func localizedName(names map[string]string) string {
	if value := strings.TrimSpace(names["zh-CN"]); value != "" {
		return value
	}
	return strings.TrimSpace(names["en"])
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
