//go:build !linux

package papers

import "testing"

func testIsolatedCollabEngine(t *testing.T, path string) collabEngine {
	t.Helper()
	t.Skip("Isolated Unix socket runtime acceptance requires Linux")
	return nil
}
