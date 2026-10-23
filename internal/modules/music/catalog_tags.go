package music

import (
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 仅扩展标签分类树；专辑与歌曲的正式关联彼此独立。
func musicTagEntitiesSQL(viewer *authctx.CurrentUser) (string, []any) {
	songVisibility, songArgs := musicEntryVisibilityCondition("songs", "uploaded_by", viewer, false)
	albumVisibility, albumArgs := musicEntryVisibilityCondition("albums", "uploaded_by", viewer, false)
	query := `SELECT assignment.tag_id, 'song' AS entity_type, songs.id AS entity_id
		FROM music_tag_assignments assignment JOIN "Songs" songs ON songs.id = assignment.entity_id
		WHERE assignment.entity_type = 'song' AND assignment.deleted_at IS NULL
		AND assignment.tag_id IN (SELECT id FROM descendants) AND ` + musicTagSearchEligibleSQL() + ` AND ` + songVisibility + `
		UNION ALL
		SELECT assignment.tag_id, 'album' AS entity_type, albums.id AS entity_id
		FROM music_tag_assignments assignment JOIN "Albums" albums ON albums.id = assignment.entity_id
		WHERE assignment.entity_type = 'album' AND assignment.deleted_at IS NULL
		AND assignment.tag_id IN (SELECT id FROM descendants) AND ` + musicTagSearchEligibleSQL() + ` AND ` + albumVisibility
	args := append([]any{}, songArgs...)
	args = append(args, albumArgs...)
	return query, args
}

const musicTagDescendantsSQL = `WITH RECURSIVE redirects AS (
	SELECT id AS root_id, id, canonical_tag_id FROM music_tags WHERE id IN ? AND deleted_at IS NULL
	UNION ALL SELECT r.root_id,t.id,t.canonical_tag_id FROM redirects r JOIN music_tags t ON t.id=r.canonical_tag_id WHERE t.deleted_at IS NULL
), descendants AS (
	SELECT root_id,id FROM redirects WHERE canonical_tag_id IS NULL
	UNION ALL
	SELECT parent.root_id, child.id FROM music_tags child
	JOIN descendants parent ON child.parent_id = parent.id WHERE child.deleted_at IS NULL AND child.canonical_tag_id IS NULL
)`

func scopeMusicTagEntries(db *gorm.DB, entityType, table string, tagID uuid.UUID, viewer *authctx.CurrentUser) *gorm.DB {
	entitiesSQL, entityArgs := musicTagEntitiesSQL(viewer)
	args := []any{[]uuid.UUID{tagID}}
	args = append(args, entityArgs...)
	args = append(args, entityType)
	return db.Where(table+`.id IN (`+musicTagDescendantsSQL+`
		SELECT entities.entity_id FROM (`+entitiesSQL+`) entities
		JOIN descendants ON descendants.id = entities.tag_id WHERE entities.entity_type = ?
	)`, args...)
}

type musicTagContentCount struct {
	TagID      uuid.UUID
	SongCount  int64
	AlbumCount int64
}

func (s *Service) musicTagContentCounts(tagIDs []uuid.UUID, viewer *authctx.CurrentUser) (map[uuid.UUID]musicTagContentCount, error) {
	entitiesSQL, entityArgs := musicTagEntitiesSQL(viewer)
	args := append([]any{tagIDs}, entityArgs...)
	var rows []musicTagContentCount
	err := s.db.Raw(musicTagDescendantsSQL+`
		SELECT descendants.root_id AS tag_id,
		COUNT(DISTINCT entities.entity_id) FILTER (WHERE entities.entity_type = 'song') AS song_count,
		COUNT(DISTINCT entities.entity_id) FILTER (WHERE entities.entity_type = 'album') AS album_count
		FROM descendants JOIN (`+entitiesSQL+`) entities ON entities.tag_id = descendants.id
		GROUP BY descendants.root_id`, args...).Scan(&rows).Error
	counts := make(map[uuid.UUID]musicTagContentCount, len(rows))
	for _, row := range rows {
		counts[row.TagID] = row
	}
	return counts, err
}

func firstMusicTagViewer(viewers []*authctx.CurrentUser) *authctx.CurrentUser {
	if len(viewers) > 0 {
		return viewers[0]
	}
	return nil
}
