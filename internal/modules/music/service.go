package music

import (
	"sync"

	"github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"
)

type Service struct {
	db                   *gorm.DB
	repo                 *Repo
	s3                   *s3.S3
	albumImportMultipart albumImportMultipartStore
	assetUploadMultipart albumImportMultipartStore
	albumLinkSuggestions AlbumLinkSuggestionProvider
	lyricsSaveMu         sync.Mutex
	lyricsVoteMu         sync.Mutex
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func NewServiceWithS3(db *gorm.DB, s3Client *s3.S3) *Service {
	return &Service{
		db:                   db,
		repo:                 NewRepo(db),
		s3:                   s3Client,
		albumImportMultipart: newS3AlbumImportMultipartStore(s3Client),
		assetUploadMultipart: newS3PublicMusicMultipartStore(s3Client),
	}
}

// WithAlbumLinkSuggestionProvider enables MusicBrainz-backed album suggestions.
// The provider is optional so catalog reads remain available without external metadata.
func (s *Service) WithAlbumLinkSuggestionProvider(provider AlbumLinkSuggestionProvider) *Service {
	s.albumLinkSuggestions = provider
	return s
}

func normalizeMusicRecommendationPage(page int, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
