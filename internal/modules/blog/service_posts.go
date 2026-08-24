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

func (s *Service) syncPostReferences(tx *gorm.DB, post model.Post) ([]reference.ResolvedReference, error) {
	if post.Status != "published" {
		return nil, s.references.RemoveSource(tx, "post", post.ID)
	}
	audience := ""
	if post.Visibility == "" || post.Visibility == "public" {
		audience = reference.AudiencePublic
	}
	return s.references.ReplacePublished(tx, reference.Source{
		Type: "post", ID: post.ID, ActorID: post.UserID, Audience: audience,
		Meta: map[string]interface{}{"module": "blog", "path": "/post/" + post.ID.String()},
	}, []reference.Field{{Name: "content", Content: post.Content}})
}

func (s *Service) postDTOs(db *gorm.DB, posts []model.Post, viewerID *uuid.UUID) ([]PostDTO, error) {
	dtos := make([]PostDTO, len(posts))
	ids := make([]uuid.UUID, 0, len(posts))
	for index, post := range posts {
		dtos[index] = newPostDTO(post)
		ids = append(ids, post.ID)
	}
	if len(ids) == 0 {
		return dtos, nil
	}
	var rows []model.ContentReference
	if err := db.Where("source_type = ? AND source_id IN ?", "post", ids).
		Order("source_id ASC, source_field ASC, start_offset ASC").Find(&rows).Error; err != nil {
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
	return dtos, nil
}

func (s *Service) populatePostRatings(db *gorm.DB, dtos []PostDTO, viewerID *uuid.UUID) error {
	if len(dtos) == 0 || !db.Migrator().HasTable(&model.PostRating{}) {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(dtos))
	for _, dto := range dtos {
		ids = append(ids, dto.ID)
	}

	type ratingAggregate struct {
		PostID      uuid.UUID `gorm:"column:post_id"`
		RatingScore float64   `gorm:"column:rating_score"`
		RatingCount int64     `gorm:"column:rating_count"`
	}
	var aggregates []ratingAggregate
	if err := db.Model(&model.PostRating{}).
		Select("post_id, AVG(score) AS rating_score, COUNT(*) AS rating_count").
		Where("post_id IN ?", ids).
		Group("post_id").
		Scan(&aggregates).Error; err != nil {
		return err
	}
	aggregatesByPostID := make(map[uuid.UUID]ratingAggregate, len(aggregates))
	for _, aggregate := range aggregates {
		aggregatesByPostID[aggregate.PostID] = aggregate
	}

	viewerRatings := make(map[uuid.UUID]int, 0)
	if viewerID != nil {
		var ratings []model.PostRating
		if err := db.Where("user_id = ? AND post_id IN ?", *viewerID, ids).Find(&ratings).Error; err != nil {
			return err
		}
		viewerRatings = make(map[uuid.UUID]int, len(ratings))
		for _, rating := range ratings {
			viewerRatings[rating.PostID] = rating.Score
		}
	}

	for index := range dtos {
		aggregate := aggregatesByPostID[dtos[index].ID]
		dtos[index].RatingScore = math.Round(aggregate.RatingScore*10) / 10
		dtos[index].RatingCount = aggregate.RatingCount
		if score, ok := viewerRatings[dtos[index].ID]; ok {
			value := score
			dtos[index].ViewerRating = &value
		}
	}
	return nil
}

func (s *Service) postDTO(db *gorm.DB, post model.Post, viewerID *uuid.UUID) (PostDTO, error) {
	items, err := s.postDTOs(db, []model.Post{post}, viewerID)
	if err != nil {
		return PostDTO{}, err
	}
	return items[0], nil
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

func buildSEOPostDTO(post model.Post) SEOPostDTO {
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

func seoPostDescription(post model.Post) string {
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

func (s *Service) CreatePost(user authctx.CurrentUser, req CreatePostRequest) (model.Post, error) {
	if user.ID == uuid.Nil {
		return model.Post{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		return model.Post{}, apperr.BadRequest("validation.invalid_request", "title and content are required")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "draft"
	}
	if _, ok := allowedPostStatuses[status]; !ok {
		return model.Post{}, apperr.BadRequest("blog.invalid_status_transition", "status is invalid")
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
				return model.Post{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
			}
			return model.Post{}, err
		}
		channelID = collection.ChannelID
	}
	channel, err := s.repo.GetChannel(channelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Post{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
		}
		return model.Post{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Post{}, apperr.Forbidden("blog.channel_forbidden", "You don't have permission to create post in this channel")
	}
	if isChannelBanned(channel) && status == "published" {
		return model.Post{}, apperr.Forbidden("blog.channel_banned", "Banned channel cannot publish posts")
	}
	collection, err := resolveBlogCollection(s.db, user.ID, channel.ID, requestedCollectionID, status == "published")
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Post{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
		}
		return model.Post{}, err
	}

	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = "public"
	}
	if _, ok := allowedPostVisibilities[visibility]; !ok {
		return model.Post{}, apperr.BadRequest("blog.invalid_visibility", "visibility is invalid")
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
		post, err := loadCanonicalBlogPost(tx, entry.ID)
		if err != nil {
			return err
		}
		if _, err := s.syncPostReferences(tx, post); err != nil {
			return err
		}
		if err := saveBlogPostVersion(tx, post, user.ID); err != nil {
			return err
		}
		return lifecycle.NewService(tx).EnqueuePublication("blog", entry.ID)
	}); err != nil {
		return model.Post{}, err
	}
	return loadCanonicalBlogPost(s.db, entry.ID)
}

func saveBlogPostVersion(tx *gorm.DB, post model.Post, editorID uuid.UUID) error {
	if post.CollectionID == nil || *post.CollectionID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "collection_id is required")
	}
	var maxVersion int
	if err := tx.Model(&model.ContentBlogVersion{}).Where("content_id = ?", post.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return err
	}
	version := model.ContentBlogVersion{
		ContentID:    post.ID,
		Version:      maxVersion + 1,
		EditorID:     editorID,
		Title:        post.Title,
		Content:      post.Content,
		Summary:      post.Summary,
		CoverURL:     post.CoverURL,
		LanguageCode: post.LanguageCode,
		Pinned:       post.Pinned,
		Visibility:   post.Visibility,
		CollectionID: *post.CollectionID,
		PublishedAt:  post.PublishedAt,
	}
	return tx.Create(&version).Error
}

