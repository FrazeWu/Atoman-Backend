package service

import (
	"testing"

	"atoman/internal/model"

	"github.com/stretchr/testify/require"
)

func TestEnsureVideoPreviewJobSkipsPublicObjectStorageURL(t *testing.T) {
	require.False(t, needsVideoPreviewJob(model.Video{StorageType: "local", VideoURL: "https://assets.atoman.org/video/source.mp4"}))
	require.False(t, needsVideoPreviewJob(model.Video{StorageType: "external", VideoURL: "https://youtube.com/watch?v=video"}))
	require.True(t, needsVideoPreviewJob(model.Video{StorageType: "local", VideoURL: "/uploads/video/source.mp4"}))
}
