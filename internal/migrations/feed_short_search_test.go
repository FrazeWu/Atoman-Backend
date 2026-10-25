package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
)

func TestFeedShortSearchIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, RunFeedShortSearchIndexes(db))
	require.NoError(t, RunFeedShortSearchIndexes(db))
	// NULL、单字及字段边界不能构造出虚假的相邻双字。
	var grams []string
	require.NoError(t, db.Raw(`SELECT unnest(public.atoman_feed_search_bigrams_v1('搜', '索', NULL))`).Scan(&grams).Error)
	require.Empty(t, grams)
	require.NoError(t, db.Raw(`SELECT unnest(public.atoman_feed_search_bigrams_v1('AB中文中文', '中文', NULL))`).Scan(&grams).Error)
	require.ElementsMatch(t, []string{"ab", "b中", "中文", "文中"}, grams)
	source := model.FeedSource{SourceType: "external_rss", Hash: "short-index"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Exec(`INSERT INTO feed_items (id, feed_source_id, guid, title, summary, author)
		SELECT gen_random_uuid(), ?, n::text, md5(n::text), md5((n+1)::text), md5((n+2)::text)
		FROM generate_series(1, 5000) n`, source.ID).Error)
	for _, text := range []string{"搜索", "中A", "aB", "😀中", "_%", `\中`} {
		for _, field := range []string{"title", "summary", "author"} {
			item := model.FeedItem{FeedSourceID: source.ID, GUID: text + field, Title: "unrelated"}
			require.NoError(t, db.Create(&item).Error)
			require.NoError(t, db.Model(&item).Update(field, "prefix"+text+"suffix").Error)
		}
		var same bool
		// 候选过滤前后必须得到完全相同的 ID 集合，包括 NULL 和特殊符号。
		pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text) + "%"
		require.NoError(t, db.Raw(`WITH expected AS (
			SELECT id FROM feed_items WHERE deleted_at IS NULL AND
			(lower(title) LIKE lower(?) OR lower(summary) LIKE lower(?) OR lower(author) LIKE lower(?))
		), actual AS (
			SELECT id FROM feed_items WHERE deleted_at IS NULL AND
			(lower(title) LIKE lower(?) OR lower(summary) LIKE lower(?) OR lower(author) LIKE lower(?))
			AND public.atoman_feed_search_bigrams_v1(title, summary, author)
			@> public.atoman_feed_search_bigrams_v1(?, '', '')
		) SELECT NOT EXISTS ((SELECT id FROM expected EXCEPT SELECT id FROM actual)
			UNION ALL (SELECT id FROM actual EXCEPT SELECT id FROM expected))`,
			pattern, pattern, pattern, pattern, pattern, pattern, text).Scan(&same).Error)
		require.True(t, same, text)
	}
	require.NoError(t, db.Exec("VACUUM ANALYZE feed_items").Error)
	var plan []string
	require.NoError(t, db.Raw(`EXPLAIN SELECT id FROM feed_items WHERE deleted_at IS NULL AND
		public.atoman_feed_search_bigrams_v1(title, summary, author)
		@> public.atoman_feed_search_bigrams_v1('搜索', '', '')`).Scan(&plan).Error)
	require.Contains(t, strings.Join(plan, "\n"), "Bitmap Index Scan on idx_feed_items_search_bigrams_v1")
}
