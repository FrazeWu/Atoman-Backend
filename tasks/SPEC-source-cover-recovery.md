# Spec: Source Cover Recovery

## Objective

Increase RSS source cover availability without presenting unrelated article images as source identity. A source avatar uses an image explicitly declared by the feed, then a favicon derived from the source URL, and finally the source-title initial.

## Tech Stack

- Go RSS/Atom sync worker in `internal/service/rss_cron.go`
- PostgreSQL through GORM models in `internal/model/feed.go`
- Frontend source-avatar presentation in `Atoman-Frontend/src/utils/feedSourcePresentation.ts`
- Existing frontend source-avatar fallback in `Atoman-Frontend`

## Commands

- Build: `cd /root/Atoman/.pi/worktree/atoman-backend-fix-subscription-source-avatars-01a04222-3e15-719a-8a2f-91e96d59 && go build ./...`
- Frontend focused test: `cd /root/Atoman/.pi/worktree/atoman-frontend-fix-subscription-source-avatars-01a04222-3e15-719a-8a2f-91e96d59 && bun run test:unit -- tests/unit/components/feed/FeedSourceIdentityCard.spec.ts tests/unit/components/feed/FeedSourceArticlesSheet.spec.ts`
- Frontend type check: `cd /root/Atoman/.pi/worktree/atoman-frontend-fix-subscription-source-avatars-01a04222-3e15-719a-8a2f-91e96d59 && bun run type-check`

## Project Structure

- `internal/service/rss_cron.go`: feed parsing and successful-sync source updates
- `internal/service/rss_cron_test.go`: parser and sync persistence regression tests
- `Atoman-Frontend/src/utils/feedSourcePresentation.ts`: derive a direct favicon candidate from a valid source URL
- `Atoman-Frontend/src/components/feed/FeedSourceIdentityCard.vue`: render cover, favicon, then initial
- `Atoman-Frontend/src/components/feed/FeedSourceArticlesSheet.vue`: render cover, favicon, then initial

## Code Style

```ts
const candidates = [coverURL, faviconURL].filter(Boolean)
```

Preserve existing naming and failed-image behavior. Do not introduce dependencies, database schema changes, a third-party favicon service, or background network fetches.

## Testing Strategy

- Add failing frontend tests proving the source-avatar candidate order: explicit cover, derived favicon, then title initial.
- Verify a broken cover advances to favicon and a broken favicon advances to the title initial.
- Run focused frontend tests, type check, and production build.

## Boundaries

- Always: preserve the existing channel-cover priority, use only the source URL to derive a favicon, and retain the frontend initial fallback.
- Ask first: use a feed-item or article image as a source cover; backfill persisted production data; expand network access rules.
- Never: bypass SSRF protection, retry around 403 access controls, use generic article/emoji/advertising images as a source avatar, or add a third-party favicon service.

## Success Criteria

- A source avatar prefers a successfully loaded, feed-declared cover URL.
- Without a cover, a valid HTTP(S) RSS URL provides a favicon candidate at its origin's `/favicon.ico`.
- If both image candidates are unavailable or fail to load, the title initial renders.
- Failed, blocked, or inaccessible feeds are not treated as successful cover recovery.

## Data Findings

- Of 485 external sources without a cover, 382 have never synced successfully: 215 HTTP-status errors, 60 parse failures, 50 SSRF blocks, 29 HTTP 403s, and 28 request failures.
- The current database has no persisted `site_url` values for external sources, while all 953 sources have an RSS URL; favicon derivation therefore uses the RSS URL's origin.
