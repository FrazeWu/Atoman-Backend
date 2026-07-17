package migrations

import (
	"fmt"
	"sort"
	"time"

	"atoman/internal/model"
	commentmodule "atoman/internal/modules/comment"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const forumTopicTargetKind = "forum_topic"
const legacyReplyLookupBatchSize = 500

type legacyReplyMigrationTopic struct {
	model.Base
	UserID        uuid.UUID
	SolvedReplyID *uuid.UUID
}

func (legacyReplyMigrationTopic) TableName() string { return "forum_topics" }

type legacyReplyMigrationReply struct {
	model.Base
	TopicID       uuid.UUID
	UserID        uuid.UUID
	ParentReplyID *uuid.UUID
	Content       string
	FloorNumber   int
	IsSolved      bool
}

func (legacyReplyMigrationReply) TableName() string { return "forum_replies" }

type legacyReplyMigrationLike struct {
	model.Base
	UserID     uuid.UUID
	TargetType string
	TargetID   uuid.UUID
}

func (legacyReplyMigrationLike) TableName() string { return "forum_likes" }

// MigrateLegacyForumReplies copies legacy forum reply data into unified comments.
// Source tables intentionally remain untouched so the migration is reversible.
func MigrateLegacyForumReplies(db *gorm.DB) error {
	if !db.Migrator().HasTable("forum_replies") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var replies []legacyReplyMigrationReply
		if err := tx.Unscoped().Order("created_at, id").Find(&replies).Error; err != nil {
			return fmt.Errorf("load legacy forum replies: %w", err)
		}
		if len(replies) == 0 {
			_, err := migrateLegacyReplyLikes(tx, map[uuid.UUID]legacyReplyMigrationReply{}, nil)
			return err
		}

		byID := make(map[uuid.UUID]legacyReplyMigrationReply, len(replies))
		for _, reply := range replies {
			byID[reply.ID] = reply
		}
		if err := validateLegacyReplyGraph(replies, byID); err != nil {
			return err
		}

		topicIDs := make([]uuid.UUID, 0)
		seenTopics := make(map[uuid.UUID]bool)
		for _, reply := range replies {
			if !seenTopics[reply.TopicID] {
				seenTopics[reply.TopicID] = true
				topicIDs = append(topicIDs, reply.TopicID)
			}
		}
		topics, err := loadLegacyForumTopics(tx, topicIDs)
		if err != nil {
			return err
		}
		if len(topics) != len(topicIDs) {
			return fmt.Errorf("legacy forum reply references missing topic")
		}
		targetByTopic, err := findOrCreateForumTargets(tx, topics)
		if err != nil {
			return err
		}
		entriesByTarget, existingByID, err := loadForumTargetComments(tx, targetByTopic)
		if err != nil {
			return err
		}
		legacyExisting, err := loadLegacyCommentEntries(tx, replies)
		if err != nil {
			return err
		}
		for id, entry := range legacyExisting {
			existingByID[id] = entry
		}
		previouslyMigrated := previouslyMigratedForumTopics(replies, legacyExisting)

		solvedByTopic := chooseSolvedReplies(topics, replies)
		rootByReply := resolveLegacyReplyRoots(replies, byID, solvedByTopic)
		floorsByReply := assignLegacyRootFloors(replies, rootByReply, targetByTopic, entriesByTarget)
		roots, children := make([]model.CommentEntry, 0), make([]model.CommentEntry, 0)
		for _, reply := range replies {
			rootID, replyToID, replyToAuthorID := legacyReplyLinks(reply, byID, rootByReply)
			entry := model.CommentEntry{
				Base:            reply.Base,
				TargetID:        targetByTopic[reply.TopicID].ID,
				AuthorID:        reply.UserID,
				RootID:          rootID,
				ReplyToID:       replyToID,
				ReplyToAuthorID: replyToAuthorID,
				FloorNumber:     floorsByReply[reply.ID],
				Content:         reply.Content,
				ContentHash:     commentmodule.ContentHash(reply.Content, nil),
				Status:          "active",
			}
			if existing, ok := existingByID[entry.ID]; ok {
				if existing.TargetID != entry.TargetID || existing.AuthorID != entry.AuthorID {
					return fmt.Errorf("comment UUID collision for legacy reply %s", entry.ID)
				}
				continue
			}
			if entry.RootID == nil {
				roots = append(roots, entry)
			} else {
				children = append(children, entry)
			}
		}
		if err := createLegacyCommentsInBatches(tx, roots, children); err != nil {
			return err
		}
		for _, entry := range append(roots, children...) {
			entriesByTarget[entry.TargetID] = append(entriesByTarget[entry.TargetID], entry)
		}
		commentLikes, err := loadForumCommentLikes(tx, entriesByTarget)
		if err != nil {
			return err
		}
		commentLikes, err = migrateLegacyReplyLikes(tx, byID, commentLikes)
		if err != nil {
			return err
		}
		pinnedByTopic := make(map[uuid.UUID]*uuid.UUID, len(targetByTopic))
		for topicID, target := range targetByTopic {
			pinned := target.PinnedCommentID
			if !previouslyMigrated[topicID] && pinned == nil {
				pinned = solvedByTopic[topicID]
			}
			pinnedByTopic[topicID] = pinned
		}
		return recountMigratedForumTargets(tx, targetByTopic, pinnedByTopic, entriesByTarget, commentLikes)
	})
}

