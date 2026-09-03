package feed

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SubscriptionHubTypePodcast = "podcast"
	SubscriptionHubTypeVideo   = "video"
	SubscriptionHubTypeBlog    = "blog"
	SubscriptionHubTypeRSS     = "rss"
)

var subscriptionHubTypes = []string{
	SubscriptionHubTypePodcast,
	SubscriptionHubTypeVideo,
	SubscriptionHubTypeBlog,
	SubscriptionHubTypeRSS,
}

type SubscriptionHubTree struct {
	Types []SubscriptionHubTypeNode `json:"types"`
}

type SubscriptionHubTypeNode struct {
	SubscriptionType string                     `json:"subscription_type"`
	HasContent       bool                       `json:"has_content"`
	Groups           []SubscriptionHubGroupNode `json:"groups"`
}

type SubscriptionHubGroupNode struct {
	model.SubscriptionHubGroup
	Memberships []model.SubscriptionHubMembership `json:"memberships"`
}

func (tree SubscriptionHubTree) Group(subscriptionType string, groupID uuid.UUID) *SubscriptionHubGroupNode {
	for typeIndex := range tree.Types {
		if tree.Types[typeIndex].SubscriptionType != subscriptionType {
			continue
		}
		for groupIndex := range tree.Types[typeIndex].Groups {
			group := &tree.Types[typeIndex].Groups[groupIndex]
			if group.ID == groupID {
				return group
			}
		}
	}
	return nil
}

type SubscriptionHubUpdatesQuery struct {
	SubscriptionType string
	GroupID          uuid.UUID
	MembershipID     uuid.UUID
	Page             int
	PageSize         int
}

func isSubscriptionHubType(value string) bool {
	for _, subscriptionType := range subscriptionHubTypes {
		if value == subscriptionType {
			return true
		}
	}
	return false
}

func subscriptionHubContentType(subscriptionType string) string {
	if subscriptionType == SubscriptionHubTypeRSS {
		return ""
	}
	return subscriptionType
}

func subscriptionHubSourceMatchesType(subscriptionType string, source *model.FeedSource) bool {
	if source == nil {
		return false
	}
	if subscriptionType == SubscriptionHubTypeRSS {
		return source.SourceType == "external_rss"
	}
	return source.SourceType == "internal_user" || source.SourceType == "internal_channel" || source.SourceType == "internal_collection"
}

func subscriptionHubTypesForLegacySource(source *model.FeedSource) []string {
	if source != nil && source.SourceType == "external_rss" {
		return []string{SubscriptionHubTypeRSS}
	}
	if source != nil {
		switch source.SourceType {
		case "internal_user", "internal_channel", "internal_collection":
			return []string{SubscriptionHubTypePodcast, SubscriptionHubTypeVideo, SubscriptionHubTypeBlog}
		}
	}
	return []string{SubscriptionHubTypeBlog}
}

