package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBooksV2ResourceGraphConstraints(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.BookWork{})
	require.NoError(t, RunBooksV2Migration(db))
	require.NoError(t, RunBooksV2Migration(db))
	user := model.User{UUID: uuid.New(), Username: "reader", Email: "reader@example.test", Password: "hash"}
	require.NoError(t, db.Create(&user).Error)
	work := model.BookWork{Title: "无出版版本的作品", LifecycleStatus: model.BookLifecycleStatusActive}
	otherWork := model.BookWork{Title: "另一个作品", LifecycleStatus: model.BookLifecycleStatusActive}
	require.NoError(t, db.Create(&work).Error)
	require.NoError(t, db.Create(&otherWork).Error)
	asset := model.BookAsset{WorkID: work.ID, Language: "zh", Format: "epub", CreatedBy: user.UUID}
	otherAsset := model.BookAsset{WorkID: work.ID, Language: "en", Format: "pdf", CreatedBy: user.UUID}
	require.NoError(t, db.Create(&asset).Error)
	require.NoError(t, db.Create(&otherAsset).Error)
	media := model.MediaAsset{Purpose: "books.file", Key: "books/private/source", ContentType: "application/epub+zip", Size: 42}
	require.NoError(t, db.Create(&media).Error)
	revision := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: media.ID, SHA256: strings.Repeat("a", 64), OriginalFilename: "book.epub", CreatedBy: user.UUID}
	require.NoError(t, db.Create(&revision).Error)
	require.NoError(t, db.Model(&asset).Update("current_revision_id", revision.ID).Error)
	require.Error(t, db.Model(&otherAsset).Update("current_revision_id", revision.ID).Error)
	require.Error(t, db.Create(&model.BookAsset{WorkID: uuid.New(), Language: "zh", Format: "epub", CreatedBy: user.UUID}).Error)

	defaultAsset := model.BookDefaultAsset{WorkID: work.ID, Language: "zh", AssetID: asset.ID, UpdatedBy: user.UUID}
	require.NoError(t, db.Create(&defaultAsset).Error)
	require.Error(t, db.Create(&model.BookDefaultAsset{WorkID: otherWork.ID, Language: "zh", AssetID: asset.ID, UpdatedBy: user.UUID}).Error)
	require.Error(t, db.Create(&model.BookDefaultAsset{WorkID: work.ID, Language: "en", AssetID: asset.ID, UpdatedBy: user.UUID}).Error)

	selection := model.BookReadingSelection{UserID: user.UUID, WorkID: work.ID, AssetID: asset.ID, RevisionID: revision.ID}
	require.NoError(t, db.Create(&selection).Error)
	require.Error(t, db.Model(&selection).Update("asset_id", otherAsset.ID).Error)
	require.Error(t, db.Create(&model.BookReadingSelection{UserID: user.UUID, WorkID: otherWork.ID, AssetID: asset.ID, RevisionID: revision.ID}).Error)
	state := model.BookReadingState{UserID: user.UUID, RevisionID: revision.ID, CurrentPercent: .2, FurthestPercent: .8}
	require.NoError(t, db.Create(&state).Error)
	require.Error(t, db.Create(&state).Error)
	session := model.BookReadingSession{UserID: user.UUID, ClientSessionID: uuid.New(), RevisionID: revision.ID, DeviceID: "web"}
	require.NoError(t, db.Create(&session).Error)
	require.Error(t, db.Model(&session).Update("last_sequence", -1).Error)
	receipt := model.BookReadingWrite{UserID: user.UUID, RevisionID: revision.ID, RequestID: uuid.New(), SessionID: session.ID, RequestHash: strings.Repeat("a", 64), ResultJSON: `{}`}
	require.NoError(t, db.Create(&receipt).Error)
	receipt.RequestID = uuid.New()
	receipt.SessionID = uuid.New()
	require.Error(t, db.Create(&receipt).Error, "a receipt must belong to its user's revision session")
	mark := model.BookReadingMark{UserID: user.UUID, RevisionID: revision.ID, Kind: "bookmark", AnchorJSON: `{"epub_start_cfi":"epubcfi(/6/2!/4/1:0)"}`, Version: 1}
	require.NoError(t, db.Create(&mark).Error)
	require.Error(t, db.Model(&mark).Update("revision_id", uuid.New()).Error)
	require.Error(t, db.Model(&mark).Update("user_id", uuid.New()).Error)
	require.Error(t, db.Model(&mark).Update("kind", "public").Error)
	require.Error(t, db.Model(&mark).Update("version", 0).Error)
	require.Error(t, db.Model(&state).Update("current_percent", .9).Error)
	require.Error(t, db.Model(&revision).Update("sha256", strings.Repeat("b", 64)).Error)
	require.Error(t, db.Model(&revision).Update("asset_id", otherAsset.ID).Error)
	require.Error(t, db.Model(&media).Update("key", "a-different-file").Error)
	require.Error(t, db.Model(&media).Update("url", "https://cdn.example.test/file").Error)
	ordinaryMedia := model.MediaAsset{Purpose: "blog.image", Key: "blog/image", ContentType: "image/png", Size: 12}
	require.NoError(t, db.Create(&ordinaryMedia).Error)
	require.NoError(t, db.Model(&ordinaryMedia).Update("key", "blog/updated").Error)
	require.NoError(t, db.Model(&revision).Update("status", model.BookRevisionStatusReady).Error)

	// Replacing a file appends a revision without touching the old identity/state.
	media.ID = uuid.Nil
	media.Key = "books/private/replacement"
	require.NoError(t, db.Create(&media).Error)
	replacement := model.BookAssetRevision{AssetID: asset.ID, Number: 2, MediaAssetID: media.ID, SHA256: strings.Repeat("b", 64), OriginalFilename: "replacement.epub", CreatedBy: user.UUID}
	require.NoError(t, db.Create(&replacement).Error)
	require.NoError(t, db.Model(&asset).Update("current_revision_id", replacement.ID).Error)
	var storedSelection model.BookReadingSelection
	require.NoError(t, db.First(&storedSelection, "user_id = ? AND work_id = ?", user.UUID, work.ID).Error)
	require.Equal(t, revision.ID, storedSelection.RevisionID)
	var storedState model.BookReadingState
	require.NoError(t, db.First(&storedState, "user_id = ? AND revision_id = ?", user.UUID, revision.ID).Error)
	require.Equal(t, .8, storedState.FurthestPercent)
}
