package music

import (
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func musicOperationLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		operation := musicOperation(c.Request.URL.Path)
		if operation == "" {
			return
		}
		status := c.Writer.Status()
		log.Printf(
			"music_operation=%q request_id=%q method=%q status=%d duration_ms=%d response_bytes=%d failed=%t",
			operation,
			c.GetString("request_id"),
			c.Request.Method,
			status,
			time.Since(startedAt).Milliseconds(),
			c.Writer.Size(),
			status >= 400,
		)
	}
}

func musicOperation(path string) string {
	switch {
	case strings.Contains(path, "/music/search"):
		return "search"
	case strings.Contains(path, "/music/imports/"):
		return "import"
	case strings.HasSuffix(path, "/music/playback-progress"):
		return "playback_progress"
	case strings.HasSuffix(path, "/music/playback-session"):
		return "playback_session"
	case strings.HasSuffix(path, "/music/plays"):
		return "play"
	case strings.Contains(path, "/music/recommend/") || strings.HasSuffix(path, "/music/home"):
		return "recommend"
	default:
		return ""
	}
}
