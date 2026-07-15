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
	return r.getArgument(id, true)
}

func (r *Repo) getArgument(id uuid.UUID, includeReferences bool) (model.Argument, error) {
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
	if includeReferences {
		var refs []model.DebateArgumentReference
		if err := r.db.Where("comment_id = ?", id).Find(&refs).Error; err != nil {
			return model.Argument{}, err
		}
		for _, ref := range refs {
			loaded, err := r.getArgument(ref.ReferencedCommentID, false)
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
		argument, err := r.GetArgument(id)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, argument)
	}
	return arguments, nil
}
