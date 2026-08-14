package indexnow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultEndpoint = "https://api.indexnow.org/indexnow"
	defaultSiteURL  = "https://www.atoman.org"
	maxURLs         = 10_000
)

var validKey = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

type Config struct {
	Key      string
	SiteURL  string
	Endpoint string
}

type Submitter struct {
	key         string
	host        string
	keyLocation string
	endpoint    string
	client      *http.Client
}

type submission struct {
	Host        string   `json:"host"`
	Key         string   `json:"key"`
	KeyLocation string   `json:"keyLocation"`
	URLList     []string `json:"urlList"`
}

func New(config Config, client *http.Client) (*Submitter, error) {
	key := strings.TrimSpace(config.Key)
	if !validKey.MatchString(key) {
		return nil, errors.New("indexnow key must be 8-128 letters, numbers, or dashes")
	}
	siteURL := strings.TrimRight(strings.TrimSpace(config.SiteURL), "/")
	if siteURL == "" {
		siteURL = defaultSiteURL
	}
	parsedSite, err := url.Parse(siteURL)
	if err != nil || parsedSite.Scheme != "https" || parsedSite.Host == "" || parsedSite.Path != "" {
		return nil, errors.New("indexnow site URL must be an HTTPS origin")
	}
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || (parsedEndpoint.Scheme != "https" && parsedEndpoint.Scheme != "http") || parsedEndpoint.Host == "" {
		return nil, errors.New("indexnow endpoint must be an HTTP(S) URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Submitter{
		key:         key,
		host:        parsedSite.Host,
		keyLocation: siteURL + "/" + key + ".txt",
		endpoint:    endpoint,
		client:      client,
	}, nil
}

func (s *Submitter) Submit(ctx context.Context, values []string) error {
	urls, err := s.normalizeURLs(values)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		return nil
	}
	body, err := json.Marshal(submission{
		Host: s.host, Key: s.key, KeyLocation: s.keyLocation, URLList: urls,
	})
	if err != nil {
		return fmt.Errorf("encode indexnow submission: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create indexnow request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Atoman-IndexNow/1.0")
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("submit indexnow request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("indexnow returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	return nil
}

func (s *Submitter) normalizeURLs(values []string) ([]string, error) {
	if len(values) > maxURLs {
		return nil, fmt.Errorf("indexnow accepts at most %d URLs per request", maxURLs)
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		var parsed *url.URL
		var err error
		if strings.HasPrefix(value, "/") {
			parsed, err = url.Parse("https://" + s.host + value)
		} else {
			parsed, err = url.Parse(value)
		}
		if err != nil || parsed.Scheme != "https" || parsed.Host != s.host || parsed.Path == "" {
			return nil, fmt.Errorf("indexnow URL must belong to https://%s: %q", s.host, value)
		}
		parsed.Fragment = ""
		unique[parsed.String()] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

type Worker struct {
	submitter *Submitter
	queue     chan string
}

var activeWorker atomic.Pointer[Worker]

func StartWorker(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	key := strings.TrimSpace(os.Getenv("INDEXNOW_KEY"))
	if key == "" {
		close(done)
		return done
	}
	submitter, err := New(Config{
		Key: key, SiteURL: os.Getenv("INDEXNOW_SITE_URL"), Endpoint: os.Getenv("INDEXNOW_ENDPOINT"),
	}, nil)
	if err != nil {
		log.Printf("WARN: IndexNow disabled: %v", err)
		close(done)
		return done
	}
	worker := &Worker{submitter: submitter, queue: make(chan string, 1000)}
	activeWorker.Store(worker)
	go func() {
		defer close(done)
		defer activeWorker.CompareAndSwap(worker, nil)
		worker.run(ctx)
	}()
	return done
}

func NotifyPaths(paths ...string) {
	worker := activeWorker.Load()
	if worker == nil {
		return
	}
	for _, path := range paths {
		select {
		case worker.queue <- path:
		default:
			log.Printf("WARN: IndexNow queue full; dropping %s", path)
		}
	}
}

func (w *Worker) run(ctx context.Context) {
	pending := map[string]struct{}{}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	flush := func() {
		if len(pending) == 0 {
			return
		}
		paths := make([]string, 0, len(pending))
		for path := range pending {
			paths = append(paths, path)
		}
		pending = map[string]struct{}{}
		submitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := w.submitter.Submit(submitCtx, paths)
		cancel()
		if err != nil {
			log.Printf("WARN: IndexNow submission failed: %v", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case path := <-w.queue:
			pending[path] = struct{}{}
			if len(pending) >= maxURLs {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
