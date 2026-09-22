package music

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	musicTagEntitySong   = "song"
	musicTagEntityAlbum  = "album"
	maxMusicTagsPerItem  = 12
	maxMusicTagNameSize  = 48
	musicTagSearchLimit  = 20
	musicTagCatalogLimit = 100
	musicTagMaxDepth     = 3
)

func normalizeMusicTagName(value string) (string, error) {
	name := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	if name == "" {
		return "", apperr.BadRequest("music.invalid_tag", "tag name is required")
	}
	if utf8.RuneCountInString(name) > maxMusicTagNameSize {
		return "", apperr.BadRequest("music.invalid_tag", "tag name is too long")
	}
	return name, nil
}

func validateMusicTagKind(kind string) error {
	switch kind {
	case model.MusicTagKindMood,
		model.MusicTagKindType,
		model.MusicTagKindScene,
		model.MusicTagKindTheme,
		model.MusicTagKindInstrument:
		return nil
	default:
		return apperr.BadRequest("music.invalid_tag_kind", "tag kind must be mood, type, scene, theme, or instrument")
	}
}

func effectiveMusicTagDepth(depth int) int {
	if depth < 1 {
		return 1
	}
	return depth
}

func sameMusicTagParent(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateMusicTagParent(db *gorm.DB, kind string, parentID *uuid.UUID) (int, error) {
	if parentID == nil {
		return 1, nil
	}
	if kind != model.MusicTagKindType {
		return 0, apperr.BadRequest("music.invalid_tag_parent", "only type tags can have parent tags")
	}

	var parent model.MusicTag
	if err := db.Select("id, kind, depth").First(&parent, "id = ?", *parentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, apperr.NotFound("music.tag_parent_not_found", "Parent tag not found")
		}
		return 0, err
	}
	if parent.Kind != kind {
		return 0, apperr.BadRequest("music.invalid_tag_parent", "parent tag must use the same kind")
	}
	parentDepth := effectiveMusicTagDepth(parent.Depth)
	if parentDepth >= musicTagMaxDepth {
		return 0, apperr.Unprocessable("music.tag_depth_limit", "type tags can have at most three levels")
	}
	return parentDepth + 1, nil
}

func normalizeMusicTagQuery(rawQuery string) (string, error) {
	query := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(rawQuery)), " "))
	if utf8.RuneCountInString(query) > maxMusicTagNameSize {
		return "", apperr.BadRequest("music.invalid_tag_query", "tag search query is too long")
	}
	return query, nil
}

func musicTagOptionDTO(tag model.MusicTag, assignmentCount, childCount int64) MusicTagOptionDTO {
	return MusicTagOptionDTO{
		ID:              tag.ID,
		Name:            tag.Name,
		Kind:            tag.Kind,
		ParentID:        tag.ParentID,
		Depth:           effectiveMusicTagDepth(tag.Depth),
		AssignmentCount: assignmentCount,
		ChildCount:      childCount,
	}
}

func (s *Service) ListMusicTagOptions(kind, rawQuery string, parentID *uuid.UUID, rootOnly bool) ([]MusicTagOptionDTO, error) {
	if kind != "" {
		if err := validateMusicTagKind(kind); err != nil {
			return nil, err
		}
	}
	query, err := normalizeMusicTagQuery(rawQuery)
	if err != nil {
		return nil, err
	}

	queryDB := s.db.Select("id, name, kind, parent_id, depth")
	if kind != "" {
		queryDB = queryDB.Where("kind = ?", kind)
	}
	if parentID != nil {
		queryDB = queryDB.Where("parent_id = ?", *parentID)
	} else if rootOnly {
		queryDB = queryDB.Where("parent_id IS NULL")
	}
	if query != "" {
		queryDB = queryDB.Where("normalized_name LIKE ?", "%"+query+"%")
	}

	var tags []model.MusicTag
	if err := queryDB.
		Order("normalized_name ASC").
		Limit(func() int {
			if query != "" {
				return musicTagSearchLimit
			}
			return musicTagCatalogLimit
		}()).
		Find(&tags).Error; err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return []MusicTagOptionDTO{}, nil
	}

	tagIDs := make([]uuid.UUID, 0, len(tags))
	for _, tag := range tags {
		tagIDs = append(tagIDs, tag.ID)
	}
	type countRow struct {
		TagID uuid.UUID
		Count int64
	}
	var assignmentCounts []countRow
	if err := s.db.Model(&model.MusicTagAssignment{}).
		Select("tag_id, COUNT(*) AS count").
		Where("tag_id IN ?", tagIDs).
		Group("tag_id").
		Scan(&assignmentCounts).Error; err != nil {
		return nil, err
	}
	var childCounts []countRow
	if err := s.db.Model(&model.MusicTag{}).
		Select("parent_id AS tag_id, COUNT(*) AS count").
		Where("parent_id IN ?", tagIDs).
		Group("parent_id").
		Scan(&childCounts).Error; err != nil {
		return nil, err
	}
	assignmentCountByTag := make(map[uuid.UUID]int64, len(assignmentCounts))
	for _, row := range assignmentCounts {
		assignmentCountByTag[row.TagID] = row.Count
	}
	childCountByTag := make(map[uuid.UUID]int64, len(childCounts))
	for _, row := range childCounts {
		childCountByTag[row.TagID] = row.Count
	}

	result := make([]MusicTagOptionDTO, 0, len(tags))
	for _, tag := range tags {
		result = append(result, musicTagOptionDTO(tag, assignmentCountByTag[tag.ID], childCountByTag[tag.ID]))
	}
	return result, nil
}

