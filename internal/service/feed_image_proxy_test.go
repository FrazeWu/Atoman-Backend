package service

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFeedImageProxySignsAndFetchesOnlyValidImages(t *testing.T) {
	t.Setenv("FEED_IMAGE_PROXY_PUBLIC_URL", "/api/v1/feed/media/image")
	t.Setenv("FEED_IMAGE_PROXY_SECRET", "image-proxy-secret-for-tests-32bytes")
	remoteURL := "https://cdn.example.com/article.webp"

	proxied := MaybeProxyFeedImageURL(remoteURL)
	parsed, err := url.Parse(proxied)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/api/v1/feed/media/image" || parsed.Query().Get("url") != remoteURL || parsed.Query().Get("sig") == "" {
		t.Fatalf("proxied_url=%q", proxied)
	}

	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })

	originalClient := feedImageProxyHTTPClient
	feedImageProxyHTTPClient = &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != remoteURL {
				t.Fatalf("url=%q", request.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"image/webp"}},
				Body:       io.NopCloser(strings.NewReader("image-bytes")),
			}, nil
		}),
	}
	t.Cleanup(func() { feedImageProxyHTTPClient = originalClient })

	body, contentType, err := FetchFeedImageProxy(remoteURL, parsed.Query().Get("sig"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "image-bytes" || contentType != "image/webp" {
		t.Fatalf("body=%q content_type=%q", body, contentType)
	}
	if _, _, err := FetchFeedImageProxy(remoteURL, "invalid"); err != ErrFeedImageProxySignature {
		t.Fatalf("invalid signature error=%v", err)
	}
}

func TestMaybeProxyFeedImageURLIsIdempotent(t *testing.T) {
	t.Setenv("FEED_IMAGE_PROXY_PUBLIC_URL", "https://api.example.com/api/v1/feed/media/image")
	t.Setenv("FEED_IMAGE_PROXY_SECRET", "image-proxy-secret-for-tests-32bytes")
	remoteURL := "https://cdn.example.com/article.webp"

	proxied := MaybeProxyFeedImageURL(remoteURL)
	if nested := MaybeProxyFeedImageURL(proxied); nested != proxied {
		t.Fatalf("nested proxy URL=%q, original=%q", nested, proxied)
	}
}

func TestFeedImageProxyRejectsWeakSecret(t *testing.T) {
	t.Setenv("FEED_IMAGE_PROXY_PUBLIC_URL", "/api/v1/feed/media/image")
	t.Setenv("FEED_IMAGE_PROXY_SECRET", "weak")
	remoteURL := "https://cdn.example.com/article.webp"
	if proxied := MaybeProxyFeedImageURL(remoteURL); proxied != remoteURL {
		t.Fatalf("proxied_url=%q", proxied)
	}
	if _, _, err := FetchFeedImageProxy(remoteURL, "invalid"); err != ErrFeedImageProxyDisabled {
		t.Fatalf("error=%v", err)
	}
}

func TestFeedImageProxyRejectsSVG(t *testing.T) {
	t.Setenv("FEED_IMAGE_PROXY_PUBLIC_URL", "/api/v1/feed/media/image")
	t.Setenv("FEED_IMAGE_PROXY_SECRET", "image-proxy-secret-for-tests-32bytes")
	remoteURL := "https://cdn.example.com/article.svg"

	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })
	originalClient := feedImageProxyHTTPClient
	feedImageProxyHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/svg+xml"}},
			Body:       io.NopCloser(strings.NewReader("<svg/>")),
		}, nil
	})}
	t.Cleanup(func() { feedImageProxyHTTPClient = originalClient })

	_, _, err := FetchFeedImageProxy(remoteURL, signFeedImageProxyURL(remoteURL, "image-proxy-secret-for-tests-32bytes"))
	if err == nil || !strings.Contains(err.Error(), "invalid feed image proxy response") {
		t.Fatalf("error=%v", err)
	}
}
