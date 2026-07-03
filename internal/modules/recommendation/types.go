package recommendation

type Mode string

const (
	ModeHot      Mode = "hot"
	ModeFeatured Mode = "featured"
	ModeDiscover Mode = "discover"
)

type EntityType string

const (
	EntityArticle EntityType = "article"
	EntityChannel EntityType = "channel"
	EntityBlog    EntityType = "blog"
	EntityPodcast EntityType = "podcast"
	EntityVideo   EntityType = "video"
	EntityAlbum   EntityType = "album"
	EntityArtist  EntityType = "artist"
)

type Candidate struct {
	Module           string
	EntityType       EntityType
	EntityID         string
	SourceKey        string
	QualityScore     float64
	TrendScore       float64
	FreshnessScore   float64
	AuthorityScore   float64
	ExposureScore    float64
	EditorialScore   float64
	PublishedAtUnix  int64
}

type RankedItem struct {
	Candidate
	FinalScore float64
}
