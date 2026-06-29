# Backend Startup Resilience Design

Date: 2026-06-29

## Goal

Prevent optional infrastructure failures, especially R2/S3 storage failures, from taking down the whole Backend API. Improve startup observability and reduce production noise from background workers while keeping current user-facing behavior for healthy deployments.

This design covers three focused areas:

1. S3/R2 startup resilience and storage endpoint degradation.
2. RSS/fulltext worker startup pressure and log noise.
3. Owner bootstrap configuration diagnostics.

## Current Problems

### S3/R2 failure causes full API outage

`cmd/start_server/main.go` initializes S3 storage before creating the Gin router. If `storage.InitS3Client()` or `storage.ValidateS3Connection()` fails, startup calls `fatalLogger.Fatal(...)`. A transient R2 issue, expired key, permission error, or bucket mismatch therefore prevents port `8080` from listening. Nginx then returns upstream failures and the public API becomes Cloudflare `502`.

### Background workers start too aggressively

`StartRSSCron` runs its first sync after 5 seconds. `StartFullTextWorker` runs after 10 seconds. After restart, this can overlap with migrations, traffic recovery, RSS fetches, fulltext extraction, and GORM slow SQL logging. The service stays functional, but logs become noisy and the database receives extra startup pressure.

### Owner bootstrap warning is ambiguous

`bootstrapOwnerFromEnv` skips when `OWNER_USERNAME`, `OWNER_EMAIL`, and `OWNER_PASSWORD` are incomplete. The current warning is technically correct but does not list which variable is missing, making production diagnosis slower.

## Design

## 1. S3/R2 Startup Resilience

### Desired behavior

The Backend API should start even if S3/R2 is unavailable. Storage-backed endpoints should fail explicitly with HTTP `503 Service Unavailable` instead of causing a process exit or panic.

### Startup flow

Update the storage initialization block in `cmd/start_server/main.go`:

- If `STORAGE_TYPE=local`, keep existing behavior.
- If `storage.InitS3Client()` fails:
  - log a warning;
  - leave `s3Client == nil`;
  - continue server startup.
- If `storage.ValidateS3Connection()` fails:
  - log a warning;
  - leave `s3Client == nil`;
  - continue server startup.
- If validation succeeds:
  - keep `s3Client`;
  - log `S3 storage initialized`.

Recommended warning format:

```text
WARN: S3 storage unavailable; storage-backed endpoints will return 503: <error>
```

The log must be explicit enough to distinguish storage degradation from successful startup.

### Storage endpoint guard

Introduce a small shared guard/helper for handlers that require object storage. The helper should produce one consistent response:

```json
{
  "code": "storage.unavailable",
  "error": "Storage service is unavailable"
}
```

HTTP status: `503 Service Unavailable`.

The helper should be used before upload, delete, or object-storage-dependent operations use `s3Client`.

### Modules to inspect and guard

Any route setup that accepts `*s3.S3` must be checked:

- `handlers.SetupBlogUploadRoutes`
- `handlers.SetupUploadRoutes`
- `handlers.SetupSongRoutes`
- `handlers.SetupAlbumRoutes`
- `handlers.SetupCorrectionRoutes`
- `handlers.SetupVideoRoutes`
- `handlers.SetupPodcastRoutes`
- `handlers.SetupDMRoutes`

Some handlers may not use storage for every endpoint. Only storage-dependent code paths need to return `503`.

### Non-goals

- Do not silently disable routes.
- Do not claim storage is healthy when validation failed.
- Do not add complex lazy initialization in this iteration.

## 2. RSS/fulltext Worker Startup Pressure and Log Noise

### Desired behavior

API startup should take priority over background work. Background workers should remain enabled by default but configurable in production.

### Environment variables

Add optional environment variables with safe defaults:

