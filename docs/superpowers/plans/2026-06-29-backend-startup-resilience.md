# Backend Startup Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Atoman Backend 在 R2/S3 不可用时仍能启动 API，并让存储接口明确返回 `503 storage.unavailable`，同时降低 RSS/fulltext worker 启动压力并改进 owner bootstrap 日志。

**Architecture:** 启动入口保留数据库、迁移、Casbin 等硬依赖，但把对象存储变成可降级依赖：初始化或校验失败时记录 WARN、保留 `s3Client == nil`、继续注册路由。所有依赖 S3 的 handler 通过同一个 guard 返回统一 503。RSS/fulltext worker 的运行参数在各自 service 文件中解析 env，默认启用但延迟启动。

**Tech Stack:** Go、Gin、GORM、AWS SDK for Go S3-compatible client、PostgreSQL、Cloudflare R2、systemd + Nginx。

## Global Constraints

- Backend 修改完成前必须在 `Atoman-Backend/` 运行 `go build ./...`。
- API 变化必须同步更新接口文档或 swagger 注释：新增 503 响应的上传/存储接口要补 `@Failure 503 {object} ErrorResponse`。
- 不把前端构建产物或前端配置重新放回 backend。
- `STORAGE_TYPE=local` 时保持现有本地上传行为，不返回 storage unavailable。
- R2/S3 初始化失败不能调用 `Fatal`，不能阻止 `r.Run(":" + port)`。
- 统一 storage unavailable 响应必须是 HTTP `503`，JSON 为 `{"code":"storage.unavailable","error":"Storage service is unavailable"}`。
- 不记录 `OWNER_PASSWORD` 或派生密码数据。

---

## File Structure

- Modify: `cmd/start_server/main.go`
  - 改 S3 初始化为降级启动。
  - 改 owner bootstrap 日志。
  - worker 启动日志保留，但由 worker 自己决定 enabled/disabled。
- Create: `internal/handlers/storage_guard.go`
  - 提供 `abortStorageUnavailable(c *gin.Context)` 与 `requireS3(c *gin.Context, s3Client *s3.S3) bool`。
- Create: `internal/handlers/storage_guard_test.go`
  - 测统一 503 响应。
- Modify: `internal/handlers/blog_upload_handler.go`
- Modify: `internal/handlers/upload_handler.go`
- Modify: `internal/handlers/dm_handler.go`
- Modify: `internal/handlers/video_handler.go`
- Modify: `internal/handlers/podcast_handler.go`
- Modify: `internal/handlers/albums_handler.go`
- Modify: `internal/handlers/songs_handler.go`
- Modify: `internal/handlers/corrections_handler.go`
  - 所有非 local 的 S3 上传/删除路径在使用 `*s3.S3` 前调用 guard。
- Modify: `internal/service/rss_cron.go`
  - 增加 `RSS_CRON_ENABLED`、`RSS_CRON_STARTUP_DELAY`、`RSS_CRON_INTERVAL` 解析。
  - 每轮 RSS sync 输出 summary。
- Create: `internal/service/rss_cron_config_test.go`
  - 测 RSS 配置默认值、禁用、非法值 fallback。
- Modify: `internal/service/fulltext_worker.go`
  - 增加 `FULLTEXT_WORKER_ENABLED`、`FULLTEXT_WORKER_STARTUP_DELAY`、`FULLTEXT_WORKER_INTERVAL`、`FULLTEXT_WORKER_BATCH_SIZE` 解析。
- Create: `internal/service/fulltext_worker_config_test.go`
  - 测 fulltext 配置默认值、禁用、非法值 fallback。
- Create or Modify: `cmd/start_server/owner_bootstrap_test.go`
  - 若 `cmd/start_server` 当前已有测试文件则追加；否则新建。

---

### Task 1: Add Shared Storage Guard

**Files:**
- Create: `internal/handlers/storage_guard.go`
- Create: `internal/handlers/storage_guard_test.go`

**Interfaces:**
- Produces: `func abortStorageUnavailable(c *gin.Context)`
- Produces: `func requireS3(c *gin.Context, s3Client *s3.S3) bool`
- Consumers: all storage-backed handlers in later tasks.