func (s *Service) SearchMusicTags(kind, rawQuery string) ([]MusicTagOptionDTO, error) {
	return s.ListMusicTagOptions(kind, rawQuery, nil, false)
}

func (s *Service) GetMusicTag(tagID uuid.UUID) (MusicTagOptionDTO, error) {
	var tag model.MusicTag
	if err := s.db.Select("id, name, kind, parent_id, depth").First(&tag, "id = ?", tagID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MusicTagOptionDTO{}, apperr.NotFound("music.tag_not_found", "Tag not found")
		}
		return MusicTagOptionDTO{}, err
	}
	var assignmentCount int64
	if err := s.db.Model(&model.MusicTagAssignment{}).Where("tag_id = ?", tag.ID).Count(&assignmentCount).Error; err != nil {
		return MusicTagOptionDTO{}, err
	}
	var childCount int64
	if err := s.db.Model(&model.MusicTag{}).Where("parent_id = ?", tag.ID).Count(&childCount).Error; err != nil {
		return MusicTagOptionDTO{}, err
	}
	return musicTagOptionDTO(tag, assignmentCount, childCount), nil
}

func (s *Service) validateMusicTagEntity(user *authctx.CurrentUser, entityType string, entityID uuid.UUID) error {
	switch entityType {
	case musicTagEntitySong:
		var song model.Song
		err := scopeVisibleMusicEntries(s.db.Model(&model.Song{}), `"Songs"`, "uploaded_by", user, false).
			Select(`"Songs".id`).First(&song, `"Songs".id = ?`, entityID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.song_not_found", "Song not found")
		}
		return err
	case musicTagEntityAlbum:
		var album model.Album
		err := scopeVisibleMusicEntries(s.db.Model(&model.Album{}), `"Albums"`, "uploaded_by", user, false).
			Select(`"Albums".id`).First(&album, `"Albums".id = ?`, entityID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.album_not_found", "Album not found")
		}
		return err
	default:
		return apperr.BadRequest("music.invalid_tag_entity", "entity type must be song or album")
	}
}

func musicTagCanDelete(user *authctx.CurrentUser, createdBy uuid.UUID) bool {
	return user != nil && user.ID != uuid.Nil && (user.ID == createdBy || authctx.RoleAtLeast(user.Role, authctx.RoleAdmin))
}

