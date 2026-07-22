package debate

import (
	"errors"
	"sort"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) GetDebateGraph(rootID uuid.UUID, view string, depth int) (DebateGraph, error) {
	if view == "" {
		view = "tree"
	}
	if view != "tree" && view != "graph" {
		return DebateGraph{}, apperr.BadRequest("validation.invalid_request", "view must be tree or graph")
	}
	if depth <= 0 {
		if view == "tree" {
			depth = 3
		} else {
			depth = 2
		}
	}
	if depth > 10 {
		depth = 10
	}
	var root model.Debate
	if err := s.db.Preload("User").First(&root, "id = ?", rootID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DebateGraph{}, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return DebateGraph{}, err
	}

	nodes := map[uuid.UUID]model.Debate{root.ID: root}
	seenRelations := map[uuid.UUID]bool{}
	relations := []model.DebateRelation{}
	type queueItem struct {
		id    uuid.UUID
		level int
	}
	queue := []queueItem{{id: root.ID}}
	boundary := []uuid.UUID{}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.level == depth {
			boundary = append(boundary, item.id)
			continue
		}
		edges, err := graphEdges(s.db, item.id, view)
		if err != nil {
			return DebateGraph{}, err
		}
		for _, edge := range edges {
			if !seenRelations[edge.ID] {
				seenRelations[edge.ID] = true
				relations = append(relations, edge)
			}
			nextID := edge.SourceDebateID
			if nextID == item.id {
				nextID = edge.TargetDebateID
			}
			if _, seen := nodes[nextID]; seen {
				continue
			}
			var node model.Debate
			if err := s.db.Preload("User").First(&node, "id = ?", nextID).Error; err != nil {
				return DebateGraph{}, err
			}
			nodes[nextID] = node
			queue = append(queue, queueItem{id: nextID, level: item.level + 1})
		}
	}

	expandable := []uuid.UUID{}
	for _, id := range boundary {
		edges, err := graphEdges(s.db, id, view)
		if err != nil {
			return DebateGraph{}, err
		}
		for _, edge := range edges {
			other := edge.SourceDebateID
			if other == id {
				other = edge.TargetDebateID
			}
			if _, loaded := nodes[other]; !loaded {
				expandable = append(expandable, id)
				break
			}
		}
	}
	nodeList := make([]model.Debate, 0, len(nodes))
	for _, node := range nodes {
		nodeList = append(nodeList, node)
	}
	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].ID.String() < nodeList[j].ID.String() })
	sort.Slice(relations, func(i, j int) bool { return relations[i].ID.String() < relations[j].ID.String() })
	sort.Slice(expandable, func(i, j int) bool { return expandable[i].String() < expandable[j].String() })
	return DebateGraph{RootID: rootID, Nodes: nodeList, Relations: relations, ExpandableNodeIDs: expandable}, nil
}

func graphEdges(db *gorm.DB, nodeID uuid.UUID, view string) ([]model.DebateRelation, error) {
	query := db.Where("status = ?", model.DebateRelationActive)
	if view == "tree" {
		query = query.Where("target_debate_id = ? AND stance = ?", nodeID, model.DebateRelationSupport)
	} else {
		query = query.Where("source_debate_id = ? OR target_debate_id = ?", nodeID, nodeID)
	}
	var edges []model.DebateRelation
	err := query.Find(&edges).Error
	return edges, err
}