- [ ] **Step 1: Write the failing test**

Create `internal/handlers/storage_guard_test.go`:

```go
package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireS3ReturnsStorageUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/upload", func(c *gin.Context) {
		if !requireS3(c, nil) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	want := `{"code":"storage.unavailable","error":"Storage service is unavailable"}`
	if w.Body.String() != want {
		t.Fatalf("body = %q, want %q", w.Body.String(), want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/handlers -run TestRequireS3ReturnsStorageUnavailable -count=1
```

Expected: FAIL with `undefined: requireS3`.

- [ ] **Step 3: Implement the guard**

Create `internal/handlers/storage_guard.go`:

```go
package handlers

import (
	"net/http"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
)

func abortStorageUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"code":  "storage.unavailable",
		"error": "Storage service is unavailable",
	})
}

func requireS3(c *gin.Context, s3Client *s3.S3) bool {
	if s3Client != nil {
		return true
	}
	abortStorageUnavailable(c)
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/handlers -run TestRequireS3ReturnsStorageUnavailable -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit if this directory is a git repository**

```bash
cd /home/fa/Atoman/Atoman-Backend
git status --short
git add internal/handlers/storage_guard.go internal/handlers/storage_guard_test.go
git commit -m "feat: add storage unavailable guard"
```

If `git status` says this is not a git repository, skip the commit and note it in the final report.

---

### Task 2: Degrade S3 Startup Instead of Failing API Startup

**Files:**
- Modify: `cmd/start_server/main.go:530-543`

**Interfaces:**
- Consumes: existing `storage.InitS3Client() (*s3.S3, error)` and `storage.ValidateS3Connection(*s3.S3) error`.
- Produces: startup behavior where `s3Client` remains nil on S3 init/validation errors and server startup continues.

- [ ] **Step 1: Write a focused helper in `main.go`**

Add this helper near `loadEnvironment()` in `cmd/start_server/main.go`:

```go
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
```

- [ ] **Step 2: Replace fatal startup block**

Replace `cmd/start_server/main.go:530-543` with:

```go
	s3Client := initializeStorageClient()
```

- [ ] **Step 3: Verify no S3 fatal remains**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
grep -n "Failed to .*S3\|ValidateS3Connection\|InitS3Client" cmd/start_server/main.go
```

Expected: no `fatalLogger.Fatal` lines around S3 init/validation; `ValidateS3Connection` only appears in `initializeStorageClient`.

- [ ] **Step 4: Build this package**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./cmd/start_server -run TestDoesNotExist -count=1
```

Expected: package compiles; output may be `testing: warning: no tests to run` and PASS.

- [ ] **Step 5: Commit if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git add cmd/start_server/main.go
git commit -m "fix: allow API startup when S3 is unavailable"
```

---

### Task 3: Apply Storage Guard to Upload and Media Handlers

**Files:**
- Modify: `internal/handlers/blog_upload_handler.go`
- Modify: `internal/handlers/upload_handler.go`
- Modify: `internal/handlers/dm_handler.go`
- Modify: `internal/handlers/video_handler.go`
- Modify: `internal/handlers/podcast_handler.go`
- Modify: `internal/handlers/albums_handler.go`
- Modify: `internal/handlers/songs_handler.go`
- Modify: `internal/handlers/corrections_handler.go`

**Interfaces:**
- Consumes: `requireS3(c, s3Client) bool` from Task 1.
- Produces: all S3-dependent code paths return `503 storage.unavailable` before dereferencing nil.

- [ ] **Step 1: Replace existing nil S3 responses**

Use this pattern in each handler before `PutObject`, `DeleteS3Object`, or other S3-only work:

```go
if !requireS3(c, s3Client) {
	return
}
```

For `dmHandler.uploadImage`, use:

```go
if !requireS3(c, h.s3) {
	return
}
```

- [ ] **Step 2: Preserve local storage behavior**

Make sure every handler with local mode keeps this order:

```go
if os.Getenv("STORAGE_TYPE") == "local" {
	// existing local file save behavior
	return
}
if !requireS3(c, s3Client) {
	return
}
// existing S3 behavior
```

Do not add the guard before local mode blocks.