func (s *Service) ensureLegacySubscriptionHubContexts(userID uuid.UUID) error {
	type groupKey struct {
		subscriptionType string
		name             string
	}
	type membershipKey struct {
		subscriptionType string
		feedSourceID     uuid.UUID
	}
	type legacyContextCandidate struct {
		subscriptionType string
		groupName        string
		groupPosition    int
		feedSource       *model.FeedSource
		title            string
		position         int
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var subscriptions []model.Subscription
		if err := tx.Preload("FeedSource").Preload("SubscriptionGroup").Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			return err
		}
		candidates := make([]legacyContextCandidate, 0, len(subscriptions))
		for _, subscription := range subscriptions {
			if subscription.FeedSource == nil {
				continue
			}
			groupName := defaultSubscriptionGroupName
			groupPosition := 0
			if subscription.SubscriptionGroup != nil && strings.TrimSpace(subscription.SubscriptionGroup.Name) != "" {
				groupName = subscription.SubscriptionGroup.Name
				groupPosition = subscription.SubscriptionGroup.Position
			}
			for _, subscriptionType := range subscriptionHubTypesForLegacySource(subscription.FeedSource) {
				candidates = append(candidates, legacyContextCandidate{
					subscriptionType: subscriptionType,
					groupName:        groupName,
					groupPosition:    groupPosition,
					feedSource:       subscription.FeedSource,
					title:            firstNonBlank(subscription.Title, subscription.FeedSource.Title),
					position:         subscription.Position,
				})
			}
		}

		var channelBookmarks []model.ChannelBookmark
		if err := tx.Where("user_id = ? AND kind IN ?", userID, []string{"podcast_show", "video_channel"}).Order("created_at ASC").Find(&channelBookmarks).Error; err != nil {
			return err
		}
		if len(channelBookmarks) > 0 {
			channelIDs := make([]uuid.UUID, 0, len(channelBookmarks))
			for _, bookmark := range channelBookmarks {
				channelIDs = append(channelIDs, bookmark.ChannelID)
			}
			channelIDs = dedupeUUIDs(channelIDs)

			var channels []model.Channel
			if err := tx.Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
				return err
			}
			channelsByID := make(map[uuid.UUID]model.Channel, len(channels))
			for _, channel := range channels {
				channelsByID[channel.ID] = channel
			}

			var sources []model.FeedSource
			if err := tx.Where("source_type = ? AND source_id IN ?", "internal_channel", channelIDs).Find(&sources).Error; err != nil {
				return err
			}
			sourcesByChannelID := make(map[uuid.UUID]model.FeedSource, len(sources))
			for _, source := range sources {
				if source.SourceID != nil {
					sourcesByChannelID[*source.SourceID] = source
				}
			}
			sourcesToCreate := make([]model.FeedSource, 0)
			for _, channelID := range channelIDs {
				if _, exists := sourcesByChannelID[channelID]; exists {
					continue
				}
				channel, exists := channelsByID[channelID]
				if !exists {
					continue
				}
				sourcesToCreate = append(sourcesToCreate, model.FeedSource{
					SourceType: "internal_channel",
					SourceID:   &channelID,
					Provider:   "internal",
					Category:   "blog",
					Hash:       buildFeedSourceHash("internal_channel", &channelID, ""),
					Title:      channel.Name,
				})
			}
			if len(sourcesToCreate) > 0 {
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "hash"}}, DoNothing: true}).Create(&sourcesToCreate).Error; err != nil {
					return err
				}
				if err := tx.Where("source_type = ? AND source_id IN ?", "internal_channel", channelIDs).Find(&sources).Error; err != nil {
					return err
				}
				for _, source := range sources {
					if source.SourceID != nil {
						sourcesByChannelID[*source.SourceID] = source
					}
				}
			}
			for position, bookmark := range channelBookmarks {
				source, exists := sourcesByChannelID[bookmark.ChannelID]
				if !exists {
					continue
				}
				subscriptionType := SubscriptionHubTypePodcast
				if bookmark.Kind == "video_channel" {
					subscriptionType = SubscriptionHubTypeVideo
				}
				candidates = append(candidates, legacyContextCandidate{
					subscriptionType: subscriptionType,
					groupName:        defaultSubscriptionGroupName,
					feedSource:       &source,
					title:            source.Title,
					position:         position,
				})
			}
		}

		desiredGroups := make([]groupKey, 0)
		desiredGroupSet := make(map[groupKey]struct{})
		desiredGroupPositions := make(map[groupKey]int)
		desiredMemberships := make(map[membershipKey]legacyContextCandidate)
		for _, candidate := range candidates {
			key := groupKey{subscriptionType: candidate.subscriptionType, name: candidate.groupName}
			if _, exists := desiredGroupSet[key]; !exists {
				desiredGroupSet[key] = struct{}{}
				desiredGroups = append(desiredGroups, key)
				desiredGroupPositions[key] = candidate.groupPosition
			}
			membershipKey := membershipKey{subscriptionType: candidate.subscriptionType, feedSourceID: candidate.feedSource.ID}
			if _, exists := desiredMemberships[membershipKey]; !exists {
				desiredMemberships[membershipKey] = candidate
			}
		}

		var groups []model.SubscriptionHubGroup
		if err := tx.Where("user_id = ?", userID).Find(&groups).Error; err != nil {
			return err
		}
		groupsByKey := make(map[groupKey]model.SubscriptionHubGroup, len(groups))
		for _, group := range groups {
			key := groupKey{subscriptionType: group.SubscriptionType, name: group.Name}
			groupsByKey[key] = group
			if position, desired := desiredGroupPositions[key]; desired && group.Position != position {
				if err := tx.Model(&model.SubscriptionHubGroup{}).Where("id = ?", group.ID).Update("position", position).Error; err != nil {
					return err
				}
			}
		}
		groupsToCreate := make([]model.SubscriptionHubGroup, 0)
		for _, key := range desiredGroups {
			if _, exists := groupsByKey[key]; exists {
				continue
			}
			groupsToCreate = append(groupsToCreate, model.SubscriptionHubGroup{
				UserID:           userID,
				SubscriptionType: key.subscriptionType,
				Name:             key.name,
				Position:         desiredGroupPositions[key],
			})
		}
		if len(groupsToCreate) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:     []clause.Column{{Name: "user_id"}, {Name: "subscription_type"}, {Name: "name"}},
				TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil}}},
				DoNothing:   true,
			}).Create(&groupsToCreate).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("user_id = ?", userID).Find(&groups).Error; err != nil {
			return err
		}
		groupsByKey = make(map[groupKey]model.SubscriptionHubGroup, len(groups))
		for _, group := range groups {
			groupsByKey[groupKey{subscriptionType: group.SubscriptionType, name: group.Name}] = group
		}

		var existingMemberships []model.SubscriptionHubMembership
		if err := tx.Where("user_id = ?", userID).Find(&existingMemberships).Error; err != nil {
			return err
		}
		existingByKey := make(map[membershipKey]model.SubscriptionHubMembership, len(existingMemberships))
		staleMembershipIDs := make([]uuid.UUID, 0)
		for _, membership := range existingMemberships {
			key := membershipKey{subscriptionType: membership.SubscriptionType, feedSourceID: membership.FeedSourceID}
			existingByKey[key] = membership
			candidate, desired := desiredMemberships[key]
			if !desired {
				staleMembershipIDs = append(staleMembershipIDs, membership.ID)
				continue
			}
			group := groupsByKey[groupKey{subscriptionType: candidate.subscriptionType, name: candidate.groupName}]
			updates := map[string]any{}
			if membership.GroupID != group.ID {
				updates["group_id"] = group.ID
			}
			if membership.Title != candidate.title {
				updates["title"] = candidate.title
			}
			if membership.Position != candidate.position {
				updates["position"] = candidate.position
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.SubscriptionHubMembership{}).Where("id = ?", membership.ID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		if len(staleMembershipIDs) > 0 {
			if err := tx.Where("id IN ?", staleMembershipIDs).Delete(&model.SubscriptionHubMembership{}).Error; err != nil {
				return err
			}
		}
		membershipsToCreate := make([]model.SubscriptionHubMembership, 0)
		for key, candidate := range desiredMemberships {
			if candidate.feedSource == nil {
				continue
			}
			if _, exists := existingByKey[key]; exists {
				continue
			}
			group, exists := groupsByKey[groupKey{subscriptionType: candidate.subscriptionType, name: candidate.groupName}]
			if !exists {
				return apperr.Internal(errors.New("subscription hub group was not created"))
			}
			membershipsToCreate = append(membershipsToCreate, model.SubscriptionHubMembership{
				UserID:           userID,
				SubscriptionType: candidate.subscriptionType,
				GroupID:          group.ID,
				FeedSourceID:     candidate.feedSource.ID,
				Title:            candidate.title,
				Position:         candidate.position,
			})
		}
		if len(membershipsToCreate) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:     []clause.Column{{Name: "user_id"}, {Name: "subscription_type"}, {Name: "feed_source_id"}},
				TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil}}},
				DoNothing:   true,
			}).Create(&membershipsToCreate).Error; err != nil {
				return err
			}
		}

		staleGroupIDs := make([]uuid.UUID, 0)
		for _, group := range groups {
			key := groupKey{subscriptionType: group.SubscriptionType, name: group.Name}
			if _, desired := desiredGroupSet[key]; !desired {
				staleGroupIDs = append(staleGroupIDs, group.ID)
			}
		}
		if len(staleGroupIDs) > 0 {
			if err := tx.Where("id IN ?", staleGroupIDs).Delete(&model.SubscriptionHubGroup{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) GetSubscriptionHubTree(userID uuid.UUID) (SubscriptionHubTree, error) {
	if err := s.ensureLegacySubscriptionHubContexts(userID); err != nil {
		return SubscriptionHubTree{}, err
	}
	tree := SubscriptionHubTree{Types: make([]SubscriptionHubTypeNode, 0, len(subscriptionHubTypes))}
	typeIndexes := make(map[string]int, len(subscriptionHubTypes))
	for _, subscriptionType := range subscriptionHubTypes {
		tree.Types = append(tree.Types, SubscriptionHubTypeNode{
			SubscriptionType: subscriptionType,
			Groups:           []SubscriptionHubGroupNode{},
		})
		typeIndexes[subscriptionType] = len(tree.Types) - 1
	}

	var groups []model.SubscriptionHubGroup
	if err := s.db.Where("user_id = ?", userID).Order("subscription_type ASC, position ASC, created_at ASC").Find(&groups).Error; err != nil {
		return SubscriptionHubTree{}, err
	}

	type groupLocation struct{ typeIndex, groupIndex int }
	groupsByID := make(map[uuid.UUID]groupLocation, len(groups))
	for _, group := range groups {
		typeIndex, ok := typeIndexes[group.SubscriptionType]
		if !ok {
			continue
		}
		tree.Types[typeIndex].Groups = append(tree.Types[typeIndex].Groups, SubscriptionHubGroupNode{
			SubscriptionHubGroup: group,
			Memberships:          []model.SubscriptionHubMembership{},
		})
		groupsByID[group.ID] = groupLocation{
			typeIndex:  typeIndex,
			groupIndex: len(tree.Types[typeIndex].Groups) - 1,
		}
	}

	var memberships []model.SubscriptionHubMembership
	if err := s.db.Preload("FeedSource").
		Where("user_id = ?", userID).
		Order("subscription_type ASC, group_id ASC, position ASC, created_at ASC").
		Find(&memberships).Error; err != nil {
		return SubscriptionHubTree{}, err
	}
	if err := s.hydrateSubscriptionHubSourceImages(memberships); err != nil {
		return SubscriptionHubTree{}, err
	}
	for _, membership := range memberships {
		location, ok := groupsByID[membership.GroupID]
		if !ok {
			continue
		}
		group := &tree.Types[location.typeIndex].Groups[location.groupIndex]
		if group.SubscriptionType != membership.SubscriptionType {
			continue
		}
		group.Memberships = append(group.Memberships, membership)
	}

	for typeIndex := range tree.Types {
		membershipsForType := make([]model.SubscriptionHubMembership, 0)
		for groupIndex := range tree.Types[typeIndex].Groups {
			membershipsForType = append(membershipsForType, tree.Types[typeIndex].Groups[groupIndex].Memberships...)
		}
		_, total, err := s.getSubscriptionHubTimeline(userID, membershipsForType, FeedQuery{
			Page:        1,
			PageSize:    1,
			ContentType: subscriptionHubContentType(tree.Types[typeIndex].SubscriptionType),
		})
		if err != nil {
			return SubscriptionHubTree{}, err
		}
		tree.Types[typeIndex].HasContent = total > 0
	}

	return tree, nil
}

func (s *Service) hydrateSubscriptionHubSourceImages(memberships []model.SubscriptionHubMembership) error {
	userIDs := make([]uuid.UUID, 0)
	channelIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	for _, membership := range memberships {
		if membership.FeedSource == nil || membership.FeedSource.SourceID == nil {
			continue
		}
		switch membership.FeedSource.SourceType {
		case "internal_user":
			userIDs = append(userIDs, *membership.FeedSource.SourceID)
		case "internal_channel":
			channelIDs = append(channelIDs, *membership.FeedSource.SourceID)
		case "internal_collection":
			collectionIDs = append(collectionIDs, *membership.FeedSource.SourceID)
		}
	}

	images := make(map[uuid.UUID]string)
	if userIDs = dedupeUUIDs(userIDs); len(userIDs) > 0 {
		var users []model.User
		if err := s.db.Where("uuid IN ?", userIDs).Find(&users).Error; err != nil {
			return err
		}
		for _, user := range users {
			images[user.UUID] = strings.TrimSpace(user.AvatarURL)
		}
	}
	if channelIDs = dedupeUUIDs(channelIDs); len(channelIDs) > 0 {
		var channels []model.Channel
		if err := s.db.Preload("User").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			image := strings.TrimSpace(channel.CoverURL)
			if image == "" && channel.User != nil {
				image = strings.TrimSpace(channel.User.AvatarURL)
			}
			images[channel.ID] = image
		}
	}
	if collectionIDs = dedupeUUIDs(collectionIDs); len(collectionIDs) > 0 {
		var collections []model.ContentCollection
		if err := s.db.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return err
		}
		for _, collection := range collections {
			images[collection.ID] = strings.TrimSpace(collection.CoverURL)
		}
		var legacyCollections []model.Collection
		if err := s.db.Where("id IN ?", collectionIDs).Find(&legacyCollections).Error; err != nil {
			return err
		}
		for _, collection := range legacyCollections {
			if images[collection.ID] == "" {
				images[collection.ID] = strings.TrimSpace(collection.CoverURL)
			}
		}
	}

	for index := range memberships {
		source := memberships[index].FeedSource
		if source == nil || source.SourceID == nil {
			continue
		}
		if image := images[*source.SourceID]; image != "" {
			source.CoverURL = image
		}
	}
	return nil
}

