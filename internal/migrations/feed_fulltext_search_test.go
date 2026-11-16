package migrations

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFeedFullTextSearchIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, RunFeedFullTextSearchIndexes(db))
	require.NoError(t, RunFeedFullTextSearchIndexes(db))

	var plain string
	require.NoError(t, db.Raw(
		`SELECT public.atoman_feed_search_document_v1(?, ?, ?, ?, ?, ?)`,
		"Title", "summary", "", "<p>Body Phrase</p>", "", "",
	).Scan(&plain).Error)
	require.Contains(t, plain, "body phrase")
	require.NotContains(t, plain, "<p>")

	var indexName string
	require.NoError(t, db.Raw(`SELECT indexname FROM pg_indexes WHERE indexname = 'idx_feed_items_search_document_trgm_v1'`).Scan(&indexName).Error)
	require.Equal(t, "idx_feed_items_search_document_trgm_v1", indexName)
	var valid bool
	require.NoError(t, db.Raw(`SELECT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_feed_items_search_document_trgm_v1'`).Scan(&valid).Error)
	require.True(t, valid)

	source := model.FeedSource{SourceType: "external_rss", Hash: "fulltext-index", Title: "Indexed"}
	require.NoError(t, db.Create(&source).Error)
	match := model.FeedItem{FeedSourceID: source.ID, GUID: "match", Title: "other", FullTextHTML: "<p>Body Phrase inside markup</p>"}
	miss := model.FeedItem{FeedSourceID: source.ID, GUID: "miss", Title: "other", FullTextHTML: "<p>unrelated</p>"}
	require.NoError(t, db.Create(&match).Error)
	require.NoError(t, db.Create(&miss).Error)
	var ids []string
	require.NoError(t, db.Raw(`SELECT id::text FROM feed_items
		WHERE deleted_at IS NULL
		  AND public.atoman_feed_search_document_v1(title, summary, author, reader_html, full_text_html, feed_content_html) LIKE ?`,
		"%body phrase%").Scan(&ids).Error)
	require.Equal(t, []string{match.ID.String()}, ids)
}

func TestFeedFullTextSearchStatementsAreConcurrentAndIdempotent(t *testing.T) {
	statements := feedFullTextSearchStatements("public.gin_trgm_ops")
	require.Len(t, statements, 2)
	require.Contains(t, statements[0], "CREATE OR REPLACE FUNCTION public.atoman_feed_search_document_v1")
	require.Contains(t, statements[1], "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_search_document_trgm_v1")
	require.Contains(t, statements[1], "public.gin_trgm_ops")
	require.NotContains(t, feedSearchDocumentIndexSQL("public.gin_trgm_ops", false), "CONCURRENTLY")
}

func TestFeedFullTextSearchIndexInsideTransaction(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.True(t, gormSessionInTransaction(tx))
		return RunFeedFullTextSearchIndexes(tx)
	}))

	var valid bool
	require.NoError(t, db.Raw(`SELECT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_feed_items_search_document_trgm_v1'`).Scan(&valid).Error)
	require.True(t, valid)
}

func TestFeedFullTextSearchSkipsWhenExtensionPrivilegeDenied(t *testing.T) {
	dsn := testdb.TestPostgresDSN()
	require.NotEmpty(t, dsn)

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminSQL.Close() })

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	dbName := "feed_ft_priv_" + suffix
	role := "feed_ft_role_" + suffix
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP DATABASE IF EXISTS " + dbName + " WITH (FORCE)").Error
		_ = admin.Exec("DROP ROLE IF EXISTS " + role).Error
	})

	owner, err := gorm.Open(postgres.Open(rewritePostgresDSN(t, dsn, dbName, "", "")), &gorm.Config{})
	require.NoError(t, err)
	ownerSQL, err := owner.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ownerSQL.Close() })

	require.NoError(t, owner.Exec(fmt.Sprintf(
		`CREATE ROLE %s LOGIN PASSWORD 'feed-ft-test' NOSUPERUSER NOCREATEDB NOCREATEROLE`, role,
	)).Error)
	require.NoError(t, owner.Exec(fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, dbName, role)).Error)
	require.NoError(t, owner.Exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role)).Error)

	limited, err := gorm.Open(postgres.Open(rewritePostgresDSN(t, dsn, dbName, role, "feed-ft-test")), &gorm.Config{})
	require.NoError(t, err)
	limitedSQL, err := limited.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = limitedSQL.Close() })

	extensionErr := limited.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`).Error
	require.Error(t, extensionErr)
	require.True(t, postgresInsufficientPrivilege(extensionErr), extensionErr.Error())

	require.NoError(t, RunFeedFullTextSearchIndexes(limited))

	var functionSchema string
	require.NoError(t, owner.Raw(`SELECT COALESCE(n.nspname, '')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.proname = 'atoman_feed_search_document_v1'`).Scan(&functionSchema).Error)
	require.Empty(t, functionSchema)
}

func TestPostgresInsufficientPrivilege(t *testing.T) {
	require.True(t, postgresInsufficientPrivilege(&pgconn.PgError{Code: "42501", Message: "permission denied"}))
	require.True(t, postgresInsufficientPrivilege(&pq.Error{Code: "42501"}))
	require.False(t, postgresInsufficientPrivilege(&pgconn.PgError{Code: "42P01", Message: "relation does not exist"}))
	require.False(t, postgresInsufficientPrivilege(nil))
}

func rewritePostgresDSN(t *testing.T, dsn, database, user, password string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	if user != "" {
		parsed.User = url.UserPassword(user, password)
	}
	parsed.Path = "/" + database
	return parsed.String()
}
