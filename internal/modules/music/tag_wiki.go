package music

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 分类和关联共享短事务锁，避免分类移动与关联添加各自验证后产生父子重复。
func lockMusicTags(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(2026102301)).Error
}

type MusicTagWikiInput struct {
	BaseVersion   int        `json:"base_version"`
	Name          string     `json:"name"`
	ParentID      *uuid.UUID `json:"parent_id"`
	TargetID      *uuid.UUID `json:"target_id"`
	TargetVersion int        `json:"target_version"`
	RevisionID    *uuid.UUID `json:"revision_id"`
	Summary       string     `json:"summary"`
}

type MusicTagAssignmentEdit struct {
	BaseVersion int         `json:"base_version"`
	Add         []uuid.UUID `json:"add"`
	Remove      []uuid.UUID `json:"remove"`
	Summary     string      `json:"summary"`
	RevisionID  *uuid.UUID  `json:"revision_id,omitempty"`
}

type MusicTagHistory struct {
	Version   int                      `json:"version"`
	Revisions []model.MusicTagRevision `json:"revisions"`
}

type musicTagSnapshot struct {
	Tags        []model.MusicTag           `json:"tags"`
	Assignments []model.MusicTagAssignment `json:"assignments"`
	Votes       []model.MusicTagVote       `json:"votes"`
}

func tagWikiConflict(message string) error {
	return apperr.Conflict("music.tag_wiki_conflict", message)
}

func tagStreamVersion(tx *gorm.DB, kind string, id uuid.UUID) (int, error) {
	var version int
	err := tx.Model(&model.MusicTagRevision{}).Select("COALESCE(MAX(version), 0)").Where("object_type = ? AND object_id = ?", kind, id).Scan(&version).Error
	return version, err
}

func tagEditWar(tx *gorm.DB, kind string, id uuid.UUID) error {
	var count int64
	if err := tx.Model(&model.MusicTagRevision{}).Where("object_type = ? AND object_id = ? AND created_at > ?", kind, id, time.Now().Add(-24*time.Hour)).Count(&count).Error; err != nil {
		return err
	}
	if count >= 20 {
		return tagWikiConflict("该范围一天内修改过于频繁，请稍后再试；历史记录仍可查看")
	}
	var reverts int64
	if err := tx.Model(&model.MusicTagRevision{}).Where("object_type=? AND object_id=? AND action='revert' AND created_at>?", kind, id, time.Now().Add(-24*time.Hour)).Count(&reverts).Error; err != nil {
		return err
	}
	if reverts >= 3 {
		return tagWikiConflict("该范围短期内反复回退，暂缓编辑以避免编辑战；历史记录仍可查看")
	}
	return nil
}

func writeTagRevision(tx *gorm.DB, userID uuid.UUID, kind string, id uuid.UUID, action, summary string, before, after musicTagSnapshot, reverts *uuid.UUID) error {
	version, err := tagStreamVersion(tx, kind, id)
	if err != nil {
		return err
	}
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	return tx.Create(&model.MusicTagRevision{ObjectType: kind, ObjectID: id, Version: version + 1, ActorID: userID, Action: action, Summary: strings.TrimSpace(summary), Before: b, After: a, RevertsID: reverts}).Error
}

func (s *Service) MusicTagHistory(user *authctx.CurrentUser, kind string, id uuid.UUID) (MusicTagHistory, error) {
	if kind != "tag" {
		if err := s.validateMusicTagEntity(user, kind, id); err != nil {
			return MusicTagHistory{}, err
		}
	} else {
		var tag model.MusicTag
		if err := s.db.First(&tag, "id=?", id).Error; err != nil {
			return MusicTagHistory{}, apperr.NotFound("music.tag_not_found", "Tag not found")
		}
	}
	var rows []model.MusicTagRevision
	err := s.db.Where("object_type = ? AND object_id = ?", kind, id).Order("version DESC").Find(&rows).Error
	v := 0
	if len(rows) > 0 {
		v = rows[0].Version
	}
	return MusicTagHistory{Version: v, Revisions: rows}, err
}