- [ ] **Step 3: Fix corrections handler nil dereference**

In `internal/handlers/corrections_handler.go`, before the `s3Client.PutObject` block for `input.Cover != nil`, insert:

```go
if !requireS3(c, s3Client) {
	return
}
```

This is required because current code directly calls `s3Client.PutObject`.

- [ ] **Step 4: Fix album/song delete paths**

Before any call shaped like:

```go
storage.DeleteS3Object(s3Client, key)
```

ensure either local mode is not in use and S3 exists:

```go
if os.Getenv("STORAGE_TYPE") == "s3" && key != "" {
	if !requireS3(c, s3Client) {
		return
	}
	if err := storage.DeleteS3Object(s3Client, key); err != nil {
		// keep existing log/error behavior
	}
}
```

If the delete is best-effort cleanup after DB mutation, keep existing best-effort semantics but avoid nil by wrapping it as:

```go
if os.Getenv("STORAGE_TYPE") == "s3" && s3Client != nil && key != "" {
	_ = storage.DeleteS3Object(s3Client, key)
}
```

Use the first strict pattern only when the request's primary action requires storage; use the second best-effort pattern for cleanup of an old object after a successful DB update.

- [ ] **Step 5: Update swagger comments for upload endpoints**

For storage-backed upload endpoints that can now return 503, add:

```go
// @Failure 503 {object} ErrorResponse
```

At minimum update the upload endpoints in:

- `internal/handlers/blog_upload_handler.go`
- `internal/handlers/upload_handler.go`
- `internal/handlers/dm_handler.go`
- `internal/handlers/video_handler.go`
- `internal/handlers/podcast_handler.go`

- [ ] **Step 6: Run handler tests/build**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/handlers -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git add internal/handlers
git commit -m "fix: return 503 for unavailable storage endpoints"
```

---

### Task 4: Add RSS Worker Runtime Configuration and Summary Logs

**Files:**
- Modify: `internal/service/rss_cron.go`
- Create: `internal/service/rss_cron_config_test.go`

**Interfaces:**
- Produces: `type rssCronConfig struct { Enabled bool; StartupDelay time.Duration; Interval time.Duration }`
- Produces: `func loadRSSCronConfig() rssCronConfig`
- Produces: `func parseEnvBool(name string, fallback bool) bool`
- Produces: `func parseEnvDuration(name string, fallback time.Duration) time.Duration`

- [ ] **Step 1: Write config tests**

Create `internal/service/rss_cron_config_test.go`:

```go
package service

import (
	"testing"
	"time"
)

func TestLoadRSSCronConfigDefaults(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "")
	t.Setenv("RSS_CRON_INTERVAL", "")

	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.StartupDelay != 60*time.Second {
		t.Fatalf("StartupDelay = %v, want 60s", cfg.StartupDelay)
	}
	if cfg.Interval != 15*time.Minute {
		t.Fatalf("Interval = %v, want 15m", cfg.Interval)
	}
}

func TestLoadRSSCronConfigOverrides(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "false")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "5s")
	t.Setenv("RSS_CRON_INTERVAL", "30m")

	cfg := loadRSSCronConfig()
	if cfg.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if cfg.StartupDelay != 5*time.Second {
		t.Fatalf("StartupDelay = %v, want 5s", cfg.StartupDelay)
	}
	if cfg.Interval != 30*time.Minute {
		t.Fatalf("Interval = %v, want 30m", cfg.Interval)
	}
}

func TestLoadRSSCronConfigInvalidFallsBack(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "not-a-bool")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "bad")
	t.Setenv("RSS_CRON_INTERVAL", "0s")

	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want fallback true")
	}
	if cfg.StartupDelay != 60*time.Second {
		t.Fatalf("StartupDelay = %v, want fallback 60s", cfg.StartupDelay)
	}
	if cfg.Interval != 15*time.Minute {
		t.Fatalf("Interval = %v, want fallback 15m", cfg.Interval)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/service -run 'TestLoadRSSCronConfig' -count=1
```

Expected: FAIL with `undefined: loadRSSCronConfig`.

- [ ] **Step 3: Implement RSS config parsing**

In `internal/service/rss_cron.go`, add imports `os` and `strconv` if absent, then add near existing RSS worker functions:

```go
type rssCronConfig struct {
	Enabled      bool
	StartupDelay time.Duration
	Interval     time.Duration
}

func parseEnvBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("WARN: invalid %s=%q; using default %t", name, raw, fallback)
		return fallback
	}
	return value
}

func parseEnvDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		log.Printf("WARN: invalid %s=%q; using default %s", name, raw, fallback)
		return fallback
	}
	return value
}

func loadRSSCronConfig() rssCronConfig {
	return rssCronConfig{
		Enabled:      parseEnvBool("RSS_CRON_ENABLED", true),
		StartupDelay: parseEnvDuration("RSS_CRON_STARTUP_DELAY", 60*time.Second),
		Interval:     parseEnvDuration("RSS_CRON_INTERVAL", 15*time.Minute),
	}
}
```

- [ ] **Step 4: Update `StartRSSCron`**

Replace the hard-coded sleep/ticker with:

```go
func StartRSSCron(db *gorm.DB) {
	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		log.Println("RSS cron worker disabled by RSS_CRON_ENABLED=false")
		return
	}
	go func() {
		time.Sleep(cfg.StartupDelay)
		log.Println("Starting initial RSS sync...")
		syncAllRSSFeeds(db)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for range ticker.C {
			log.Println("Running scheduled RSS sync...")
			syncAllRSSFeeds(db)
		}
	}()
}
```

- [ ] **Step 5: Add RSS summary counters**

Inside `syncAllRSSFeeds`, introduce counters:

```go	total := 0
	success := 0
	failed := 0
	skipped := 0
	defer func() {
		log.Printf("RSS sync completed: total=%d success=%d failed=%d skipped=%d", total, success, failed, skipped)
	}()
```

Then increment:

- `total++` for every non-empty URL considered.
- `skipped++` when URL is empty or non-absolute.
- `failed++` when fetch/parse or DB work for a URL fails.
- `success++` once that URL finishes without error.

- [ ] **Step 6: Run tests**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/service -run 'TestLoadRSSCronConfig' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git add internal/service/rss_cron.go internal/service/rss_cron_config_test.go
git commit -m "feat: configure RSS cron worker startup"
```

---

### Task 5: Add Fulltext Worker Runtime Configuration

**Files:**
- Modify: `internal/service/fulltext_worker.go`
- Create: `internal/service/fulltext_worker_config_test.go`

**Interfaces:**
- Produces: `type fullTextWorkerConfig struct { Enabled bool; StartupDelay time.Duration; Interval time.Duration; BatchSize int }`
- Produces: `func loadFullTextWorkerConfig() fullTextWorkerConfig`
- Consumes: `parseEnvBool` and `parseEnvDuration` from Task 4.

- [ ] **Step 1: Write config tests**

Create `internal/service/fulltext_worker_config_test.go`:

```go
package service

import (
	"testing"
	"time"
)

func TestLoadFullTextWorkerConfigDefaults(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "")

	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.StartupDelay != 120*time.Second {
		t.Fatalf("StartupDelay = %v, want 120s", cfg.StartupDelay)
	}
	if cfg.Interval != 2*time.Minute {
		t.Fatalf("Interval = %v, want 2m", cfg.Interval)
	}
	if cfg.BatchSize != 4 {
		t.Fatalf("BatchSize = %d, want 4", cfg.BatchSize)
	}
}

func TestLoadFullTextWorkerConfigOverrides(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "false")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "10s")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "5m")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "2")

	cfg := loadFullTextWorkerConfig()
	if cfg.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if cfg.StartupDelay != 10*time.Second || cfg.Interval != 5*time.Minute || cfg.BatchSize != 2 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
}

func TestLoadFullTextWorkerConfigInvalidFallsBack(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "bad")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "bad")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "0s")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "0")

	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled || cfg.StartupDelay != 120*time.Second || cfg.Interval != 2*time.Minute || cfg.BatchSize != 4 {
		t.Fatalf("unexpected fallback cfg: %+v", cfg)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/service -run 'TestLoadFullTextWorkerConfig' -count=1
```

Expected: FAIL with `undefined: loadFullTextWorkerConfig`.