func loadLegacyForumTopics(tx *gorm.DB, ids []uuid.UUID) ([]legacyReplyMigrationTopic, error) {
	result := make([]legacyReplyMigrationTopic, 0, len(ids))
	for start := 0; start < len(ids); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(ids))
		var batch []legacyReplyMigrationTopic
		if err := tx.Unscoped().Where("id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load legacy forum topics: %w", err)
		}
		result = append(result, batch...)
	}
	return result, nil
}

func previouslyMigratedForumTopics(replies []legacyReplyMigrationReply, existing map[uuid.UUID]model.CommentEntry) map[uuid.UUID]bool {
	result := make(map[uuid.UUID]bool)
	for _, reply := range replies {
		if _, ok := existing[reply.ID]; ok {
			result[reply.TopicID] = true
		}
	}
	return result
}

func validateLegacyReplyGraph(replies []legacyReplyMigrationReply, byID map[uuid.UUID]legacyReplyMigrationReply) error {
	states := make(map[uuid.UUID]uint8, len(replies))
	var visit func(uuid.UUID) error
	visit = func(id uuid.UUID) error {
		if states[id] == 1 {
			return fmt.Errorf("cycle in legacy forum replies at %s", id)
		}
		if states[id] == 2 {
			return nil
		}
		states[id] = 1
		reply := byID[id]
		if reply.ParentReplyID != nil {
			parent, ok := byID[*reply.ParentReplyID]
			if !ok {
				return fmt.Errorf("orphan legacy forum reply parent %s", *reply.ParentReplyID)
			}
			if parent.TopicID != reply.TopicID {
				return fmt.Errorf("cross-topic legacy forum reply parent %s", parent.ID)
			}
			if err := visit(parent.ID); err != nil {
				return err
			}
		}
		states[id] = 2
		return nil
	}
	for _, reply := range replies {
		if err := visit(reply.ID); err != nil {
			return err
		}
	}
	return nil
}