func (s *Service) ListPostVersions(user authctx.CurrentUser, postID uuid.UUID) ([]model.BlogPostVersion, error) {
	post, err := s.repo.GetPost(postID)
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
	result := make([]model.BlogPostVersion, 0, len(versions))
	for _, version := range versions {
		result = append(result, buildBlogVersionResponse(version))
	}
	return result, nil
}

func (s *Service) RestorePostVersion(user authctx.CurrentUser, postID uuid.UUID, versionNumber int) (model.Post, error) {
	var restored model.Post
	err := s.db.Transaction(func(tx *gorm.DB) error {
		post, err := loadCanonicalBlogPost(tx, postID)
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
		restored, err = loadCanonicalBlogPost(tx, postID)
		if err != nil {
			return err
		}
		if _, err := s.syncPostReferences(tx, restored); err != nil {
			return err
		}
		return saveBlogPostVersion(tx, restored, user.ID)
	})
	return restored, err
}

func (s *Service) reorderCollectionPosts(collection model.Collection, orderedPostIDs []uuid.UUID, userID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		posts, err := loadCanonicalBlogPosts(tx, canonicalBlogPostsQuery(tx).Where("memberships.collection_id = ?", collection.ID))
		if err != nil {
			return err
		}
		if len(posts) != len(orderedPostIDs) {
			return apperr.BadRequest("validation.invalid_request", "post_ids must include every post in the collection")
		}

		postSet := make(map[uuid.UUID]model.Post, len(posts))
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