func (s *Service) ListMusicTags(user *authctx.CurrentUser, entityType string, entityID uuid.UUID) ([]MusicTagDTO, error) {
	if err := s.validateMusicTagEntity(user, entityType, entityID); err != nil {
		return nil, err
	}

	var assignments []model.MusicTagAssignment
	if err := s.db.Preload("Tag").Where("entity_type = ? AND entity_id = ?", entityType, entityID).Find(&assignments).Error; err != nil {
		return nil, err
	}
	if len(assignments) == 0 {
		return []MusicTagDTO{}, nil
	}

	assignmentIDs := make([]uuid.UUID, 0, len(assignments))
	for _, assignment := range assignments {
		assignmentIDs = append(assignmentIDs, assignment.ID)
	}
	var votes []model.MusicTagVote
	if err := s.db.Where("assignment_id IN ?", assignmentIDs).Find(&votes).Error; err != nil {
		return nil, err
	}

	type voteSummary struct {
		upvotes   int64
		downvotes int64
		viewer    string
	}
	summaries := make(map[uuid.UUID]voteSummary, len(assignmentIDs))
	for _, vote := range votes {
		summary := summaries[vote.AssignmentID]
		if vote.Vote == "up" {
			summary.upvotes++
		} else {
			summary.downvotes++
		}
		if user != nil && vote.UserID == user.ID {
			summary.viewer = vote.Vote
		}
		summaries[vote.AssignmentID] = summary
	}

	result := make([]MusicTagDTO, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.Tag == nil {
			continue
		}
		summary := summaries[assignment.ID]
		result = append(result, MusicTagDTO{
			ID:           assignment.Tag.ID,
			AssignmentID: assignment.ID,
			Name:         assignment.Tag.Name,
			Kind:         assignment.Tag.Kind,
			ParentID:     assignment.Tag.ParentID,
			Depth:        effectiveMusicTagDepth(assignment.Tag.Depth),
			Upvotes:      summary.upvotes,
			Downvotes:    summary.downvotes,
			Score:        summary.upvotes - summary.downvotes,
			ViewerVote:   summary.viewer,
			CanDelete:    musicTagCanDelete(user, assignment.CreatedBy),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if result[i].Upvotes != result[j].Upvotes {
			return result[i].Upvotes > result[j].Upvotes
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func findOrCreateMusicTag(tx *gorm.DB, userID uuid.UUID, kind, rawName string, parentID *uuid.UUID) (model.MusicTag, error) {
	name, err := normalizeMusicTagName(rawName)
	if err != nil {
		return model.MusicTag{}, err
	}
	depth, err := validateMusicTagParent(tx, kind, parentID)
	if err != nil {
		return model.MusicTag{}, err
	}

	var tag model.MusicTag
	result := tx.Where("kind = ? AND normalized_name = ?", kind, name).First(&tag)
	if result.Error == nil {
		if !sameMusicTagParent(tag.ParentID, parentID) {
			return model.MusicTag{}, apperr.Conflict("music.tag_exists", "tag already exists under another parent")
		}
		return tag, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return model.MusicTag{}, result.Error
	}

	tag = model.MusicTag{
		Name:           strings.TrimSpace(rawName),
		NormalizedName: name,
		Kind:           kind,
		ParentID:       parentID,
		Depth:          depth,
		CreatedBy:      userID,
	}
	if err := tx.Create(&tag).Error; err != nil {
		return model.MusicTag{}, err
	}
	return tag, nil
}

func (s *Service) CreateMusicTag(user authctx.CurrentUser, kind, rawName string, parentID *uuid.UUID) (MusicTagOptionDTO, bool, error) {
	if user.ID == uuid.Nil {
		return MusicTagOptionDTO{}, false, apperr.Unauthorized("Login required")
	}
	if err := validateMusicTagKind(kind); err != nil {
		return MusicTagOptionDTO{}, false, err
	}

	var tag model.MusicTag
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		before := tx.Where("kind = ? AND normalized_name = ?", kind, strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(rawName)), " "))).First(&model.MusicTag{})
		existed := before.Error == nil
		tag, err = findOrCreateMusicTag(tx, user.ID, kind, rawName, parentID)
		if err != nil {
			return err
		}
		created = !existed
		return nil
	})
	if err != nil {
		return MusicTagOptionDTO{}, false, err
	}

	option, err := s.GetMusicTag(tag.ID)
	return option, created, err
}

func (s *Service) AddMusicTag(user authctx.CurrentUser, entityType string, entityID uuid.UUID, kind, rawName string, parentID *uuid.UUID) (MusicTagDTO, error) {
	if user.ID == uuid.Nil {
		return MusicTagDTO{}, apperr.Unauthorized("Login required")
	}
	if err := validateMusicTagKind(kind); err != nil {
		return MusicTagDTO{}, err
	}
	if err := s.validateMusicTagEntity(&user, entityType, entityID); err != nil {
		return MusicTagDTO{}, err
	}

	var assignment model.MusicTagAssignment
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.MusicTagAssignment{}).Where("entity_type = ? AND entity_id = ?", entityType, entityID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxMusicTagsPerItem {
			return apperr.Unprocessable("music.tag_limit_reached", "an item can have at most 12 tags")
		}

		tag, err := findOrCreateMusicTag(tx, user.ID, kind, rawName, parentID)
		if err != nil {
			return err
		}

		result := tx.Where("entity_type = ? AND entity_id = ? AND tag_id = ?", entityType, entityID, tag.ID).First(&assignment)
		if result.Error == nil {
			return apperr.Conflict("music.tag_exists", "tag already exists on this item")
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		assignment = model.MusicTagAssignment{EntityType: entityType, EntityID: entityID, TagID: tag.ID, CreatedBy: user.ID}
		return tx.Create(&assignment).Error
	})
	if err != nil {
		return MusicTagDTO{}, err
	}
	return s.musicTagDTO(&user, entityType, entityID, assignment.TagID)
}