func findOrCreateForumTargets(tx *gorm.DB, topics []legacyReplyMigrationTopic) (map[uuid.UUID]model.DiscussionTarget, error) {
	keys := make([]string, len(topics))
	for i, topic := range topics {
		keys[i] = topic.ID.String()
	}
	existing := make([]model.DiscussionTarget, 0, len(topics))
	for start := 0; start < len(keys); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(keys))
		var batch []model.DiscussionTarget
		if err := tx.Unscoped().Where("kind = ? AND resource_key IN ?", forumTopicTargetKind, keys[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load forum targets: %w", err)
		}
		existing = append(existing, batch...)
	}
	byKey := make(map[string]model.DiscussionTarget, len(existing))
	for _, target := range existing {
		byKey[target.ResourceKey] = target
	}
	missing := make([]model.DiscussionTarget, 0)
	for _, topic := range topics {
		if target, ok := byKey[topic.ID.String()]; ok {
			if target.ResourceID != topic.ID {
				return nil, fmt.Errorf("forum target collision for topic %s", topic.ID)
			}
			continue
		}
		ownerID := topic.UserID
		missing = append(missing, model.DiscussionTarget{Kind: forumTopicTargetKind, ResourceID: topic.ID, ResourceKey: topic.ID.String(), OwnerID: &ownerID, NextFloor: 1})
	}
	if len(missing) > 0 {
		if err := tx.CreateInBatches(&missing, legacyReplyLookupBatchSize).Error; err != nil {
			return nil, fmt.Errorf("create forum targets: %w", err)
		}
		for _, target := range missing {
			byKey[target.ResourceKey] = target
		}
	}
	result := make(map[uuid.UUID]model.DiscussionTarget, len(topics))
	for _, topic := range topics {
		result[topic.ID] = byKey[topic.ID.String()]
	}
	return result, nil
}

func loadForumTargetComments(tx *gorm.DB, targets map[uuid.UUID]model.DiscussionTarget) (map[uuid.UUID][]model.CommentEntry, map[uuid.UUID]model.CommentEntry, error) {
	targetIDs := make([]uuid.UUID, 0, len(targets))
	for _, target := range targets {
		targetIDs = append(targetIDs, target.ID)
	}
	byTarget := make(map[uuid.UUID][]model.CommentEntry, len(targets))
	byID := make(map[uuid.UUID]model.CommentEntry)
	for start := 0; start < len(targetIDs); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(targetIDs))
		var batch []model.CommentEntry
		if err := tx.Unscoped().Where("target_id IN ?", targetIDs[start:end]).Find(&batch).Error; err != nil {
			return nil, nil, fmt.Errorf("load forum target comments: %w", err)
		}
		for _, entry := range batch {
			byTarget[entry.TargetID] = append(byTarget[entry.TargetID], entry)
			byID[entry.ID] = entry
		}
	}
	return byTarget, byID, nil
}

func loadLegacyCommentEntries(tx *gorm.DB, replies []legacyReplyMigrationReply) (map[uuid.UUID]model.CommentEntry, error) {
	ids := make([]uuid.UUID, len(replies))
	for i, reply := range replies {
		ids[i] = reply.ID
	}
	result := make(map[uuid.UUID]model.CommentEntry)
	for start := 0; start < len(ids); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(ids))
		var batch []model.CommentEntry
		if err := tx.Unscoped().Where("id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load existing legacy comments: %w", err)
		}
		for _, entry := range batch {
			result[entry.ID] = entry
		}
	}
	return result, nil
}

func chooseSolvedReplies(topics []legacyReplyMigrationTopic, replies []legacyReplyMigrationReply) map[uuid.UUID]*uuid.UUID {
	byTopic := make(map[uuid.UUID][]legacyReplyMigrationReply)
	for _, reply := range replies {
		byTopic[reply.TopicID] = append(byTopic[reply.TopicID], reply)
	}
	result := make(map[uuid.UUID]*uuid.UUID, len(topics))
	for _, topic := range topics {
		if topic.SolvedReplyID != nil {
			if reply, ok := findLegacyReply(byTopic[topic.ID], *topic.SolvedReplyID); ok && !reply.DeletedAt.Valid {
				id := reply.ID
				result[topic.ID] = &id
				continue
			}
		}
		for _, reply := range byTopic[topic.ID] {
			if reply.IsSolved && !reply.DeletedAt.Valid {
				id := reply.ID
				result[topic.ID] = &id
				break
			}
		}
	}
	return result
}

