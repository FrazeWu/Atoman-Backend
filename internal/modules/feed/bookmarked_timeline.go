package feed

import (
	"sort"

	"atoman/internal/model"
	blogmodule "atoman/internal/modules/blog"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

type bookmarkedTimelineItem struct {
	item TimelineItemDTO
	at   int64
}

func (s *Service) GetBookmarkedFeed(user authctx.CurrentUser, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Authentication is required")
	}

	var blogBookmarks []model.Bookmark
	if err := s.db.Where("user_id = ?", user.ID).Find(&blogBookmarks).Error; err != nil {
		return nil, 0, err
	}
	blogIDs := make([]uuid.UUID, 0, len(blogBookmarks))
	blogTimes := make(map[uuid.UUID]int64, len(blogBookmarks))
	for _, bookmark := range blogBookmarks {
		blogIDs = append(blogIDs, bookmark.ContentID)
		blogTimes[bookmark.ContentID] = bookmark.CreatedAt.UnixNano()
	}
	posts := make([]model.Post, 0, len(blogIDs))
	if len(blogIDs) > 0 {
		var err error
		posts, err = blogmodule.LoadCanonicalBlogPosts(s.db, blogmodule.CanonicalBlogPostsQuery(s.db).
			Where("posts.id IN ? AND posts.status = ?", blogIDs, "published"))
		if err != nil {
			return nil, 0, err
		}
	}

	var podcastBookmarks []model.PodcastEpisodeBookmark
	if err := s.db.Where("user_id = ? AND kind = ?", user.ID, "favorite").Find(&podcastBookmarks).Error; err != nil {
		return nil, 0, err
	}
	podcastIDs := make([]uuid.UUID, 0, len(podcastBookmarks))
	podcastTimes := make(map[uuid.UUID]int64, len(podcastBookmarks))
	for _, bookmark := range podcastBookmarks {
		podcastIDs = append(podcastIDs, bookmark.EpisodeID)
		podcastTimes[bookmark.EpisodeID] = bookmark.CreatedAt.UnixNano()
	}
	episodes := make([]model.PodcastEpisode, 0, len(podcastIDs))
	if len(podcastIDs) > 0 {
		var err error
		episodes, err = contentmodule.LoadPodcastEpisodes(s.db, contentmodule.PodcastQuery(s.db).
			Where("episodes.episode_id IN ? AND posts.status = ?", podcastIDs, "published"))
		if err != nil {
			return nil, 0, err
		}
	}

	var videoBookmarks []model.VideoBookmark
	if err := s.db.Where("user_id = ?", user.ID).Find(&videoBookmarks).Error; err != nil {
		return nil, 0, err
	}
	videoIDs := make([]uuid.UUID, 0, len(videoBookmarks))
	videoTimes := make(map[uuid.UUID]int64, len(videoBookmarks))
	for _, bookmark := range videoBookmarks {
		videoIDs = append(videoIDs, bookmark.VideoID)
		videoTimes[bookmark.VideoID] = bookmark.CreatedAt.UnixNano()
	}
	videos := make([]model.Video, 0, len(videoIDs))
	if len(videoIDs) > 0 {
		var err error
		videos, err = contentmodule.LoadVideos(s.db, contentmodule.VideoQuery(s.db).
			Where("videos.video_id IN ? AND posts.status = ?", videoIDs, "published"))
		if err != nil {
			return nil, 0, err
		}
	}

	postIDs := make([]uuid.UUID, 0, len(posts))
	for _, post := range posts {
		postIDs = append(postIDs, post.ID)
	}
	engagementCounts, err := s.repo.ListPostEngagementCounts(postIDs)
	if err != nil {
		return nil, 0, err
	}
	engagementByPostID := make(map[uuid.UUID]PostEngagementCount, len(engagementCounts))
	for _, count := range engagementCounts {
		engagementByPostID[count.PostID] = count
	}

	rows := make([]bookmarkedTimelineItem, 0, len(posts)+len(episodes)+len(videos))
	for index := range posts {
		rows = append(rows, bookmarkedTimelineItem{
			item: TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[index], engagementByPostID[posts[index].ID]), PublishedAt: postTimelinePublishedAt(posts[index])},
			at:   blogTimes[posts[index].ID],
		})
	}
	for index := range episodes {
		rows = append(rows, bookmarkedTimelineItem{
			item: TimelineItemDTO{Type: "podcast_episode", PodcastEpisode: &episodes[index], PublishedAt: postTimelinePublishedAt(*episodes[index].Post)},
			at:   podcastTimes[episodes[index].ID],
		})
	}
	for index := range videos {
		rows = append(rows, bookmarkedTimelineItem{
			item: TimelineItemDTO{Type: "video", Video: &videos[index], PublishedAt: videos[index].CreatedAt},
			at:   videoTimes[videos[index].ID],
		})
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].at > rows[right].at })

	items := make([]TimelineItemDTO, len(rows))
	for index := range rows {
		items[index] = rows[index].item
	}
	paged, total := paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
	return paged, total, nil
}
