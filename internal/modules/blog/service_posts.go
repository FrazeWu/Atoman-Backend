package blog

import (
	"errors"
	stdhtml "html"
	"math"
	"regexp"
	"strings"
	"time"

	"atoman/internal/feedlanguage"
	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"golang.org/x/net/html"
	"gorm.io/gorm"
)

var (
	markdownImagePattern      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	markdownLinkPattern       = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	markdownLinePrefixPattern = regexp.MustCompile(`(?m)^\s{0,3}(?:#{1,6}|>|[-+*])\s*`)
)

var allowedPostStatuses = map[string]struct{}{
	"draft":     {},
	"published": {},
}

var allowedPostVisibilities = map[string]struct{}{
	"public":    {},
	"followers": {},
	"private":   {},
}

func (s *Service) syncBlogContentReferences(tx *gorm.DB, content BlogContent) ([]reference.ResolvedReference, error) {
	if content.Status != "published" {
		return nil, s.references.RemoveSource(tx, "post", content.ID)
	}
	audience := ""
	if content.Visibility == "" || content.Visibility == "public" {
		audience = reference.AudiencePublic
	}
	return s.references.ReplacePublished(tx, reference.Source{
		Type: "post", ID: content.ID, ActorID: content.UserID, Audience: audience,
		Meta: map[string]interface{}{"module": "blog", "path": "/post/" + content.ID.String()},
	}, []reference.Field{{Name: "content", Content: content.Content}})
}

