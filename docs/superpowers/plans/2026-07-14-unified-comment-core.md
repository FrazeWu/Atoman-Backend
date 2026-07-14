# Unified Comment Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Atoman Backend 与 Web 建立统一评论核心，并将博客、视频、播客、RSS、音乐、论坛、辩题和时间线 Wiki 修订全部接入。

**Architecture:** 后端新增 `internal/modules/comment`，以统一目标和两层楼中楼承载公共能力，通过目标解析器与强类型扩展接入业务模块。前端新增共享 API、组合函数和评论组件；普通模块直接复用，论坛、辩题和时间线保留业务展示。开发期间旧逻辑暂时共存，所有消费者切换后直接删除，不提供兼容转发。

**Tech Stack:** Go 1.23、Gin、GORM、PostgreSQL 16、Goldmark、S3/R2、Vue 3.5、TypeScript 5.9、Pinia、Marked、DOMPurify、Vitest、Playwright。

---

## File Structure

### Backend

- Create `Atoman-Backend/internal/model/comment.go`: 统一目标、评论和公共关系模型。
- Create `Atoman-Backend/internal/model/comment_extensions.go`: 辩题与时间线强类型扩展。
- Create `Atoman-Backend/internal/migrations/unified_comments.go`: 评论核心索引。
- Create `Atoman-Backend/internal/modules/comment/dto.go`, `markdown.go`, `mention.go`, `time_anchor.go`, `ranking.go`, `target.go`, `target_resolvers.go`, `repo.go`, `service.go`, `interaction.go`, `notification.go`, and `http.go`: 评论核心各单一职责单元。
- Modify `Atoman-Backend/internal/app/router.go`: 组合并挂载统一服务。
- Modify `Atoman-Backend/internal/handlers/user_handler.go`: 提及搜索覆盖全部活跃用户。
- Modify `Atoman-Backend/internal/model/music.go`: 增加歌曲时长。
- Modify the exact forum, debate, voting, and timeline files listed in Tasks 11-13: 接入强类型扩展。
- Remove the legacy files listed in Task 14, then regenerate Swagger.

### Frontend

- Create `Atoman-Frontend/src/api/comments.ts`: 统一 DTO 与请求客户端。
- Create `Atoman-Frontend/src/composables/useComments.ts`: 分页、回复和交互状态。
- Create `Atoman-Frontend/src/composables/useCommentMarkdown.ts`, `useCommentMentions.ts`, and `useMediaTimeAnchors.ts`: 内容辅助逻辑。
- Create `Atoman-Frontend/src/components/comment/CommentSection.vue`, `CommentThread.vue`, `CommentItem.vue`, `CommentComposer.vue`, and `CommentReportDialog.vue`: 共享评论表面。
- Modify the exact module views and tests listed in Tasks 10-13; finally delete the two legacy components listed in Task 14.

---

### Task 1: Core Schema And Indexes

**Files:**
- Create: `Atoman-Backend/internal/model/comment.go`
- Create: `Atoman-Backend/internal/model/comment_extensions.go`
- Create: `Atoman-Backend/internal/migrations/unified_comments.go`
- Create: `Atoman-Backend/internal/migrations/unified_comments_test.go`
- Modify: `Atoman-Backend/cmd/migrate/main.go`
- Modify: `Atoman-Backend/internal/model/music.go`

- [ ] **Step 1: Write the failing schema test**

```go
func TestUnifiedCommentSchemaCreatesRequiredIndexes(t *testing.T) {
    db := testdb.Open(t)
    require.NoError(t, db.AutoMigrate(
        &model.DiscussionTarget{}, &model.CommentEntry{},
        &model.CommentMention{}, &model.CommentAttachment{},
        &model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{},
    ))
    require.NoError(t, RunUnifiedCommentIndexes(db))
    require.True(t, db.Migrator().HasIndex(&model.DiscussionTarget{}, "uq_discussion_target_kind_key"))
    require.True(t, db.Migrator().HasIndex(&model.CommentLike{}, "uq_comment_like_user"))
    require.True(t, db.Migrator().HasIndex(&model.CommentReport{}, "uq_comment_report_user"))
}
```

- [ ] **Step 2: Run it and verify failure**

Run: `cd /root/Atoman/Atoman-Backend && go test ./internal/migrations -run TestUnifiedCommentSchema -count=1`

Expected: FAIL because the models and migration do not exist.

- [ ] **Step 3: Implement the core and extension models**

```go
type DiscussionTarget struct {
    Base
    Kind string `gorm:"not null;index"`
    ResourceKey string `gorm:"type:text;not null"`
    OwnerID *uuid.UUID `gorm:"type:uuid;index"`
    CommentCount int `gorm:"not null;default:0"`
    RootCount int `gorm:"not null;default:0"`
    NextFloor int `gorm:"not null;default:1"`
    PinnedCommentID *uuid.UUID `gorm:"type:uuid"`
}

type CommentEntry struct {
    Base
    TargetID uuid.UUID `gorm:"type:uuid;not null;index"`
    AuthorID uuid.UUID `gorm:"type:uuid;not null;index"`
    RootID *uuid.UUID `gorm:"type:uuid;index"`
    ReplyToID *uuid.UUID `gorm:"type:uuid;index"`
    FloorNumber *int
    Content string `gorm:"type:text;not null"`
    ContentHash string `gorm:"not null;index"`
    Status string `gorm:"not null;default:'active';index"`
    EditedAt *time.Time
    LikeCount, ReplyCount, ReportCount int
    HotScore float64 `gorm:"not null;default:0;index"`
}
```

Add relation models and `TimelineRevisionProposal`, `DebateArgumentDetail`, `DebateArgumentReference`, and `DebateArgumentDebateRef`. Add `DurationSec int` to `model.Song`. Use `CommentEntry` so staged work can compile beside legacy `Comment`.

```go
type TimelineRevisionProposal struct {
    Base
    CommentID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex"`
    TargetKind string `gorm:"not null"`
    TargetID uuid.UUID `gorm:"type:uuid;not null;index"`
    PatchJSON json.RawMessage `gorm:"type:jsonb;not null"`
    Evidence string `gorm:"type:text;not null"`
    Status string `gorm:"not null;default:'pending';index"`
    ReviewerID *uuid.UUID `gorm:"type:uuid"`
    AppliedRevisionID *uuid.UUID `gorm:"type:uuid"`
}

