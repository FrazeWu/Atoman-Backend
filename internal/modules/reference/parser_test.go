package reference

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const testReferenceID = "11111111-2222-4333-8444-555555555555"

func TestParseFindsUserAndResourceReferencesWithCodePointOffsets(t *testing.T) {
	content := "你好 @alice 引用了 @post:" + testReferenceID + "。"

	got, err := Parse(content)

	require.NoError(t, err)
	require.Equal(t, []ParsedReference{
		{Kind: KindUser, TargetType: TargetTypeUser, Identifier: "alice", Start: 3, End: 9},
		{Kind: KindResource, TargetType: "post", Identifier: testReferenceID, Start: 14, End: 56},
	}, got)
}

func TestParseSupportsEveryResourceType(t *testing.T) {
	for _, targetType := range SupportedResourceTypes {
		t.Run(targetType, func(t *testing.T) {
			content := fmt.Sprintf("@%s:%s", targetType, testReferenceID)
			got, err := Parse(content)
			require.NoError(t, err)
			require.Equal(t, []ParsedReference{{
				Kind: KindResource, TargetType: targetType, Identifier: testReferenceID,
				Start: 0, End: len([]rune(content)),
			}}, got)
		})
	}
}

func TestParseSkipsCodeAndLinkDestinationsButKeepsProse(t *testing.T) {
	content := "正文 @alice [链接](https://example.com/@hidden) `@inline`\n\n```text\n@block\n```\n\n> @bob"

	got, err := Parse(content)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "alice", got[0].Identifier)
	require.Equal(t, "bob", got[1].Identifier)
	require.Equal(t, "@bob", string([]rune(content)[got[1].Start:got[1].End]))
}

func TestParseSkipsDebateDirectionReferences(t *testing.T) {
	content := "@debate:" + testReferenceID + ":support @debate:" + testReferenceID + ":oppose @debate:" + testReferenceID

	got, err := Parse(content)

	require.NoError(t, err)
	require.Equal(t, []ParsedReference{{
		Kind: KindResource, TargetType: "debate", Identifier: testReferenceID,
		Start: len([]rune("@debate:" + testReferenceID + ":support @debate:" + testReferenceID + ":oppose ")),
		End:   len([]rune(content)),
	}}, got)
}

func TestParseRejectsMalformedResourceTokens(t *testing.T) {
	for name, content := range map[string]string{
		"unsupported type": "@unknown:" + testReferenceID,
		"invalid uuid":     "@post:not-a-uuid",
		"trailing word":    "@post:" + testReferenceID + "extra",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(content)
			require.ErrorIs(t, err, ErrInvalidSyntax)
		})
	}
}

func TestParseDoesNotTreatEmailOrEmbeddedAtSignAsMention(t *testing.T) {
	got, err := Parse("mail@example.com foo@bar and (@alice)")

	require.NoError(t, err)
	require.Equal(t, []ParsedReference{{
		Kind: KindUser, TargetType: TargetTypeUser, Identifier: "alice", Start: 30, End: 36,
	}}, got)
}

func TestParseLenientKeepsValidReferencesAroundIncompleteDraftToken(t *testing.T) {
	content := "@alice @post:typing @album:" + testReferenceID

	got := ParseLenient(content)

	require.Len(t, got, 2)
	require.Equal(t, "alice", got[0].Identifier)
	require.Equal(t, "album", got[1].TargetType)
}
