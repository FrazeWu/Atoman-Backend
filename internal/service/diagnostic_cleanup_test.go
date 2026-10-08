package service

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRecordingDiagnosticDoesNotRunRetentionCleanup(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSourceDiagnostic{})
	old := model.FeedSourceDiagnostic{Base: model.Base{CreatedAt: time.Now().Add(-100 * 24 * time.Hour)}, FeedSourceID: uuid.New(), Kind: "old"}
	require.NoError(t, db.Create(&old).Error)
	require.NoError(t, recordFeedSourceDiagnostic(db, old.FeedSourceID, nil, "new", "", "", 0, nil))
	var count int64
	require.NoError(t, db.Unscoped().Model(&model.FeedSourceDiagnostic{}).Count(&count).Error)
	require.Equal(t, int64(2), count, "记录诊断不应同步扫描并清理旧数据")
}

func TestDiagnosticCleanupKeepsRecentAndDeletesOnlyOneBatch(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSourceDiagnostic{})
	now := time.Now().UTC()
	entries := make([]model.FeedSourceDiagnostic, 1002)
	for i := range entries {
		entries[i] = model.FeedSourceDiagnostic{Base: model.Base{CreatedAt: now.Add(-100 * 24 * time.Hour)}, FeedSourceID: uuid.New(), Kind: "old"}
	}
	entries[1001].CreatedAt = now
	require.NoError(t, db.CreateInBatches(&entries, 100).Error)
	require.NoError(t, cleanupFeedSourceDiagnostics(db, now))
	var count int64
	require.NoError(t, db.Unscoped().Model(&model.FeedSourceDiagnostic{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.First(&model.FeedSourceDiagnostic{}, "id = ?", entries[1001].ID).Error)
}
