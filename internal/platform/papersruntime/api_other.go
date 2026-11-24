//go:build !linux

package papersruntime

import (
	"atoman/internal/modules/papers"
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
)

func API(_ context.Context, db *gorm.DB) (*papers.Service, error) {
	svc := papers.NewService(db)
	if os.Getenv("PAPERS_COMPILE_ENABLE") == "1" {
		return svc, errors.New("Papers compilation requires the approved Linux deployment")
	}
	return svc, nil
}

func APIWithWorker(ctx context.Context, db *gorm.DB) (*papers.Service, <-chan struct{}, error) {
	svc, err := API(ctx, db)
	done := make(chan struct{})
	close(done)
	return svc, done, err
}
