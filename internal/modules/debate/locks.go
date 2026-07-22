package debate

import (
	"fmt"
	"regexp"
	"sort"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type debateReferenceToken struct {
	Raw, Stance string
	ResourceID  uuid.UUID
	Start, End  int
}

var debateReferencePattern = regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_-]*):([0-9a-fA-F-]{36})(?::([A-Za-z]+))?`)

func parseDebateReferences(content string) ([]debateReferenceToken, error) {
	matches := debateReferencePattern.FindAllStringSubmatchIndex(content, -1)
	refs := make([]debateReferenceToken, 0, len(matches))
	for _, match := range matches {
		raw := content[match[0]:match[1]]
		kind := content[match[2]:match[3]]
		id, err := uuid.Parse(content[match[4]:match[5]])
		stance := ""
		if match[6] >= 0 {
			stance = content[match[6]:match[7]]
		}
		if err != nil || kind != "debate" || (stance != model.DebateRelationSupport && stance != model.DebateRelationOppose) {
			return nil, fmt.Errorf("invalid debate reference %q", raw)
		}
		refs = append(refs, debateReferenceToken{Raw: raw, ResourceID: id, Stance: stance,
			Start: utf8.RuneCountInString(content[:match[0]]), End: utf8.RuneCountInString(content[:match[1]])})
	}
	return refs, nil
}

func debateReferenceIDs(content string) ([]uuid.UUID, error) {
	refs, err := parseDebateReferences(content)
	if err != nil {
		return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid debate reference")
	}
	ids := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ResourceID)
	}
	return ids, nil
}

func sortedUniqueDebateIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

// All debate mutations acquire Debate rows in this order before revisions or relations.
func lockDebatesInOrder(tx *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]model.Debate, error) {
	locked := make(map[uuid.UUID]model.Debate, len(ids))
	for _, id := range sortedUniqueDebateIDs(ids) {
		var debate model.Debate
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Limit(1).Find(&debate)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			locked[id] = debate
		}
	}
	return locked, nil
}
