package reputation

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Handler struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/users/:userID", h.getUserSnapshot)
	group.GET("/blog/posts/:postID", h.getBlogSnapshot)
	group.POST("/runs", h.runShadow)
}

// getUserSnapshot godoc
// @Summary 获取用户质量快照
// @Tags reputation
// @Produce json
// @Param userID path string true "用户 UUID"
// @Success 200 {object} UserSnapshotDTO
// @Failure 400 {object} map[string]any
// @Router /api/v1/reputation/users/{userID} [get]
func (h *Handler) getUserSnapshot(c *gin.Context) {
	userID, ok := reputationUUID(c, "userID")
	if !ok {
		return
	}
	result, err := h.service.LatestUserSnapshot(c.Request.Context(), userID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBlogSnapshot godoc
// @Summary 获取博客质量影子快照
// @Tags reputation
// @Produce json
// @Param postID path string true "博客 UUID"
// @Success 200 {object} BlogSnapshotDTO
// @Failure 400 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /api/v1/reputation/blog/posts/{postID} [get]
func (h *Handler) getBlogSnapshot(c *gin.Context) {
	postID, ok := reputationUUID(c, "postID")
	if !ok {
		return
	}
	result, err := h.service.LatestBlogSnapshot(c.Request.Context(), postID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = apperr.NotFound("reputation.blog_snapshot_not_found", "No published reputation snapshot for this post")
		}
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// runShadow godoc
// @Summary 执行声誉影子计算
// @Tags reputation
// @Produce json
// @Success 201 {object} map[string]string
// @Failure 401 {object} map[string]any
// @Failure 403 {object} map[string]any
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/reputation/runs [post]
func (h *Handler) runShadow(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("reputation.run_forbidden", "Admin role required"))
		return
	}
	run, err := h.service.RunShadow(c.Request.Context(), time.Now().UTC())
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, gin.H{"run_id": run.ID.String(), "status": run.Status})
}

func reputationUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func StartWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	if strings.EqualFold(os.Getenv("REPUTATION_WORKER_ENABLED"), "false") ||
		!db.Migrator().HasTable(&model.ReputationRun{}) ||
		!db.Migrator().HasTable(&model.BlogQualitySnapshot{}) ||
		!db.Migrator().HasTable(&model.UserReputationSnapshot{}) {
		close(done)
		return done
	}
	interval := 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("REPUTATION_WORKER_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		}
	}
	go func() {
		defer close(done)
		service := NewService(db)
		run := func() {
			result, err := service.RunShadow(ctx, time.Now().UTC())
			if err != nil {
				log.Printf("reputation shadow run failed: %v", err)
				return
			}
			log.Printf("reputation shadow run published: %s", result.ID)
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return done
}
