// Package papersdaemon runs one persistent, bounded compile queue worker.
// It has no privileged sandbox credentials: execution is delegated to the
// authenticated Unix-socket Launcher through papersqueue.Runner.
package papersdaemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"atoman/internal/platform/papersqueue"
)

var ErrConfig = errors.New("papers daemon: invalid configuration")

type Stepper interface {
	Step(context.Context, string) (bool, error)
}
type Config struct {
	WorkerID     string
	PollInterval time.Duration
	ErrorBackoff time.Duration
	MaxBackoff   time.Duration
}

func (c Config) validate() error {
	if c.WorkerID == "" || len(c.WorkerID) > 64 ||
		c.PollInterval < 100*time.Millisecond || c.PollInterval > 30*time.Second ||
		c.ErrorBackoff < 100*time.Millisecond || c.MaxBackoff < c.ErrorBackoff ||
		c.MaxBackoff > time.Minute {
		return ErrConfig
	}
	for _, ch := range c.WorkerID {
		if !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') &&
			!(ch >= '0' && ch <= '9') && ch != '_' && ch != '.' && ch != ':' && ch != '-' {
			return ErrConfig
		}
	}
	return nil
}

type State struct {
	Steps       uint64
	Errors      uint64
	LastError   string
	LastSuccess time.Time
	Blocked     bool
}
type Daemon struct {
	Stepper Stepper
	Config  Config
	mu      sync.RWMutex
	state   State
}

func New(stepper Stepper, cfg Config) (*Daemon, error) {
	if stepper == nil || cfg.validate() != nil {
		return nil, ErrConfig
	}
	return &Daemon{Stepper: stepper, Config: cfg}, nil
}
func (d *Daemon) Snapshot() State {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state
}
func (d *Daemon) record(worked bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if worked {
		d.state.Steps++
	}
	if err != nil {
		d.state.Errors++
		d.state.LastError = err.Error()
		d.state.Blocked = errors.Is(err, papersqueue.ErrBlocked)
	} else {
		d.state.Blocked = false
		d.state.LastError = ""
		if worked {
			d.state.LastSuccess = time.Now().UTC()
		}
	}
}
func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run processes one step at a time. A blocked slot is NOT overridden.
// Transport uncertainty is retried after backoff; the PostgreSQL lease
// still protects the previous SandboxID against overlapping execution.
func (d *Daemon) Run(ctx context.Context) error {
	if d == nil || d.Stepper == nil || d.Config.validate() != nil {
		return ErrConfig
	}
	backoff := d.Config.ErrorBackoff
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		worked, err := d.Stepper.Step(ctx, d.Config.WorkerID)
		if ctx.Err() != nil {
			return nil
		}
		d.record(worked, err)
		if err != nil {
			if !wait(ctx, backoff) {
				return nil
			}
			if backoff < d.Config.MaxBackoff {
				backoff *= 2
				if backoff > d.Config.MaxBackoff {
					backoff = d.Config.MaxBackoff
				}
			}
			continue
		}
		backoff = d.Config.ErrorBackoff
		if !worked && !wait(ctx, d.Config.PollInterval) {
			return nil
		}
		// After a completed task, immediately look for queued work.
	}
}
func (d *Daemon) Validate() error {
	if d == nil || d.Stepper == nil {
		return fmt.Errorf("%w: nil worker", ErrConfig)
	}
	return d.Config.validate()
}
