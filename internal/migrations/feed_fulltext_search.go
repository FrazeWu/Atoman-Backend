package migrations

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// feedSearchDocumentIndex is the v2 trigram index over title + summary + author.
// v1 covered title + summary + author + reader_html + full_text_html +
// feed_content_html; benchmarking on the production-shaped feed_items table
// (406k rows, ~5 GB) showed v1's GIN maintenance forced TOAST decompression
// of reader_html / full_text_html / feed_content_html on every RSS cron UPDATE,
// adding ~2.5x write latency (1.35 s -> 3.33 s per 300 rows) and producing a
// 1.38 GB index. v2 drops the three large HTML TOAST columns; RSS cron never
// touches reader_html or full_text_html, and feed_content_html updates no
// longer re-evaluate the document function. Body-content search now falls
// back to the slow non-indexed LIKE path in subscribed_search.go.
const feedSearchDocumentIndex = "idx_feed_items_search_document_trgm_v2"

const feedSearchDocumentFunctionSQL = `CREATE OR REPLACE FUNCTION public.atoman_feed_search_document_v2(
	title text, summary text, author text
) RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $fn$
	SELECT lower(concat_ws(' ',
		COALESCE(title, ''),
		COALESCE(summary, ''),
		COALESCE(author, '')
	))
$fn$`

// RunFeedFullTextSearchIndexes adds the feed-item document used by subscribed
// full-text search. It does not enable the dedup snapshot read path.
//
// Startup migrations run through db.Exec on the root connection, outside a
// transaction, so the trigram index is built with CREATE INDEX CONCURRENTLY.
// A caller already inside a transaction gets a regular CREATE INDEX instead:
// PostgreSQL rejects CONCURRENTLY in a transaction block, and a failed
// statement aborts that transaction. CREATE EXTENSION IF NOT EXISTS pg_trgm
// is attempted first. When the role lacks privilege, an extension a superuser
// already installed is still used; if it is not installed, the index is
// skipped and search falls back to column scans. The document function is
// likewise skipped, rather than failing startup, when this role cannot create
// it. Index creation is idempotent, including a previous CONCURRENTLY attempt
// that left an invalid index behind.
func RunFeedFullTextSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	if err := db.Exec(feedSearchDocumentFunctionSQL).Error; err != nil {
		if postgresInsufficientPrivilege(err) {
			log.Printf("feed full-text search: skipping document function, role lacks privilege: %v", err)
			return nil
		}
		return fmt.Errorf("create feed full-text search document: %w", err)
	}

	operatorClass, err := feedSearchTrgmOperatorClass(db)
	if err != nil {
		if postgresInsufficientPrivilege(err) {
			log.Printf("feed full-text search: skipping trigram index, pg_trgm is unavailable to this role: %v", err)
			return nil
		}
		return err
	}

	concurrent := !gormSessionInTransaction(db)
	if err := ensureFeedSearchDocumentIndex(db, operatorClass, concurrent); err != nil {
		return fmt.Errorf("create feed full-text search index: %w", err)
	}
	return nil
}

func feedFullTextSearchStatements(operatorClass string) []string {
	return []string{
		feedSearchDocumentFunctionSQL,
		feedSearchDocumentIndexSQL(operatorClass, true),
	}
}

func feedSearchDocumentIndexSQL(operatorClass string, concurrent bool) string {
	concurrently := ""
	if concurrent {
		concurrently = "CONCURRENTLY "
	}
	return fmt.Sprintf(`CREATE INDEX %sIF NOT EXISTS %s
		ON feed_items USING GIN (public.atoman_feed_search_document_v2(title, summary, author) %s)
		WHERE deleted_at IS NULL`, concurrently, feedSearchDocumentIndex, operatorClass)
}

// feedSearchTrgmOperatorClass enables pg_trgm when this role is allowed to.
// A privilege error still returns the operator class when the extension is
// already installed. The returned error is a privilege error only when the
// extension cannot be used at all.
func feedSearchTrgmOperatorClass(db *gorm.DB) (string, error) {
	operatorClass, err := pgTrgmOperatorClass(db)
	if err == nil {
		return operatorClass, nil
	}
	if !postgresInsufficientPrivilege(err) {
		return "", err
	}
	existing, lookupErr := existingPgTrgmOperatorClass(db)
	if lookupErr != nil {
		return "", lookupErr
	}
	if existing != "" {
		return existing, nil
	}
	return "", err
}

func existingPgTrgmOperatorClass(db *gorm.DB) (string, error) {
	var extensionSchema string
	if err := db.Raw(`SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_trgm'`).Scan(&extensionSchema).Error; err != nil {
		return "", fmt.Errorf("resolve pg_trgm schema: %w", err)
	}
	if extensionSchema == "" {
		return "", nil
	}
	return quotePostgresIdentifier(extensionSchema) + ".gin_trgm_ops", nil
}

func ensureFeedSearchDocumentIndex(db *gorm.DB, operatorClass string, concurrent bool) error {
	invalid, err := feedSearchDocumentIndexInvalid(db)
	if err != nil {
		return err
	}
	if invalid {
		drop := "DROP INDEX IF EXISTS " + feedSearchDocumentIndex
		if concurrent {
			drop = "DROP INDEX CONCURRENTLY IF EXISTS " + feedSearchDocumentIndex
		}
		if err := db.Exec(drop).Error; err != nil {
			return err
		}
	}
	return db.Exec(feedSearchDocumentIndexSQL(operatorClass, concurrent)).Error
}

func feedSearchDocumentIndexInvalid(db *gorm.DB) (bool, error) {
	var invalid bool
	err := db.Raw(`SELECT EXISTS (
		SELECT 1
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE c.relname = ?
		  AND t.relname = 'feed_items'
		  AND n.nspname = current_schema()
		  AND NOT i.indisvalid
	)`, feedSearchDocumentIndex).Scan(&invalid).Error
	return invalid, err
}

func gormSessionInTransaction(db *gorm.DB) bool {
	if db == nil || db.Statement == nil {
		return false
	}
	_, ok := db.Statement.ConnPool.(*sql.Tx)
	return ok
}

func postgresInsufficientPrivilege(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "42501" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlstate 42501") ||
		strings.Contains(message, "insufficient privilege") ||
		strings.Contains(message, "permission denied")
}
