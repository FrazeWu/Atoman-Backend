package requestmeta

import (
	"net"
	"net/netip"
	"os"
	"strings"

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

func FromGin(c *gin.Context) Info {
	address, ok := clientAddress(c)
	if !ok {
		return Info{UserAgent: truncate(strings.TrimSpace(c.Request.UserAgent()), 512)}
	}
	info := Info{
		IPAddress: address.String(),
		IPPrefix:  ipPrefix(address),
		UserAgent: truncate(strings.TrimSpace(c.Request.UserAgent()), 512),
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

func clientAddress(c *gin.Context) (netip.Addr, bool) {
	remote := strings.TrimSpace(c.Request.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	peer, peerOK := parseAddress(remote)
	if peerOK && peer.IsLoopback() {
		for _, raw := range []string{
			c.GetHeader("CF-Connecting-IP"),
			c.GetHeader("X-Real-IP"),
			firstForwardedAddress(c.GetHeader("X-Forwarded-For")),
		} {
			if address, ok := parseAddress(raw); ok {
				return address, true
			}
		}
	}
	if address, ok := parseAddress(c.ClientIP()); ok {
		return address, true
	}
	return peer, peerOK
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
	reader, err := geoip2.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer reader.Close()
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