func replaceAlbumImportTags(tx *gorm.DB, userID, albumID uuid.UUID, tags []AlbumImportTagPayload) error {
	if err := tx.Where("entity_type = ? AND entity_id = ?", musicTagEntityAlbum, albumID).Delete(&model.MusicTagAssignment{}).Error; err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(tags))
	for _, input := range tags {
		if err := validateMusicTagKind(input.Kind); err != nil {
			return err
		}
		name, err := normalizeMusicTagName(input.Name)
		if err != nil {
			return err
		}
		key := input.Kind + "\x00" + name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if len(seen) > maxMusicTagsPerItem {
			return apperr.Unprocessable("music.tag_limit_reached", "an item can have at most 12 tags")
		}
		tag, err := findOrCreateMusicTag(tx, userID, input.Kind, input.Name, nil)
		if err != nil {
			return err
		}
		assignment := model.MusicTagAssignment{EntityType: musicTagEntityAlbum, EntityID: albumID, TagID: tag.ID, CreatedBy: userID}
		if err := tx.Where("entity_type = ? AND entity_id = ? AND tag_id = ?", musicTagEntityAlbum, albumID, tag.ID).FirstOrCreate(&assignment).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeleteMusicTag(user authctx.CurrentUser, entityType string, entityID, tagID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if err := s.validateMusicTagEntity(&user, entityType, entityID); err != nil {
		return err
	}
	var assignment model.MusicTagAssignment
	if err := s.db.Where("entity_type = ? AND entity_id = ? AND tag_id = ?", entityType, entityID, tagID).First(&assignment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.tag_not_found", "Tag not found")
		}
		return err
	}
	if !musicTagCanDelete(&user, assignment.CreatedBy) {
		return apperr.Forbidden("music.tag_forbidden", "Only the tag creator or an administrator can delete this tag")
	}
	return s.db.Delete(&assignment).Error
}

func (s *Service) VoteMusicTag(user authctx.CurrentUser, entityType string, entityID, tagID uuid.UUID, vote string) (MusicTagDTO, error) {
	if user.ID == uuid.Nil {
		return MusicTagDTO{}, apperr.Unauthorized("Login required")
	}
	if vote != "up" && vote != "down" && vote != "none" {
		return MusicTagDTO{}, apperr.BadRequest("music.invalid_tag_vote", "vote must be up, down, or none")
	}
	if err := s.validateMusicTagEntity(&user, entityType, entityID); err != nil {
		return MusicTagDTO{}, err
	}
	var assignment model.MusicTagAssignment
	if err := s.db.Where("entity_type = ? AND entity_id = ? AND tag_id = ?", entityType, entityID, tagID).First(&assignment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MusicTagDTO{}, apperr.NotFound("music.tag_not_found", "Tag not found")
		}
		return MusicTagDTO{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if vote == "none" {
			return tx.Where("assignment_id = ? AND user_id = ?", assignment.ID, user.ID).Delete(&model.MusicTagVote{}).Error
		}
		entry := model.MusicTagVote{AssignmentID: assignment.ID, UserID: user.ID, Vote: vote}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "assignment_id"}, {Name: "user_id"}},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "deleted_at IS NULL"},
			}},
			DoUpdates: clause.AssignmentColumns([]string{"vote", "updated_at"}),
		}).Create(&entry).Error
	}); err != nil {
		return MusicTagDTO{}, err
	}
	return s.musicTagDTO(&user, entityType, entityID, tagID)
}

func (s *Service) musicTagDTO(user *authctx.CurrentUser, entityType string, entityID, tagID uuid.UUID) (MusicTagDTO, error) {
	tags, err := s.ListMusicTags(user, entityType, entityID)
	if err != nil {
		return MusicTagDTO{}, err
	}
	for _, tag := range tags {
		if tag.ID == tagID {
			return tag, nil
		}
	}
	return MusicTagDTO{}, apperr.NotFound("music.tag_not_found", "Tag not found")
}
