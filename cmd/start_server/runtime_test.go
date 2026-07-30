package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeManagedServer struct {
	shutdownOnce sync.Once
	shutdown     chan struct{}
}

func (s *fakeManagedServer) ListenAndServe() error {
	<-s.shutdown
	return http.ErrServerClosed
}

func (s *fakeManagedServer) Shutdown(context.Context) error {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
	return nil
}

func TestNewHTTPServerUsesProductionTimeouts(t *testing.T) {
	server := newHTTPServer(":8080", http.NewServeMux())

	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 30*time.Second {
		t.Fatalf("unexpected read timeouts: header=%s read=%s", server.ReadHeaderTimeout, server.ReadTimeout)
	}
	if server.WriteTimeout != 60*time.Second || server.IdleTimeout != 120*time.Second {
		t.Fatalf("unexpected write/idle timeouts: write=%s idle=%s", server.WriteTimeout, server.IdleTimeout)
	}
}

func TestShouldRunMigrationsOnStartDefaultsByEnvironment(t *testing.T) {
	if shouldRunMigrationsOnStart("production", "") {
		t.Fatal("production must not run migrations on startup by default")
	}
	if !shouldRunMigrationsOnStart("development", "") {
		t.Fatal("development should run migrations on startup by default")
	}
	if !shouldRunMigrationsOnStart("production", "true") {
		t.Fatal("explicit override should enable startup migrations")
	}
	if shouldRunMigrationsOnStart("development", "false") {
		t.Fatal("explicit override should disable startup migrations")
	}
}

func TestServeUntilShutdownStopsServerWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := &fakeManagedServer{shutdown: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- serveUntilShutdown(ctx, server, time.Second) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveUntilShutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
	select {
	case <-server.shutdown:
	default:
		t.Fatal("expected Shutdown to be called")
	}
}

func TestServeUntilShutdownReturnsListenError(t *testing.T) {
	want := errors.New("listen failed")
	server := managedServerFunc{
		listen:   func() error { return want },
		shutdown: func(context.Context) error { return nil },
	}

	err := serveUntilShutdown(context.Background(), server, time.Second)
	if !errors.Is(err, want) {
		t.Fatalf("serveUntilShutdown() error = %v, want %v", err, want)
	}
}

type managedServerFunc struct {
	listen   func() error
	shutdown func(context.Context) error
}

func (s managedServerFunc) ListenAndServe() error              { return s.listen() }
func (s managedServerFunc) Shutdown(ctx context.Context) error { return s.shutdown(ctx) }
