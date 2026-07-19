package resourceref

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	KindPost       = "post"
	KindThread     = "thread"
	KindDebate     = "debate"
	KindFeed       = "feed"
	KindArticle    = "article"
	KindArtist     = "artist"
	KindAlbum      = "album"
	KindSong       = "song"
	KindPlaylist   = "playlist"
	KindPodcast    = "podcast"
	KindEpisode    = "episode"
	KindVideo      = "video"
	KindPerson     = "person"
	KindEvent      = "event"
	KindChannel    = "channel"
	KindCollection = "collection"
	KindComment    = "comment"

	QualifierSupport = "support"
	QualifierOppose  = "oppose"
)

var (
	ErrUnknownKind       = errors.New("unknown resource reference kind")
	ErrInvalidResourceID = errors.New("invalid resource reference ID")
	ErrInvalidQualifier  = errors.New("invalid resource reference qualifier")
)

var supportedKinds = map[string]struct{}{
	KindPost: {}, KindThread: {}, KindDebate: {}, KindFeed: {}, KindArticle: {},
	KindArtist: {}, KindAlbum: {}, KindSong: {}, KindPlaylist: {}, KindPodcast: {},
	KindEpisode: {}, KindVideo: {}, KindPerson: {}, KindEvent: {}, KindChannel: {},
	KindCollection: {}, KindComment: {},
}

const canonicalUUIDLength = 36

type Reference struct {
	Raw        string
	Kind       string
	ResourceID uuid.UUID
	Qualifier  string
	Start      int
	End        int
}

// Parse returns resource references in source order. Start and End are UTF-8 byte offsets.
func Parse(content string) ([]Reference, error) {
	var references []Reference
	for offset := 0; offset < len(content); {
		relativeStart := strings.IndexByte(content[offset:], '@')
		if relativeStart < 0 {
			break
		}
		start := offset + relativeStart
		kindEnd := start + 1
		for kindEnd < len(content) && isKindByte(content[kindEnd]) {
			kindEnd++
		}
		if kindEnd == start+1 || kindEnd >= len(content) || content[kindEnd] != ':' {
			offset = start + 1
			continue
		}

		kind := content[start+1 : kindEnd]
		if !isSupportedKind(kind) {
			if isTrailingColon(content, kindEnd) {
				offset = kindEnd + 1
				continue
			}
			return nil, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
		}

		valueStart := kindEnd + 1
		if len(content)-valueStart < canonicalUUIDLength {
			return nil, fmt.Errorf("%w at byte %d", ErrInvalidResourceID, start)
		}
		resourceEnd := valueStart + canonicalUUIDLength
		resourceID, err := parseCanonicalUUID(content[valueStart:resourceEnd])
		if err != nil {
			return nil, fmt.Errorf("%w at byte %d", ErrInvalidResourceID, start)
		}

		end, qualifier, err := parseSuffix(content, kind, resourceEnd)
		if err != nil {
			return nil, fmt.Errorf("%w at byte %d", err, start)
		}

		references = append(references, Reference{
			Raw:        content[start:end],
			Kind:       kind,
			ResourceID: resourceID,
			Qualifier:  qualifier,
			Start:      start,
			End:        end,
		})
		offset = end
	}
	return references, nil
}

func isSupportedKind(kind string) bool {
	_, ok := supportedKinds[kind]
	return ok
}

func isKindByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '_' || value == '-'
}

func isReferenceValueByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-'
}

func parseCanonicalUUID(value string) (uuid.UUID, error) {
	resourceID, err := uuid.Parse(value)
	if err != nil || !strings.EqualFold(resourceID.String(), value) {
		return uuid.Nil, ErrInvalidResourceID
	}
	return resourceID, nil
}

func parseSuffix(content string, kind string, resourceEnd int) (int, string, error) {
	if resourceEnd == len(content) {
		if kind == KindDebate {
			return 0, "", ErrInvalidQualifier
		}
		return resourceEnd, "", nil
	}

	if content[resourceEnd] != ':' {
		if isReferenceValueByte(content[resourceEnd]) {
			return 0, "", ErrInvalidResourceID
		}
		if kind == KindDebate {
			return 0, "", ErrInvalidQualifier
		}
		return resourceEnd, "", nil
	}

	if isTrailingColon(content, resourceEnd) {
		if kind == KindDebate {
			return 0, "", ErrInvalidQualifier
		}
		return resourceEnd, "", nil
	}
	if kind != KindDebate {
		return 0, "", ErrInvalidQualifier
	}

	qualifierStart := resourceEnd + 1
	for _, qualifier := range []string{QualifierSupport, QualifierOppose} {
		if !strings.HasPrefix(content[qualifierStart:], qualifier) {
			continue
		}
		end := qualifierStart + len(qualifier)
		if end == len(content) {
			return end, qualifier, nil
		}
		if content[end] == ':' {
			if isTrailingColon(content, end) {
				return end, qualifier, nil
			}
			return 0, "", ErrInvalidQualifier
		}
		if isReferenceValueByte(content[end]) {
			return 0, "", ErrInvalidQualifier
		}
		return end, qualifier, nil
	}
	return 0, "", ErrInvalidQualifier
}

func isTrailingColon(content string, colon int) bool {
	if colon+1 == len(content) {
		return true
	}
	next := content[colon+1]
	return next != ':' && !isReferenceValueByte(next)
}