func (s *Service) DeleteSubscriptionHubSource(userID, feedSourceID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var source model.FeedSource
		if err := tx.First(&source, "id = ?", feedSourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("subscription_hub.source_not_found", "Subscription source not found")
			}
			return err
		}
		if err := tx.Where("user_id = ? AND feed_source_id = ?", userID, feedSourceID).Delete(&model.Subscription{}).Error; err != nil {
			return err
		}
		if source.SourceType == "internal_channel" && source.SourceID != nil {
			if err := tx.Where("user_id = ? AND channel_id = ? AND kind IN ?", userID, *source.SourceID, []string{"podcast_show", "video_channel"}).Delete(&model.ChannelBookmark{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("user_id = ? AND feed_source_id = ?", userID, feedSourceID).Delete(&model.SubscriptionHubMembership{}).Error
	})
}

func (s *Service) GetSubscriptionHubUpdates(user authctx.CurrentUser, query SubscriptionHubUpdatesQuery) ([]TimelineItemDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Authentication is required")
	}
	if err := s.ensureLegacySubscriptionHubContexts(user.ID); err != nil {
		return nil, 0, err
	}
	query.SubscriptionType = strings.ToLower(strings.TrimSpace(query.SubscriptionType))
	if !isSubscriptionHubType(query.SubscriptionType) {
		return nil, 0, apperr.BadRequest("subscription_hub.invalid_type", "subscription type must be podcast, video, blog, or rss")
	}

	db := s.db.Where("user_id = ? AND subscription_type = ?", user.ID, query.SubscriptionType)
	if query.GroupID != uuid.Nil {
		var group model.SubscriptionHubGroup
		err := s.db.Where("id = ? AND user_id = ? AND subscription_type = ?", query.GroupID, user.ID, query.SubscriptionType).First(&group).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, 0, apperr.NotFound("subscription_hub.group_not_found", "Subscription group not found")
			}
			return nil, 0, err
		}
		db = db.Where("group_id = ?", query.GroupID)
	}
	if query.MembershipID != uuid.Nil {
		db = db.Where("id = ?", query.MembershipID)
	}

	var memberships []model.SubscriptionHubMembership
	if err := db.Where("user_id = ?", user.ID).Preload("FeedSource").Order("position ASC, created_at ASC").Find(&memberships).Error; err != nil {
		return nil, 0, err
	}
	for _, membership := range memberships {
		if !subscriptionHubSourceMatchesType(query.SubscriptionType, membership.FeedSource) {
			return nil, 0, apperr.BadRequest("subscription_hub.invalid_source", "subscription source does not match its type")
		}
	}

	feedQuery := FeedQuery{
		Page:        normalizedPage(query.Page),
		PageSize:    normalizedPageSize(query.PageSize),
		ContentType: subscriptionHubContentType(query.SubscriptionType),
	}
	return s.getSubscriptionHubTimeline(user.ID, memberships, feedQuery)
}

