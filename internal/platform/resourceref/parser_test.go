package resourceref

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseIgnoresUserMentionsAlongsideResourceReferences(t *testing.T) {
	const raw = "@album:550e8400-e29b-41d4-a716-446655440000"
	content := "请 @alice 看 " + raw

	references, err := Parse(content)

	require.NoError(t, err)
	require.Equal(t, []Reference{{
		Raw:        raw,
		Kind:       KindAlbum,
		ResourceID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Start:      len("请 @alice 看 "),
		End:        len(content),
	}}, references)
}

func TestParseSupportsFixedKinds(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	kinds := []string{
		KindPost,
		KindThread,
		KindFeed,
		KindArticle,
		KindArtist,
		KindAlbum,
		KindSong,
		KindPlaylist,
		KindPodcast,
		KindEpisode,
		KindVideo,
		KindPerson,
		KindEvent,
		KindChannel,
		KindCollection,
		KindComment,
	}

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			raw := fmt.Sprintf("@%s:%s", kind, id)
			references, err := Parse(raw)
			require.NoError(t, err)
			require.Equal(t, []Reference{{
				Raw: raw, Kind: kind, ResourceID: id, Start: 0, End: len(raw),
			}}, references)
		})
	}
}

func TestParseRejectsUnknownKind(t *testing.T) {
	_, err := Parse("@unknown:550e8400-e29b-41d4-a716-446655440000")
	require.ErrorIs(t, err, ErrUnknownKind)
}

func TestParseRejectsInvalidUUID(t *testing.T) {
	_, err := Parse("@album:not-a-uuid")
	require.ErrorIs(t, err, ErrInvalidResourceID)
}

func TestParseRequiresDebateQualifier(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	for _, qualifier := range []string{QualifierSupport, QualifierOppose} {
		t.Run(qualifier, func(t *testing.T) {
			raw := fmt.Sprintf("@%s:%s:%s", KindDebate, id, qualifier)
			references, err := Parse(raw)
			require.NoError(t, err)
			require.Equal(t, []Reference{{
				Raw: raw, Kind: KindDebate, ResourceID: id, Qualifier: qualifier, Start: 0, End: len(raw),
			}}, references)
		})
	}

	_, err := Parse("@debate:550e8400-e29b-41d4-a716-446655440000")
	require.ErrorIs(t, err, ErrInvalidQualifier)

	_, err = Parse("@debate:550e8400-e29b-41d4-a716-446655440000:neutral")
	require.ErrorIs(t, err, ErrInvalidQualifier)
}

func TestParseRejectsQualifierOnNonDebate(t *testing.T) {
	_, err := Parse("@album:550e8400-e29b-41d4-a716-446655440000:support")
	require.ErrorIs(t, err, ErrInvalidQualifier)
}

func TestParseStopsBeforeASCIITrailingPunctuation(t *testing.T) {
	raw := "@post:550e8400-e29b-41d4-a716-446655440000"
	content := "See " + raw + ", then continue."

	references, err := Parse(content)

	require.NoError(t, err)
	require.Len(t, references, 1)
	require.Equal(t, raw, references[0].Raw)
	require.Equal(t, raw, content[references[0].Start:references[0].End])
}

func TestParseHandlesUnicodeAndChineseTrailingPunctuation(t *testing.T) {
	raw := "@debate:550e8400-e29b-41d4-a716-446655440000:oppose"
	content := "中文正文（反方引用 " + raw + "。）"

	references, err := Parse(content)

	require.NoError(t, err)
	require.Len(t, references, 1)
	require.Equal(t, raw, references[0].Raw)
	require.Equal(t, len("中文正文（反方引用 "), references[0].Start)
	require.Equal(t, raw, content[references[0].Start:references[0].End])
}

func TestParseKeepsDuplicateReferencesWithDistinctPositions(t *testing.T) {
	raw := "@song:550e8400-e29b-41d4-a716-446655440000"
	content := raw + " / " + raw

	references, err := Parse(content)

	require.NoError(t, err)
	require.Len(t, references, 2)
	require.Equal(t, 0, references[0].Start)
	require.Equal(t, len(raw)+3, references[1].Start)
	for _, reference := range references {
		require.Equal(t, raw, content[reference.Start:reference.End])
	}
}

func TestParserErrorsAreComparableSentinels(t *testing.T) {
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrUnknownKind), ErrUnknownKind))
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrInvalidResourceID), ErrInvalidResourceID))
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrInvalidQualifier), ErrInvalidQualifier))
}