func (s *Service) blogContentDTOs(db *gorm.DB, contents []BlogContent, viewerID *uuid.UUID) ([]BlogContentDTO, error) {
	dtos := make([]BlogContentDTO, len(contents))
	ids := make([]uuid.UUID, 0, len(contents))
	for index, content := range contents {
		dtos[index] = newBlogContentDTOFromCanonical(content)
		ids = append(ids, content.ID)
	}
	if len(ids) == 0 {
		return dtos, nil
	}
	var rows []model.ContentReference
	if err := db.Where("source_type = ? AND source_id IN ?", "post", ids).Order("source_id ASC, source_field ASC, start_offset ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	viewer := reference.Viewer{}
	if viewerID != nil {
		viewer.UserID = *viewerID
	}
	resolved, err := s.references.ResolveStoredRows(db, viewer, rows)
	if err != nil {
		return nil, err
	}
	for index := range dtos {
		if items, ok := resolved[dtos[index].ID]; ok {
			dtos[index].References = items
		}
	}
	if err := s.populatePostRatings(db, dtos, viewerID); err != nil {
		return nil, err
	}
	if err := s.populatePostWeightedRatings(db, dtos); err != nil {
		return nil, err
	}
	return dtos, nil
}

func (s *Service) populatePostRatings(db *gorm.DB, dtos []BlogContentDTO, viewerID *uuid.UUID) error {
	if len(dtos) == 0 || !db.Migrator().HasTable(&model.PostRating{}) {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(dtos))
	for _, dto := range dtos {
		ids = append(ids, dto.ID)
	}

	type ratingAggregate struct {
		ContentID   uuid.UUID `gorm:"column:content_id"`
		RatingScore float64   `gorm:"column:rating_score"`
		RatingCount int64     `gorm:"column:rating_count"`
	}
	var aggregates []ratingAggregate
	if err := db.Model(&model.PostRating{}).
		Select("content_id, AVG(score) AS rating_score, COUNT(*) AS rating_count").
		Where("content_id IN ?", ids).
		Group("content_id").
		Scan(&aggregates).Error; err != nil {
		return err
	}
	aggregatesByContentID := make(map[uuid.UUID]ratingAggregate, len(aggregates))
	for _, aggregate := range aggregates {
		aggregatesByContentID[aggregate.ContentID] = aggregate
	}

	viewerRatings := make(map[uuid.UUID]int, 0)
	if viewerID != nil {
		var ratings []model.PostRating
		if err := db.Where("user_id = ? AND content_id IN ?", *viewerID, ids).Find(&ratings).Error; err != nil {
			return err
		}
		viewerRatings = make(map[uuid.UUID]int, len(ratings))
		for _, rating := range ratings {
			viewerRatings[rating.ContentID] = rating.Score
		}
	}

	for index := range dtos {
		aggregate := aggregatesByContentID[dtos[index].ID]
		dtos[index].RatingScore = math.Round(aggregate.RatingScore*10) / 10
		dtos[index].RatingCount = aggregate.RatingCount
		if score, ok := viewerRatings[dtos[index].ID]; ok {
			value := score
			dtos[index].ViewerRating = &value
		}
	}
	return nil
}

func (s *Service) populatePostWeightedRatings(db *gorm.DB, dtos []BlogContentDTO) error {
	if len(dtos) == 0 || !db.Migrator().HasTable(&model.ReputationRun{}) || !db.Migrator().HasTable(&model.BlogQualitySnapshot{}) {
		return nil
	}
	var run model.ReputationRun
	if err := db.Where("status = ?", model.ReputationRunPublished).Order("published_at DESC").First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	ids := make([]uuid.UUID, 0, len(dtos))
	for _, dto := range dtos {
		ids = append(ids, dto.ID)
	}
	var snapshots []model.BlogQualitySnapshot
	if err := db.Where("run_id = ? AND post_id IN ?", run.ID, ids).Find(&snapshots).Error; err != nil {
		return err
	}
	byPostID := make(map[uuid.UUID]model.BlogQualitySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byPostID[snapshot.PostID] = snapshot
	}
	for index := range dtos {
		snapshot, ok := byPostID[dtos[index].ID]
		if !ok {
			continue
		}
		dtos[index].WeightedRatingCount = snapshot.ValidRatingCount
		dtos[index].WeightedRatingActive = snapshot.QualityActive
		if snapshot.QualityActive && snapshot.WeightedScore != nil {
			score := math.Round(*snapshot.WeightedScore*10) / 10
			dtos[index].WeightedRatingScore = &score
		}
	}
	return nil
}

func (s *Service) GetSEOPost(id uuid.UUID) (SEOPostDTO, error) {
	post, err := s.repo.GetPublicPublishedPost(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SEOPostDTO{}, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return SEOPostDTO{}, err
	}
	return buildSEOPostDTO(post), nil
}

func (s *Service) ListSEOSitemap() ([]SEOSitemapItemDTO, error) {
	posts, err := s.repo.ListPublicPublishedPosts()
	if err != nil {
		return nil, err
	}
	items := make([]SEOSitemapItemDTO, 0, len(posts))
	for _, post := range posts {
		items = append(items, SEOSitemapItemDTO{
			Path:         seoPostPath(post.ID),
			LastModified: post.UpdatedAt,
		})
	}
	return items, nil
}

func buildSEOPostDTO(post BlogContent) SEOPostDTO {
	authorName := ""
	if post.User != nil {
		authorName = strings.TrimSpace(post.User.DisplayName)
		if authorName == "" {
			authorName = post.User.Username
		}
	}
	return SEOPostDTO{
		ID:          post.ID,
		Title:       post.Title,
		Description: seoPostDescription(post),
		ImageURL:    post.CoverURL,
		AuthorName:  authorName,
		PublishedAt: post.PublishedAt,
		UpdatedAt:   post.UpdatedAt,
		Path:        seoPostPath(post.ID),
	}
}

func seoPostPath(id uuid.UUID) string {
	return "/posts/post/" + id.String()
}

func seoPostDescription(post BlogContent) string {
	if summary := strings.TrimSpace(post.Summary); summary != "" {
		return summary
	}

	plain := markdownImagePattern.ReplaceAllString(post.Content, "$1")
	plain = markdownLinkPattern.ReplaceAllString(plain, "$1")
	plain = stripHTMLTags(plain)
	plain = markdownLinePrefixPattern.ReplaceAllString(plain, "")
	plain = strings.NewReplacer("**", "", "__", "", "~~", "", "`", "", "*", "", "_", "").Replace(plain)
	plain = stdhtml.UnescapeString(strings.Join(strings.Fields(plain), " "))
	runes := []rune(plain)
	if len(runes) > 160 {
		return string(runes[:160])
	}
	return plain
}

func stripHTMLTags(value string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(value))
	parts := make([]string, 0)
	skippedTag := ""
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return strings.Join(parts, " ")
		case html.TextToken:
			if skippedTag == "" {
				parts = append(parts, string(tokenizer.Text()))
			}
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "script" || string(name) == "style" {
				skippedTag = string(name)
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == skippedTag {
				skippedTag = ""
			}
		}
	}
}

