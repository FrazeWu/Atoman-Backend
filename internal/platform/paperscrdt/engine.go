// Package paperscrdt wraps a pinned Yjs runtime for private, internal
// checkpoint validation. There is deliberately no public listener.
package paperscrdt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxText   = 1 << 20
	MaxState  = 4 << 20
	MaxUpdate = 256 << 10
	MaxVector = 64 << 10
	maxJSON   = 13 << 20
)

var (
	ErrInvalid     = errors.New("invalid CRDT document or update")
	ErrUnavailable = errors.New("CRDT runtime unavailable")
)

type Request struct {
	Op     string `json:"op"`
	Text   string `json:"text"`
	State  []byte `json:"state,omitempty"`
	Update []byte `json:"update,omitempty"`
}
type Checkpoint struct {
	Text   string `json:"text"`
	State  []byte `json:"state"`
	Vector []byte `json:"vector"`
}
type Engine struct {
	node   string
	script string
	slots  chan struct{}
}

func validText(s string) bool {
	return len(s) <= MaxText && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func ValidCheckpoint(c Checkpoint) bool {
	return validText(c.Text) && len(c.State) > 0 && len(c.State) <= MaxState && len(c.Vector) > 0 && len(c.Vector) <= MaxVector
}

func New(node, script string) (*Engine, error) {
	for _, p := range []string{node, script} {
		if !filepath.IsAbs(p) {
			return nil, ErrUnavailable
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrUnavailable
		}
	}
	return &Engine{node: node, script: script, slots: make(chan struct{}, 2)}, nil
}

type boundedBuffer struct{ buffer bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxJSON-b.buffer.Len() {
		return 0, ErrInvalid
	}
	return b.buffer.Write(p)
}
func validateRequest(req Request) error {
	if !validText(req.Text) || len(req.State) > MaxState || len(req.Update) > MaxUpdate {
		return ErrInvalid
	}
	switch req.Op {
	case "seed":
		if len(req.State) != 0 || len(req.Update) != 0 {
			return ErrInvalid
		}
	case "read":
		if len(req.State) == 0 || len(req.Update) != 0 {
			return ErrInvalid
		}
	case "merge":
		if len(req.State) == 0 || len(req.Update) == 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, req Request) (Checkpoint, error) {
	var out Checkpoint
	if err := validateRequest(req); err != nil {
		return out, err
	}
	if e == nil || ctx == nil {
		return out, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return out, ErrUnavailable
	}
	input, err := json.Marshal(req)
	if err != nil || len(input) > maxJSON {
		return out, ErrInvalid
	}
	cmd := exec.CommandContext(ctx, e.node, "--max-old-space-size=96", "--max-semi-space-size=8", e.script)
	cmd.Dir = filepath.Dir(e.script)
	// Never inherit database credentials, NODE_OPTIONS, proxy tokens, or
	// other service environment. This is not yet a production sandbox.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "NODE_ENV=production"}
	cmd.Stdin = bytes.NewReader(input)
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 2 {
			return out, ErrInvalid
		}
		return out, ErrUnavailable
	}
	decoder := json.NewDecoder(&output.buffer)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil || !ValidCheckpoint(out) {
		return Checkpoint{}, ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Checkpoint{}, ErrUnavailable
	}
	if (req.Op == "seed" || req.Op == "read") && out.Text != req.Text {
		return Checkpoint{}, ErrUnavailable
	}
	return out, nil
}
