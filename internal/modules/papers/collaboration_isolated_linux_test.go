//go:build linux

package papers

import (
	"testing"

	"atoman/internal/platform/paperscrdt"
	"github.com/stretchr/testify/require"
)

func testIsolatedCollabEngine(t *testing.T, path string) collabEngine {
	t.Helper()
	engine, err := paperscrdt.NewUnixClient(path)
	require.NoError(t, err)
	return engine
}
