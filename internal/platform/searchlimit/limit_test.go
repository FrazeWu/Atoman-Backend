package searchlimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquireHonorsCancellationAndReleasesBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, Acquire(ctx), context.Canceled)
	for range Capacity {
		require.NoError(t, Acquire(context.Background()))
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, Acquire(ctx), context.DeadlineExceeded)
	for range Capacity {
		Release()
	}
	require.NoError(t, Acquire(context.Background()))
	Release()
}
