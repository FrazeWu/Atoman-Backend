package handlers

import "testing"

func TestVideoHandlerOnlyOwnsRouteAssembly(t *testing.T) {
	functions := topLevelFunctionNames(t, "video_handler.go")
	if len(functions) != 1 || !functions["SetupVideoRoutes"] {
		t.Fatalf("video_handler.go functions = %v, want SetupVideoRoutes only", functions)
	}
}

func TestVideoSwaggerAnnotationsStayWithTheirHandlers(t *testing.T) {
	assertSwaggerAnnotations(t, "video*_handler.go", []string{
		"ReprocessVideo",
		"UploadVideoFile",
		"UploadVideoCover",
		"GetVideos",
		"GetVideo",
		"IncrementVideoView",
		"CreateVideo",
		"UpdateVideo",
		"DeleteVideo",
		"GetRecommendedVideos",
	})
}
