package comment

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	markLabelPinned     = "置顶"
	markLabelBestAnswer = "最佳回答"
)

type databaseTargetResolvers struct {
	db *gorm.DB
}

func NewTargetRegistry(db *gorm.DB) *TargetRegistry {
	resolvers := &databaseTargetResolvers{db: db}
	return &TargetRegistry{resolvers: map[string]TargetResolver{
		TargetKindBlogPost:       targetResolverFunc(resolvers.resolveBlogPost),
		TargetKindVideo:          targetResolverFunc(resolvers.resolveVideo),
		TargetKindPodcastEpisode: targetResolverFunc(resolvers.resolvePodcastEpisode),
		TargetKindFeedArticle:    targetResolverFunc(resolvers.resolveFeedArticle),
		TargetKindMusicArtist:    targetResolverFunc(resolvers.resolveMusicArtist),
		TargetKindMusicAlbum:     targetResolverFunc(resolvers.resolveMusicAlbum),
		TargetKindMusicSong:      targetResolverFunc(resolvers.resolveMusicSong),
		TargetKindForumTopic:     targetResolverFunc(resolvers.resolveForumTopic),
		TargetKindDebate:         targetResolverFunc(resolvers.resolveDebate),
		TargetKindTimelineEvent:  targetResolverFunc(resolvers.resolveTimelineEvent),
		TargetKindTimelinePerson: targetResolverFunc(resolvers.resolveTimelinePerson),
	}}
}

func (r *databaseTargetResolvers) resolveBlogPost(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var post model.Post
	if err := r.db.First(&post, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindBlogPost, resourceID, err)
	}
	visible, err := r.canViewPublishedContent(viewer, post.UserID, post.ChannelID, post.Status, post.Visibility)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ownedTarget(TargetKindBlogPost, post.ID, post.UserID, visible, 0, markLabelPinned), nil
}

func (r *databaseTargetResolvers) resolveVideo(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var video model.Video
	if err := r.db.First(&video, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindVideo, resourceID, err)
	}
	visible, err := r.canViewPublishedContent(viewer, video.UserID, video.ChannelID, video.Status, video.Visibility)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ownedTarget(TargetKindVideo, video.ID, video.UserID, visible, video.DurationSec, markLabelPinned), nil
}

func (r *databaseTargetResolvers) resolvePodcastEpisode(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var episode model.PodcastEpisode
	if err := r.db.Preload("Post").First(&episode, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindPodcastEpisode, resourceID, err)
	}
	if episode.Post == nil {
		return ResolvedTarget{}, fmt.Errorf("%w: podcast episode %s has no post", ErrInvalidTargetResource, resourceID)
	}
	visible, err := r.canViewPublishedContent(
		viewer,
		episode.Post.UserID,
		episode.Post.ChannelID,
		episode.Post.Status,
		episode.Post.Visibility,
	)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ownedTarget(
		TargetKindPodcastEpisode,
		episode.ID,
		episode.Post.UserID,
		visible,
		episode.DurationSec,
		markLabelPinned,
	), nil
}

func (r *databaseTargetResolvers) resolveFeedArticle(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var item model.FeedItem
	if err := r.db.Preload("FeedSource").First(&item, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindFeedArticle, resourceID, err)
	}
	if item.FeedSource == nil {
		return ResolvedTarget{}, fmt.Errorf("%w: feed article %s has no source", ErrInvalidTargetResource, resourceID)
	}
	resourceKey, err := normalizeArticleURL(item.Link)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("%w: feed article %s: %v", ErrInvalidTargetResource, resourceID, err)
	}
	return ResolvedTarget{
		Kind:        TargetKindFeedArticle,
		ResourceKey: resourceKey,
		Visible:     !item.FeedSource.Hidden,
	}, nil
}

func (r *databaseTargetResolvers) resolveMusicArtist(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var artist model.Artist
	if err := r.db.First(&artist, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindMusicArtist, resourceID, err)
	}
	return communityTarget(TargetKindMusicArtist, artist.ID, !isMusicHiddenStatus(artist.EntryStatus)), nil
}

func (r *databaseTargetResolvers) resolveMusicAlbum(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var album model.Album
	if err := r.db.First(&album, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindMusicAlbum, resourceID, err)
	}
	visible := !isMusicHiddenStatus(album.EntryStatus) && !isMusicHiddenStatus(album.Status)
	return communityTarget(TargetKindMusicAlbum, album.ID, visible), nil
}

