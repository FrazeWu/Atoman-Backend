package feed

import (
	"testing"
)

func TestDedupSnapshotInsertBatchOptIn(t *testing.T) {
	t.Setenv("FEED_SCALE_ISOLATED", "")
	t.Setenv("FEED_SNAPSHOT_BATCH_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_ROW_BATCH", "1000")
	t.Setenv("FEED_SNAPSHOT_MEMBER_BATCH", "1500")
	rows, members := dedupSnapshotInsertBatchSizes()
	if rows != 350 || members != 300 {
		t.Fatalf("production defaults changed: %d/%d", rows, members)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	rows, members = dedupSnapshotInsertBatchSizes()
	if rows != 1000 || members != 1500 {
		t.Fatalf("isolated opt in did not work: %d/%d", rows, members)
	}
	t.Setenv("FEED_SNAPSHOT_ROW_BATCH", "9000")
	t.Setenv("FEED_SNAPSHOT_MEMBER_BATCH", "-1")
	rows, members = dedupSnapshotInsertBatchSizes()
	if rows != 350 || members != 300 {
		t.Fatalf("invalid batch accepted: %d/%d", rows, members)
	}
}
