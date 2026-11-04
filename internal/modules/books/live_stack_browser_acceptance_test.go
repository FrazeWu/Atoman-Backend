package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This intentionally runs only when BOOK_QA_REAL_STACK=1, against a dedicated
// disposable PostgreSQL container and MinIO instance on local loopback ports.
// The QA-only authentication endpoint and header must never be registered in
// production routers or any non-test Go binary.
func TestBookLiveStackChromeAcceptance(t *testing.T) {
	if os.Getenv("BOOK_QA_REAL_STACK") != "1" {
		t.Skip("explicit isolated QA setup required")
	}
	frontend := os.Getenv("BOOK_QA_FRONTEND_ROOT")
	require.NotEmpty(t, frontend)
	require.Equal(t, "http://127.0.0.1:19100", os.Getenv("BOOK_QA_MINIO_ENDPOINT"))
	require.NotEmpty(t, os.Getenv("BOOK_QA_S3_ACCESS_KEY"))
	require.NotEmpty(t, os.Getenv("BOOK_QA_S3_SECRET_KEY"))
	require.Contains(t, os.Getenv("TEST_POSTGRES_DSN"), ":56543/")
	t.Setenv("S3_BUCKET", "atoman-books-qa-main-20261104")
	t.Setenv("BOOK_PRIVATE_BUCKET", "atoman-books-qa-private-20261104")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Revision{}, &model.EditConflict{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPublisher{}, &model.BookProducer{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{})
	require.NoError(t, migrations.RunBooksV2Migration(db))
	ownerUser := model.User{UUID: uuid.New(), Username: "qa-book-owner", Email: "qa-book-owner@example.invalid", Password: "not-a-real-password", IsActive: true}
	otherUser := model.User{UUID: uuid.New(), Username: "qa-book-other", Email: "qa-book-other@example.invalid", Password: "not-a-real-password", IsActive: true}
	require.NoError(t, db.Create(&ownerUser).Error)
	require.NoError(t, db.Create(&otherUser).Error)
	owner := authctx.CurrentUser{ID: ownerUser.UUID, Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: otherUser.UUID, Role: authctx.RoleUser}
	awsSession, err := session.NewSession(&aws.Config{Region: aws.String("us-east-1"), Endpoint: aws.String(os.Getenv("BOOK_QA_MINIO_ENDPOINT")), Credentials: credentials.NewStaticCredentials(os.Getenv("BOOK_QA_S3_ACCESS_KEY"), os.Getenv("BOOK_QA_S3_SECRET_KEY"), ""), S3ForcePathStyle: aws.Bool(true), DisableSSL: aws.Bool(true), MaxRetries: aws.Int(1)})
	require.NoError(t, err)
	client := s3.New(awsSession)
	mainBucket := "atoman-books-qa-main-20261104"
	privateBucket := "atoman-books-qa-private-20261104"
	for _, bucket := range []string{mainBucket, privateBucket} {
		_, err = client.CreateBucket(&s3.CreateBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
	}
	objectKey := "book/revisions/qa-actual-minio-file.txt"
	bookBody := "AToMAN QA: 这是来自独立 MinIO 的真实 TXT 文件。\n实际对象存储中的章节\n这是第二段可供阅读器渲染的内容。\n"
	_, err = client.PutObject(&s3.PutObjectInput{Bucket: aws.String(privateBucket), Key: aws.String(objectKey), Body: strings.NewReader(bookBody), ContentType: aws.String("text/plain; charset=utf-8")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(privateBucket), Key: aws.String(objectKey)})
		for _, b := range []string{privateBucket, mainBucket} {
			_, _ = client.DeleteBucket(&s3.DeleteBucketInput{Bucket: aws.String(b)})
		}
	})
	service := NewService(db).WithS3(client)
	ctx := context.Background()
	work, err := service.CreateBookWork(ctx, owner, CreateBookWorkInput{Title: "真实联调验收作品", Authors: []string{"测试作者"}})
	require.NoError(t, err)
	workID := uuid.MustParse(work.ID)
	asset, err := service.CreateBookResource(ctx, owner, workID, CreateBookResourceInput{Language: "zh", Format: "txt"})
	require.NoError(t, err)
	media := model.MediaAsset{Key: objectKey, Purpose: "books.file", ContentType: "text/plain; charset=utf-8", Size: int64(len([]byte(bookBody)))}
	require.NoError(t, db.Create(&media).Error)
	sum := sha256.Sum256([]byte(bookBody))
	rev := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: media.ID, SHA256: hex.EncodeToString(sum[:]), OriginalFilename: "real-qa-book.txt", Status: "ready", CreatedBy: owner.ID}
	require.NoError(t, db.Create(&rev).Error)
	require.NoError(t, db.Model(&asset).Update("current_revision_id", rev.ID).Error)
	// The default is private even for a catalog-visible work. Explicitly opt
	// this synthetic, rights-owned fixture into publicly readable content.
	require.Error(t, service.CanReadBookRevision(ctx, uuid.Nil, rev.ID))
	_, err = service.SetBookFileVisibility(ctx, owner, rev.ID, true, true)
	require.NoError(t, err)
	require.NoError(t, service.CanReadBookRevision(ctx, uuid.Nil, rev.ID))
	mark, err := service.SaveBookReadingMark(ctx, owner, rev.ID, uuid.New(), txtBookHighlight(0, 7, "来自真实对象存储的划线"))
	require.NoError(t, err)
	trashed, err := service.SaveBookReadingMark(ctx, owner, rev.ID, uuid.New(), txtBookHighlight(8, 18, "等待浏览器恢复的私人划线"))
	require.NoError(t, err)
	_, err = service.DeleteBookReadingMark(ctx, owner, rev.ID, trashed.ID, trashed.Version)
	require.NoError(t, err)
	note, err := service.SaveBookPrivateNote(ctx, owner, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Title: "真实笔记", Body: "已选入合集的公开部分"})
	require.NoError(t, err)
	secret, err := service.SaveBookPrivateNote(ctx, owner, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Title: "绝不公开的私有笔记", Body: "不允许泄漏的原始秘密"})
	require.NoError(t, err)
	_ = secret
	publicNote, err := service.SaveBookPrivateNote(ctx, owner, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Title: "公开笔记", Body: "仅完全公开合集能被索引"})
	require.NoError(t, err)
	makePublished := func(title string, id uuid.UUID, visibility string) NoteCollectionDTO {
		t.Helper()
		collectionID := uuid.New()
		draft := NoteCollectionDraft{Title: title, Description: "独立测试快照", Items: []BookNoteReference{{Kind: "chapter_note", ID: id}}}
		saved, e := service.SaveNoteCollection(ctx, owner, collectionID, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
		require.NoError(t, e)
		pub, e := service.PublishNoteCollection(ctx, owner, collectionID, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: visibility, ConfirmPublish: true})
		require.NoError(t, e)
		return pub
	}
	linked := makePublished("仅持有链接可访问的合集", note.ID, "link")
	indexed := makePublished("可以公开发现的合集", publicNote.ID, "public")
	draftID := uuid.New()
	draft := NoteCollectionDraft{Title: "浏览器准备发布的私人草稿", Description: "只可本人修改", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}, {Kind: "highlight", ID: mark.ID}}}
	_, err = service.SaveNoteCollection(ctx, owner, draftID, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Book-QA-Actor") {
		case "owner":
			authctx.SetCurrentUser(c, owner)
		case "other":
			authctx.SetCurrentUser(c, other)
		}
		c.Next()
	})
	router.GET("/api/v1/auth/session", func(c *gin.Context) {
		var current model.User
		switch c.GetHeader("X-Book-QA-Actor") {
		case "owner":
			current = ownerUser
		case "other":
			current = otherUser
		default:
			c.Status(http.StatusNoContent)
			return
		}
		c.JSON(http.StatusOK, gin.H{"csrf_token": "qa-token-only-not-production", "user": gin.H{"uuid": current.UUID, "id": current.ID, "username": current.Username, "email": current.Email, "role": "user", "is_active": true}})
	})
	router.GET("/api/v1/books/__qa/fixture", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"work_id": work.ID, "asset_id": asset.ID.String(), "revision_id": rev.ID.String(), "collection_id": draftID.String(), "note_id": note.ID.String(), "highlight_id": mark.ID.String(), "trashed_highlight_id": trashed.ID.String(), "linked_share_path": linked.SharePath, "public_share_path": indexed.SharePath, "private_bucket": privateBucket})
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(router)
	server.Listener = listener
	server.Start()
	defer server.Close()
	t.Logf("QA browser API listens on ephemeral loopback: %s (independent test schema and bucket)", server.URL)
	playwright := filepath.Join(frontend, "node_modules", ".bin", "playwright")
	browserCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(browserCtx, playwright, "test", "tests/e2e-smoke/book-notes-real-stack.spec.ts", "--config=playwright.smoke.config.ts", "--reporter=line")
	cmd.Dir = frontend
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_SMOKE_PORT=5192", "VITE_API_URL=/api/v1", "VITE_DEV_PROXY_TARGET="+server.URL, "VITE_DEV_OBJECT_STORAGE_PROXY_TARGET="+os.Getenv("BOOK_QA_MINIO_ENDPOINT"), "BOOK_QA_REAL_BROWSER=1")
	output, err := cmd.CombinedOutput()
	t.Logf("Chrome true-backend E2E log:\n%s", string(output))
	require.NoError(t, err, "real-stack Chromium acceptance failed")
}
