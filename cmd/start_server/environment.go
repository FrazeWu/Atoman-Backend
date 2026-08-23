package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func resolveEnvFile(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "dev":
		return ".env.dev"
	case "prod":
		return ".env.prod"
	default:
		return ".env.dev"
	}
}

func loadEnvironment(mode string) string {
	envFile := resolveEnvFile(mode)
	if err := godotenv.Load(envFile); err == nil {
		return "Loaded " + envFile
	}
	if err := godotenv.Load(".env"); err == nil {
		return "Loaded .env"
	}
	return "No .env file found, using system environment variables"
}

func initializeStorageClient() *s3.S3 {
	if os.Getenv("STORAGE_TYPE") == "local" {
		log.Println("Storage mode: local (S3 disabled)")
		return nil
	}

	s3Client, err := storage.InitS3Client()
	if err != nil {
		log.Printf("WARN: S3 storage unavailable; storage-backed endpoints will return 503: %v", err)
		return nil
	}
	if err := storage.ValidateS3Connection(s3Client); err != nil {
		log.Printf("WARN: S3 storage unavailable; storage-backed endpoints will return 503: %v", err)
		return nil
	}

	log.Println("S3 storage initialized")
	return s3Client
}

func canonicalFrontendOrigins() []string {
	return []string{
		"https://www.atoman.org",
		"https://atoman.org",
	}
}

func configuredAllowedOrigins() []string {
	allowedOrigins := canonicalFrontendOrigins()
	allowedOrigins = append(allowedOrigins,
		"http://localhost:5173",
		"http://localhost:3000",
		"http://127.0.0.1:5173",
		"http://127.0.0.1:3000",
	)
	for _, origin := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			allowedOrigins = append(allowedOrigins, origin)
		}
	}
	return allowedOrigins
}

func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if originAllowed(origin, allowedOrigins) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Request-ID")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		c.Writer.Header().Set("Access-Control-Max-Age", "600")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func originAllowed(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if origin == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*.")
			if strings.HasPrefix(origin, "https://") && strings.HasSuffix(strings.TrimPrefix(origin, "https://"), suffix) {
				return true
			}
			if strings.HasPrefix(origin, "http://") && strings.HasSuffix(strings.TrimPrefix(origin, "http://"), suffix) {
				return true
			}
		}
	}
	return false
}

func validateAuthEnvironment() error {
	if os.Getenv("ENV") == "production" && strings.TrimSpace(os.Getenv("AUTH_CODE_SECRET")) == "" {
		return fmt.Errorf("AUTH_CODE_SECRET environment variable is required")
	}
	return nil
}
