package comment

import (
	"errors"
	"unicode"

	"github.com/google/uuid"
)

type MentionInput struct {
	UserID uuid.UUID
	Start  int
	End    int
}

func ValidateMentions(content string, inputs []MentionInput) error {
	runes := []rune(content)
	for i, input := range inputs {
		if input.UserID == uuid.Nil {
			return errors.New("mention user ID is required")
		}
		if input.Start < 0 || input.End > len(runes) || input.End-input.Start < 2 {
			return errors.New("invalid mention range")
		}
		if runes[input.Start] != '@' {
			return errors.New("mention must start with @")
		}
		for _, current := range runes[input.Start+1 : input.End] {
			if !isUsernameRune(current) {
				return errors.New("invalid mention username")
			}
		}
		if input.End < len(runes) && isUsernameRune(runes[input.End]) {
			return errors.New("mention range does not cover the full username")
		}
		for _, previous := range inputs[:i] {
			if input.Start < previous.End && previous.Start < input.End {
				return errors.New("mention ranges overlap")
			}
		}
	}
	return nil
}

func MentionRecipients(authorID uuid.UUID, replyAuthorID *uuid.UUID, inputs []MentionInput) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{authorID: {}}
	recipients := make([]uuid.UUID, 0, len(inputs)+1)
	appendRecipient := func(userID uuid.UUID) {
		if userID == uuid.Nil {
			return
		}
		if _, exists := seen[userID]; exists {
			return
		}
		seen[userID] = struct{}{}
		recipients = append(recipients, userID)
	}

	if replyAuthorID != nil {
		appendRecipient(*replyAuthorID)
	}
	for _, input := range inputs {
		appendRecipient(input.UserID)
	}
	return recipients
}

func isUsernameRune(current rune) bool {
	return unicode.IsLetter(current) || unicode.IsNumber(current) || current == '_' || current == '-'
}
