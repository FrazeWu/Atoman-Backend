// Package archiveguard bounds resources used while processing untrusted archives.
package archiveguard

import (
	"errors"
	"io"
	"math"
)

// MaxRARDictionaryBytes is a per-reader limit, independent of declared file sizes.
const MaxRARDictionaryBytes int64 = 64 << 20

var ErrExpandedSize = errors.New("archive expands beyond size limit")

// CopyBounded validates actual output, including archives with unknown entry sizes.
// At most remaining+1 bytes are read/written; callers discard partial output on error.
func CopyBounded(dst io.Writer, src io.Reader, remaining int64) (int64, error) {
	if remaining < 0 || remaining == math.MaxInt64 {
		return 0, errors.New("invalid archive expansion limit")
	}
	n, err := io.Copy(dst, io.LimitReader(src, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		return n, ErrExpandedSize
	}
	return n, nil
}
