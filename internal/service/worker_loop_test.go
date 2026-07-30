package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartPeriodicWorkerCancellationDuringStartupDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := startPeriodicWorker(ctx, time.Hour, time.Hour, func() { calls.Add(1) })

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if calls.Load() != 0 {
		t.Fatalf("worker ran %d times after cancellation", calls.Load())
	}
}

func TestStartPeriodicWorkerStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := startPeriodicWorker(ctx, 0, 5*time.Millisecond, func() { calls.Add(1) })

	deadline := time.After(time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("worker did not run")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
