package comment

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateMentionsUsesCodePointOffsets(t *testing.T) {
	err := ValidateMentions("你好 @阿明", []MentionInput{{UserID: uuid.New(), Start: 3, End: 6}})
	require.NoError(t, err)
}

func TestValidateMentionsAcceptsRepeatedUserOccurrences(t *testing.T) {
	userID := uuid.New()
	err := ValidateMentions("@阿明 回应 @阿明", []MentionInput{
		{UserID: userID, Start: 0, End: 3},
		{UserID: userID, Start: 7, End: 10},
	})
	require.NoError(t, err)
}

func TestValidateMentionsRejectsInvalidRangesAndText(t *testing.T) {
	userID := uuid.New()
	tests := map[string]struct {
		content string
		inputs  []MentionInput
	}{
		"negative start": {"@甲", []MentionInput{{UserID: userID, Start: -1, End: 2}}},
		"past end":       {"@甲", []MentionInput{{UserID: userID, Start: 0, End: 3}}},
		"empty range":    {"@甲", []MentionInput{{UserID: userID, Start: 1, End: 1}}},
		"missing at":     {"阿明", []MentionInput{{UserID: userID, Start: 0, End: 2}}},
		"empty username": {"@ hi", []MentionInput{{UserID: userID, Start: 0, End: 1}}},
		"includes space": {"@阿 明", []MentionInput{{UserID: userID, Start: 0, End: 4}}},
		"overlap": {"@阿明", []MentionInput{
			{UserID: userID, Start: 0, End: 3},
			{UserID: uuid.New(), Start: 0, End: 2},
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateMentions(tc.content, tc.inputs))
		})
	}
}

func TestValidateMentionsRejectsNilUserID(t *testing.T) {
	err := ValidateMentions("@阿明", []MentionInput{{UserID: uuid.Nil, Start: 0, End: 3}})
	require.Error(t, err)
}

func TestMentionRecipientsDeduplicatesInStableOrder(t *testing.T) {
	authorID := uuid.New()
	replyAuthorID := uuid.New()
	mentionedFirst := uuid.New()
	mentionedSecond := uuid.New()
	got := MentionRecipients(authorID, &replyAuthorID, []MentionInput{
		{UserID: mentionedFirst},
		{UserID: replyAuthorID},
		{UserID: authorID},
		{UserID: mentionedSecond},
		{UserID: mentionedFirst},
	})
	require.Equal(t, []uuid.UUID{replyAuthorID, mentionedFirst, mentionedSecond}, got)
}

func TestMentionRecipientsHandlesNoReply(t *testing.T) {
	authorID := uuid.New()
	mentioned := uuid.New()
	require.Equal(t, []uuid.UUID{mentioned}, MentionRecipients(authorID, nil, []MentionInput{{UserID: mentioned}}))
}

func TestMentionRecipientsFiltersNilUserIDs(t *testing.T) {
	authorID := uuid.New()
	nilReplyAuthorID := uuid.Nil
	mentioned := uuid.New()
	got := MentionRecipients(authorID, &nilReplyAuthorID, []MentionInput{
		{UserID: uuid.Nil},
		{UserID: mentioned},
	})
	require.Equal(t, []uuid.UUID{mentioned}, got)
}
