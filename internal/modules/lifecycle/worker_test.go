package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartPeriodicLifecycleWorkerStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := startPeriodicLifecycleWorker(ctx, 5*time.Millisecond, func() { calls.Add(1) })

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