func (s *Service) CreateBlogContent(user authctx.CurrentUser, req CreatePostRequest) (BlogContent, error) {
	if user.ID == uuid.Nil {
		return BlogContent{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		return BlogContent{}, apperr.BadRequest("validation.invalid_request", "title and content are required")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "draft"
	}
	if _, ok := allowedPostStatuses[status]; !ok {
		return BlogContent{}, apperr.BadRequest("blog.invalid_status_transition", "status is invalid")
	}

	channelID := req.ChannelID
	var requestedCollectionID *uuid.UUID
	if req.CollectionID != uuid.Nil {
		requestedCollectionID = &req.CollectionID
	}
	if requestedCollectionID != nil && channelID == uuid.Nil {
		var collection model.ContentCollection
		if err := s.db.First(&collection, "id = ?", *requestedCollectionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return BlogContent{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
			}
			return BlogContent{}, err
		}
		channelID = collection.ChannelID
	}
	channel, err := s.repo.GetChannel(channelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BlogContent{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
		}
		return BlogContent{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return BlogContent{}, apperr.Forbidden("blog.channel_forbidden", "You don't have permission to create post in this channel")
	}
	if isChannelBanned(channel) && status == "published" {
		return BlogContent{}, apperr.Forbidden("blog.channel_banned", "Banned channel cannot publish posts")
	}
	collection, err := resolveBlogCollection(s.db, user.ID, channel.ID, requestedCollectionID, status == "published")
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BlogContent{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
		}
		return BlogContent{}, err
	}

	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = "public"
	}
	if _, ok := allowedPostVisibilities[visibility]; !ok {
		return BlogContent{}, apperr.BadRequest("blog.invalid_visibility", "visibility is invalid")
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = strings.TrimSpace(req.Excerpt)
	}
	languageCode := feedlanguage.Detect(strings.Join([]string{req.Title, summary, req.Content}, " "))
	entry := model.ContentEntry{
		AuthorID:   &user.ID,
		ChannelID:  channel.ID,
		Kind:       "blog",
		Title:      strings.TrimSpace(req.Title),
		Summary:    summary,
		CoverURL:   strings.TrimSpace(req.CoverURL),
		Visibility: visibility,
		Status:     status,
	}
	if status == "published" {
		now := time.Now().UTC()
		entry.PublishedAt = &now
	}
	blogExtension := model.ContentBlogExtension{
		Content:      strings.TrimSpace(req.Content),
		LanguageCode: languageCode,
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		blogExtension.ContentID = entry.ID
		if err := tx.Create(&blogExtension).Error; err != nil {
			return err
		}
		if err := s.syncBlogContentMediaAssets(tx, entry.ID, user.ID, entry.CoverURL, blogExtension.Content); err != nil {
			return err
		}
		if collection != nil {
			var maxPosition int
			if err := tx.Model(&model.ContentCollectionMembership{}).Where("collection_id = ?", collection.ID).
				Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.ContentCollectionMembership{ContentID: entry.ID, CollectionID: collection.ID, Position: maxPosition + 1}).Error; err != nil {
				return err
			}
		}
		if status != "published" {
			return nil
		}
		content, err := loadCanonicalBlogContent(tx, entry.ID)
		if err != nil {
			return err
		}
		if _, err := s.syncBlogContentReferences(tx, content); err != nil {
			return err
		}
		if err := saveBlogContentVersion(tx, content, user.ID); err != nil {
			return err
		}
		return lifecycle.NewService(tx).EnqueuePublication("blog", entry.ID)
	}); err != nil {
		return BlogContent{}, err
	}
	return loadCanonicalBlogContent(s.db, entry.ID)
}

