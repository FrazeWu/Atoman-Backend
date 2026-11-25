//go:build linux

package papers

import (
	"context"
	"os"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/paperscrdt"
	"github.com/stretchr/testify/require"
)

func TestDurableCollabProductionStartupProbe(t *testing.T) {
	path := os.Getenv("PAPERS_CRDT_TEST_SOCKET")
	if path == "" {
		t.Skip("requires the disposable isolated Unix runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newCompileHTTPFixture(t, true)
	client, err := paperscrdt.NewUnixClient(path)
	require.NoError(t, err)
	// Invalid startup probes never enable collaboration or disable compile.
	require.Error(t, f.svc.verifyCollaborationRuntime(ctx, testStubEngine{}))
	require.Nil(t, f.svc.collaboration)
	require.Contains(t, f.request("GET", collabPath(f), "", f.ownerToken).Body.String(), `"enabled":false`)
	require.Contains(t, f.request("GET", f.base()+"/compile-capability", "", f.ownerToken).Body.String(), `"enabled":true`)
	require.NoError(t, f.svc.EnableProductionCollaboration(ctx, client))
	require.NotNil(t, f.svc.collaboration)
	require.Contains(t, f.request("GET", collabPath(f), "", f.ownerToken).Body.String(), `"enabled":true`)
	require.Error(t, f.svc.EnableProductionCollaboration(ctx, client))
	var count int64
	require.NoError(t, f.db.Model(&model.PaperCollaborativeDocument{}).Count(&count).Error)
	require.Zero(t, count, "synthetic startup probe must not create persistent source")
	// Missing schema is fail-closed. This is a disposable test database.
	require.NoError(t, f.db.Migrator().DropTable(&model.PaperCollaborativeDocument{}))
	disabled := NewService(f.db)
	require.Error(t, disabled.EnableProductionCollaboration(ctx, client))
	require.Nil(t, disabled.collaboration)
}