func captureTagSnapshot(tx *gorm.DB, tagIDs, assignmentIDs []uuid.UUID) (musicTagSnapshot, error) {
	out := musicTagSnapshot{Tags: []model.MusicTag{}, Assignments: []model.MusicTagAssignment{}, Votes: []model.MusicTagVote{}}
	if len(tagIDs) > 0 {
		if err := tx.Unscoped().Where("id IN ?", tagIDs).Order("id").Find(&out.Tags).Error; err != nil {
			return out, err
		}
	}
	if len(assignmentIDs) > 0 {
		if err := tx.Unscoped().Where("id IN ?", assignmentIDs).Order("id").Find(&out.Assignments).Error; err != nil {
			return out, err
		}
		if err := tx.Unscoped().Where("assignment_id IN ?", assignmentIDs).Order("id").Find(&out.Votes).Error; err != nil {
			return out, err
		}
	}
	return out, nil
}

func snapshotIDs(snapshot musicTagSnapshot) ([]uuid.UUID, []uuid.UUID) {
	tags, assignments := []uuid.UUID{}, []uuid.UUID{}
	for _, tag := range snapshot.Tags {
		tags = append(tags, tag.ID)
	}
	for _, assignment := range snapshot.Assignments {
		assignments = append(assignments, assignment.ID)
	}
	return tags, assignments
}

