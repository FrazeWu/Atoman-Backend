package debate

import (
	"errors"
	"sort"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxDebateGraphNodes = 200
	maxDebateGraphEdges = 500
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
	frontier := []uuid.UUID{root.ID}
	graph := DebateGraph{RootID: rootID}
	for level := 0; level < depth && len(frontier) > 0; level++ {
		sort.Slice(frontier, func(i, j int) bool { return frontier[i].String() < frontier[j].String() })
		edges, truncated, err := graphEdgesForNodes(s.db, frontier, view, maxDebateGraphEdges-len(relations)+1)
		if err != nil {
			return DebateGraph{}, err
		}
		graph.HasMore = graph.HasMore || truncated
		frontierSet := make(map[uuid.UUID]struct{}, len(frontier))
		for _, id := range frontier {
			frontierSet[id] = struct{}{}
		}
		pending := make([]uuid.UUID, 0)
		pendingSet := make(map[uuid.UUID]struct{})
		for _, edge := range edges {
			if seenRelations[edge.ID] {
				continue
			}
			if len(relations) >= maxDebateGraphEdges {
				graph.HasMore = true
				continue
			}
			nextID := graphNextNode(edge, view, frontierSet, nodes, pendingSet)
			if nextID != uuid.Nil {
				if len(nodes)+len(pendingSet) >= maxDebateGraphNodes {
					graph.HasMore = true
					continue
				}
				pendingSet[nextID] = struct{}{}
				pending = append(pending, nextID)
			}
			seenRelations[edge.ID] = true
			relations = append(relations, edge)
		}
		if len(pending) == 0 {
			frontier = nil
			continue
		}
		var loaded []model.Debate
		if err := s.db.Preload("User").Where("id IN ?", pending).Find(&loaded).Error; err != nil {
			return DebateGraph{}, err
		}
		frontier = make([]uuid.UUID, 0, len(loaded))
		for _, node := range loaded {
			if _, exists := nodes[node.ID]; exists {
				continue
			}
			nodes[node.ID] = node
			frontier = append(frontier, node.ID)
		}
	}

	boundary := frontier
	expandableSet := make(map[uuid.UUID]struct{})
	if len(boundary) > 0 {
		edges, truncated, err := graphEdgesForNodes(s.db, boundary, view, maxDebateGraphEdges+1)
		if err != nil {
			return DebateGraph{}, err
		}
		graph.HasMore = graph.HasMore || truncated
		boundarySet := make(map[uuid.UUID]struct{}, len(boundary))
		for _, id := range boundary {
			boundarySet[id] = struct{}{}
		}
		for _, edge := range edges {
			if nextID := graphNextNode(edge, view, boundarySet, nodes, nil); nextID != uuid.Nil {
				if view == "tree" {
					expandableSet[edge.TargetDebateID] = struct{}{}
				} else if _, ok := boundarySet[edge.SourceDebateID]; ok {
					expandableSet[edge.SourceDebateID] = struct{}{}
				} else {
					expandableSet[edge.TargetDebateID] = struct{}{}
				}
			}
		}
	}
	nodeList := make([]DebateGraphNodeDTO, 0, len(nodes))
	for _, node := range nodes {
		nodeList = append(nodeList, DebateGraphNodeDTO{Debate: node, User: model.PublicUserFromUser(node.User)})
	}
	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].ID.String() < nodeList[j].ID.String() })
	sort.Slice(relations, func(i, j int) bool { return relations[i].ID.String() < relations[j].ID.String() })
	expandable := make([]uuid.UUID, 0, len(expandableSet))
	for id := range expandableSet {
		expandable = append(expandable, id)
	}
	sort.Slice(expandable, func(i, j int) bool { return expandable[i].String() < expandable[j].String() })
	graph.Nodes = nodeList
	graph.Relations = relations
	graph.ExpandableNodeIDs = expandable
	return graph, nil
}

func graphNextNode(edge model.DebateRelation, view string, frontier map[uuid.UUID]struct{}, nodes map[uuid.UUID]model.Debate, pending map[uuid.UUID]struct{}) uuid.UUID {
	candidate := uuid.Nil
	if view == "tree" {
		if _, ok := frontier[edge.TargetDebateID]; ok {
			candidate = edge.SourceDebateID
		}
	} else if _, ok := frontier[edge.SourceDebateID]; ok {
		candidate = edge.TargetDebateID
	} else if _, ok := frontier[edge.TargetDebateID]; ok {
		candidate = edge.SourceDebateID
	}
	if candidate == uuid.Nil {
		return uuid.Nil
	}
	if _, ok := nodes[candidate]; ok {
		return uuid.Nil
	}
	if _, ok := pending[candidate]; ok {
		return uuid.Nil
	}
	return candidate
}

func graphEdgesForNodes(db *gorm.DB, nodeIDs []uuid.UUID, view string, limit int) ([]model.DebateRelation, bool, error) {
	if len(nodeIDs) == 0 {
		return nil, false, nil
	}
	query := db.Where("status = ?", model.DebateRelationActive)
	if view == "tree" {
		query = query.Where("target_debate_id IN ? AND stance = ?", nodeIDs, model.DebateRelationSupport)
	} else {
		query = query.Where("source_debate_id IN ? OR target_debate_id IN ?", nodeIDs, nodeIDs)
	}
	var edges []model.DebateRelation
	if err := query.Order("id ASC").Limit(limit).Find(&edges).Error; err != nil {
		return nil, false, err
	}
	truncated := len(edges) == limit
	if truncated {
		edges = edges[:len(edges)-1]
	}
	return edges, truncated, nil
}
