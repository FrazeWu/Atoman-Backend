package main

import (
	"context"
	"errors"
	"net/http"
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