// 在内存中验证候选整棵分类树，但仅持久化发生变化的节点。
func validateTagTree(tx *gorm.DB, affected ...uuid.UUID) error {
	var tags []model.MusicTag
	if err := tx.Where("canonical_tag_id IS NULL").Find(&tags).Error; err != nil {
		return err
	}
	byID := map[uuid.UUID]*model.MusicTag{}
	for i := range tags {
		byID[tags[i].ID] = &tags[i]
	}
	var depth func(uuid.UUID, map[uuid.UUID]bool) (int, error)
	depth = func(id uuid.UUID, seen map[uuid.UUID]bool) (int, error) {
		tag := byID[id]
		if tag == nil {
			return 0, tagWikiConflict("父级标签不存在或已合并")
		}
		if seen[id] {
			return 0, tagWikiConflict("分类不能形成循环")
		}
		seen[id] = true
		if tag.ParentID == nil {
			return 1, nil
		}
		parent := byID[*tag.ParentID]
		if tag.Kind != model.MusicTagKindType || parent == nil || parent.Kind != tag.Kind {
			return 0, tagWikiConflict("只有风格可以建立同类父子关系")
		}
		d, err := depth(*tag.ParentID, seen)
		if err != nil {
			return 0, err
		}
		if d >= musicTagMaxDepth {
			return 0, tagWikiConflict("风格最多三级")
		}
		return d + 1, nil
	}
	for i := range tags {
		d, err := depth(tags[i].ID, map[uuid.UUID]bool{})
		if err != nil {
			return err
		}
		if tags[i].Depth != d {
			if err := tx.Model(&tags[i]).Updates(map[string]any{"depth": d, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
	}
	if len(affected) == 0 {
		return nil
	}
	var redundant int64
	err := tx.Raw(`WITH RECURSIVE ancestors AS (
	 SELECT id AS child_id,parent_id FROM music_tags WHERE canonical_tag_id IS NULL AND deleted_at IS NULL AND parent_id IS NOT NULL
	 UNION ALL SELECT a.child_id,t.parent_id FROM ancestors a JOIN music_tags t ON t.id=a.parent_id WHERE t.parent_id IS NOT NULL
	) SELECT COUNT(*) FROM ancestors tree JOIN music_tag_assignments child ON child.tag_id=tree.child_id AND child.deleted_at IS NULL
	 JOIN music_tag_assignments parent ON parent.tag_id=tree.parent_id AND parent.entity_type=child.entity_type AND parent.entity_id=child.entity_id AND parent.deleted_at IS NULL WHERE tree.child_id IN ? OR tree.parent_id IN ?`, affected, affected).Scan(&redundant).Error
	if err != nil {
		return err
	}
	if redundant > 0 {
		return tagWikiConflict("分类调整会使作品同时绑定父级和子级，请先在作品标签中原子替换")
	}
	return nil
}

func validateAssignedTagBranches(tx *gorm.DB, kind string, id uuid.UUID) error {
	var count int64
	err := tx.Raw(`WITH RECURSIVE ancestors AS (
	 SELECT t.id AS child_id,t.parent_id FROM music_tags t JOIN music_tag_assignments a ON a.tag_id=t.id
	 WHERE a.entity_type=? AND a.entity_id=? AND a.deleted_at IS NULL AND t.parent_id IS NOT NULL
	 UNION ALL SELECT a.child_id,t.parent_id FROM ancestors a JOIN music_tags t ON t.id=a.parent_id WHERE t.parent_id IS NOT NULL
	) SELECT COUNT(*) FROM ancestors tree JOIN music_tag_assignments a ON a.tag_id=tree.parent_id
	 WHERE a.entity_type=? AND a.entity_id=? AND a.deleted_at IS NULL`, kind, id, kind, id).Scan(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return tagWikiConflict("同分支已有父级或子级风格，请确认后原子替换")
	}
	return nil
}

func restoreOrAddTagAssignment(tx *gorm.DB, userID uuid.UUID, kind string, id, tagID uuid.UUID) (model.MusicTagAssignment, error) {
	var assignment model.MusicTagAssignment
	err := tx.Unscoped().Where("entity_type=? AND entity_id=? AND tag_id=?", kind, id, tagID).Order("deleted_at ASC NULLS FIRST, created_at ASC").First(&assignment).Error
	if err == nil {
		if !assignment.DeletedAt.Valid {
			return assignment, apperr.Conflict("music.tag_exists", "tag already exists on this item")
		}
		assignment.DeletedAt = gorm.DeletedAt{}
		return assignment, tx.Unscoped().Save(&assignment).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return assignment, err
	}
	assignment = model.MusicTagAssignment{EntityType: kind, EntityID: id, TagID: tagID, CreatedBy: userID}
	return assignment, tx.Create(&assignment).Error
}

func (s *Service) EditMusicTagAssignments(user authctx.CurrentUser, kind string, id uuid.UUID, input MusicTagAssignmentEdit) (MusicTagHistory, error) {
	if user.ID == uuid.Nil {
		return MusicTagHistory{}, apperr.Unauthorized("Login required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockMusicTags(tx); err != nil {
			return err
		}
		if err := NewService(tx).validateMusicTagEntity(&user, kind, id); err != nil {
			return err
		}
		if err := revisionservice.ValidateMusicEntryEdit(tx, kind, id, user.ID); err != nil {
			return err
		}
		v, err := tagStreamVersion(tx, kind, id)
		if err != nil {
			return err
		}
		if input.BaseVersion != v {
			return tagWikiConflict("标签关联已被修改，请刷新后重试")
		}
		if err := tagEditWar(tx, kind, id); err != nil {
			return err
		}
		if input.RevisionID != nil {
			var result musicTagSnapshot
			return s.revertTagWiki(tx, user, kind, id, MusicTagWikiInput{RevisionID: input.RevisionID, Summary: input.Summary}, &result, false, nil)
		}
		var rows []model.MusicTagAssignment
		if err := tx.Unscoped().Where("entity_type=? AND entity_id=?", kind, id).Find(&rows).Error; err != nil {
			return err
		}
		ids := []uuid.UUID{}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		before, err := captureTagSnapshot(tx, nil, ids)
		if err != nil {
			return err
		}
		if len(input.Remove) > 0 {
			if err := tx.Where("entity_type=? AND entity_id=? AND tag_id IN ?", kind, id, input.Remove).Delete(&model.MusicTagAssignment{}).Error; err != nil {
				return err
			}
		}
		for _, tagID := range input.Add {
			var tag model.MusicTag
			if err := tx.Where("canonical_tag_id IS NULL").First(&tag, "id=?", tagID).Error; err != nil {
				return tagWikiConflict("标签不存在或已合并，请刷新")
			}
			assignment, err := restoreOrAddTagAssignment(tx, user.ID, kind, id, tagID)
			if err != nil {
				return err
			}
			ids = append(ids, assignment.ID)
		}
		var count int64
		if err := tx.Model(&model.MusicTagAssignment{}).Where("entity_type=? AND entity_id=?", kind, id).Count(&count).Error; err != nil {
			return err
		}
		if count > 12 {
			return apperr.Unprocessable("music.tag_limit_reached", "an item can have at most 12 tags")
		}
		if err := validateAssignedTagBranches(tx, kind, id); err != nil {
			return err
		}
		after, err := captureTagSnapshot(tx, nil, ids)
		if err != nil {
			return err
		}
		return writeTagRevision(tx, user.ID, kind, id, "assignments", input.Summary, before, after, nil)
	})
	if err != nil {
		return MusicTagHistory{}, err
	}
	return s.MusicTagHistory(&user, kind, id)
}

func (s *Service) EditMusicTag(user authctx.CurrentUser, id uuid.UUID, action string, input MusicTagWikiInput, preview bool) (musicTagSnapshot, error) {
	if user.ID == uuid.Nil {
		return musicTagSnapshot{}, apperr.Unauthorized("Login required")
	}
	var result musicTagSnapshot
	previewRollback := errors.New("tag preview rollback")
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockMusicTags(tx); err != nil {
			return err
		}
		var tag model.MusicTag
		if err := tx.First(&tag, "id=?", id).Error; err != nil {
			return apperr.NotFound("music.tag_not_found", "Tag not found")
		}
		if tag.CanonicalTagID != nil && action != "revert" {
			return tagWikiConflict("该标签已合并，请访问目标标签")
		}
		if tag.Version != input.BaseVersion {
			return tagWikiConflict("标签版本已更新，请刷新后重试")
		}
		if err := tagEditWar(tx, "tag", id); err != nil {
			return err
		}
		var tags []model.MusicTag
		if err := tx.Raw(`WITH RECURSIVE tree AS (SELECT id FROM music_tags WHERE id=? UNION ALL SELECT child.id FROM music_tags child JOIN tree ON child.parent_id=tree.id WHERE child.deleted_at IS NULL) SELECT music_tags.* FROM music_tags JOIN tree ON music_tags.id=tree.id ORDER BY music_tags.id`, id).Scan(&tags).Error; err != nil {
			return err
		}
		tagIDs := []uuid.UUID{}
		for _, t := range tags {
			tagIDs = append(tagIDs, t.ID)
		}
		assignmentIDs := []uuid.UUID{}
		if action == "merge" {
			if input.TargetID == nil || *input.TargetID == id {
				return tagWikiConflict("请选择另一个目标标签")
			}
			for _, childID := range tagIDs {
				if childID == *input.TargetID {
					return tagWikiConflict("不能合并到自己的子分类")
				}
			}
			var target model.MusicTag
			if err := tx.First(&target, "id=? AND canonical_tag_id IS NULL", input.TargetID).Error; err != nil {
				return err
			}
			if target.Version != input.TargetVersion {
				return tagWikiConflict("目标标签版本已更新")
			}
			if target.Kind != tag.Kind {
				return tagWikiConflict("仅可合并相同类型的标签")
			}
			if err := tagEditWar(tx, "tag", target.ID); err != nil {
				return err
			}
			tagIDs = append(tagIDs, target.ID)
			if err := tx.Unscoped().Model(&model.MusicTagAssignment{}).Where("tag_id IN ?", []uuid.UUID{id, target.ID}).Pluck("id", &assignmentIDs).Error; err != nil {
				return err
			}
		}
		// 预览和历史包含整个受影响子树的关联，而非只有直接绑定根的作品。
		if err := tx.Unscoped().Model(&model.MusicTagAssignment{}).Where("tag_id IN ?", tagIDs).Pluck("id", &assignmentIDs).Error; err != nil {
			return err
		}
		before, err := captureTagSnapshot(tx, tagIDs, assignmentIDs)
		if err != nil {
			return err
		}
		switch action {
		case "rename":
			name, err := normalizeMusicTagName(input.Name)
			if err != nil {
				return err
			}
			var count int64
			tx.Model(&model.MusicTag{}).Where("kind=? AND normalized_name=? AND id<>?", tag.Kind, name, id).Count(&count)
			if count > 0 {
				return tagWikiConflict("该类型中已有同名标签")
			}
			tag.Name, tag.NormalizedName = strings.TrimSpace(input.Name), name
		case "move":
			tag.ParentID = input.ParentID
		case "merge":
			if err := mergeTagAssignments(tx, user.ID, id, *input.TargetID, &assignmentIDs); err != nil {
				return err
			}
			if err := tx.Model(&model.MusicTag{}).Where("parent_id=?", id).Updates(map[string]any{"parent_id": *input.TargetID, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
			tag.CanonicalTagID = input.TargetID
			if err := tx.Model(&model.MusicTag{}).Where("id=?", input.TargetID).UpdateColumn("version", gorm.Expr("version+1")).Error; err != nil {
				return err
			}
		case "revert":
			return s.revertTagWiki(tx, user, "tag", id, input, &result, preview, previewRollback)
		default:
			return apperr.BadRequest("music.invalid_tag_action", "Unsupported tag action")
		}
		tag.Version++
		if err := tx.Save(&tag).Error; err != nil {
			return err
		}
		affected := tagIDs
		if action == "rename" {
			affected = nil
		}
		if err := validateTagTree(tx, affected...); err != nil {
			return err
		}
		after, err := captureTagSnapshot(tx, tagIDs, assignmentIDs)
		if err != nil {
			return err
		}
		result = after
		if preview {
			return previewRollback
		}
		if action == "merge" {
			if err := writeTagRevision(tx, user.ID, "tag", *input.TargetID, "merge_target", input.Summary, before, after, nil); err != nil {
				return err
			}
		}
		return writeTagRevision(tx, user.ID, "tag", id, action, input.Summary, before, after, nil)
	})
	if errors.Is(err, previewRollback) {
		err = nil
	}
	return result, err
}

// 重叠票按同一用户最后一次修改保留一张有效票；原来源记录保留用于审计和回退。
func mergeTagAssignments(tx *gorm.DB, userID, source, target uuid.UUID, ids *[]uuid.UUID) error {
	var sources []model.MusicTagAssignment
	if err := tx.Where("tag_id=?", source).Find(&sources).Error; err != nil {
		return err
	}
	for _, from := range sources {
		var to model.MusicTagAssignment
		err := tx.Unscoped().Where("tag_id=? AND entity_type=? AND entity_id=?", target, from.EntityType, from.EntityID).Order("deleted_at ASC NULLS FIRST,created_at ASC").First(&to).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			to = model.MusicTagAssignment{TagID: target, EntityType: from.EntityType, EntityID: from.EntityID, CreatedBy: userID}
			if err := tx.Create(&to).Error; err != nil {
				return err
			}
			*ids = append(*ids, to.ID)
		} else if err != nil {
			return err
		} else {
			to.DeletedAt = gorm.DeletedAt{}
			if err := tx.Unscoped().Save(&to).Error; err != nil {
				return err
			}
		}
		var votes []model.MusicTagVote
		if err := tx.Where("assignment_id=?", from.ID).Find(&votes).Error; err != nil {
			return err
		}
		for _, vote := range votes {
			var existing model.MusicTagVote
			err := tx.Where("assignment_id=? AND user_id=?", to.ID, vote.UserID).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				copy := model.MusicTagVote{AssignmentID: to.ID, UserID: vote.UserID, Vote: vote.Vote}
				if err := tx.Create(&copy).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if vote.UpdatedAt.After(existing.UpdatedAt) {
				if err := tx.Model(&existing).Update("vote", vote.Vote).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(&from).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) revertTagWiki(tx *gorm.DB, user authctx.CurrentUser, kind string, id uuid.UUID, input MusicTagWikiInput, result *musicTagSnapshot, preview bool, rollback error) error {
	if input.RevisionID == nil {
		return tagWikiConflict("请选择要撤销的修订")
	}
	var revision model.MusicTagRevision
	if err := tx.First(&revision, "id=? AND object_type=? AND object_id=?", input.RevisionID, kind, id).Error; err != nil {
		return err
	}
	if revision.Action == "create" {
		return tagWikiConflict("标签创建保留可追溯身份，请通过合并整理重复标签")
	}
	var before, after musicTagSnapshot
	if err := json.Unmarshal(revision.Before, &before); err != nil {
		return err
	}
	if err := json.Unmarshal(revision.After, &after); err != nil {
		return err
	}
	tagIDs, assignmentIDs := snapshotIDs(after)
	if len(tagIDs) > 0 {
		var added int64
		query := tx.Model(&model.MusicTagAssignment{}).Where("tag_id IN ?", tagIDs)
		if len(assignmentIDs) > 0 {
			query = query.Where("id NOT IN ?", assignmentIDs)
		}
		if err := query.Count(&added).Error; err != nil {
			return err
		}
		if added > 0 {
			return tagWikiConflict("相关标签已有新的作品关联，无法安全回退")
		}
	}
	current, err := captureTagSnapshot(tx, tagIDs, assignmentIDs)
	if err != nil {
		return err
	}
	// 不比较 JSONB 的键顺序，只比较重新编码后的结构。
	expected, _ := json.Marshal(after)
	actual, _ := json.Marshal(current)
	if !bytes.Equal(expected, actual) {
		return tagWikiConflict("相关分类、关联或投票已有后续修改，无法安全回退")
	}
	for _, tag := range before.Tags {
		tag.NormalizedName, err = normalizeMusicTagName(tag.Name)
		if err != nil {
			return err
		}
		var duplicate int64
		if err := tx.Model(&model.MusicTag{}).Where("kind=? AND normalized_name=? AND id<>?", tag.Kind, tag.NormalizedName, tag.ID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return tagWikiConflict("旧名称已被其他标签使用，无法安全回退")
		}
		for _, now := range current.Tags {
			if tag.ID == now.ID {
				tag.Version = now.Version + 1
			}
		}
		if err := tx.Unscoped().Save(&tag).Error; err != nil {
			return err
		}
	}
	oldAssignments := map[uuid.UUID]bool{}
	for _, row := range before.Assignments {
		oldAssignments[row.ID] = true
	}
	for _, row := range after.Assignments {
		if !oldAssignments[row.ID] {
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range before.Assignments {
		if err := tx.Unscoped().Save(&row).Error; err != nil {
			return err
		}
	}
	oldVotes := map[uuid.UUID]bool{}
	for _, row := range before.Votes {
		oldVotes[row.ID] = true
		if err := tx.Unscoped().Save(&row).Error; err != nil {
			return err
		}
	}
	for _, row := range after.Votes {
		if !oldVotes[row.ID] {
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
		}
	}
	if err := validateTagTree(tx, tagIDs...); err != nil {
		return err
	}
	if kind != "tag" {
		if err := validateAssignedTagBranches(tx, kind, id); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.MusicTagAssignment{}).Where("entity_type=? AND entity_id=?", kind, id).Count(&count).Error; err != nil {
			return err
		}
		if count > maxMusicTagsPerItem {
			return tagWikiConflict("回退后超过十二个标签，请先调整关联")
		}
	}
	*result, err = captureTagSnapshot(tx, tagIDs, assignmentIDs)
	if err != nil {
		return err
	}
	if preview {
		return rollback
	}
	return writeTagRevision(tx, user.ID, kind, id, "revert", input.Summary, current, *result, &revision.ID)
}
