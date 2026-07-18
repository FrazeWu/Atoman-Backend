package music

type ParsedLyricLine struct {
	LineKey     string
	LineIndex   int
	TimeMS      *int
	Text        string
	Translation string
}