func saveBlogContentVersion(tx *gorm.DB, content BlogContent, editorID uuid.UUID) error {
	if content.CollectionID == nil || *content.CollectionID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "collection_id is required")
	}
	var maxVersion int
	if err := tx.Model(&model.ContentBlogVersion{}).Where("content_id = ?", content.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return err
	}
	return tx.Create(&model.ContentBlogVersion{ContentID: content.ID, Version: maxVersion + 1, EditorID: editorID, Title: content.Title, Content: content.Content, Summary: content.Summary, CoverURL: content.CoverURL, LanguageCode: content.LanguageCode, Pinned: content.Pinned, Visibility: content.Visibility, CollectionID: *content.CollectionID, PublishedAt: content.PublishedAt}).Error
}

func (s *Service) ListPostVersions(user authctx.CurrentUser, postID uuid.UUID) ([]BlogContentVersionDTO, error) {
	post, err := s.repo.GetBlogContent(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return nil, err
	}
	if post.UserID != user.ID {
		return nil, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view post versions")
	}
	var versions []model.ContentBlogVersion
	if err := s.db.Where("content_id = ?", postID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	result := make([]BlogContentVersionDTO, 0, len(versions))
	for _, version := range versions {
		result = append(result, newBlogContentVersionDTO(version))
	}
	return result, nil
}

func (s *Service) RestoreBlogContentVersion(user authctx.CurrentUser, postID uuid.UUID, versionNumber int) (BlogContent, error) {
	var restored BlogContent
	err := s.db.Transaction(func(tx *gorm.DB) error {
		post, err := loadCanonicalBlogContent(tx, postID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.post_not_found", "Post not found")
			}
			return err
		}
		if post.UserID != user.ID {
			return apperr.Forbidden("blog.post_forbidden", "You don't have permission to restore this post")
		}
		var version model.ContentBlogVersion
		if err := tx.Where("content_id = ? AND version = ?", postID, versionNumber).First(&version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.version_not_found", "Post version not found")
			}
			return err
		}
		if err := tx.Model(&model.ContentEntry{}).Where("id = ?", postID).Updates(map[string]any{
			"title": version.Title, "summary": version.Summary, "cover_url": version.CoverURL,
			"visibility": version.Visibility,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ContentBlogExtension{}).Where("content_id = ?", postID).Updates(map[string]any{
			"content": version.Content, "language_code": version.LanguageCode, "pinned": version.Pinned,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("content_id = ?", postID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ContentCollectionMembership{ContentID: postID, CollectionID: version.CollectionID}).Error; err != nil {
			return err
		}
		restored, err = loadCanonicalBlogContent(tx, postID)
		if err != nil {
			return err
		}
		content := restored
		if err := s.syncBlogContentMediaAssets(tx, content.ID, content.UserID, content.CoverURL, content.Content); err != nil {
			return err
		}
		if _, err := s.syncBlogContentReferences(tx, content); err != nil {
			return err
		}
		return saveBlogContentVersion(tx, content, user.ID)
	})
	return restored, err
}

func (s *Service) reorderCollectionPosts(collection BlogCollection, orderedPostIDs []uuid.UUID, userID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		posts, err := LoadCanonicalBlogContents(tx, canonicalBlogPostsQuery(tx).Where("memberships.collection_id = ?", collection.ID))
		if err != nil {
			return err
		}
		if len(posts) != len(orderedPostIDs) {
			return apperr.BadRequest("validation.invalid_request", "post_ids must include every post in the collection")
		}

		postSet := make(map[uuid.UUID]BlogContent, len(posts))
		for _, post := range posts {
			postSet[post.ID] = post
		}
		for _, postID := range orderedPostIDs {
			post, exists := postSet[postID]
			if !exists {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection")
			}
			if post.UserID != userID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to reorder this collection")
			}
			if post.CollectionID == nil || *post.CollectionID != collection.ID {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection channel")
			}
		}
		for position, postID := range orderedPostIDs {
			if err := tx.Model(&model.ContentCollectionMembership{}).
				Where("collection_id = ? AND content_id = ?", collection.ID, postID).
				Update("position", position).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func isChannelBanned(channel model.Channel) bool {
	if channel.BanUntil != nil {
		return channel.BanUntil.After(time.Now())
	}
	return strings.TrimSpace(channel.BanReason) != ""
}
