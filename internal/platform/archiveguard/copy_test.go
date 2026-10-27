package archiveguard

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestCopyBounded(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit, want int64
		overflow    bool
	}{
		{"empty", "", 0, 0, false},
		{"exact", "abcd", 4, 4, false},
		{"smaller", "abc", 4, 3, false},
		{"unknown-size-overflow", strings.Repeat("x", 1024), 4, 5, true},
		{"no-budget", "x", 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			n, err := CopyBounded(&dst, strings.NewReader(tc.input), tc.limit)
			if n != tc.want || int64(dst.Len()) != tc.want {
				t.Fatalf("copied %d bytes; want %d", n, tc.want)
			}
			if errors.Is(err, ErrExpandedSize) != tc.overflow {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.overflow && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCopyBoundedRejectsInvalidLimits(t *testing.T) {
	for _, limit := range []int64{-1, math.MaxInt64} {
		n, err := CopyBounded(io.Discard, strings.NewReader("data"), limit)
		if n != 0 || err == nil {
			t.Fatalf("invalid limit %d: %d, %v", limit, n, err)
		}
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCopyBoundedPreservesWriteFailure(t *testing.T) {
	want := errors.New("disk full")
	_, err := CopyBounded(failingWriter{want}, strings.NewReader("data"), 8)
	if !errors.Is(err, want) {
		t.Fatalf("got %v; want %v", err, want)
	}
}