func (s *Service) getSubscriptionHubTimeline(userID uuid.UUID, memberships []model.SubscriptionHubMembership, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	if len(memberships) == 0 {
		return []TimelineItemDTO{}, 0, nil
	}

	userIDs := make([]uuid.UUID, 0)
	channelIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	feedSourceIDs := make([]uuid.UUID, 0)
	for _, membership := range memberships {
		source := membership.FeedSource
		if source == nil || source.SourceID == nil && source.SourceType != "external_rss" {
			continue
		}
		switch source.SourceType {
		case "internal_user":
			userIDs = append(userIDs, *source.SourceID)
		case "internal_channel":
			channelIDs = append(channelIDs, *source.SourceID)
		case "internal_collection":
			collectionIDs = append(collectionIDs, *source.SourceID)
		case "external_rss":
			feedSourceIDs = append(feedSourceIDs, source.ID)
		}
	}

	userIDs = dedupeUUIDs(userIDs)
	channelIDs = dedupeUUIDs(channelIDs)
	collectionIDs = dedupeUUIDs(collectionIDs)
	feedSourceIDs = dedupeUUIDs(feedSourceIDs)
	if query.ContentType == "blog" {
		return s.getSubscribedBlogFeed(userID, userIDs, channelIDs, collectionIDs, query)
	}
	if len(userIDs) == 0 && len(channelIDs) == 0 && len(collectionIDs) == 0 && !query.HideDuplicates && strings.TrimSpace(query.Search) == "" {
		return s.getSubscribedExternalFeed(userID, feedSourceIDs, query, map[uuid.UUID]time.Time{})
	}

	posts := make([]model.Post, 0)
	userPosts, err := s.repo.ListPublishedPostsByUserIDs(userIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, userPosts...)
	channelPosts, err := s.repo.ListPublishedPostsByChannelIDs(channelIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, channelPosts...)
	collectionPosts, err := s.repo.ListPublishedPostsByCollectionIDs(collectionIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, collectionPosts...)
	posts = dedupePosts(posts)
	posts = filterVisibleSubscribedPosts(posts, userID, userIDs, channelIDs, collectionIDs)

	postIDs := make([]uuid.UUID, 0, len(posts))
	for i := range posts {
		postIDs = append(postIDs, posts[i].ID)
	}
	engagementCounts, err := s.repo.ListPostEngagementCounts(postIDs)
	if err != nil {
		return nil, 0, err
	}
	engagementByPostID := make(map[uuid.UUID]PostEngagementCount, len(engagementCounts))
	for _, count := range engagementCounts {
		engagementByPostID[count.PostID] = count
	}
	episodes, err := s.repo.ListPodcastEpisodesByPostIDs(postIDs)
	if err != nil {
		return nil, 0, err
	}
	episodeByPostID := make(map[uuid.UUID]model.PodcastEpisode, len(episodes))
	for _, episode := range episodes {
		episodeByPostID[episode.PostID] = episode
	}
	videos, err := s.repo.ListPublishedVideosByScope(userIDs, channelIDs, collectionIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	videos = dedupeVideos(videos)
	feedItems, err := s.repo.ListFeedItemsBySourceIDs(feedSourceIDs, map[uuid.UUID]time.Time{})
	if err != nil {
		return nil, 0, err
	}
	readMap, err := s.readMap(userID, feedItems)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(videos)+len(feedItems))
	for i := range posts {
		if episode, ok := episodeByPostID[posts[i].ID]; ok {
			episode.Post = &posts[i]
			items = append(items, TimelineItemDTO{Type: "podcast_episode", PodcastEpisode: &episode, PublishedAt: postTimelinePublishedAt(posts[i])})
			continue
		}
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], engagementByPostID[posts[i].ID]), PublishedAt: postTimelinePublishedAt(posts[i])})
	}
	for i := range videos {
		items = append(items, TimelineItemDTO{Type: "video", Video: &videos[i], PublishedAt: videos[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt, IsRead: feedItemClusterRead(feedItems[i], readMap)})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, total := paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
	return paged, total, nil
}
