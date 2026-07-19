package debate

import (
	"sort"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/resourceref"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func debateReferenceIDs(content string) ([]uuid.UUID, error) {
	refs, err := resourceref.Parse(content)
	if err != nil {
		return nil, apperr.BadRequest("debate.reference_invalid", "Content contains an invalid resource reference")
	}
	ids := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == resourceref.KindDebate {
			ids = append(ids, ref.ResourceID)
		}
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