type DebateArgumentDetail struct {
    CommentID uuid.UUID `gorm:"type:uuid;primaryKey"`
    ArgumentType string `gorm:"not null"`
    SourceURL, SourceTitle, SourceExcerpt string
    Conclusion string `gorm:"type:text"`
}
```

- [ ] **Step 4: Add indexes and register migrations**

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key
ON discussion_targets (kind, resource_key);
CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_root_floor
ON comment_entries (target_id, floor_number)
WHERE floor_number IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_like_user
ON comment_likes (comment_id, user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_report_user
ON comment_reports (comment_id, reporter_id);
```

Register new models and `RunUnifiedCommentIndexes` in `cmd/migrate/main.go`; do not remove legacy models yet.

- [ ] **Step 5: Verify and commit**

Run: `cd /root/Atoman/Atoman-Backend && go test ./internal/migrations ./internal/model -count=1 && go build ./...`

Expected: PASS.

```bash
git add internal/model/comment.go internal/model/comment_extensions.go internal/model/music.go internal/migrations/unified_comments.go internal/migrations/unified_comments_test.go cmd/migrate/main.go
git commit -m "feat: add unified comment schema"
```

---

### Task 2: Restricted Markdown, Mentions, And Time Anchors

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/markdown.go`
- Create: `Atoman-Backend/internal/modules/comment/mention.go`
- Create: `Atoman-Backend/internal/modules/comment/time_anchor.go`
- Create: `Atoman-Backend/internal/modules/comment/markdown_test.go`
- Create: `Atoman-Backend/internal/modules/comment/mention_test.go`
- Create: `Atoman-Backend/internal/modules/comment/time_anchor_test.go`
- Modify: `Atoman-Backend/go.mod`
- Modify: `Atoman-Backend/go.sum`

- [ ] **Step 1: Write failing parser tests**

```go
func TestParseTimeAnchorsFindsEveryValidToken(t *testing.T) {
    got := ParseTimeAnchors("开头 1:24，中段 01:02:03，错误 2:99", 4000)
    require.Equal(t, []int{84, 3723}, anchorSeconds(got))
}

func TestValidateMentionsUsesCodePointOffsets(t *testing.T) {
    err := ValidateMentions("你好 @阿明", []MentionInput{{UserID: uuid.New(), Start: 3, End: 6}})
    require.NoError(t, err)
}

