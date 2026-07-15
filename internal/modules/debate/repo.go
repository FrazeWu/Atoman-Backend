package debate

import (
	"atoman/internal/model"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) CreateDebate(debate *model.Debate) error { return r.db.Create(debate).Error }

func (r *Repo) GetDebate(id uuid.UUID) (model.Debate, error) {
	var debate model.Debate
	err := r.db.Preload("User").First(&debate, "id = ?", id).Error
	return debate, err
}

func (r *Repo) SaveDebate(debate *model.Debate) error { return r.db.Save(debate).Error }

func (r *Repo) DeleteDebate(id uuid.UUID) error {
	return r.db.Delete(&model.Debate{}, "id = ?", id).Error
}

func (r *Repo) GetArgument(id uuid.UUID) (model.Argument, error) {
	return r.getArgument(id, true, true)
}

func (r *Repo) getArgument(id uuid.UUID, includeReferences bool, includeAttachments bool) (model.Argument, error) {
	var entry model.CommentEntry
	err := r.db.Table("comment_entries AS comments").
		Select("comments.*").
		Joins("JOIN debate_argument_details AS details ON details.comment_id = comments.id").
		Joins("JOIN discussion_targets AS targets ON targets.id = comments.target_id AND targets.kind = ?", "debate").
		Where("comments.id = ?", id).First(&entry).Error
	if err != nil {
		return model.Argument{}, err
	}
	var target model.DiscussionTarget
	if err := r.db.First(&target, "id = ?", entry.TargetID).Error; err != nil {
		return model.Argument{}, err
	}
	var detail model.DebateArgumentDetail
	if err := r.db.First(&detail, "comment_id = ?", id).Error; err != nil {
		return model.Argument{}, err
	}
	var user model.User
	if err := r.db.First(&user, "uuid = ?", entry.AuthorID).Error; err != nil {
		return model.Argument{}, err
	}
	var voteCount int
	if err := r.db.Model(&model.DebateVote{}).Select("COALESCE(SUM(vote_type), 0)").Where("argument_id = ?", id).Scan(&voteCount).Error; err != nil {
		return model.Argument{}, err
	}
	argument := model.Argument{
		Base:     model.Base{ID: entry.ID, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt},
		DebateID: target.ResourceID, ParentID: entry.ReplyToID, UserID: entry.AuthorID, User: &user,
		Content: entry.Content, ArgumentType: model.ArgumentType(detail.ArgumentType), VoteCount: voteCount,
		SourceURL: detail.SourceURL, SourceTitle: detail.SourceTitle, SourceExcerpt: detail.SourceExcerpt,
		Conclusion: detail.Conclusion, IsConcluded: detail.Conclusion != "", IsFolded: detail.IsFolded, FoldNote: detail.FoldNote,
	}
	var mentions []model.CommentMention
	if err := r.db.Where("comment_id = ?", id).Order("start_offset ASC").Find(&mentions).Error; err != nil {
		return model.Argument{}, err
	}
	for _, mention := range mentions {
		argument.Mentions = append(argument.Mentions, model.ArgumentMention{UserID: mention.UserID, Start: mention.StartOffset, End: mention.EndOffset})
	}
	if includeAttachments {
		attachments, err := r.loadArgumentAttachments([]uuid.UUID{id})
		if err != nil {
			return model.Argument{}, err
		}
		argument.Attachments = attachments[id]
		for _, attachment := range argument.Attachments {
			argument.AttachmentIDs = append(argument.AttachmentIDs, attachment.ID)
		}
	}
	if includeReferences {
		var refs []model.DebateArgumentReference
		if err := r.db.Where("comment_id = ?", id).Find(&refs).Error; err != nil {
			return model.Argument{}, err
		}
		for _, ref := range refs {
			loaded, err := r.getArgument(ref.ReferencedCommentID, false, includeAttachments)
			if err != nil {
				return model.Argument{}, err
			}
			argument.References = append(argument.References, loaded)
		}
		var debateRefs []model.DebateArgumentDebateRef
		if err := r.db.Where("comment_id = ?", id).Find(&debateRefs).Error; err != nil {
			return model.Argument{}, err
		}
		for _, ref := range debateRefs {
			var debate model.Debate
			if err := r.db.First(&debate, "id = ?", ref.DebateID).Error; err != nil {
				return model.Argument{}, err
			}
			argument.ReferencedDebates = append(argument.ReferencedDebates, debate)
		}
	}
	return argument, nil
}

func (r *Repo) loadArgumentAttachments(ids []uuid.UUID) (map[uuid.UUID][]model.ArgumentAttachment, error) {
	type row struct {
		CommentID   uuid.UUID
		ID          uuid.UUID
		URL         string
		ContentType string
		Position    int
	}
	var rows []row
	err := r.db.Table("comment_attachments AS attachments").
		Select("attachments.comment_id, assets.id, assets.url, assets.content_type, attachments.position").
		Joins("JOIN media_assets AS assets ON assets.id = attachments.media_asset_id AND assets.deleted_at IS NULL").
		Where("attachments.comment_id IN ? AND attachments.deleted_at IS NULL", ids).
		Order("attachments.comment_id ASC, attachments.position ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID][]model.ArgumentAttachment, len(ids))
	for _, item := range rows {
		result[item.CommentID] = append(result[item.CommentID], model.ArgumentAttachment{ID: item.ID, URL: item.URL, ContentType: item.ContentType, Position: item.Position})
	}
	return result, nil
}

func (r *Repo) ListDebates(query ListDebatesQuery) ([]model.Debate, int64, error) {
	db := r.db.Model(&model.Debate{})
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		db = db.Where("title LIKE ? OR description LIKE ? OR content LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var debates []model.Debate
	err := db.Preload("User").Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&debates).Error
	return debates, total, err
}

func (r *Repo) ListArguments(debateID uuid.UUID) ([]model.Argument, error) {
	var ids []uuid.UUID
	err := r.db.Table("comment_entries AS comments").
		Joins("JOIN debate_argument_details AS details ON details.comment_id = comments.id").
		Joins("JOIN discussion_targets AS targets ON targets.id = comments.target_id").
		Where("targets.kind = ? AND targets.resource_id = ? AND comments.deleted_at IS NULL AND comments.status IN ?", "debate", debateID, []string{"active", "auto_folded"}).
		Order("comments.created_at ASC").Pluck("comments.id", &ids).Error
	if err != nil {
		return nil, err
	}
	arguments := make([]model.Argument, 0, len(ids))
	for _, id := range ids {
		argument, err := r.getArgument(id, true, false)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, argument)
	}
	attachments, err := r.loadArgumentAttachments(ids)
	if err != nil {
		return nil, err
	}
	for index := range arguments {
		arguments[index].Attachments = attachments[arguments[index].ID]
		for _, attachment := range arguments[index].Attachments {
			arguments[index].AttachmentIDs = append(arguments[index].AttachmentIDs, attachment.ID)
		}
	}
	return arguments, nil
}
