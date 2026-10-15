package books

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCreatePublicBookCreatesVisibleWorkEditionAndSeparatePeople(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPublisher{}, &model.BookProducer{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.BookPostLink{}, &model.AuditLog{}, &model.UserBookImport{}, &model.UserBookAsset{}, &model.PublishedBookAsset{})
	user := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	service := NewService(db)
	bookImport := model.UserBookImport{UserID: user.ID, Title: "上传文件名", Format: "epub", Status: model.BookImportStatusScanning}
	require.NoError(t, db.Create(&bookImport).Error)
	sourceAsset := model.UserBookAsset{ImportID: bookImport.ID, UserID: user.ID, OriginalFilename: "book.epub", ContentType: "application/epub+zip", Format: "epub", SizeBytes: 100, SHA256: "source-hash", ObjectKey: "private/book.epub", ProcessingStatus: model.BookAssetStatusScanning}
	require.NoError(t, db.Create(&sourceAsset).Error)
	published := model.PublishedBookAsset{SourceAssetID: sourceAsset.ID, Format: "epub", ObjectKey: "public/book.epub", SHA256: "public-hash", Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&published).Error)
	input := CreatePublicBookInput{ImportID: bookImport.ID.String(), Title: "书名", Language: "中文", Authors: []string{"作者甲", "作者乙", "作者甲"}, SourceURL: "https://example.test/book", PublicDistributionConfirmed: true}
	input.Edition.Publisher = "Publisher Name"
	input.Edition.Producer = "Producer Name"
	input.Edition.ISBN13 = "9787111000001"
	input.Edition.PublishedDate = "2026-10-13"

	created, err := service.CreatePublicBook(context.Background(), user, input)
	require.NoError(t, err)
	require.Equal(t, "书名", created.Title)
	require.Equal(t, model.BookLifecycleStatusActive, created.LifecycleStatus)
	require.Len(t, created.Editions, 1)
	require.Equal(t, "Publisher Name", created.Editions[0].Publisher)
	require.Equal(t, "Producer Name", created.Editions[0].Producer)
	require.Len(t, created.Authors, 2)
	require.Equal(t, []string{"作者甲", "作者乙"}, []string{created.Authors[0].Name, created.Authors[1].Name})
	var linkedImport model.UserBookImport
	require.NoError(t, db.First(&linkedImport, "id = ?", bookImport.ID).Error)
	require.NotNil(t, linkedImport.WorkID)
	require.Equal(t, created.ID, linkedImport.WorkID.String())
	require.NotNil(t, linkedImport.EditionID)
	var linkedAsset model.PublishedBookAsset
	require.NoError(t, db.First(&linkedAsset, "id = ?", published.ID).Error)
	require.NotNil(t, linkedAsset.WorkID)
	require.NotNil(t, linkedAsset.EditionID)
	require.Equal(t, created.ID, linkedAsset.WorkID.String())
	var sourceCount int64
	require.NoError(t, db.Model(&model.BookSource{}).Count(&sourceCount).Error)
	require.EqualValues(t, 2, sourceCount)
	var auditLog model.AuditLog
	require.NoError(t, db.Where("action = ? AND entity_id = ?", "books.work.created", created.ID).First(&auditLog).Error)
	require.Contains(t, auditLog.Metadata, `"public_distribution_confirmed":true`)

	searchResult, total, err := service.SearchPublicCatalog(context.Background(), "书名", 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, searchResult, 1)
}

func TestCreatePublicBookRequiresPublicDistributionConfirmation(t *testing.T) {
	user := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	_, err := NewService(nil).CreatePublicBook(context.Background(), user, CreatePublicBookInput{Title: "书名"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "permission to share this book publicly")
}
