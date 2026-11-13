package books

import (
	"context"
	"strings"
)

type FacetCountItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type BookWorkFacetsResult struct {
	Languages []FacetCountItem `json:"languages"`
	Decades   []FacetCountItem `json:"decades"`
	Ratings   []FacetCountItem `json:"ratings"`
}

var languageLabels = map[string]string{
	"zh":    "中文",
	"zh-CN": "简体中文",
	"zh-TW": "繁体中文",
	"en":    "英语",
	"ja":    "日语",
	"fr":    "法语",
	"de":    "德语",
	"ru":    "俄语",
	"es":    "西班牙语",
	"it":    "意大利语",
	"la":    "拉丁语",
}

func (s *Service) GetBookWorkFacets(ctx context.Context, query string) (BookWorkFacetsResult, error) {
	out := BookWorkFacetsResult{
		Languages: []FacetCountItem{},
		Decades:   []FacetCountItem{},
		Ratings:   []FacetCountItem{},
	}

	query = strings.TrimSpace(query)
	base := s.buildPublicCatalogBaseQuery(ctx, query, true)

	// 1. Languages
	var langRows []struct {
		Lang  string `gorm:"column:lang"`
		Count int64  `gorm:"column:count"`
	}
	if err := s.db.WithContext(ctx).Table("(?) AS b", base.Select("book_works.language")).
		Select("COALESCE(NULLIF(b.language, ''), 'other') AS lang, count(*) AS count").
		Group("lang").Order("count DESC").Limit(8).Scan(&langRows).Error; err != nil {
		return out, err
	}
	for _, row := range langRows {
		label, ok := languageLabels[row.Lang]
		if !ok {
			if row.Lang == "other" {
				label = "其他语言"
			} else {
				label = strings.ToUpper(row.Lang)
			}
		}
		out.Languages = append(out.Languages, FacetCountItem{
			Key:   row.Lang,
			Label: label,
			Count: row.Count,
		})
	}

	// 2. Decades
	var decadeRows []struct {
		Era   string `gorm:"column:era"`
		Count int64  `gorm:"column:count"`
	}
	eraSQL := `
		SELECT
			CASE
				WHEN EXTRACT(YEAR FROM e.published_date) >= 2020 THEN '2020s'
				WHEN EXTRACT(YEAR FROM e.published_date) >= 2010 THEN '2010s'
				WHEN EXTRACT(YEAR FROM e.published_date) >= 2000 THEN '2000s'
				WHEN EXTRACT(YEAR FROM e.published_date) > 0 THEN 'classic'
				ELSE 'unknown'
			END AS era,
			count(DISTINCT b.id) AS count
		FROM (?) AS b
		JOIN book_editions e ON e.work_id = b.id AND e.deleted_at IS NULL AND e.lifecycle_status = 'active' AND e.edit_status <> 'closed'
		WHERE e.published_date IS NOT NULL
		GROUP BY era
	`
	if err := s.db.WithContext(ctx).Raw(eraSQL, base.Select("book_works.id")).Scan(&decadeRows).Error; err != nil {
		return out, err
	}
	eraMap := make(map[string]int64)
	for _, r := range decadeRows {
		eraMap[r.Era] = r.Count
	}
	for _, def := range []struct {
		key, label string
	}{
		{"2020s", "2020年代"},
		{"2010s", "2010年代"},
		{"2000s", "2000年代"},
		{"classic", "2000年以前"},
	} {
		if c, exists := eraMap[def.key]; exists && c > 0 {
			out.Decades = append(out.Decades, FacetCountItem{
				Key:   def.key,
				Label: def.label,
				Count: c,
			})
		}
	}

	// 3. Ratings
	ratingsQuery := bookWeightedRatingQuery(s.db.WithContext(ctx))
	var ratingTiers []struct {
		Tier  string `gorm:"column:tier"`
		Count int64  `gorm:"column:count"`
	}
	tierSQL := `
		SELECT
			t.tier,
			count(DISTINCT b.id) AS count
		FROM (?) AS b
		JOIN (?) AS r ON r.work_id = b.id
		CROSS JOIN (VALUES ('9+', 9.0), ('8+', 8.0), ('7+', 7.0)) AS t(tier, min_score)
		WHERE r.weighted_rating_score >= t.min_score
		GROUP BY t.tier
		ORDER BY t.tier DESC
	`
	if err := s.db.WithContext(ctx).Raw(tierSQL, base.Select("book_works.id"), ratingsQuery).Scan(&ratingTiers).Error; err == nil {
		tierOrder := []struct{ key, label string }{
			{"9+", "9.0分以上"},
			{"8+", "8.0分以上"},
			{"7+", "7.0分以上"},
		}
		tierMap := make(map[string]int64)
		for _, r := range ratingTiers {
			tierMap[r.Tier] = r.Count
		}
		for _, def := range tierOrder {
			if c, exists := tierMap[def.key]; exists && c > 0 {
				out.Ratings = append(out.Ratings, FacetCountItem{
					Key:   def.key,
					Label: def.label,
					Count: c,
				})
			}
		}
	}

	return out, nil
}