func findLegacyReply(replies []legacyReplyMigrationReply, id uuid.UUID) (legacyReplyMigrationReply, bool) {
	for _, reply := range replies {
		if reply.ID == id {
			return reply, true
		}
	}
	return legacyReplyMigrationReply{}, false
}

func assignLegacyRootFloors(replies []legacyReplyMigrationReply, rootByReply map[uuid.UUID]uuid.UUID, targets map[uuid.UUID]model.DiscussionTarget, entries map[uuid.UUID][]model.CommentEntry) map[uuid.UUID]*int {
	result := make(map[uuid.UUID]*int)
	used := make(map[uuid.UUID]map[int]bool)
	next := make(map[uuid.UUID]int)
	for topicID, target := range targets {
		used[topicID] = make(map[int]bool)
		for _, entry := range entries[target.ID] {
			if entry.RootID != nil {
				continue
			}
			if entry.FloorNumber != nil && *entry.FloorNumber > 0 {
				used[topicID][*entry.FloorNumber] = true
				if *entry.FloorNumber >= next[topicID] {
					next[topicID] = *entry.FloorNumber + 1
				}
			}
		}
		if next[topicID] < 1 {
			next[topicID] = 1
		}
	}
	for _, reply := range replies {
		if rootByReply[reply.ID] != reply.ID {
			continue
		}
		floor := reply.FloorNumber
		if floor <= 0 || used[reply.TopicID][floor] {
			floor = next[reply.TopicID]
			for used[reply.TopicID][floor] {
				floor++
			}
		}
		used[reply.TopicID][floor] = true
		if floor >= next[reply.TopicID] {
			next[reply.TopicID] = floor + 1
		}
		value := floor
		result[reply.ID] = &value
	}
	return result
}

func legacyReplyLinks(reply legacyReplyMigrationReply, byID map[uuid.UUID]legacyReplyMigrationReply, rootByReply map[uuid.UUID]uuid.UUID) (*uuid.UUID, *uuid.UUID, *uuid.UUID) {
	if rootByReply[reply.ID] == reply.ID {
		return nil, nil, nil
	}
	parent := byID[*reply.ParentReplyID]
	rootID, parentID, parentAuthor := rootByReply[reply.ID], parent.ID, parent.UserID
	return &rootID, &parentID, &parentAuthor
}

func resolveLegacyReplyRoots(replies []legacyReplyMigrationReply, byID map[uuid.UUID]legacyReplyMigrationReply, solved map[uuid.UUID]*uuid.UUID) map[uuid.UUID]uuid.UUID {
	result := make(map[uuid.UUID]uuid.UUID, len(replies))
	var resolve func(uuid.UUID) uuid.UUID
	resolve = func(id uuid.UUID) uuid.UUID {
		if rootID, ok := result[id]; ok {
			return rootID
		}
		reply := byID[id]
		if isLegacyMigratedRoot(reply, byID, solved) {
			result[id] = id
			return id
		}
		rootID := resolve(*reply.ParentReplyID)
		result[id] = rootID
		return rootID
	}
	for _, reply := range replies {
		resolve(reply.ID)
	}
	return result
}

func isLegacyMigratedRoot(reply legacyReplyMigrationReply, byID map[uuid.UUID]legacyReplyMigrationReply, solved map[uuid.UUID]*uuid.UUID) bool {
	if reply.ParentReplyID == nil || solved[reply.TopicID] != nil && *solved[reply.TopicID] == reply.ID {
		return true
	}
	return !reply.DeletedAt.Valid && byID[*reply.ParentReplyID].DeletedAt.Valid
}

func createLegacyCommentsInBatches(tx *gorm.DB, roots, children []model.CommentEntry) error {
	if len(roots) > 0 {
		if err := tx.Unscoped().CreateInBatches(&roots, legacyReplyLookupBatchSize).Error; err != nil {
			return fmt.Errorf("migrate legacy forum roots: %w", err)
		}
	}
	if len(children) > 0 {
		if err := tx.Unscoped().CreateInBatches(&children, legacyReplyLookupBatchSize).Error; err != nil {
			return fmt.Errorf("migrate legacy forum child replies: %w", err)
		}
	}
	return nil
}