func TestRenderCommentMarkdownRejectsHTMLAndImages(t *testing.T) {
    _, err := RenderCommentMarkdown("<script>x</script> ![x](https://x.test/a.png)")
    require.Error(t, err)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment -run 'Test(Parse|Validate|Render)' -count=1`

Expected: FAIL because the package is incomplete.

- [ ] **Step 3: Add Goldmark and implement exact helpers**

Run: `go get github.com/yuin/goldmark@v1.7.8`

```go
func NormalizeContent(raw string) string
func RenderCommentMarkdown(raw string) (string, error)
func ValidateMentions(content string, inputs []MentionInput) error
func MentionRecipients(authorID uuid.UUID, replyAuthorID *uuid.UUID, inputs []MentionInput) []uuid.UUID
func ParseTimeAnchors(content string, durationSec int) []TimeAnchor
```

Allow only paragraph/line break, emphasis, strong, code span, HTTP(S) link, and blockquote. Reject image/raw HTML nodes. Use `[]rune` offsets. Detect every `M:SS` and `H:MM:SS`; omit anchors beyond known duration.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/modules/comment -run 'Test(Parse|Validate|Render|Mention)' -count=1`

Expected: PASS.

```bash
git add go.mod go.sum internal/modules/comment/markdown.go internal/modules/comment/markdown_test.go internal/modules/comment/mention.go internal/modules/comment/mention_test.go internal/modules/comment/time_anchor.go internal/modules/comment/time_anchor_test.go
git commit -m "feat: add comment content parsers"
```

---

### Task 3: Target Registry And Resolvers

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/target.go`
- Create: `Atoman-Backend/internal/modules/comment/target_resolvers.go`
- Create: `Atoman-Backend/internal/modules/comment/target_test.go`

- [ ] **Step 1: Write failing resolver tests**

```go
func TestFeedResolverSharesNormalizedOriginalURL(t *testing.T) {
    svc, db := newCommentTestService(t)
    a := seedFeedItem(t, db, "https://example.com/post/?utm_source=a")
    b := seedFeedItem(t, db, "https://EXAMPLE.com/post")
    first, err := svc.targets.Resolve(Viewer{}, TargetRef{Kind: TargetFeedArticle, ResourceID: a.ID})
    require.NoError(t, err)
    second, err := svc.targets.Resolve(Viewer{}, TargetRef{Kind: TargetFeedArticle, ResourceID: b.ID})
    require.NoError(t, err)
    require.Equal(t, first.ResourceKey, second.ResourceKey)
}
```

Also test private content, unknown kinds, video/podcast/song durations, and ownerless RSS/music targets.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment -run 'Test.*Resolver|TestTargetRegistry' -count=1`

Expected: FAIL with undefined registry types.

- [ ] **Step 3: Implement the registry**

```go
type TargetRef struct { Kind string; ResourceID uuid.UUID }
type ResolvedTarget struct {
    Kind, ResourceKey string
    OwnerID *uuid.UUID
    Visible bool
    DurationSec int
    MarkLabel string
}
type TargetResolver interface {
    Resolve(viewer Viewer, resourceID uuid.UUID) (ResolvedTarget, error)
}
```

Register only the eleven kinds from the spec. Query existing models and reuse their visibility rules. Normalize RSS scheme/host/path, remove fragment and tracking parameters, and return nil owner for external/community-owned targets.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/modules/comment -run 'Test.*Resolver|TestTargetRegistry' -count=1`

Expected: PASS.

```bash
git add internal/modules/comment/target.go internal/modules/comment/target_resolvers.go internal/modules/comment/target_test.go
git commit -m "feat: resolve comment targets across modules"
```

---

### Task 4: Thread Creation, Paging, And Ranking

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/dto.go`
- Create: `Atoman-Backend/internal/modules/comment/repo.go`
- Create: `Atoman-Backend/internal/modules/comment/service.go`
- Create: `Atoman-Backend/internal/modules/comment/service_test.go`
- Create: `Atoman-Backend/internal/modules/comment/test_helpers_test.go`
- Create: `Atoman-Backend/internal/modules/comment/ranking.go`
- Create: `Atoman-Backend/internal/modules/comment/ranking_test.go`

- [ ] **Step 1: Write failing service tests**

```go
func TestReplyToChildKeepsSingleVisualLevel(t *testing.T) {
    svc, user, target := seededCommentService(t)
    root := mustCreateComment(t, svc, user, target, CreateCommentInput{Content: "root"})
    child := mustCreateComment(t, svc, user, target, CreateCommentInput{Content: "child", ReplyToID: &root.ID})
    nested := mustCreateComment(t, svc, user, target, CreateCommentInput{Content: "nested", ReplyToID: &child.ID})
    require.Equal(t, root.ID, *nested.RootID)
    require.Equal(t, child.ID, *nested.ReplyToID)
    require.Nil(t, nested.FloorNumber)
}
```

Also test two simultaneous root creates receiving distinct floors, immutable floors, 20-root pages, three-child previews, public counts, marked-root-first ordering, and hot ordering.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment -run 'TestReply|TestCreate|TestList|TestHot' -count=1`

Expected: FAIL with missing service types.

- [ ] **Step 3: Define DTOs and transactional service**

```go
type CreateCommentInput struct {
    Content string `json:"content"`
    ReplyToID *uuid.UUID `json:"reply_to_id"`
    Mentions []MentionInput `json:"mentions"`
    AttachmentIDs []uuid.UUID `json:"attachment_ids"`
}
```

Repository methods accept `*gorm.DB`. Lock `discussion_targets` with `clause.Locking{Strength:"UPDATE"}` before allocating a root floor. Derive `RootID` server-side. Validate up to four author-owned completed image assets and insert mentions/anchors in the same transaction.

Expose the typed-extension transaction boundary now so later modules do not invent a second create path:

```go
type ExtensionWriter func(tx *gorm.DB, comment *model.CommentEntry) error

func (s *Service) CreateWithExtension(
    user authctx.CurrentUser,
    target TargetRef,
    input CreateCommentInput,
    write ExtensionWriter,
) (CommentDTO, error)
```

`Create` calls `CreateWithExtension` with a nil writer. Add shared test seed/load helpers to `test_helpers_test.go` for all later comment-package tests.

- [ ] **Step 4: Implement the fixed ranking formula**

```go
func HotScore(rootLikes, childLikes, childCount int, age time.Duration) float64 {
    numerator := float64(rootLikes*3 + childLikes + childCount*2)
    return numerator / math.Pow(math.Max(0, age.Hours())+2, 1.2)
}
```

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/modules/comment -run 'TestReply|TestCreate|TestList|TestHot' -count=1`

Expected: PASS.

```bash
git add internal/modules/comment/dto.go internal/modules/comment/repo.go internal/modules/comment/service.go internal/modules/comment/service_test.go internal/modules/comment/test_helpers_test.go internal/modules/comment/ranking.go internal/modules/comment/ranking_test.go
git commit -m "feat: implement comment threading core"
```

---

### Task 5: Edit, Delete, Likes, And Unique Marking

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/interaction.go`
- Create: `Atoman-Backend/internal/modules/comment/interaction_test.go`
- Modify: `Atoman-Backend/internal/modules/comment/repo.go`
- Modify: `Atoman-Backend/internal/modules/comment/service.go`

- [ ] **Step 1: Write failing interaction tests**

```go
func TestDeleteRootRemovesWholeFloor(t *testing.T) {
    svc, owner, target := seededCommentService(t)
    root := mustCreateComment(t, svc, owner, target, CreateCommentInput{Content: "root"})
    mustCreateComment(t, svc, owner, target, CreateCommentInput{Content: "child", ReplyToID: &root.ID})
    require.NoError(t, svc.Delete(owner, root.ID))
    require.Equal(t, int64(0), countComments(t, svc.db, target.ID))
}
```

Also test exact `edited_at`, child-only deletion, content-owner deletion, idempotent likes, root-score refresh, one marked root, and ownerless targets refusing marking.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment -run 'Test(Edit|Delete|Like|Pin)' -count=1`

Expected: FAIL with missing methods.

- [ ] **Step 3: Implement edit and delete transactions**

Revalidate text, mentions, attachments, and anchors on edit; set `edited_at=now`. Root delete selects the floor IDs, deletes dependent relations and comment notifications, clears `pinned_comment_id`, then fixes target counts. Child delete removes one row and fixes root score/counts.

- [ ] **Step 4: Implement likes and marking**

Use `FirstOrCreate` for like and conditional delete for unlike. Recompute the root score after either action. Marking verifies the resolved owner, an active root in the same target, and atomically replaces the single marked ID.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/modules/comment -run 'Test(Edit|Delete|Like|Pin)' -count=1`

Expected: PASS.

```bash
git add internal/modules/comment/interaction.go internal/modules/comment/interaction_test.go internal/modules/comment/repo.go internal/modules/comment/service.go
git commit -m "feat: add comment interactions"
```

---

### Task 6: Reports, Notifications, And Abuse Limits

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/notification.go`
- Create: `Atoman-Backend/internal/modules/comment/notification_test.go`
- Modify: `Atoman-Backend/internal/modules/comment/interaction.go`
- Modify: `Atoman-Backend/internal/modules/comment/interaction_test.go`
- Modify: `Atoman-Backend/internal/model/notification.go`
- Modify: `Atoman-Backend/internal/migrations/notification_dm_indexes.go`

- [ ] **Step 1: Write failing governance tests**

```go
func TestFourthDistinctReportAutoFolds(t *testing.T) {
    svc, comment := seededReportedComment(t)
    for i := 0; i < 4; i++ {
        reporter := seedUser(t, svc.db, fmt.Sprintf("reporter-%d", i))
        require.NoError(t, svc.Report(reporter, comment.ID, "spam"))
    }
    require.Equal(t, StatusAutoFolded, loadComment(t, svc.db, comment.ID).Status)
}
```

Also test duplicate reports, rejected-report restoration, moderator audit, reply/mention deduplication, self-filtering, like aggregation, sixth-comment rate failure, and five-minute duplicate rejection.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment -run 'Test(Fourth|Report|Notification|Rate|Duplicate)' -count=1`

Expected: FAIL.

- [ ] **Step 3: Implement reports and moderation**

Validate reasons against `spam`, `harassment`, `hate`, `sexual`, `violence`, `misinformation`, and `other`; require a note for `other`. Count only `pending` and `upheld`; fold on the fourth distinct reporter. Permit only `restore`, `hide`, `delete`, `uphold_report`, and `reject_report`, require moderator role, and call `audit.Record` for each action.

- [ ] **Step 4: Implement notifications and limits**

Add `AggregationKey` to notifications and a partial aggregation index. Create one recipient set from reply plus mentions; never notify the actor. Likes update one unread aggregate per recipient/comment. Store target kind/resource ID and comment/root ID in metadata. Before create, count the author's previous-minute rows and query the same target/author/content hash within five minutes.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/modules/comment ./internal/modules/notification ./internal/migrations -count=1`

Expected: PASS.

```bash
git add internal/modules/comment/notification.go internal/modules/comment/notification_test.go internal/modules/comment/interaction.go internal/modules/comment/interaction_test.go internal/model/notification.go internal/migrations/notification_dm_indexes.go
git commit -m "feat: add comment governance and notifications"
```

---

### Task 7: Unified HTTP And Swagger Contract

**Files:**
- Create: `Atoman-Backend/internal/modules/comment/http.go`
- Create: `Atoman-Backend/internal/modules/comment/http_test.go`
- Modify: `Atoman-Backend/internal/app/router.go`
- Modify: `Atoman-Backend/internal/app/router_test.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler_test.go`
- Modify: `Atoman-Backend/internal/handlers/upload_handler.go`
- Modify: `Atoman-Backend/internal/handlers/upload_handler_test.go`
- Modify: `Atoman-Backend/docs/docs.go`
- Modify: `Atoman-Backend/docs/swagger.json`
- Modify: `Atoman-Backend/docs/swagger.yaml`

- [ ] **Step 1: Write failing route tests**

```go
func TestRegisterV1RoutesMountsUnifiedComments(t *testing.T) {
    r, db := newRouterTestDB(t)
    RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
    req := httptest.NewRequest(http.MethodGet, "/api/v1/discussions/blog_post/not-a-uuid/comments", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    require.Equal(t, http.StatusBadRequest, w.Code)
}
```

Cover all routes, auth, stable error envelopes, root page cap, child paging, and mention search returning non-followed active users.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/comment ./internal/app ./internal/handlers -run 'Test.*UnifiedComment|TestSearchUsersMention' -count=1`

Expected: FAIL because routes are absent and mention search is follow-only.

- [ ] **Step 3: Implement and mount handlers**

Mount public reads with optional auth, mutations with auth, and moderation with role checks. Construct one service in `router.go`. Add list/create, child list, edit/delete, like, report, mark, report queue, and moderation actions exactly as specified.

Add `comment.image` to the existing upload-purpose allowlist. Reuse verified JPEG/PNG/GIF/WebP handling and the 10 MB asset limit; return a `MediaAsset` ID for `attachment_ids`.

- [ ] **Step 4: Update mention search and docs**

Remove the `follows` join for `scope=mention`, require authentication, keep `is_active=true`, cap 20, and rank username prefix first. Add Swagger annotations and regenerate:

Run: `go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/start_server/main.go`

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/modules/comment ./internal/app ./internal/handlers -count=1 && go build ./...`

Expected: PASS; generated docs contain the unified paths.

```bash
git add internal/modules/comment/http.go internal/modules/comment/http_test.go internal/app/router.go internal/app/router_test.go internal/handlers/user_handler.go internal/handlers/user_handler_test.go internal/handlers/upload_handler.go internal/handlers/upload_handler_test.go docs/docs.go docs/swagger.json docs/swagger.yaml
git commit -m "feat: expose unified comment API"
```

---

### Task 8: Frontend Comment API And State

**Files:**
- Create: `Atoman-Frontend/src/api/comments.ts`
- Create: `Atoman-Frontend/src/composables/useComments.ts`
- Create: `Atoman-Frontend/src/composables/useCommentMentions.ts`
- Create: `Atoman-Frontend/src/composables/useCommentMarkdown.ts`
- Create: `Atoman-Frontend/src/composables/useMediaTimeAnchors.ts`
- Create: `Atoman-Frontend/tests/unit/api/comments.spec.ts`
- Create: `Atoman-Frontend/tests/unit/composables/useComments.spec.ts`
- Create: `Atoman-Frontend/tests/unit/composables/useCommentMentions.spec.ts`
- Create: `Atoman-Frontend/tests/unit/composables/useCommentMarkdown.spec.ts`
- Create: `Atoman-Frontend/tests/unit/composables/useMediaTimeAnchors.spec.ts`
- Modify: `Atoman-Frontend/src/composables/useApi.ts`
- Modify: `Atoman-Frontend/src/types.ts`
- Modify: `Atoman-Frontend/src/stores/notification.ts`
- Modify: `Atoman-Frontend/src/views/feed/InboxPage.vue`

- [ ] **Step 1: Write failing client tests**

```ts
it('converts JS indexes to Unicode code-point mention offsets', () => {
  expect(toMentionRange('😀你好 @阿明', 5, 8)).toEqual({ start: 4, end: 7 })
})
```

Also test target paths, auth headers, paging, child expansion, optimistic-like rollback, restricted Markdown, and multiple media anchors.

- [ ] **Step 2: Verify failure**

Run: `bun run test:unit -- tests/unit/api/comments.spec.ts tests/unit/composables/useComments.spec.ts`

Expected: FAIL because modules do not exist.

- [ ] **Step 3: Define types and client**

```ts
export type CommentTargetKind = 'blog_post' | 'video' | 'podcast_episode' | 'feed_article' |
  'music_artist' | 'music_album' | 'music_song' | 'forum_topic' | 'debate' |
  'timeline_event' | 'timeline_person'
export interface CommentTargetRef { kind: CommentTargetKind; resourceId: string }
export interface CreateCommentInput {
  content: string
  reply_to_id?: string
  mentions: { user_id: string; start: number; end: number }[]
  attachment_ids: string[]
}
```

Implement list, replies, create, update, delete, image upload, like, report, marking, moderator report-list, and moderation-action calls in `comments.ts`.

- [ ] **Step 4: Implement composables**

`useComments` owns one normalized root list. `useCommentMarkdown` uses a separate restricted Marked instance plus DOMPurify. `useCommentMentions` searches `scope=mention` and converts positions with `Array.from`. `useMediaTimeAnchors` renders and seeks server-provided anchors. Extend the notification store and inbox routing to open the module target and scroll to the root/comment IDs from metadata.

- [ ] **Step 5: Verify and commit**

Run: `bun run test:unit -- tests/unit/api/comments.spec.ts tests/unit/composables/useComments.spec.ts tests/unit/composables/useCommentMentions.spec.ts tests/unit/composables/useCommentMarkdown.spec.ts tests/unit/composables/useMediaTimeAnchors.spec.ts tests/unit/stores/notification.spec.ts tests/unit/views/feed/InboxPage.forum-routing.spec.ts && bun run type-check`

Expected: PASS.

```bash
git add src/api/comments.ts src/composables/useComments.ts src/composables/useCommentMentions.ts src/composables/useCommentMarkdown.ts src/composables/useMediaTimeAnchors.ts src/composables/useApi.ts src/types.ts src/stores/notification.ts src/views/feed/InboxPage.vue tests/unit/api/comments.spec.ts tests/unit/composables/useComments.spec.ts tests/unit/composables/useCommentMentions.spec.ts tests/unit/composables/useCommentMarkdown.spec.ts tests/unit/composables/useMediaTimeAnchors.spec.ts tests/unit/stores/notification.spec.ts tests/unit/views/feed/InboxPage.forum-routing.spec.ts
git commit -m "feat: add frontend comment client"
```

---

### Task 9: Shared Comment Components

**Files:**
- Create: `Atoman-Frontend/src/components/comment/CommentSection.vue`
- Create: `Atoman-Frontend/src/components/comment/CommentThread.vue`
- Create: `Atoman-Frontend/src/components/comment/CommentItem.vue`
- Create: `Atoman-Frontend/src/components/comment/CommentComposer.vue`
- Create: `Atoman-Frontend/src/components/comment/CommentReportDialog.vue`
- Create: `Atoman-Frontend/src/views/setting/SettingCommentModeration.vue`
- Modify: `Atoman-Frontend/src/views/setting/SettingLayout.vue`
- Modify: `Atoman-Frontend/src/router.ts`
- Create: `Atoman-Frontend/tests/unit/components/comment/CommentSection.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/comment/CommentThread.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/comment/CommentItem.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/comment/CommentComposer.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/comment/CommentReportDialog.spec.ts`
- Create: `Atoman-Frontend/tests/unit/views/setting/SettingCommentModeration.spec.ts`

- [ ] **Step 1: Write failing component tests**

```ts
it('renders every reply at one child depth', () => {
  const wrapper = mount(CommentThread, { props: { root, replies: [child, nestedReply] } })
  expect(wrapper.findAll('[data-comment-depth="1"]')).toHaveLength(2)
  expect(wrapper.find('[data-comment-depth="2"]').exists()).toBe(false)
})
```

Cover login state, 2,000-code-point cap, image-only submit, four-image cap, mentions, three-child preview, folding, exact edit time, one marked root, and seek emit.

- [ ] **Step 2: Verify failure**

Run: `bun run test:unit -- tests/unit/components/comment`

Expected: FAIL because components do not exist.

- [ ] **Step 3: Implement components**

The composer emits only `CreateCommentInput`; it never calculates floor/root/time payloads. Use existing UI primitives and Lucide icons. `CommentSection` accepts:

```ts
type Props = {
  target: CommentTargetRef
  noun?: '评论' | '讨论' | '回复' | '修订提案'
  markLabel?: '置顶' | '最佳回答'
  currentTime?: () => number | null
}
```

Use a segmented sort control, stable thread dimensions, and in-place child expansion.

Add a moderator-only settings route that lists pending reports and invokes restore/hide/delete/uphold/reject actions. Keep it as a dense work queue rather than duplicating the public comment surface.

- [ ] **Step 4: Verify and commit**

Run: `bun run test:unit -- tests/unit/components/comment tests/unit/views/setting/SettingCommentModeration.spec.ts && bun run type-check`

Expected: PASS.

```bash
git add src/components/comment src/views/setting/SettingCommentModeration.vue src/views/setting/SettingLayout.vue src/router.ts tests/unit/components/comment tests/unit/views/setting/SettingCommentModeration.spec.ts
git commit -m "feat: build shared comment surface"
```

---

### Task 10: Standard Content Module Integration

**Files:**
- Modify: `Atoman-Frontend/src/views/blog/PostDetailView.vue`
- Modify: `Atoman-Frontend/src/views/blog/PostEditorView.vue`
- Modify: `Atoman-Frontend/src/views/blog/BlogSettingsView.vue`
- Modify: `Atoman-Frontend/src/views/video/VideoDetailView.vue`
- Modify: `Atoman-Frontend/src/views/podcast/PodcastEpisodeView.vue`
- Modify: `Atoman-Frontend/src/views/feed/FeedItemDetailView.vue`
- Modify: `Atoman-Frontend/src/components/feed/FeedArticleSheet.vue`
- Modify: `Atoman-Frontend/src/components/music/AlbumDrawer.vue`
- Modify: `Atoman-Frontend/src/components/music/ArtistDrawer.vue`
- Modify: `Atoman-Frontend/src/components/music/NestedActionDrawer.vue`
- Modify: `Atoman-Frontend/tests/unit/components/CommentSection.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/components/VideoCommentSection.spec.ts`
- Create: `Atoman-Frontend/tests/unit/views/podcast/PodcastEpisodeView.spec.ts`
- Create: `Atoman-Frontend/tests/unit/views/feed/FeedItemDetailView.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/components/feed/FeedArticleSheet.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/components/music/AlbumDrawer.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/components/music/ArtistDrawer.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/components/music/NestedActionDrawer.spec.ts`

- [ ] **Step 1: Write failing integration tests**

```ts
expect(wrapper.findComponent(CommentSection).props('target')).toEqual({
  kind: 'podcast_episode', resourceId: episode.id,
})
```

Assert correct target kinds, media current-time callbacks, identical RSS resolution from detail/sheet, discussion wording for music, and removal of Web close/anonymous settings.

- [ ] **Step 2: Verify failure**

Run: `bun run test:unit -- tests/unit/components/CommentSection.spec.ts tests/unit/components/VideoCommentSection.spec.ts tests/unit/components/feed/FeedArticleSheet.spec.ts tests/unit/components/music/AlbumDrawer.spec.ts tests/unit/components/music/ArtistDrawer.spec.ts`

Expected: FAIL against legacy components.

- [ ] **Step 3: Mount the shared surface**

Use `blog_post`, `video`, `podcast_episode`, and `feed_article` target refs in their detail surfaces. Connect player `seek(seconds)` callbacks for video and podcast. Remove Web `allow_comments` and `comment_mode` controls and payload fields.

- [ ] **Step 4: Replace music discussion UI**

Replace album custom discussion logic, add artist discussions, and expose the active song discussion in the nested drawer. Use noun `讨论` and pass song current time.

- [ ] **Step 5: Verify and commit**

Run: `bun run test:unit -- tests/unit/views/blog tests/unit/views/video tests/unit/views/podcast tests/unit/components/feed tests/unit/components/music && bun run type-check`

Expected: PASS.

```bash
git add src/views/blog/PostDetailView.vue src/views/blog/PostEditorView.vue src/views/blog/BlogSettingsView.vue src/views/video/VideoDetailView.vue src/views/podcast/PodcastEpisodeView.vue src/views/feed/FeedItemDetailView.vue src/components/feed/FeedArticleSheet.vue src/components/music/AlbumDrawer.vue src/components/music/ArtistDrawer.vue src/components/music/NestedActionDrawer.vue tests/unit/components/CommentSection.spec.ts tests/unit/components/VideoCommentSection.spec.ts tests/unit/views/podcast/PodcastEpisodeView.spec.ts tests/unit/views/feed/FeedItemDetailView.spec.ts tests/unit/components/feed/FeedArticleSheet.spec.ts tests/unit/components/music/AlbumDrawer.spec.ts tests/unit/components/music/ArtistDrawer.spec.ts tests/unit/components/music/NestedActionDrawer.spec.ts
git commit -m "feat: connect comments to content modules"
```

---

### Task 11: Forum Replies On The Core

**Files:**
- Modify: `Atoman-Backend/internal/modules/forum/http.go`
- Modify: `Atoman-Backend/internal/modules/forum/service.go`
- Modify: `Atoman-Backend/internal/modules/forum/repo.go`
- Modify: `Atoman-Backend/internal/modules/forum_engagement/service.go`
- Modify: `Atoman-Backend/internal/modules/forum_moderation/service.go`
- Create: `Atoman-Backend/internal/modules/forum/http_test.go`
- Modify: `Atoman-Backend/internal/modules/forum/service_test.go`
- Modify: `Atoman-Backend/internal/modules/forum/repo_test.go`
- Create: `Atoman-Backend/internal/modules/forum_engagement/service_test.go`
- Modify: `Atoman-Backend/internal/modules/forum_moderation/service_test.go`
- Modify: `Atoman-Frontend/src/stores/forum.ts`
- Modify: `Atoman-Frontend/src/views/forum/ForumTopicView.vue`
- Modify: `Atoman-Frontend/src/components/forum/ForumReplyNode.vue`
- Modify: `Atoman-Frontend/tests/e2e/specs/forum.spec.ts`

- [ ] **Step 1: Write failing forum tests**

```go
func TestForumReplyUsesCommentFloor(t *testing.T) {
    service, topic, user := seededForumServiceWithComments(t)
    reply, err := service.CreateReply(user, CreateReplyRequest{TopicID: topic.ID, Content: "answer"})
    require.NoError(t, err)
    require.NotNil(t, reply.FloorNumber)
    require.Equal(t, 1, *reply.FloorNumber)
}
```

Also test quote-to-`reply_to_id`, topic reply totals, topic-owner-only best answer, drafts unchanged, and existing forum layout.

- [ ] **Step 2: Verify failure**

Run Backend: `go test ./internal/modules/forum ./internal/modules/forum_engagement ./internal/modules/forum_moderation -count=1`

Run Frontend: `bun run test:unit -- tests/unit/views/forum`

Expected: FAIL until forum uses comment service.

- [ ] **Step 3: Replace persistence and UI**

Inject `*comment.Service` into forum services. During staged compile, map old request fields to core inputs; remove the mapping with old routes in Task 14. Keep topic likes/bookmarks and drafts. Frontend uses `useComments({kind:'forum_topic', resourceId:topic.id})`, preserves forum composition, and labels the target mark “最佳回答”.

- [ ] **Step 4: Verify and commit**

Run Backend: `go test ./internal/modules/forum ./internal/modules/forum_engagement ./internal/modules/forum_moderation -count=1`

Run Frontend: `bun run test:unit -- tests/unit/views/forum && bun run type-check`

Expected: PASS.

```bash
git -C /root/Atoman/Atoman-Backend add internal/modules/forum/http.go internal/modules/forum/service.go internal/modules/forum/repo.go internal/modules/forum/http_test.go internal/modules/forum/service_test.go internal/modules/forum/repo_test.go internal/modules/forum_engagement/service.go internal/modules/forum_engagement/service_test.go internal/modules/forum_moderation/service.go internal/modules/forum_moderation/service_test.go
git -C /root/Atoman/Atoman-Backend commit -m "feat: move forum replies to comment core"
git -C /root/Atoman/Atoman-Frontend add src/stores/forum.ts src/views/forum/ForumTopicView.vue src/components/forum/ForumReplyNode.vue tests/e2e/specs/forum.spec.ts tests/unit/views/forum/forum-routing-prefix.spec.ts
git -C /root/Atoman/Atoman-Frontend commit -m "feat: render forum replies from comment core"
```

---

### Task 12: Debate Arguments As Typed Extensions

**Files:**
- Modify: `Atoman-Backend/internal/modules/debate/dto.go`
- Modify: `Atoman-Backend/internal/modules/debate/service.go`
- Modify: `Atoman-Backend/internal/modules/debate/repo.go`
- Modify: `Atoman-Backend/internal/modules/debate/http.go`
- Modify: `Atoman-Backend/internal/modules/debate_voting/service.go`
- Modify: `Atoman-Backend/internal/modules/debate/http_test.go`
- Modify: `Atoman-Backend/internal/modules/debate/service_test.go`
- Create: `Atoman-Backend/internal/modules/debate_voting/service_test.go`
- Modify: `Atoman-Frontend/src/views/debate/DebateTopicView.vue`
- Modify: `Atoman-Frontend/src/components/debate/ArgumentNode.vue`
- Modify: `Atoman-Frontend/tests/e2e/specs/debate.spec.ts`

- [ ] **Step 1: Write failing extension tests**

```go
func TestCreateArgumentWritesCommentAndTypedDetail(t *testing.T) {
    svc, db, user, debate := seededDebateWithCommentService(t)
    got, err := svc.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "evidence", ArgumentType: "evidence"})
    require.NoError(t, err)
    var comments, details int64
    require.NoError(t, db.Model(&model.CommentEntry{}).Count(&comments).Error)
    require.NoError(t, db.Model(&model.DebateArgumentDetail{}).Count(&details).Error)
    require.Equal(t, int64(1), comments)
    require.Equal(t, int64(1), details)
    require.Equal(t, got.ID, loadArgumentDetail(t, db).CommentID)
}
```

Also test one-floor replies, reference joins, votes using comment IDs, concluded debate rejection, folding, and conclusions.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/modules/debate ./internal/modules/debate_voting -count=1`

Expected: FAIL against legacy `arguments`.

- [ ] **Step 3: Implement typed transactions and UI mapping**

Add the new typed `POST /api/v1/debates/{id}/arguments` and `PATCH /api/v1/debate-arguments/{comment_id}` routes from the spec; do not retain `/api/v1/debate/topics/{id}/arguments` as a compatibility alias. Call `comment.Service.CreateWithExtension` so comment and detail commit together. Use comment UUIDs for argument references and votes. Frontend keeps argument type/evidence/reference/conclusion visuals while reply, mention, edit, delete, and fold use the core.

- [ ] **Step 4: Verify and commit**

Run Backend: `go test ./internal/modules/debate ./internal/modules/debate_voting -count=1`

Run Frontend: `bun run test:unit -- tests/unit/views/debate && bun run type-check`

Expected: PASS.

```bash
git -C /root/Atoman/Atoman-Backend add internal/modules/debate/dto.go internal/modules/debate/service.go internal/modules/debate/repo.go internal/modules/debate/http.go internal/modules/debate/http_test.go internal/modules/debate/service_test.go internal/modules/debate_voting/service.go internal/modules/debate_voting/service_test.go
git -C /root/Atoman/Atoman-Backend commit -m "feat: model debate arguments as comments"
git -C /root/Atoman/Atoman-Frontend add src/views/debate/DebateTopicView.vue src/components/debate/ArgumentNode.vue tests/e2e/specs/debate.spec.ts tests/unit/views/debate/DebateLayout.spec.ts
git -C /root/Atoman/Atoman-Frontend commit -m "feat: connect debate arguments to comment core"
```

---

### Task 13: Timeline Wiki Revision Proposals

**Files:**
- Create: `Atoman-Backend/internal/handlers/timeline_revision_proposal.go`
- Create: `Atoman-Backend/internal/handlers/timeline_revision_proposal_test.go`
- Create: `Atoman-Backend/internal/service/timeline_revision_proposal_service.go`
- Create: `Atoman-Backend/internal/service/timeline_revision_proposal_service_test.go`
- Modify: `Atoman-Backend/internal/handlers/timeline_handler.go`
- Create: `Atoman-Frontend/src/api/timelineRevisionProposals.ts`
- Create: `Atoman-Frontend/src/components/timeline/TimelineRevisionProposal.vue`
- Create: `Atoman-Frontend/tests/unit/components/timeline/TimelineRevisionProposal.spec.ts`
- Modify: `Atoman-Frontend/src/views/timeline/TimelineHomeView.vue`
- Modify: `Atoman-Frontend/src/views/timeline/PersonMapView.vue`
- Create: `Atoman-Frontend/tests/e2e/specs/timeline.spec.ts`

- [ ] **Step 1: Write failing proposal tests**

```go
func TestAcceptTimelineProposalAppliesPatchAndRecordsRevision(t *testing.T) {
    svc, db, maintainer, event := seededTimelineProposalService(t)
    proposal := mustCreateEventProposal(t, svc, maintainer, event.ID, map[string]any{"location": "Berlin"})
    require.NoError(t, svc.Decide(maintainer, proposal.CommentID, "accept"))
    require.Equal(t, "Berlin", loadEvent(t, db, event.ID).Location)
    require.Equal(t, int64(1), countTimelineRevisions(t, db, event.ID))
}
```

Also test event/person field allowlists, evidence requirement, child discussion, creator/moderator permissions, rejection, and audit.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/handlers ./internal/service -run Test.*TimelineProposal -count=1`

Expected: FAIL because proposal handlers do not exist.

- [ ] **Step 3: Implement typed proposal transactions**

Put field validation and the accept/reject transaction in `timeline_revision_proposal_service.go`; keep handlers limited to auth, decoding, and response mapping. Add event/person create routes and one decision route. Require at least one changed whitelisted field plus source evidence. On accept, lock target and proposal, apply patch, write the existing revision snapshot and audit, and mark the proposal accepted in one transaction.

- [ ] **Step 4: Implement timeline UI**

`timelineRevisionProposals.ts` owns typed create/decision calls. Event detail modal and person side panel use noun `修订提案`. The root composer captures changed fields/evidence; children reuse `CommentThread`. Show accept/reject to target creator or moderator-or-higher only. Add a Playwright acceptance flow to `timeline.spec.ts`.

- [ ] **Step 5: Verify and commit**

Run Backend: `go test ./internal/handlers ./internal/service -run 'Test.*Timeline' -count=1`

Run Frontend: `bun run test:unit -- tests/unit/components/timeline tests/unit/views/timeline && bun run type-check`

Expected: PASS.

```bash
git -C /root/Atoman/Atoman-Backend add internal/handlers/timeline_handler.go internal/handlers/timeline_revision_proposal.go internal/handlers/timeline_revision_proposal_test.go internal/service/timeline_revision_proposal_service.go internal/service/timeline_revision_proposal_service_test.go
git -C /root/Atoman/Atoman-Backend commit -m "feat: add timeline wiki revision discussions"
git -C /root/Atoman/Atoman-Frontend add src/api/timelineRevisionProposals.ts src/views/timeline/TimelineHomeView.vue src/views/timeline/PersonMapView.vue src/components/timeline/TimelineRevisionProposal.vue tests/unit/components/timeline/TimelineRevisionProposal.spec.ts tests/unit/views/timeline/TimelineHomeView.map-loading.spec.ts tests/unit/views/timeline/PersonListView.reactivity.spec.ts tests/e2e/specs/timeline.spec.ts
git -C /root/Atoman/Atoman-Frontend commit -m "feat: add timeline revision proposal UI"
```

---

### Task 14: Remove Legacy Logic And Verify

**Files:**
- Remove: `Atoman-Frontend/src/components/blog/CommentSection.vue`
- Remove: `Atoman-Frontend/src/components/video/VideoCommentSection.vue`
- Remove: `Atoman-Frontend/src/composables/useVideoTimestamp.ts`
- Remove: `Atoman-Backend/internal/handlers/discussion_handler.go`
- Remove: `Atoman-Backend/internal/handlers/discussion_handler_test.go`
- Modify: `Atoman-Backend/internal/model/feed.go`
- Modify: `Atoman-Backend/internal/model/revision.go`
- Modify: `Atoman-Backend/internal/model/forum.go`
- Modify: `Atoman-Backend/internal/model/debate.go`
- Modify: `Atoman-Backend/internal/handlers/video_handler.go`
- Modify: `Atoman-Backend/internal/handlers/video_handler_test.go`
- Modify: `Atoman-Backend/internal/modules/blog/http.go`
- Modify: `Atoman-Backend/internal/modules/blog/service.go`
- Modify: `Atoman-Backend/internal/modules/blog/repo.go`
- Modify: `Atoman-Backend/internal/modules/blog/http_test.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler_test.go`
- Modify: `Atoman-Backend/internal/modules/portal/service.go`
- Modify: `Atoman-Backend/internal/modules/portal/service_test.go`
- Modify: `Atoman-Backend/internal/handlers/entry_status_handler.go`
- Modify: `Atoman-Backend/internal/handlers/protection_handler.go`
- Modify: `Atoman-Backend/cmd/migrate/main.go`
- Modify: `Atoman-Backend/docs/docs.go`
- Modify: `Atoman-Backend/docs/swagger.json`
- Modify: `Atoman-Backend/docs/swagger.yaml`
- Modify: `Atoman-Frontend/src/composables/useApi.ts`
- Modify: `Atoman-Frontend/src/types.ts`
- Remove: `Atoman-Frontend/tests/unit/components/CommentSection.spec.ts`
- Remove: `Atoman-Frontend/tests/unit/components/VideoCommentSection.spec.ts`
- Remove: `Atoman-Frontend/tests/unit/composables/useVideoTimestamp.spec.ts`
- Create: `Atoman-Frontend/tests/e2e/specs/comments.spec.ts`

- [ ] **Step 1: Add failing removal and E2E assertions**

Backend route tests assert old blog/video comment, music discussion, forum reply, and legacy argument mutations return 404. Add:

```ts
test('ordinary and media comments use the shared core', async ({ page }) => {
  await publishComment(page, { target: 'blog', text: '@测试者 你好' })
  await expect(page.getByText('你好')).toBeVisible()
  await publishComment(page, { target: 'video', text: '1:24 这里开始' })
  await page.getByRole('button', { name: '1:24' }).click()
  await expectVideoTime(page, 84)
})
```

- [ ] **Step 2: Remove legacy code**

Delete old components/API paths. Remove `model.Comment`, `Discussion`, `DiscussionReadState`, `ForumReply`, and `Argument` after all consumers compile on core models. Replace blog profile/portal comment counts and music entry discussion counts with `discussion_targets` aggregates. Remove Backend behavior for `allow_comments` and `comment_mode`; accept the deprecated iOS request field but ignore it.

- [ ] **Step 3: Regenerate docs and run Backend verification**

```bash
cd /root/Atoman/Atoman-Backend
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/start_server/main.go
go test ./...
go build ./...
```

Expected: Swagger contains only unified comment and typed extension routes; tests/build pass.

- [ ] **Step 4: Run Frontend verification**

```bash
cd /root/Atoman/Atoman-Frontend
bun run type-check
bun run test:unit
bun run build
```

Expected: PASS.

- [ ] **Step 5: Run focused Playwright**

After starting repository services, run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:e2e -- tests/e2e/specs/comments.spec.ts tests/e2e/specs/forum.spec.ts tests/e2e/specs/debate.spec.ts tests/e2e/specs/timeline.spec.ts
```

Expected: ordinary, media, forum best-answer, debate, and timeline proposal flows pass.

- [ ] **Step 6: Commit final cleanup**

```bash
git -C /root/Atoman/Atoman-Backend add internal/handlers/discussion_handler.go internal/handlers/discussion_handler_test.go internal/model/feed.go internal/model/revision.go internal/model/forum.go internal/model/debate.go internal/handlers/video_handler.go internal/handlers/video_handler_test.go internal/modules/blog/http.go internal/modules/blog/service.go internal/modules/blog/repo.go internal/modules/blog/http_test.go internal/handlers/user_handler.go internal/handlers/user_handler_test.go internal/modules/portal/service.go internal/modules/portal/service_test.go internal/handlers/entry_status_handler.go internal/handlers/protection_handler.go cmd/migrate/main.go docs/docs.go docs/swagger.json docs/swagger.yaml
git -C /root/Atoman/Atoman-Backend commit -m "refactor: retire legacy interaction APIs"
git -C /root/Atoman/Atoman-Frontend add src/components/blog/CommentSection.vue src/components/video/VideoCommentSection.vue src/composables/useVideoTimestamp.ts src/composables/useApi.ts src/types.ts tests/unit/components/CommentSection.spec.ts tests/unit/components/VideoCommentSection.spec.ts tests/unit/composables/useVideoTimestamp.spec.ts tests/e2e/specs/comments.spec.ts tests/e2e/specs/forum.spec.ts tests/e2e/specs/debate.spec.ts tests/e2e/specs/timeline.spec.ts
git -C /root/Atoman/Atoman-Frontend commit -m "test: verify unified comment workflows"
```
