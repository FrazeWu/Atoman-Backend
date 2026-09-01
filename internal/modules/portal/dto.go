package portal

import "time"

type HotArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type HotItem struct {
	ID              string      `json:"id"`
	Module          string      `json:"module"`
	Kind            string      `json:"kind"`
	Title           string      `json:"title"`
	Summary         string      `json:"summary"`
	ImageURL        string      `json:"image_url"`
	Artists         []HotArtist `json:"artists,omitempty"`
	PlayCount       int64       `json:"play_count,omitempty"`
	BookmarkCount   int64       `json:"bookmark_count,omitempty"`
	AuthorName      string      `json:"author_name,omitempty"`
	AuthorUsername  string      `json:"author_username,omitempty"`
	AuthorAvatarURL string      `json:"author_avatar_url,omitempty"`
	SourceName      string      `json:"source_name,omitempty"`
	SourceImageURL  string      `json:"source_image_url,omitempty"`
	TargetPath      string      `json:"target_path"`
	Score           float64     `json:"score"`
	ScoreLabel      string      `json:"score_label"`
	PublishedAt     *time.Time  `json:"published_at,omitempty"`
}

type HotSection struct {
	Module string    `json:"module"`
	Title  string    `json:"title"`
	Items  []HotItem `json:"items"`
}

type HotResponse struct {
	Featured      []HotItem    `json:"featured"`
	FeaturedTotal int          `json:"featured_total"`
	Sections      []HotSection `json:"sections"`
}
