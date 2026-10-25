package migrations

import "gorm.io/gorm"

// RunFeedShortSearchIndexes 保留原子串语义，仅为两字查询提供候选过滤。
// 不修改函数定义来升级索引语义；需要调整时应新增版本并重建对应索引。
func RunFeedShortSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	for _, statement := range feedShortSearchStatements() {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func feedShortSearchStatements() []string {
	// 一次拆分字符再配对，避免逐位置 substring 反复遍历长 UTF-8 摘要。
	return []string{
		`CREATE OR REPLACE FUNCTION public.atoman_feed_search_bigrams_v1(title text, summary text, author text)
		RETURNS text[] LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $fn$
			SELECT ARRAY(
				SELECT DISTINCT token FROM (
					SELECT ch || lead(ch) OVER (PARTITION BY field_no ORDER BY pos) AS token
					FROM unnest(ARRAY[lower(title), lower(summary), lower(author)])
						WITH ORDINALITY AS fields(value, field_no)
					CROSS JOIN LATERAL unnest(string_to_array(value, NULL))
						WITH ORDINALITY AS chars(ch, pos)
				) pairs WHERE token IS NOT NULL ORDER BY token
			)
		$fn$`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_search_bigrams_v1
			ON feed_items USING GIN (public.atoman_feed_search_bigrams_v1(title, summary, author))
			WHERE deleted_at IS NULL`,
	}
}