func loadForumCommentLikes(tx *gorm.DB, entries map[uuid.UUID][]model.CommentEntry) ([]model.CommentLike, error) {
	commentIDs := make([]uuid.UUID, 0)
	for _, targetEntries := range entries {
		for _, entry := range targetEntries {
			commentIDs = append(commentIDs, entry.ID)
		}
	}
	result := make([]model.CommentLike, 0)
	for start := 0; start < len(commentIDs); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(commentIDs))
		var batch []model.CommentLike
		if err := tx.Unscoped().Where("comment_id IN ?", commentIDs[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load forum comment likes: %w", err)
		}
		result = append(result, batch...)
	}
	return result, nil
}

func migrateLegacyReplyLikes(tx *gorm.DB, replies map[uuid.UUID]legacyReplyMigrationReply, existing []model.CommentLike) ([]model.CommentLike, error) {
	if !tx.Migrator().HasTable("forum_likes") {
		return existing, nil
	}
	var likes []legacyReplyMigrationLike
	if err := tx.Unscoped().Where("target_type = ?", "reply").Order("created_at, id").Find(&likes).Error; err != nil {
		return nil, fmt.Errorf("load legacy reply likes: %w", err)
	}
	byID := make(map[uuid.UUID]model.CommentLike, len(existing))
	allRelations := make(map[string]bool, len(existing))
	liveRelations := make(map[string]bool, len(existing))
	for _, like := range existing {
		byID[like.ID] = like
		key := commentLikeRelationKey(like.CommentID, like.UserID)
		allRelations[key] = true
		if !like.DeletedAt.Valid {
			liveRelations[key] = true
		}
	}
	legacyLikeIDs := make([]uuid.UUID, len(likes))
	for i, like := range likes {
		legacyLikeIDs[i] = like.ID
	}
	for start := 0; start < len(legacyLikeIDs); start += legacyReplyLookupBatchSize {
		end := min(start+legacyReplyLookupBatchSize, len(legacyLikeIDs))
		var batch []model.CommentLike
		if err := tx.Unscoped().Where("id IN ?", legacyLikeIDs[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load existing legacy reply likes: %w", err)
		}
		for _, like := range batch {
			byID[like.ID] = like
		}
	}
	missing := make([]model.CommentLike, 0)
	for _, like := range likes {
		if _, ok := replies[like.TargetID]; !ok {
			return nil, fmt.Errorf("orphan legacy reply like %s", like.ID)
		}
		if existingByID, ok := byID[like.ID]; ok {
			if existingByID.CommentID != like.TargetID || existingByID.UserID != like.UserID {
				return nil, fmt.Errorf("comment like UUID collision for legacy like %s", like.ID)
			}
			continue
		}
		key := commentLikeRelationKey(like.TargetID, like.UserID)
		if like.DeletedAt.Valid && allRelations[key] || !like.DeletedAt.Valid && liveRelations[key] {
			continue
		}
		missing = append(missing, model.CommentLike{Base: like.Base, CommentID: like.TargetID, UserID: like.UserID})
		allRelations[key] = true
		if !like.DeletedAt.Valid {
			liveRelations[key] = true
		}
	}
	if len(missing) > 0 {
		if err := tx.Unscoped().CreateInBatches(&missing, legacyReplyLookupBatchSize).Error; err != nil {
			return nil, fmt.Errorf("migrate legacy reply likes: %w", err)
		}
	}
	return append(existing, missing...), nil
}

func commentLikeRelationKey(commentID, userID uuid.UUID) string {
	return commentID.String() + ":" + userID.String()
}

func recountMigratedForumTargets(tx *gorm.DB, targets map[uuid.UUID]model.DiscussionTarget, pinnedByTopic map[uuid.UUID]*uuid.UUID, entriesByTarget map[uuid.UUID][]model.CommentEntry, likes []model.CommentLike) error {
	visible := []string{"active", "auto_folded"}
	now := time.Now()
	likeCounts := make(map[uuid.UUID]int)
	for _, like := range likes {
		if !like.DeletedAt.Valid {
			likeCounts[like.CommentID]++
		}
	}
	commentUpdates := make([]model.CommentEntry, 0)
	targetUpdates := make([]model.DiscussionTarget, 0, len(targets))
	for topicID, target := range targets {
		entries := entriesByTarget[target.ID]
		rootReplies := make(map[uuid.UUID]int)
		rootChildLikes := make(map[uuid.UUID]int)
		visibleRoots := make(map[uuid.UUID]bool)
		commentCount, rootCount, maxFloor := 0, 0, 0
		for _, entry := range entries {
			if entry.RootID == nil && entry.FloorNumber != nil && *entry.FloorNumber > maxFloor {
				maxFloor = *entry.FloorNumber
			}
			if entry.RootID == nil && !entry.DeletedAt.Valid && containsString(visible, entry.Status) {
				visibleRoots[entry.ID] = true
			}
		}
		for _, entry := range entries {
			if entry.DeletedAt.Valid || !containsString(visible, entry.Status) {
				continue
			}
			if entry.RootID == nil {
				commentCount++
				rootCount++
			} else if visibleRoots[*entry.RootID] {
				commentCount++
				rootReplies[*entry.RootID]++
				rootChildLikes[*entry.RootID] += likeCounts[entry.ID]
			}
		}
		for i := range entries {
			entries[i].LikeCount = likeCounts[entries[i].ID]
			entries[i].ReplyCount = rootReplies[entries[i].ID]
			if entries[i].RootID == nil {
				entries[i].HotScore = commentmodule.HotScore(entries[i].LikeCount, rootChildLikes[entries[i].ID], entries[i].ReplyCount, now.Sub(entries[i].CreatedAt))
			}
			commentUpdates = append(commentUpdates, entries[i])
		}
		pinned := pinnedByTopic[topicID]
		if pinned != nil {
			entry, ok := findCommentEntry(entries, *pinned)
			if !ok || entry.RootID != nil || entry.DeletedAt.Valid || !containsString(visible, entry.Status) {
				pinned = nil
			}
		}
		target.CommentCount = commentCount
		target.RootCount = rootCount
		target.NextFloor = maxFloor + 1
		target.PinnedCommentID = pinned
		if maxFloor == 0 {
			target.NextFloor = 1
		}
		targetUpdates = append(targetUpdates, target)
	}
	if err := writeTargetAggregates(tx, targetUpdates); err != nil {
		return err
	}
	return writeCommentAggregates(tx, commentUpdates)
}

func writeTargetAggregates(tx *gorm.DB, targets []model.DiscussionTarget) error {
	if len(targets) == 0 {
		return nil
	}
	if err := tx.Unscoped().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"comment_count", "root_count", "next_floor", "pinned_comment_id"}),
	}).CreateInBatches(&targets, legacyReplyLookupBatchSize).Error; err != nil {
		return fmt.Errorf("write forum target aggregates: %w", err)
	}
	return nil
}

func writeCommentAggregates(tx *gorm.DB, entries []model.CommentEntry) error {
	if len(entries) == 0 {
		return nil
	}
	// Floor is not part of the update and clearing it in the insert candidate avoids
	// selecting the partial floor unique index instead of the primary-key conflict.
	for i := range entries {
		entries[i].FloorNumber = nil
	}
	if err := tx.Unscoped().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"like_count", "reply_count", "hot_score"}),
	}).CreateInBatches(&entries, legacyReplyLookupBatchSize).Error; err != nil {
		return fmt.Errorf("write forum comment aggregates: %w", err)
	}
	return nil
}

func findCommentEntry(entries []model.CommentEntry, id uuid.UUID) (model.CommentEntry, bool) {
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return model.CommentEntry{}, false
}

func containsString(values []string, target string) bool {
	i := sort.SearchStrings(values, target)
	return i < len(values) && values[i] == target
}
