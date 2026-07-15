package debate

import (
	"atoman/internal/model"
	"strings"
	"time"

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
		Conclusion: detail.Conclusion, IsConcluded: detail.Conclusion != "", IsFolded: detail.IsFolded || entry.Status == "auto_folded", FoldNote: detail.FoldNote,
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

func (r *Repo) ListArguments(debateID uuid.UUID, page, pageSize int) ([]model.Argument, int64, error) {
	type row struct {
		ID, AuthorID                                                                               uuid.UUID
		ReplyToID                                                                                  *uuid.UUID
		Content, Status, ArgumentType, SourceURL, SourceTitle, SourceExcerpt, Conclusion, FoldNote string
		IsFolded                                                                                   bool
		CreatedAt, UpdatedAt                                                                       time.Time
	}
	var rows []row
	base := r.db.Table("comment_entries AS comments").
		Joins("JOIN debate_argument_details AS details ON details.comment_id = comments.id").
		Joins("JOIN discussion_targets AS targets ON targets.id = comments.target_id").
		Where("targets.kind = ? AND targets.resource_id = ? AND comments.deleted_at IS NULL AND comments.status IN ?", "debate", debateID, []string{"active", "auto_folded"})
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := base.
		Select("comments.id, comments.author_id, comments.reply_to_id, comments.content, comments.status, comments.created_at, comments.updated_at, details.argument_type, details.source_url, details.source_title, details.source_excerpt, details.conclusion, details.is_folded, details.fold_note").
		Order("comments.created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	ids, userIDs := make([]uuid.UUID, 0, len(rows)), make([]uuid.UUID, 0, len(rows))
	for _, item := range rows {
		ids = append(ids, item.ID)
		userIDs = append(userIDs, item.AuthorID)
	}
	var users []model.User
	if len(userIDs) > 0 {
		if err := r.db.Where("uuid IN ?", userIDs).Find(&users).Error; err != nil {
			return nil, 0, err
		}
	}
	userMap := make(map[uuid.UUID]*model.User, len(users))
	for i := range users {
		userMap[users[i].UUID] = &users[i]
	}
	type voteRow struct {
		ArgumentID uuid.UUID
		VoteCount  int
	}
	var votes []voteRow
	if len(ids) > 0 {
		if err := r.db.Model(&model.DebateVote{}).Select("argument_id, COALESCE(SUM(vote_type), 0) AS vote_count").Where("argument_id IN ?", ids).Group("argument_id").Scan(&votes).Error; err != nil {
			return nil, 0, err
		}
	}
	voteMap := make(map[uuid.UUID]int, len(votes))
	for _, vote := range votes {
		voteMap[vote.ArgumentID] = vote.VoteCount
	}
	var mentionRows []model.CommentMention
	if len(ids) > 0 {
		if err := r.db.Where("comment_id IN ?", ids).Order("comment_id, start_offset").Find(&mentionRows).Error; err != nil {
			return nil, 0, err
		}
	}
	mentionMap := make(map[uuid.UUID][]model.ArgumentMention)
	for _, mention := range mentionRows {
		mentionMap[mention.CommentID] = append(mentionMap[mention.CommentID], model.ArgumentMention{UserID: mention.UserID, Start: mention.StartOffset, End: mention.EndOffset})
	}
	attachments, err := r.loadArgumentAttachments(ids)
	if err != nil {
		return nil, 0, err
	}
	arguments := make([]model.Argument, 0, len(rows))
	indexByID := make(map[uuid.UUID]int, len(rows))
	for _, item := range rows {
		argument := model.Argument{Base: model.Base{ID: item.ID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}, DebateID: debateID, ParentID: item.ReplyToID, UserID: item.AuthorID, User: userMap[item.AuthorID], Content: item.Content, ArgumentType: model.ArgumentType(item.ArgumentType), VoteCount: voteMap[item.ID], SourceURL: item.SourceURL, SourceTitle: item.SourceTitle, SourceExcerpt: item.SourceExcerpt, Conclusion: item.Conclusion, IsConcluded: item.Conclusion != "", IsFolded: item.IsFolded || item.Status == "auto_folded", FoldNote: item.FoldNote, Mentions: mentionMap[item.ID], Attachments: attachments[item.ID]}
		for _, attachment := range argument.Attachments {
			argument.AttachmentIDs = append(argument.AttachmentIDs, attachment.ID)
		}
		indexByID[item.ID] = len(arguments)
		arguments = append(arguments, argument)
	}
	var refs []model.DebateArgumentReference
	if len(ids) > 0 {
		if err := r.db.Where("comment_id IN ?", ids).Find(&refs).Error; err != nil {
			return nil, 0, err
		}
	}
	missing := make([]uuid.UUID, 0)
	seenMissing := map[uuid.UUID]bool{}
	for _, ref := range refs {
		if _, exists := indexByID[ref.ReferencedCommentID]; !exists && !seenMissing[ref.ReferencedCommentID] {
			seenMissing[ref.ReferencedCommentID] = true
			missing = append(missing, ref.ReferencedCommentID)
		}
	}
	briefMap := make(map[uuid.UUID]model.Argument, len(missing))
	if len(missing) > 0 {
		var briefRows []row
		if err := r.db.Table("comment_entries AS comments").Select("comments.id, comments.author_id, comments.reply_to_id, comments.content, comments.status, comments.created_at, comments.updated_at, details.argument_type, details.source_url, details.source_title, details.source_excerpt, details.conclusion, details.is_folded, details.fold_note").
			Joins("JOIN debate_argument_details AS details ON details.comment_id = comments.id").Where("comments.id IN ? AND comments.deleted_at IS NULL AND comments.status IN ?", missing, []string{"active", "auto_folded"}).Scan(&briefRows).Error; err != nil {
			return nil, 0, err
		}
		briefUserIDs := make([]uuid.UUID, 0, len(briefRows))
		for _, item := range briefRows {
			briefUserIDs = append(briefUserIDs, item.AuthorID)
		}
		var briefUsers []model.User
		if err := r.db.Where("uuid IN ?", briefUserIDs).Find(&briefUsers).Error; err != nil {
			return nil, 0, err
		}
		briefUsersMap := make(map[uuid.UUID]*model.User, len(briefUsers))
		for index := range briefUsers {
			briefUsersMap[briefUsers[index].UUID] = &briefUsers[index]
		}
		var briefVotes []voteRow
		if err := r.db.Model(&model.DebateVote{}).Select("argument_id, COALESCE(SUM(vote_type), 0) AS vote_count").Where("argument_id IN ?", missing).Group("argument_id").Scan(&briefVotes).Error; err != nil {
			return nil, 0, err
		}
		briefVoteMap := make(map[uuid.UUID]int, len(briefVotes))
		for _, vote := range briefVotes {
			briefVoteMap[vote.ArgumentID] = vote.VoteCount
		}
		for _, item := range briefRows {
			briefMap[item.ID] = model.Argument{Base: model.Base{ID: item.ID, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}, DebateID: debateID, ParentID: item.ReplyToID, UserID: item.AuthorID, User: briefUsersMap[item.AuthorID], Content: item.Content, ArgumentType: model.ArgumentType(item.ArgumentType), VoteCount: briefVoteMap[item.ID], SourceURL: item.SourceURL, SourceTitle: item.SourceTitle, SourceExcerpt: item.SourceExcerpt, Conclusion: item.Conclusion, IsConcluded: item.Conclusion != "", IsFolded: item.IsFolded || item.Status == "auto_folded", FoldNote: item.FoldNote}
		}
	}
	for _, ref := range refs {
		from, ok1 := indexByID[ref.CommentID]
		to, ok2 := indexByID[ref.ReferencedCommentID]
		if ok1 && ok2 {
			arguments[from].References = append(arguments[from].References, arguments[to])
		} else if ok1 {
			if brief, exists := briefMap[ref.ReferencedCommentID]; exists {
				arguments[from].References = append(arguments[from].References, brief)
			}
		}
	}
	var debateRefs []model.DebateArgumentDebateRef
	if len(ids) > 0 {
		if err := r.db.Where("comment_id IN ?", ids).Find(&debateRefs).Error; err != nil {
			return nil, 0, err
		}
	}
	debateIDs := make([]uuid.UUID, 0, len(debateRefs))
	for _, ref := range debateRefs {
		debateIDs = append(debateIDs, ref.DebateID)
	}
	var debates []model.Debate
	if len(debateIDs) > 0 {
		if err := r.db.Where("id IN ?", debateIDs).Find(&debates).Error; err != nil {
			return nil, 0, err
		}
	}
	debateMap := make(map[uuid.UUID]model.Debate, len(debates))
	for _, item := range debates {
		debateMap[item.ID] = item
	}
	for _, ref := range debateRefs {
		if index, ok := indexByID[ref.CommentID]; ok {
			if referenced, exists := debateMap[ref.DebateID]; exists {
				arguments[index].ReferencedDebates = append(arguments[index].ReferencedDebates, referenced)
			}
		}
	}
	return arguments, total, nil
}