func (r *databaseTargetResolvers) resolveMusicSong(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var song model.Song
	if err := r.db.First(&song, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindMusicSong, resourceID, err)
	}
	target := communityTarget(TargetKindMusicSong, song.ID, !isMusicHiddenStatus(song.Status))
	target.DurationSec = song.DurationSec
	return target, nil
}

func (r *databaseTargetResolvers) resolveForumTopic(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var topic model.ForumTopic
	if err := r.db.First(&topic, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindForumTopic, resourceID, err)
	}
	return ownedTarget(TargetKindForumTopic, topic.ID, topic.UserID, true, 0, markLabelBestAnswer), nil
}

func (r *databaseTargetResolvers) resolveDebate(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var debate model.Debate
	if err := r.db.First(&debate, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindDebate, resourceID, err)
	}
	return ownedTarget(TargetKindDebate, debate.ID, debate.UserID, true, 0, markLabelPinned), nil
}

func (r *databaseTargetResolvers) resolveTimelineEvent(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var event model.TimelineEvent
	if err := r.db.First(&event, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindTimelineEvent, resourceID, err)
	}
	visible := event.IsPublic || viewerOwns(viewer, event.UserID)
	return ownedTarget(TargetKindTimelineEvent, event.ID, event.UserID, visible, 0, markLabelPinned), nil
}

func (r *databaseTargetResolvers) resolveTimelinePerson(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var person model.TimelinePerson
	if err := r.db.First(&person, "id = ?", resourceID).Error; err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindTimelinePerson, resourceID, err)
	}
	visible := person.IsPublic || viewerOwns(viewer, person.UserID)
	return ownedTarget(TargetKindTimelinePerson, person.ID, person.UserID, visible, 0, markLabelPinned), nil
}

func (r *databaseTargetResolvers) canViewPublishedContent(
	viewer Viewer,
	ownerID uuid.UUID,
	channelID *uuid.UUID,
	status string,
	visibility string,
) (bool, error) {
	if viewerOwns(viewer, ownerID) {
		return true, nil
	}
	if status != "published" {
		return false, nil
	}
	switch visibility {
	case "", "public":
		return true, nil
	case "private":
		return false, nil
	case "followers":
		if viewer.UserID == nil || channelID == nil {
			return false, nil
		}
		return r.isChannelSubscriber(*viewer.UserID, *channelID)
	default:
		return false, nil
	}
}

func (r *databaseTargetResolvers) isChannelSubscriber(userID uuid.UUID, channelID uuid.UUID) (bool, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+channelID.String())))
	var source model.FeedSource
	if err := r.db.Where("hash = ?", hash).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("resolve channel subscription source: %w", err)
	}

	var count int64
	if err := r.db.Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", userID, source.ID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("resolve channel subscription: %w", err)
	}
	return count > 0, nil
}

func targetLookupError(kind string, resourceID uuid.UUID, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s %s", ErrTargetNotFound, kind, resourceID)
	}
	return fmt.Errorf("resolve %s %s: %w", kind, resourceID, err)
}

func ownedTarget(kind string, resourceID uuid.UUID, ownerID uuid.UUID, visible bool, durationSec int, markLabel string) ResolvedTarget {
	return ResolvedTarget{
		Kind:        kind,
		ResourceKey: resourceID.String(),
		OwnerID:     &ownerID,
		Visible:     visible,
		DurationSec: durationSec,
		MarkLabel:   markLabel,
	}
}

func communityTarget(kind string, resourceID uuid.UUID, visible bool) ResolvedTarget {
	return ResolvedTarget{Kind: kind, ResourceKey: resourceID.String(), Visible: visible}
}

func viewerOwns(viewer Viewer, ownerID uuid.UUID) bool {
	return viewer.UserID != nil && *viewer.UserID == ownerID
}

func isMusicHiddenStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "closed", "rejected", "draft":
		return true
	default:
		return false
	}
}

func normalizeArticleURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Opaque != "" {
		return "", errors.New("original URL must be an absolute HTTP(S) URL")
	}

	cleanPath := path.Clean(parsed.Path)
	if cleanPath == "." {
		cleanPath = "/"
	}
	parsed.Path = cleanPath
	parsed.RawPath = ""
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.ForceQuery = false

	query := parsed.Query()
	for key := range query {
		lowerKey := strings.ToLower(key)
		if strings.HasPrefix(lowerKey, "utm_") || lowerKey == "fbclid" || lowerKey == "gclid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
