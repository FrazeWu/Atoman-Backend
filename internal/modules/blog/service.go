package blog

import (
	"context"
	"io"

	"atoman/internal/modules/reference"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ExportAssetReader interface {
	ReadExportAsset(context.Context, string, int64) (io.ReadCloser, error)
}

type Service struct {
	db           *gorm.DB
	repo         *Repo
	references   *reference.Service
	exportAssets ExportAssetReader
}

func (s *Service) WithExportAssetReader(reader ExportAssetReader) *Service {
	s.exportAssets = reader
	return s
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepo(db), references: reference.NewService(db)}
}

func dedupeUUIDs(values []uuid.UUID) []uuid.UUID {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