- [ ] **Step 3: Implement fulltext config parsing**

In `internal/service/fulltext_worker.go`, add import `os` and `strconv` if needed, then add:

```go
type fullTextWorkerConfig struct {
	Enabled      bool
	StartupDelay time.Duration
	Interval     time.Duration
	BatchSize    int
}

func parseEnvPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("WARN: invalid %s=%q; using default %d", name, raw, fallback)
		return fallback
	}
	return value
}

func loadFullTextWorkerConfig() fullTextWorkerConfig {
	return fullTextWorkerConfig{
		Enabled:      parseEnvBool("FULLTEXT_WORKER_ENABLED", FullTextWorkerEnabledDefault),
		StartupDelay: parseEnvDuration("FULLTEXT_WORKER_STARTUP_DELAY", 120*time.Second),
		Interval:     parseEnvDuration("FULLTEXT_WORKER_INTERVAL", fullTextWorkerInterval),
		BatchSize:    parseEnvPositiveInt("FULLTEXT_WORKER_BATCH_SIZE", fullTextWorkerBatchSize),
	}
}
```

- [ ] **Step 4: Update `StartFullTextWorker` and `runFullTextCycle`**

Replace `StartFullTextWorker` with:

```go
func StartFullTextWorker(db *gorm.DB) {
	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		log.Println("fulltext worker disabled by FULLTEXT_WORKER_ENABLED=false")
		return
	}
	go func() {
		time.Sleep(cfg.StartupDelay)
		runFullTextCycle(db, time.Now(), cfg.BatchSize)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for range ticker.C {
			runFullTextCycle(db, time.Now(), cfg.BatchSize)
		}
	}()
}
```

Change the signature and loop:

```go
func runFullTextCycle(db *gorm.DB, now time.Time, batchSize int) {
	if err := recoverStaleFullTextFetches(db, now); err != nil {
		log.Printf("fulltext worker recover stale fetches failed: %v", err)
	}

	for i := 0; i < batchSize; i++ {
		// existing body unchanged
	}
}
```

Update any tests or call sites that call `runFullTextCycle` to pass `fullTextWorkerBatchSize` or an explicit test batch size.

