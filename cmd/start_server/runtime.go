package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const shutdownTimeout = 15 * time.Second

type managedServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func shouldRunMigrationsOnStart(environment string, override string) bool {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return strings.ToLower(strings.TrimSpace(environment)) != "production"
}

func serveUntilShutdown(ctx context.Context, server managedServer, timeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveErr
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func waitForWorkers(timeout time.Duration, workers ...<-chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for _, done := range workers {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func databaseLogTarget(dbType string, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.Contains(rawURL, "=") && !strings.Contains(rawURL, "://") {
		return databaseLogTargetFromDSN(dbType, rawURL)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(dbType) + " database"
	}

	parts := []string{strings.TrimSpace(dbType) + " database"}
	if host := parsed.Host; host != "" {
		parts = append(parts, "host="+host)
	}
	if dbName := strings.TrimPrefix(parsed.EscapedPath(), "/"); dbName != "" {
		if decoded, err := url.PathUnescape(dbName); err == nil {
			dbName = decoded
		}
		parts = append(parts, "dbname="+dbName)
	}
	return strings.Join(parts, " ")
}

func databaseLogTargetFromDSN(dbType string, dsn string) string {
	values := map[string]string{}
	for _, field := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(value, "'\"")
	}

	parts := []string{strings.TrimSpace(dbType) + " database"}
	host := values["host"]
	if port := values["port"]; host != "" && port != "" {
		host += ":" + port
	}
	if host != "" {
		parts = append(parts, "host="+host)
	}
	if dbName := values["dbname"]; dbName != "" {
		parts = append(parts, "dbname="+dbName)
	}
	return strings.Join(parts, " ")
}
