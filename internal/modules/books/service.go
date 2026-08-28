package books

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"
)

// Service owns book authorization and future catalog/import operations.
type Service struct {
	db           *gorm.DB
	bookUpload   bookUploadStore
	virusScanner bookVirusScanner
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (s *Service) WithBookUploadStore(store bookUploadStore) *Service {
	s.bookUpload = store
	return s
}

func (s *Service) WithS3(client *s3.S3) *Service {
	s.bookUpload = newS3BookUploadStore(client)
	return s
}

func (s *Service) WithVirusScanner(scanner bookVirusScanner) *Service {
	s.virusScanner = scanner
	return s
}
