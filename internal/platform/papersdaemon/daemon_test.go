package papersdaemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"atoman/internal/platform/papersqueue"
	"github.com/stretchr/testify/require"
)

type fakeStepper struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	fn        func(int) (bool, error)
}

func (f *fakeStepper) Step(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.fn != nil {
		return f.fn(n)
	}
	return false, nil
}
func (f *fakeStepper) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxActive
}
func fixtureConfig() Config {
	return Config{WorkerID: "p0-worker", PollInterval: 10 * time.Millisecond, ErrorBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}
}
func TestRejectBadConfig(t *testing.T) {
	cfg := fixtureConfig()
	_, err := New(&fakeStepper{}, cfg)
	require.ErrorIs(t, err, ErrConfig)
	cfg.PollInterval = 100 * time.Millisecond
	cfg.ErrorBackoff = 100 * time.Millisecond
	cfg.MaxBackoff = time.Second
	_, err = New(&fakeStepper{}, cfg)
	require.NoError(t, err)
	cfg.WorkerID = "../../shell"
	_, err = New(&fakeStepper{}, cfg)
	require.ErrorIs(t, err, ErrConfig)
}
func TestDaemonRetriesBlockedAndUnknownSafely(t *testing.T) {
	cfg := fixtureConfig()
	cfg.PollInterval = 100 * time.Millisecond
	cfg.ErrorBackoff = 100 * time.Millisecond
	cfg.MaxBackoff = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stepper := &fakeStepper{}
	stepper.fn = func(n int) (bool, error) {
		switch n {
		case 1:
			return false, papersqueue.ErrBlocked
		case 2:
			return true, papersqueue.ErrExecutionUncertain
		case 3:
			cancel()
			return true, nil
		default:
			t.Error("unexpected fourth call")
			return false, nil
		}
	}
	daemon, err := New(stepper, cfg)
	require.NoError(t, err)
	require.NoError(t, daemon.Run(ctx))
	calls, maxActive := stepper.stats()
	require.Equal(t, 3, calls)
	require.Equal(t, 1, maxActive)
	state := daemon.Snapshot()
	require.EqualValues(t, 2, state.Errors)
	require.True(t, state.Blocked || state.LastError != "")
}
func TestDaemonIdleStopsOnCancellation(t *testing.T) {
	cfg := fixtureConfig()
	cfg.PollInterval = 100 * time.Millisecond
	cfg.ErrorBackoff = 100 * time.Millisecond
	cfg.MaxBackoff = 200 * time.Millisecond
	daemon, err := New(&fakeStepper{}, cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 190*time.Millisecond)
	defer cancel()
	require.NoError(t, daemon.Run(ctx))
	calls, _ := daemon.Stepper.(*fakeStepper).stats()
	require.GreaterOrEqual(t, calls, 1)
	require.LessOrEqual(t, calls, 3)
}
func TestBackoffOnGenericErrors(t *testing.T) {
	cfg := fixtureConfig()
	cfg.PollInterval = 100 * time.Millisecond
	cfg.ErrorBackoff = 100 * time.Millisecond
	cfg.MaxBackoff = 200 * time.Millisecond
	stepper := &fakeStepper{fn: func(int) (bool, error) { return false, errors.New("database offline") }}
	daemon, err := New(stepper, cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 340*time.Millisecond)
	defer cancel()
	require.NoError(t, daemon.Run(ctx))
	state := daemon.Snapshot()
	require.GreaterOrEqual(t, state.Errors, uint64(2))
	require.Equal(t, "database offline", state.LastError)
}
