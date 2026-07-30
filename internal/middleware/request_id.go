package middleware

import (
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const requestIDContextKey = "request_id"

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		c.Set(requestIDContextKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func AccessLogMiddleware(logger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		logger.Printf(
			"request_id=%q method=%q path=%q status=%d duration_ms=%d",
			RequestID(c), c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(startedAt).Milliseconds(),
		)
	}
}

func RequestID(c *gin.Context) string {
	value, _ := c.Get(requestIDContextKey)
	requestID, _ := value.(string)
	return requestID
}