```env
RSS_CRON_ENABLED=true
RSS_CRON_STARTUP_DELAY=60s
RSS_CRON_INTERVAL=15m

FULLTEXT_WORKER_ENABLED=true
FULLTEXT_WORKER_STARTUP_DELAY=120s
FULLTEXT_WORKER_INTERVAL=2m
FULLTEXT_WORKER_BATCH_SIZE=4
```

Defaults preserve the current feature behavior while reducing immediate post-restart pressure.

Invalid duration or integer values should fall back to defaults and log a warning. Disabled workers should log one line and not start their goroutine.

### RSS worker logging

Keep per-source errors, but add a per-cycle summary to make operational status easier to read:

```text
RSS sync completed: total=<n> success=<n> failed=<n> skipped=<n>
```

This can be implemented by counting outcomes inside `syncAllRSSFeeds`.

### Fulltext worker logging

Fulltext worker should continue logging claim/process errors. It should not add success logs for every item. The batch size and interval env settings give production a simple pressure control without code changes.

### Slow SQL boundary

Do not perform broad SQL optimization in this implementation. The current focus is resilience and controllability.

Follow-up optimization candidates, outside this design:

- Add or verify indexes for fulltext claim queries.
- Replace RSS item `COUNT`-then-`INSERT` with a uniqueness constraint and conflict-safe insert.
- Review unread count query indexes for DM and notifications.

## 3. Owner Bootstrap Diagnostics

### Desired behavior

Owner bootstrap should not block API startup, but logs should clearly explain whether it is disabled, partially configured, already satisfied, or successful.

### Logging states

Update `bootstrapOwnerFromEnv` logging:

- All owner variables empty:
  ```text
  Owner bootstrap disabled: OWNER_* variables are empty
  ```
- Some but not all variables set:
  ```text
  WARN: owner bootstrap partially configured; missing OWNER_PASSWORD
  ```
  If more than one variable is missing, list all missing variable names.
- Existing owner found:
  ```text
  owner user "<username>" already exists; startup bootstrap left it unchanged
  ```
- New owner created:
  ```text
  owner user "<username>" bootstrapped successfully
  ```

Never log `OWNER_PASSWORD` or derived password data.

### Behavior

Keep current non-fatal behavior:

- Empty config: skip.
- Partial config: warn and skip.
- Complete config: create owner if absent, leave existing user unchanged.

## Testing Strategy

### Unit tests

Add focused tests where practical:

1. Storage guard returns HTTP 503 and consistent JSON when `s3Client == nil`.
2. Storage-dependent handlers do not panic when `s3Client == nil`.
3. Worker config parsing uses defaults for empty values.
4. Worker config parsing falls back on invalid values and logs warning.
5. `RSS_CRON_ENABLED=false` and `FULLTEXT_WORKER_ENABLED=false` prevent worker startup.
6. Owner bootstrap logs correct states for empty, partial, complete, existing-user cases.

### Build verification

Run before completion:

```bash
go build ./...
```

### Manual production verification

After deployment:

```bash
sudo systemctl restart atoman-backend
sleep 10
curl -v http://127.0.0.1:8080/swagger/index.html
curl -v https://api.atoman.org/api/v1/auth/session
```

Expected:

- Swagger responds `200` locally.
- Public auth/session no longer returns `502`.
- If S3/R2 is intentionally broken, non-storage API still responds while upload/storage operations return `503`.

## Rollout Notes

- Existing healthy deployments should keep working without new required environment variables.
- Production can tune worker behavior by adding env vars to `.env.prod`.
- Storage credential errors remain visible in logs but no longer prevent the API from starting.

## Risks and Mitigations

### Risk: nil `s3Client` causes panic in a missed handler

Mitigation: inspect every route setup that receives `*s3.S3`; add tests for representative storage routes.

### Risk: degraded storage is overlooked because API stays up

Mitigation: clear warning log at startup and consistent 503 responses for storage endpoints.

### Risk: worker env parsing introduces inconsistent defaults

Mitigation: centralize parsing helpers and test default/invalid/disabled cases.
