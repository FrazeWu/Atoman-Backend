package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/ratelimit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxClientErrorBody    = 8 << 10
	clientErrorIPLimit    = 30
	clientErrorIPWindow   = time.Minute
	clientErrorMaxSpan    = 93
	clientErrorSummaryCap = 200
)

var clientErrorKinds = map[string]struct{}{
	"window":             {},
	"unhandledrejection": {},
	"vue":                {},
	"chunk":              {},
}

type clientErrorPayload struct {
	Release   string `json:"release"`
	Route     string `json:"route"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Message   string `json:"message"`
	Stack     string `json:"stack"`
	Context   string `json:"context"`
	UserAgent string `json:"user_agent"`
}

type clientErrorSummaryGroup struct {
	Release string           `json:"release"`
	Route   string           `json:"route"`
	Count   int64            `json:"count"`
	Kinds   map[string]int64 `json:"kinds"`
}

type clientErrorSummary struct {
	From   string                    `json:"from"`
	To     string                    `json:"to"`
	Groups []clientErrorSummaryGroup `json:"groups"`
}

// SetupClientErrorRoutes registers anonymous ingest and the admin summary.
// Ingest is public so logged-out pages can report failures. It does not
// require a CSRF header because sendBeacon cannot set one.
func SetupClientErrorRoutes(router *gin.Engine, db *gorm.DB) {
	router.POST("/api/v1/client-errors", RecordClientError(db, ratelimit.NewFromEnv()))

	admin := router.Group("/api/v1/admin/client-errors")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	admin.GET("/summary", GetClientErrorSummary(db))
}

func RecordClientError(db *gorm.DB, limiter *ratelimit.Limiter) gin.HandlerFunc {
	return recordClientError(db, limiter, clientErrorIPLimit, clientErrorIPWindow, time.Now)
}

func recordClientError(db *gorm.DB, limiter *ratelimit.Limiter, limit int, window time.Duration, now func() time.Time) gin.HandlerFunc {
	if limiter == nil {
		limiter = ratelimit.New()
	}
	if now == nil {
		now = time.Now
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && !middleware.IsTrustedWebOrigin(origin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid origin"})
			return
		}
		if !allowClientError(c, limiter, limit, window) {
			return
		}

		body, err := readClientErrorBody(c)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "client error report is too large"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client error report"})
			return
		}

		report, ok := normalizeClientError(body)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client error report"})
			return
		}
		if err := storeClientError(db, report, now().UTC()); err != nil {
			log.Printf("client_error_store_failed release=%q route=%q kind=%q err=%q", report.Release, report.Route, report.Kind, err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record client error"})
			return
		}
		log.Printf(
			"client_error release=%q route=%q kind=%q name=%q message=%q user_agent=%q",
			report.Release, report.Route, report.Kind, report.Name, report.Message, report.UserAgent,
		)
		c.Status(http.StatusNoContent)
	}
}

func allowClientError(c *gin.Context, limiter *ratelimit.Limiter, limit int, window time.Duration) bool {
	sum := sha256.Sum256([]byte(c.ClientIP()))
	allowed, retryAfter := limiter.Allow("client-error:"+hex.EncodeToString(sum[:8]), limit, window)
	if allowed {
		return true
	}
	seconds := int(retryAfter.Seconds())
	if retryAfter > time.Duration(seconds)*time.Second {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", fmt.Sprintf("%d", seconds))
	c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limited"})
	return false
}

func readClientErrorBody(c *gin.Context) ([]byte, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxClientErrorBody)
	defer c.Request.Body.Close()
	return io.ReadAll(c.Request.Body)
}

func normalizeClientError(body []byte) (clientErrorPayload, bool) {
	if len(body) == 0 || body[0] != '{' {
		return clientErrorPayload{}, false
	}
	var payload clientErrorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return clientErrorPayload{}, false
	}
	release, ok := sanitizeClientErrorRelease(payload.Release)
	if !ok {
		return clientErrorPayload{}, false
	}
	route, ok := sanitizeClientErrorRoute(payload.Route)
	if !ok {
		return clientErrorPayload{}, false
	}
	if _, known := clientErrorKinds[payload.Kind]; !known {
		return clientErrorPayload{}, false
	}
	name := sanitizeClientErrorField(payload.Name, clientErrorMaxName)
	if name == "" {
		name = "Error"
	}
	message := sanitizeClientErrorField(payload.Message, clientErrorMaxMessage)
	stack := sanitizeClientErrorField(limitClientErrorLines(payload.Stack, clientErrorMaxLines), clientErrorMaxStack)
	context := sanitizeClientErrorField(payload.Context, clientErrorMaxContext)
	userAgent := sanitizeClientErrorField(payload.UserAgent, clientErrorMaxAgent)
	return clientErrorPayload{
		Release:   release,
		Route:     route,
		Kind:      payload.Kind,
		Name:      name,
		Message:   message,
		Stack:     stack,
		Context:   context,
		UserAgent: userAgent,
	}, true
}

func storeClientError(db *gorm.DB, report clientErrorPayload, now time.Time) error {
	row := model.ClientErrorDaily{
		Day:             now.Format("2006-01-02"),
		Release:         report.Release,
		Route:           report.Route,
		Kind:            report.Kind,
		Fingerprint:     clientErrorFingerprint(report.Release, report.Route, report.Kind, report.Name, report.Message),
		Count:           1,
		SampleName:      report.Name,
		SampleMessage:   report.Message,
		SampleStack:     report.Stack,
		SampleContext:   report.Context,
		SampleUserAgent: report.UserAgent,
		UpdatedAt:       now,
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "day"},
			{Name: "release"},
			{Name: "route"},
			{Name: "kind"},
			{Name: "fingerprint"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"count":             gorm.Expr("client_error_daily.count + ?", 1),
			"sample_name":       row.SampleName,
			"sample_message":    row.SampleMessage,
			"sample_stack":      row.SampleStack,
			"sample_context":    row.SampleContext,
			"sample_user_agent": row.SampleUserAgent,
			"updated_at":        row.UpdatedAt,
		}),
	}).Create(&row).Error
}

func GetClientErrorSummary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to, ok := clientErrorSummaryRange(c, time.Now().UTC())
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client error summary range"})
			return
		}
		release := strings.TrimSpace(c.Query("release"))
		if release != "" {
			sanitized, valid := sanitizeClientErrorRelease(release)
			if !valid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client error summary range"})
				return
			}
			release = sanitized
		}
		route := strings.TrimSpace(c.Query("route"))
		if route != "" {
			sanitized, valid := sanitizeClientErrorRoute(route)
			if !valid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client error summary range"})
				return
			}
			route = sanitized
		}

		groups, err := queryClientErrorSummary(db, from, to, release, route)
		if err != nil {
			log.Printf("client_error_summary_failed err=%q", err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query client errors"})
			return
		}
		httpx.OK(c, http.StatusOK, clientErrorSummary{From: from, To: to, Groups: groups})
	}
}

func clientErrorSummaryRange(c *gin.Context, now time.Time) (string, string, bool) {
	today := now.Format("2006-01-02")
	from := strings.TrimSpace(c.Query("from"))
	to := strings.TrimSpace(c.Query("to"))
	if from == "" {
		from = now.AddDate(0, 0, -6).Format("2006-01-02")
	}
	if to == "" {
		to = today
	}
	fromDay, err := time.Parse("2006-01-02", from)
	if err != nil {
		return "", "", false
	}
	toDay, err := time.Parse("2006-01-02", to)
	if err != nil || toDay.Before(fromDay) {
		return "", "", false
	}
	if toDay.Sub(fromDay) > clientErrorMaxSpan*24*time.Hour {
		return "", "", false
	}
	return fromDay.Format("2006-01-02"), toDay.Format("2006-01-02"), true
}

type clientErrorAggregateRow struct {
	Release string
	Route   string
	Kind    string
	Count   int64
}

func queryClientErrorSummary(db *gorm.DB, from, to, release, route string) ([]clientErrorSummaryGroup, error) {
	query := db.Model(&model.ClientErrorDaily{}).
		Select("release, route, kind, SUM(count) AS count").
		Where("day >= ? AND day <= ?", from, to)
	if release != "" {
		query = query.Where("release = ?", release)
	}
	if route != "" {
		query = query.Where("route = ?", route)
	}
	var rows []clientErrorAggregateRow
	if err := query.Group("release, route, kind").Order("SUM(count) DESC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	groups := make([]clientErrorSummaryGroup, 0)
	index := map[string]int{}
	for _, row := range rows {
		key := row.Release + "\n" + row.Route
		position, exists := index[key]
		if !exists {
			if len(groups) >= clientErrorSummaryCap {
				continue
			}
			position = len(groups)
			index[key] = position
			groups = append(groups, clientErrorSummaryGroup{
				Release: row.Release,
				Route:   row.Route,
				Kinds:   map[string]int64{},
			})
		}
		groups[position].Count += row.Count
		groups[position].Kinds[row.Kind] += row.Count
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count == groups[j].Count {
			if groups[i].Release == groups[j].Release {
				return groups[i].Route < groups[j].Route
			}
			return groups[i].Release < groups[j].Release
		}
		return groups[i].Count > groups[j].Count
	})
	return groups, nil
}
