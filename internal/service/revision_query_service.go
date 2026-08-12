package service

import (
	"encoding/json"
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *RevisionService) GetRevisions(contentType string, contentID uuid.UUID, limit, offset int) ([]RevisionDTO, int64, error) {
	var revisions []model.Revision
	var total int64
	query := s.db.Where("content_id = ? AND content_type = ?", contentID, contentType)
	if err := query.Model(&model.Revision{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Preload("Editor").Preload("Reviewer").Order("version_number DESC").Limit(limit).Offset(offset).Find(&revisions).Error; err != nil {
		return nil, 0, err
	}
	result := make([]RevisionDTO, 0, len(revisions))
	for i := range revisions {
		result = append(result, revisionDTO(revisions[i]))
	}
	return result, total, nil
}

func (s *RevisionService) GetContributors(contentType string, contentID uuid.UUID, limit int) ([]RevisionContributorDTO, int64, error) {
	if limit <= 0 || limit > 10 {
		limit = 10
	}
	baseQuery := func() *gorm.DB {
		return s.db.Table("revisions").Where("content_type = ? AND content_id = ? AND status = ?", contentType, contentID, "approved")
	}
	var total int64
	if err := baseQuery().Distinct("editor_id").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var contributors []RevisionContributorDTO
	err := baseQuery().Select(`users.uuid AS user_id, users.username, users.display_name, users.avatar_url,
			COUNT(revisions.id) AS revision_count, MAX(revisions.created_at) AS last_contributed_at`).
		Joins(`JOIN "Users" AS users ON users.uuid = revisions.editor_id`).
		Group("users.uuid, users.username, users.display_name, users.avatar_url").
		Order("MAX(revisions.created_at) DESC, users.uuid ASC").Limit(limit).Scan(&contributors).Error
	if err != nil {
		return nil, 0, err
	}
	return contributors, total, nil
}

func revisionUserDTO(user *model.User) *RevisionUserDTO {
	if user == nil {
		return nil
	}
	return &RevisionUserDTO{UUID: user.UUID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL}
}

func revisionDTO(revision model.Revision) RevisionDTO {
	return RevisionDTO{
		ID: revision.ID, ContentType: revision.ContentType, ContentID: revision.ContentID,
		VersionNumber: revision.VersionNumber, PreviousRevisionID: revision.PreviousRevisionID,
		ContentSnapshot: json.RawMessage(revision.ContentSnapshot), EditorID: revision.EditorID,
		Editor: revisionUserDTO(revision.Editor), EditSummary: revision.EditSummary,
		EditType: revision.EditType, Status: revision.Status, ReviewerID: revision.ReviewerID,
		Reviewer: revisionUserDTO(revision.Reviewer), ReviewedAt: revision.ReviewedAt,
		ReviewNotes: revision.ReviewNotes, IsCurrent: revision.IsCurrent, CreatedAt: revision.CreatedAt,
	}
}

func (s *RevisionService) GetRevision(contentType string, contentID uuid.UUID, version int) (RevisionDTO, error) {
	var revision model.Revision
	err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?", contentID, contentType, version).
		Preload("Editor").Preload("Reviewer").First(&revision).Error
	if err != nil {
		return RevisionDTO{}, err
	}
	return revisionDTO(revision), nil
}

func (s *RevisionService) GetRevisionDiff(contentType string, contentID uuid.UUID, version1, version2 int) (map[string]interface{}, error) {
	var rev1, rev2 model.Revision
	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?", contentID, contentType, version1).First(&rev1).Error; err != nil {
		return nil, fmt.Errorf("revision %d not found", version1)
	}
	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?", contentID, contentType, version2).First(&rev2).Error; err != nil {
		return nil, fmt.Errorf("revision %d not found", version2)
	}
	var data1, data2 map[string]interface{}
	json.Unmarshal(rev1.ContentSnapshot, &data1)
	json.Unmarshal(rev2.ContentSnapshot, &data2)
	diff := make(map[string]interface{})
	for key, val2 := range data2 {
		val1 := data1[key]
		if fmt.Sprintf("%v", val1) != fmt.Sprintf("%v", val2) {
			diff[key] = map[string]interface{}{"from": val1, "to": val2}
		}
	}
	return diff, nil
}