- [ ] **Step 5: Run tests**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./internal/service -run 'TestLoadFullTextWorkerConfig|TestLoadRSSCronConfig' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git add internal/service/fulltext_worker.go internal/service/fulltext_worker_config_test.go
git commit -m "feat: configure fulltext worker startup"
```

---

### Task 6: Improve Owner Bootstrap Diagnostics

**Files:**
- Modify: `cmd/start_server/main.go:221-252`
- Create or Modify: `cmd/start_server/owner_bootstrap_test.go`

**Interfaces:**
- Produces: `func missingOwnerEnvVars(username string, email string, password string) []string`
- Consumes: existing `bootstrapOwnerFromEnv(db *gorm.DB) error`.

- [ ] **Step 1: Add test for missing env var helper**

Create `cmd/start_server/owner_bootstrap_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestMissingOwnerEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		username string
		email    string
		password string
		want     []string
	}{
		{name: "none", want: []string{"OWNER_USERNAME", "OWNER_EMAIL", "OWNER_PASSWORD"}},
		{name: "password missing", username: "admin", email: "admin@example.com", want: []string{"OWNER_PASSWORD"}},
		{name: "username email missing", password: "secret", want: []string{"OWNER_USERNAME", "OWNER_EMAIL"}},
		{name: "complete", username: "admin", email: "admin@example.com", password: "secret", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingOwnerEnvVars(tt.username, tt.email, tt.password)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("missingOwnerEnvVars() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./cmd/start_server -run TestMissingOwnerEnvVars -count=1
```

Expected: FAIL with `undefined: missingOwnerEnvVars`.

- [ ] **Step 3: Implement helper and logs**

In `cmd/start_server/main.go`, add:

```go
func missingOwnerEnvVars(username string, email string, password string) []string {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(username) == "" {
		missing = append(missing, "OWNER_USERNAME")
	}
	if strings.TrimSpace(email) == "" {
		missing = append(missing, "OWNER_EMAIL")
	}
	if password == "" {
		missing = append(missing, "OWNER_PASSWORD")
	}
	return missing
}
```

Update the top of `bootstrapOwnerFromEnv` to:

```go
	username := strings.TrimSpace(os.Getenv("OWNER_USERNAME"))
	email := strings.TrimSpace(os.Getenv("OWNER_EMAIL"))
	password := os.Getenv("OWNER_PASSWORD")
	missing := missingOwnerEnvVars(username, email, password)
	if len(missing) == 3 {
		log.Println("Owner bootstrap disabled: OWNER_* variables are empty")
		return nil
	}
	if len(missing) > 0 {
		log.Printf("WARN: owner bootstrap partially configured; missing %s", strings.Join(missing, ", "))
		return nil
	}
```

Update success log after `created`:

```go
	if created {
		log.Printf("owner user %q bootstrapped successfully", user.Username)
	}
```

Keep existing existing-user log unchanged.

- [ ] **Step 4: Run test**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./cmd/start_server -run TestMissingOwnerEnvVars -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git add cmd/start_server/main.go cmd/start_server/owner_bootstrap_test.go
git commit -m "chore: clarify owner bootstrap startup logs"
```

---

### Task 7: Final Verification and Production Check Notes

**Files:**
- Modify only if tests reveal compile errors from earlier tasks.

**Interfaces:**
- Consumes all previous task outputs.
- Produces verified backend build.

- [ ] **Step 1: Run package tests touched by implementation**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go test ./cmd/start_server ./internal/handlers ./internal/service -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full backend build**

Run:

```bash
cd /home/fa/Atoman/Atoman-Backend
go build ./...
```

Expected: command exits 0 with no output.

- [ ] **Step 3: Optional local degraded-startup smoke test**

If a safe local env is available, run with invalid S3 credentials and a valid database connection:

```bash
cd /home/fa/Atoman/Atoman-Backend
STORAGE_TYPE=s3 AWS_ACCESS_KEY_ID=bad AWS_SECRET_ACCESS_KEY=bad go run ./cmd/start_server
```

Expected log contains:

```text
WARN: S3 storage unavailable; storage-backed endpoints will return 503:
Server starting on port 8080
```

Stop the process with Ctrl-C after confirming startup. Do not run this against production from the local terminal unless the operator explicitly asks.

- [ ] **Step 4: Production verification after deploy**

Ask the server operator to run:

```bash
sudo systemctl restart atoman-backend
sleep 10
curl -v http://127.0.0.1:8080/swagger/index.html
curl -v https://api.atoman.org/api/v1/auth/session
```

Expected:

- Local Swagger returns `HTTP/1.1 200 OK`.
- Public auth/session returns `204` or another non-502 API response.
- If S3/R2 is intentionally broken, non-storage API still responds and upload/storage endpoints return HTTP `503` with `storage.unavailable`.

- [ ] **Step 5: Commit final fixes if possible**

```bash
cd /home/fa/Atoman/Atoman-Backend
git status --short
git add .
git commit -m "test: verify backend startup resilience"
```

Only commit if there are verification-related changes that were not already committed by earlier tasks.

---

## Self-Review

- Spec coverage:
  - S3/R2 startup resilience is covered by Tasks 1-3.
  - Storage endpoints returning 503 are covered by Tasks 1 and 3.
  - RSS worker env configuration and summary logging are covered by Task 4.
  - Fulltext worker env configuration is covered by Task 5.
  - Owner bootstrap diagnostics are covered by Task 6.
  - Build verification is covered by Task 7.
- Placeholder scan: no `TBD`, `TODO`, `implement later`, or vague “add tests” steps remain; every code-changing step includes concrete code or an exact pattern.
- Type consistency:
  - `requireS3(c *gin.Context, s3Client *s3.S3) bool` is defined in Task 1 and consumed consistently in Task 3.
  - `parseEnvBool` and `parseEnvDuration` are defined in Task 4 and reused by Task 5.
  - `runFullTextCycle` signature changes to `runFullTextCycle(db *gorm.DB, now time.Time, batchSize int)` and Task 5 explicitly requires updating call sites.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-29-backend-startup-resilience.md`. Two execution options:

**1. Subagent-Driven (recommended)** - Dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
