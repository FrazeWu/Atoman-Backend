package portal

import "time"

type HotItem struct {
	ID          string     `json:"id"`
	Module      string     `json:"module"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	ImageURL    string     `json:"image_url"`
	TargetPath  string     `json:"target_path"`
	Score       float64    `json:"score"`
	ScoreLabel  string     `json:"score_label"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type HotSection struct {
	Module string    `json:"module"`
	Title  string    `json:"title"`
	Items  []HotItem `json:"items"`
}

type HotResponse struct {
	Featured []HotItem    `json:"featured"`
	Sections []HotSection `json:"sections"`
}
