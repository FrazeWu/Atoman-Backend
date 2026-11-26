package papers

import (
	"encoding/json"
	"io"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"github.com/google/uuid"
)

const (
	paperAIHistoryTurns = 6
	paperAIHistoryBytes = 16 << 10
)

type paperAIHistoryTurn struct {
	Task        string `json:"task"`
	Instruction string `json:"instruction"`
	Reply       string `json:"reply"`
}

func validPaperAIHistory(history []paperAIHistoryTurn, key string) bool {
	if len(history) > paperAIHistoryTurns {
		return false
	}
	total := 0
	for _, turn := range history {
		if turn.Task != "revise" && turn.Task != "explain" && turn.Task != "project" {
			return false
		}
		if strings.TrimSpace(turn.Instruction) == "" || strings.TrimSpace(turn.Reply) == "" ||
			!paperAIValidText(turn.Instruction, 4000) || !paperAIValidText(turn.Reply, paperAIHistoryBytes) ||
			strings.Contains(turn.Instruction, key) || strings.Contains(turn.Reply, key) {
			return false
		}
		total += len(turn.Instruction) + len(turn.Reply)
		if total > paperAIHistoryBytes {
			return false
		}
	}
	return true
}

type paperAIProjectEdit struct {
	FileID       uuid.UUID `json:"file_id"`
	Path         string    `json:"path"`
	BaseRevision int64     `json:"base_revision"`
	Before       string    `json:"before"`
	After        string    `json:"after"`
}
type paperAIProposedEdit struct {
	Path   string  `json:"path"`
	Before string  `json:"before"`
	After  *string `json:"after"`
}

func paperAIProjectChanged() error {
	return apperr.Conflict("papers.ai_context_changed", "Saved project revision changed; review source again")
}
func parsePaperAIProject(raw string, approved map[string]model.PaperFile) (string, []paperAIProjectEdit, error) {
	invalid := func() (string, []paperAIProjectEdit, error) {
		return "", nil, paperAIProviderError("papers.ai_invalid_project_response")
	}
	if !paperAIValidText(raw, paperAIResponseLimit) {
		return invalid()
	}
	var parsed struct {
		Summary string                 `json:"summary"`
		Edits   *[]paperAIProposedEdit `json:"edits"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&parsed) != nil || parsed.Edits == nil || !paperAIValidText(parsed.Summary, 4000) || strings.TrimSpace(parsed.Summary) == "" {
		return invalid()
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || len(*parsed.Edits) > paperAIExtraLimit+1 || len(*parsed.Edits) > len(approved) {
		return invalid()
	}
	seen := map[uuid.UUID]bool{}
	edits := make([]paperAIProjectEdit, 0, len(*parsed.Edits))
	for _, item := range *parsed.Edits {
		file, ok := approved[item.Path]
		if !ok || file.ID == uuid.Nil || file.Kind != "text" || file.Revision <= 0 || seen[file.ID] || item.After == nil ||
			item.Before == "" || !paperAIValidText(item.Before, paperAISourceLimit) || !paperAIValidText(*item.After, paperAIResponseLimit) || item.Before == *item.After {
			return invalid()
		}
		// Count overlapping matches too. The model may not choose the occurrence
		// implicitly when two identical substrings exist in an approved file.
		i := strings.Index(file.Content, item.Before)
		if i < 0 || strings.Contains(file.Content[i+1:], item.Before) {
			return invalid()
		}
		next := file.Content[:i] + *item.After + file.Content[i+len(item.Before):]
		if validateText(next) != nil {
			return invalid()
		}
		seen[file.ID] = true
		edits = append(edits, paperAIProjectEdit{FileID: file.ID, Path: file.Path, BaseRevision: file.Revision, Before: item.Before, After: *item.After})
	}
	return parsed.Summary, edits, nil
}

func paperAIProjectSecretFree(summary string, edits []paperAIProjectEdit, key string) bool {
	if key == "" || strings.Contains(summary, key) {
		return false
	}
	for _, edit := range edits {
		if strings.Contains(edit.Path, key) || strings.Contains(edit.Before, key) || strings.Contains(edit.After, key) {
			return false
		}
	}
	return true
}
