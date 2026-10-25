package adapters

import (
	"context"
	"strings"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type feedSearchSQLRecorder struct {
	logger.Interface
	statements []string
}

func (r *feedSearchSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.statements = append(r.statements, statement)
}

func TestFeedShortSearchExecutionMatchesSubstringAndVisibility(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, migrations.RunFeedShortSearchIndexes(db))
	for _, query := range []string{"搜索", "中国", "aB", "😀中", "_%", `\中`, "e\u0301", "İA"} {
		t.Run(query, func(t *testing.T) {
			var expected []string
			for _, state := range []string{"public", "hidden", "internal", "deleted_source", "deleted_item"} {
				source := model.FeedSource{SourceType: "external_rss", Hidden: state == "hidden", Hash: query + state}
				if state == "internal" {
					source.SourceType = "internal"
				}
				require.NoError(t, db.Create(&source).Error)
				for _, field := range []string{"title", "summary", "author"} {
					item := model.FeedItem{FeedSourceID: source.ID, GUID: field, Title: "unrelated", PublishedAt: time.Now()}
					require.NoError(t, db.Create(&item).Error)
					require.NoError(t, db.Model(&item).Update(field, "prefix"+query+"suffix").Error)
					if state == "public" {
						expected = append(expected, item.ID.String())
					}
					if state == "deleted_item" {
						require.NoError(t, db.Delete(&item).Error)
					}
				}
				if state == "deleted_source" {
					require.NoError(t, db.Delete(&source).Error)
				}
			}
			provider := NewPublicProvider(db, "feed", "RSS")
			for _, sort := range []string{"relevance", "recent"} {
				req := search.ProviderSearchRequest{Scope: search.ScopePublic, Type: "article", Query: query, Sort: sort, Limit: 2, ExactTotal: true}
				first, err := provider.Search(context.Background(), search.Viewer{}, req)
				require.NoError(t, err)
				require.EqualValues(t, 3, *first.Total)
				req.Offset = 2
				second, err := provider.Search(context.Background(), search.Viewer{}, req)
				require.NoError(t, err)
				require.EqualValues(t, 3, *second.Total)
				var ids []string
				for _, item := range append(first.Items, second.Items...) {
					ids = append(ids, item.ID)
				}
				require.ElementsMatch(t, expected, ids)
			}
		})
	}
}

func TestFeedShortSearchActualCountAndListUseBigramIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	source := model.FeedSource{SourceType: "external_rss", Hash: "execution-plan"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Exec(`INSERT INTO feed_items (id,feed_source_id,guid,title,summary,author)
		SELECT gen_random_uuid(),?,n::text,md5(n::text),md5(n::text),md5(n::text)
		FROM generate_series(1,5000) n`, source.ID).Error)
	// 同时保留旧索引，确保不是因为测试库缺旧索引才选中双字索引。
	require.NoError(t, migrations.RunFeedShortSearchIndexes(db))
	require.NoError(t, db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public").Error)
	for _, field := range []string{"title", "summary", "author"} {
		require.NoError(t, db.Exec("CREATE INDEX ON feed_items USING gin(lower("+field+") public.gin_trgm_ops) WHERE deleted_at IS NULL").Error)
	}
	require.NoError(t, db.Exec("ANALYZE feed_items").Error)
	for _, query := range []string{"搜索", "中国", "aB", "😀中"} {
		t.Run(query, func(t *testing.T) {
			recorder := &feedSearchSQLRecorder{Interface: logger.Discard}
			provider := NewPublicProvider(db.Session(&gorm.Session{Logger: recorder}), "feed", "RSS")
			_, err := provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{
				Scope: search.ScopePublic, Type: "article", Query: query, Sort: "relevance", Limit: 20, ExactTotal: true,
			})
			require.NoError(t, err)
			require.Len(t, recorder.statements, 2)
			for _, statement := range recorder.statements {
				var plan []string
				require.NoError(t, db.Raw("EXPLAIN "+statement).Scan(&plan).Error)
				require.Contains(t, strings.Join(plan, "\n"), "Bitmap Index Scan on idx_feed_items_search_bigrams_v1")
			}
		})
	}
}
