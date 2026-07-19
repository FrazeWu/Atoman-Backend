package resourceref

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
			return nil, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
		}

		valueStart := kindEnd + 1
		end := valueStart
		for end < len(content) {
			r, size := utf8.DecodeRuneInString(content[end:])
			if !isReferenceValueRune(r) {
				break
			}
			end += size
		}

		parts := strings.Split(content[valueStart:end], ":")
		resourceID, err := parseCanonicalUUID(parts[0])
		if err != nil {
			return nil, fmt.Errorf("%w at byte %d", ErrInvalidResourceID, start)
		}

		qualifier, err := validateQualifier(kind, parts)
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

func isReferenceValueRune(value rune) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' || value == ':'
}

func parseCanonicalUUID(value string) (uuid.UUID, error) {
	resourceID, err := uuid.Parse(value)
	if err != nil || !strings.EqualFold(resourceID.String(), value) {
		return uuid.Nil, ErrInvalidResourceID
	}
	return resourceID, nil
}

func validateQualifier(kind string, parts []string) (string, error) {
	if kind != KindDebate {
		if len(parts) != 1 {
			return "", ErrInvalidQualifier
		}
		return "", nil
	}
	if len(parts) != 2 || parts[1] != QualifierSupport && parts[1] != QualifierOppose {
		return "", ErrInvalidQualifier
	}
	return parts[1], nil
}
