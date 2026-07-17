package comment

import (
	"errors"
	"fmt"

	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	TargetKindBlogPost       = "blog_post"
	TargetKindVideo          = "video"
	TargetKindPodcastEpisode = "podcast_episode"
	TargetKindFeedArticle    = "feed_article"
	TargetKindMusicArtist    = "music_artist"
	TargetKindMusicAlbum     = "music_album"
	TargetKindMusicSong      = "music_song"
	TargetKindForumTopic     = "forum_topic"
	TargetKindDebate         = "debate"
	TargetKindTimelineEvent  = "timeline_event"
	TargetKindTimelinePerson = "timeline_person"
)

var (
	ErrUnknownTargetKind     = errors.New("unknown comment target kind")
	ErrTargetNotFound        = errors.New("comment target not found")
	ErrInvalidTargetResource = errors.New("invalid comment target resource")
)

type TargetRef struct {
	Kind       string
	ResourceID uuid.UUID
}

type Viewer struct {
	UserID *uuid.UUID
	Role   string
}

type ForumPolicy interface {
	CanViewTopic(viewer Viewer, topicID uuid.UUID) error
	CheckCreateComment(tx *gorm.DB, user authctx.CurrentUser, topicID uuid.UUID, content string) error
	CheckUpdateComment(tx *gorm.DB, authorID, commentID uuid.UUID, content string) error
	CommentNotificationAudience(tx *gorm.DB, topicID, actorID uuid.UUID) (string, []uuid.UUID, error)
	EvaluateTrust(userID uuid.UUID)
}

func viewerFromUser(user authctx.CurrentUser) Viewer {
	viewer := Viewer{Role: user.Role}
	if user.ID != uuid.Nil {
		viewer.UserID = &user.ID
	}
	if viewer.Role == "" {
		viewer.Role = authctx.RoleAnonymous
	}
	return viewer
}

type ResolvedTarget struct {
	Kind        string
	ResourceID  uuid.UUID
	ResourceKey string
	OwnerID     *uuid.UUID
	Visible     bool
	Locked      bool
	DurationSec int
	MarkLabel   string
}

type TargetResolver interface {
	Resolve(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error)
}

type TargetRegistry struct {
	resolvers map[string]TargetResolver
}

func (r *TargetRegistry) Resolve(viewer Viewer, ref TargetRef) (ResolvedTarget, error) {
	resolver, ok := r.resolvers[ref.Kind]
	if !ok {
		return ResolvedTarget{}, fmt.Errorf("%w: %s", ErrUnknownTargetKind, ref.Kind)
	}
	if ref.ResourceID == uuid.Nil {
		return ResolvedTarget{}, ErrInvalidTargetResource
	}
	return resolver.Resolve(viewer, ref.ResourceID)
}

type targetResolverFunc func(Viewer, uuid.UUID) (ResolvedTarget, error)

func (resolve targetResolverFunc) Resolve(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	return resolve(viewer, resourceID)
}
