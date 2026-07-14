package comment

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
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
}

type ResolvedTarget struct {
	Kind        string
	ResourceKey string
	OwnerID     *uuid.UUID
	Visible     bool
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
