# 私信模块重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可上线的 Web 私信 MVP，使用户可联系用户或频道，频道所有者自动以频道身份回复，并具备私有图片、稳定分页、实时对账、统一未读、拉黑和举报能力。

**Architecture:** 后端将旧 `internal/handlers/dm_handler.go` 重写为独立 `internal/modules/dm`，HTTP、业务规则、数据库访问和实时推送分别收口，旧表通过扩展与回填升级为带参与方类型的新模型。前端以 typed client 和 normalized Pinia Store 为唯一数据入口，WebSocket 只做低延迟更新，连接成功后始终以 REST 数据对账。

**Tech Stack:** Go、Gin、GORM、PostgreSQL、S3/MinIO、Vue 3、TypeScript、Pinia、Vitest、Playwright

---

## 执行约定

- 所有路径均以 `/root/Atoman` 为根目录；Backend 与 Frontend 是两个独立 Git 仓库，分别提交。
- 后端每个任务至少运行该任务列出的定向测试；完成后必须运行 `go build ./...`。
- 前端每个任务至少运行该任务列出的 Vitest；完成后必须运行 `bun run type-check`。
- 不提交两个仓库中已经存在的无关修改和未跟踪文件。
- HTTP 只返回 `dm` 包中的公开 DTO，不直接序列化 GORM Model，也不向普通用户返回 `ActorUserID`。
- 迁移保留旧列，运行时代码只读新字段；旧 HTTP 路由在新前后端同一发布窗口中硬切换。

## 文件结构

### Backend

- Modify: `Atoman-Backend/internal/model/dm.go` — DM v2 数据模型及稳定枚举。
- Modify: `Atoman-Backend/internal/model/user.go` — 新用户私信默认值改为 `one_before_reply`。
- Create: `Atoman-Backend/internal/migrations/dm_v2.go` — 表扩展、旧数据回填、约束和索引。
- Create: `Atoman-Backend/internal/migrations/dm_v2_test.go` — SQLite 迁移与幂等回归。
- Create: `Atoman-Backend/internal/migrations/dm_v2_postgres_test.go` — PostgreSQL 约束、并发唯一性和游标前置条件。
- Modify: `Atoman-Backend/cmd/migrate/main.go` — 注册 DM v2 模型与迁移。
- Modify: `Atoman-Backend/cmd/migrate/main_test.go` — 总迁移入口覆盖。
- Modify: `Atoman-Backend/cmd/start_server/main.go` — 启动迁移避开旧 DM AutoMigrate，并初始化私有图片存储。
- Modify: `Atoman-Backend/cmd/start_server/main_test.go` — 启动迁移顺序与存储配置回归。
- Create: `Atoman-Backend/internal/modules/dm/errors.go` — 稳定错误码。
- Create: `Atoman-Backend/internal/modules/dm/dto.go` — HTTP 与实时事件公开契约。
- Create: `Atoman-Backend/internal/modules/dm/repo.go` — 会话、消息、邮箱、图片、举报及计数查询。
- Create: `Atoman-Backend/internal/modules/dm/policy.go` — 身份推导、发起权限、拉黑和限流规则。
- Create: `Atoman-Backend/internal/modules/dm/service.go` — 事务边界与用例编排。
- Create: `Atoman-Backend/internal/modules/dm/image_store.go` — 私有本地/S3 图片存储及签名读取。
- Create: `Atoman-Backend/internal/modules/dm/realtime.go` — `UserHub` 事件适配。
- Create: `Atoman-Backend/internal/modules/dm/http.go` — 用户与管理员 HTTP 路由。
- Create: `Atoman-Backend/internal/modules/dm/*_test.go` — Service、HTTP、Realtime 和存储测试。
- Create: `Atoman-Backend/cmd/migrate_dm_images/main.go` — 将旧图片复制到私有 Bucket 并绑定 `DMImage`。
- Create: `Atoman-Backend/cmd/migrate_dm_images/main_test.go` — 旧图片识别、复制与失败中止测试。
- Modify: `Atoman-Backend/internal/modules/notification/repo.go` — typed DM 与频道收件箱未读查询。
- Modify: `Atoman-Backend/internal/modules/notification/service.go` — 向 DM 提供全站未读计数。
- Modify: `Atoman-Backend/internal/app/router.go` — 装配新 DM 模块。
- Modify: `Atoman-Backend/internal/app/router_test.go` — 新路由存在、旧路由消失。
- Delete: `Atoman-Backend/internal/handlers/dm_handler.go` — 删除旧 Handler。
- Delete: `Atoman-Backend/internal/handlers/dm_handler_test.go` — 由模块测试替代。
- Modify: `Atoman-Backend/internal/handlers/swagger_types.go` — 删除旧 DM Swagger 类型。
- Modify: `Atoman-Backend/internal/handlers/user_handler.go` — 私信设置不再由通用用户设置写入。
- Modify: `Atoman-Backend/internal/handlers/user_handler_test.go` — 删除旧设置契约断言。
- Modify: `Atoman-Backend/docker-compose.dev.yml` — 创建不公开的 `atoman-dm-dev` Bucket。
- Modify: `Atoman-Backend/.env.example` — 增加 `DM_S3_BUCKET`。
- Modify: `Atoman-Backend/docs/swagger.yaml`、`docs/swagger.json`、`docs/docs.go` — 生成新契约。

### Frontend

- Create: `Atoman-Frontend/src/api/dm.ts` — DM DTO、错误映射与全部 API 调用。
- Create: `Atoman-Frontend/tests/unit/api/dm.spec.ts` — Cookie Session、CSRF 和 DTO 契约。
- Rewrite: `Atoman-Frontend/src/stores/dm.ts` — normalized Store、游标和事件归并。
- Rewrite: `Atoman-Frontend/tests/unit/stores/dm.spec.ts` — 新会话、去重、分页、未读和过期请求。
- Modify: `Atoman-Frontend/src/stores/inbox.ts` — WebSocket 指数重连及连接后对账。
- Modify: `Atoman-Frontend/tests/unit/stores/inbox.spec.ts` — 重连、事件路由和定时器测试。
- Modify: `Atoman-Frontend/src/stores/notification.ts` — DM 未读成为统一角标状态源。
- Create: `Atoman-Frontend/src/components/dm/DMMailboxSelector.vue` — 个人/频道收件箱选择。
- Create: `Atoman-Frontend/src/components/dm/DMConversationList.vue` — 会话列表与加载更多。
- Create: `Atoman-Frontend/src/components/dm/DMConversationPane.vue` — 消息详情与移动端返回。
- Create: `Atoman-Frontend/src/components/dm/DMComposer.vue` — 文本、单图和自动身份提示。
- Create: `Atoman-Frontend/src/components/dm/DMReportModal.vue` — 单条消息举报。
- Create: `Atoman-Frontend/src/components/dm/DMSettingsPanel.vue` — 用户或频道私信权限。
- Create: `Atoman-Frontend/src/components/dm/DMAdminReportsPanel.vue` — 管理员举报处理。
- Create: `Atoman-Frontend/tests/unit/components/dm/*.spec.ts` — 组件交互与响应式行为。
- Modify: `Atoman-Frontend/src/views/feed/InboxPage.vue` — 接入 DM 组件及移动端两级页面。
- Modify: `Atoman-Frontend/src/views/blog/ProfileView.vue` — 用户私信入口。
- Modify: `Atoman-Frontend/src/views/blog/ChannelView.vue` — 频道私信入口。
- Modify: `Atoman-Frontend/src/components/user/UserBlogSettingsPanel.vue` — 个人设置改用 DM API。
- Modify: `Atoman-Frontend/src/views/blog/UserManagementView.vue` — 移除重复的旧私信设置调用。
- Modify: `Atoman-Frontend/src/views/studio/StudioSettingsView.vue` — 频道私信权限。
- Modify: `Atoman-Frontend/src/views/setting/SettingCommunityView.vue` — 管理员举报区。
- Modify: `Atoman-Frontend/src/composables/useApi.ts`、`src/types.ts` — 删除旧 DM 契约。
- Create: `Atoman-Frontend/tests/e2e/specs/dm.real.spec.ts` — 双用户、频道、图片、拉黑和举报真实链路。

### 开发基础设施

- 当前工作区的开发 Compose 实际位于 `Atoman-Backend/docker-compose.dev.yml`，本计划直接修改该现有文件，不另建同名文件。

## Task 1：升级 DM 数据模型并迁移旧数据

**Files:**
- Modify: `Atoman-Backend/internal/model/dm.go`
- Modify: `Atoman-Backend/internal/model/user.go`
- Create: `Atoman-Backend/internal/migrations/dm_v2.go`
- Create: `Atoman-Backend/internal/migrations/dm_v2_test.go`
- Create: `Atoman-Backend/internal/migrations/dm_v2_postgres_test.go`
- Modify: `Atoman-Backend/cmd/migrate/main.go`
- Modify: `Atoman-Backend/cmd/migrate/main_test.go`
- Modify: `Atoman-Backend/cmd/start_server/main.go`
- Modify: `Atoman-Backend/cmd/start_server/main_test.go`

- [ ] **Step 1：写旧数据回填失败测试**

在 `dm_v2_test.go` 先创建旧版 `dm_conversations`、`dm_messages` 和明确设置为 `anyone` 的 `user_settings`，调用 `RunDMV2Migration` 后断言：

```go
require.Equal(t, "user", conversation.ParticipantAType)
require.Equal(t, "user", conversation.ParticipantBType)
require.Equal(t, "user", message.SenderType)
require.Equal(t, message.SenderID, message.ActorUserID)
require.Equal(t, message.ID, message.ClientMessageID)
require.Equal(t, "anyone", settings.DMPermission)
require.NoError(t, RunDMV2Migration(db))
```

在 PostgreSQL 测试中断言非法类型、`channel -> channel`、重复 typed conversation、重复 `(actor_user_id, client_message_id)` 和一图多消息都会被数据库拒绝。

- [ ] **Step 2：运行迁移测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/migrations ./cmd/migrate ./cmd/start_server -run 'TestRunDMV2|TestMigrateSchemaCreatesDMV2|TestStartupDMV2MigrationOrder' -count=1
```

Expected: FAIL，提示 `RunDMV2Migration`、typed 字段或新表不存在。

- [ ] **Step 3：定义 DM v2 模型**

在 `internal/model/dm.go` 定义并让 `TableName` 保持旧表名：

```go
const (
	DMPartyUser    = "user"
	DMPartyChannel = "channel"

	DMPermissionOneBeforeReply = "one_before_reply"
	DMPermissionFollowingOnly  = "following_only"
	DMPermissionAnyone         = "anyone"
	DMPermissionClosed         = "closed"

	DMReportPending   = "pending"
	DMReportResolved  = "resolved"
	DMReportDismissed = "dismissed"
)

type DMConversation struct {
	Base
	ParticipantAType  string     `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	ParticipantA      uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ParticipantBType  string     `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	ParticipantB      uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	LastMessageAt     *time.Time `json:"-"`
	LastMessagePreview string    `json:"-" gorm:"size:100"`
}

type DMMessage struct {
	Base
	ConversationID uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	SenderType      string     `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	SenderID        uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ActorUserID     uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ClientMessageID uuid.UUID  `json:"-" gorm:"type:uuid;not null"`
	Content         string     `json:"-" gorm:"type:text"`
	ImageID         *uuid.UUID `json:"-" gorm:"type:uuid"`
	ReadAt          *time.Time `json:"-"`
}

type DMImage struct {
	Base
	UploadedByUserID uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	ObjectKey        string    `json:"-" gorm:"type:text;not null"`
	ContentType      string    `json:"-" gorm:"type:varchar(64);not null"`
	SizeBytes        int64     `json:"-" gorm:"not null"`
}

type DMChannelSettings struct {
	ChannelID  uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	Permission string    `json:"-" gorm:"type:varchar(32);not null;default:'one_before_reply'"`
}

type DMMessageReport struct {
	Base
	MessageID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ReporterUserID      uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ReportedActorUserID uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	Reason              string     `json:"-" gorm:"type:varchar(64);not null"`
	Detail              string     `json:"-" gorm:"type:text"`
	SnapshotContent     string     `json:"-" gorm:"type:text"`
	SnapshotImageKey    string     `json:"-" gorm:"type:text"`
	Status              string     `json:"-" gorm:"type:varchar(16);not null;default:'pending'"`
	ReviewedByUserID    *uuid.UUID `json:"-" gorm:"type:uuid"`
	ReviewedAt          *time.Time `json:"-"`
}
```

同时把 `UserSettings.DMPermission` 的 GORM 默认值改成 `one_before_reply`；迁移只把空值改成该值，保留现有明确的 `anyone`、`following_only` 和 `one_before_reply`。

- [ ] **Step 4：实现可重复执行的扩展、回填和约束**

旧表已有数据，不能先对 `DMConversation`/`DMMessage` 直接执行带 `NOT NULL` 的 AutoMigrate。`RunDMV2Migration` 必须先按方言增加可空列、完成回填，再收紧 PostgreSQL 约束：

```go
func RunDMV2Migration(db *gorm.DB) error {
	if err := addNullableDMV2Columns(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&model.DMImage{},
		&model.DMChannelSettings{},
		&model.DMMessageReport{},
	); err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dm_conversations SET participant_a_type = 'user', participant_b_type = 'user' WHERE participant_a_type = '' OR participant_b_type = ''`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dm_messages SET sender_type = 'user' WHERE sender_type IS NULL OR sender_type = ''`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dm_messages SET actor_user_id = sender_id WHERE actor_user_id IS NULL OR actor_user_id = '00000000-0000-0000-0000-000000000000'`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dm_messages SET client_message_id = id WHERE client_message_id IS NULL OR client_message_id = '00000000-0000-0000-0000-000000000000'`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE user_settings SET dm_permission = 'one_before_reply' WHERE dm_permission IS NULL OR trim(dm_permission) = ''`).Error; err != nil {
		return err
	}
	return createDMV2Constraints(db)
}
```

PostgreSQL 的 `addNullableDMV2Columns` 精确执行：

```sql
ALTER TABLE dm_conversations ADD COLUMN IF NOT EXISTS participant_a_type varchar(16);
ALTER TABLE dm_conversations ADD COLUMN IF NOT EXISTS participant_b_type varchar(16);
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS sender_type varchar(16);
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS actor_user_id uuid;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS client_message_id uuid;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS image_id uuid;
```

如果会话或消息表不存在，helper 直接对对应新 Model 执行 AutoMigrate；只有表已存在时才走可空列扩展，因此全新数据库和旧数据库共用同一入口。回填完成后对参与方 type、sender type、actor 和 client message ID 执行 `ALTER COLUMN ... SET NOT NULL`，再把三个 type 列的 default 设置为 `user`；SQLite 只验证回填值与唯一索引，因为它不能直接收紧已有列。

`createDMV2Constraints` 在 PostgreSQL 创建以下命名约束/索引，在 SQLite 测试环境创建可表达的唯一索引：

```sql
DROP INDEX IF EXISTS uq_dm_conversation;
CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_conversation_typed
ON dm_conversations (participant_a_type, participant_a, participant_b_type, participant_b)
WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_actor_client_message
ON dm_messages (actor_user_id, client_message_id)
WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_message_image
ON dm_messages (image_id)
WHERE image_id IS NOT NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_message_reporter
ON dm_message_reports (message_id, reporter_user_id)
WHERE deleted_at IS NULL;
```

PostgreSQL CHECK 约束必须验证 A 为用户、B 为用户或频道、用户会话 UUID 规范化排序，以及举报状态只能为三种稳定值：

```sql
CHECK (participant_a_type = 'user');
CHECK (participant_b_type IN ('user', 'channel'));
CHECK (participant_b_type = 'channel' OR (participant_a <> participant_b AND participant_a::text < participant_b::text));
CHECK (status IN ('pending', 'resolved', 'dismissed'));
```

- [ ] **Step 5：把迁移接入唯一入口**

从 `migrateSchema` 的通用 `models` slice 中移除旧 `DMConversation` 和 `DMMessage`，避免 Generic AutoMigrate 早于回填；三个新模型也只由 `RunDMV2Migration` 创建。在旧 `RunNotificationDMIndexes` 之后调用：

```go
if err := migrations.RunDMV2Migration(db); err != nil {
	return fmt.Errorf("dm v2 migration: %w", err)
}
```

`cmd/start_server/main.go` 还有一条独立的启动迁移路径，也必须从它的 `models` slice 移除旧 DM 两个模型，并在 Generic AutoMigrate 完成后调用同一个 `RunDMV2Migration`。两条启动路径不得复制迁移 SQL。

旧 `RunNotificationDMIndexes` 删除 `uq_dm_conversation` 创建逻辑，但保留通知索引；消息索引由 DM v2 迁移统一负责。

- [ ] **Step 6：运行 SQLite 和 PostgreSQL 迁移测试**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/migrations ./cmd/migrate ./cmd/start_server -run 'TestRunDMV2|TestMigrateSchemaCreatesDMV2|TestStartupDMV2MigrationOrder' -count=1
TEST_DATABASE_URL='postgres://atoman:atoman_secret@localhost:5432/atoman_dev?sslmode=disable' go test ./internal/migrations -run TestRunDMV2Postgres -count=1
```

Expected: 两条命令均 PASS；第二条在未提供 PostgreSQL 时只能显式 SKIP，不得伪装为通过。

- [ ] **Step 7：提交 Backend 迁移**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/model/dm.go internal/model/user.go internal/migrations/dm_v2.go internal/migrations/dm_v2_test.go internal/migrations/dm_v2_postgres_test.go internal/migrations/notification_dm_indexes.go cmd/migrate/main.go cmd/migrate/main_test.go cmd/start_server/main.go cmd/start_server/main_test.go
git commit -m "feat(dm): add typed dm data model"
```

## Task 2：建立独立 DM Service、Repo、DTO 和身份规则

**Files:**
- Create: `Atoman-Backend/internal/modules/dm/errors.go`
- Create: `Atoman-Backend/internal/modules/dm/dto.go`
- Create: `Atoman-Backend/internal/modules/dm/repo.go`
- Create: `Atoman-Backend/internal/modules/dm/policy.go`
- Create: `Atoman-Backend/internal/modules/dm/service.go`
- Create: `Atoman-Backend/internal/modules/dm/service_test.go`
- Create: `Atoman-Backend/internal/modules/dm/policy_test.go`

- [ ] **Step 1：写用户与频道身份失败测试**

测试表至少包含：用户联系用户、用户联系频道、频道所有者回复、非所有者读取频道会话、频道所有者用目标接口联系用户、用户联系自己的频道、`channel -> channel`。核心断言：

```go
require.Equal(t, model.DMPartyUser, userMessage.SenderType)
require.Equal(t, sender.UUID, userMessage.SenderID)
require.Equal(t, sender.UUID, userMessage.ActorUserID)

require.Equal(t, model.DMPartyChannel, channelReply.SenderType)
require.Equal(t, channel.ID, channelReply.SenderID)
require.Equal(t, owner.UUID, channelReply.ActorUserID)

require.ErrorIs(t, nonOwnerErr, ErrConversationForbidden)
require.ErrorIs(t, selfChannelErr, ErrSelfTarget)
```

- [ ] **Step 2：运行 Service 测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(ServiceIdentity|PolicyResolveSender)' -count=1
```

Expected: FAIL，提示 `NewService`、`TargetRef` 或发送方法不存在。

- [ ] **Step 3：定义稳定错误和公开 DTO**

`errors.go` 用 `errors.Is` 可比较的包级错误表达领域分支，并由 HTTP 层映射为设计中的稳定 code：

```go
var (
	ErrTargetNotFound        = errors.New("dm target not found")
	ErrSelfTarget            = errors.New("dm self target")
	ErrPermissionDenied      = errors.New("dm permission denied")
	ErrWaitingReply          = errors.New("dm waiting reply")
	ErrBlocked               = errors.New("dm blocked")
	ErrConversationForbidden = errors.New("dm conversation forbidden")
	ErrImageInvalid          = errors.New("dm image invalid")
	ErrRateLimited           = errors.New("dm rate limited")
	ErrMessageNotFound       = errors.New("dm message not found")
	ErrAlreadyReported       = errors.New("dm already reported")
)
```

`dto.go` 固定以下跨层类型，ID 一律使用 UUID 字符串：

```go
type PartyDTO struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Username    string `json:"username,omitempty"`
	Slug        string `json:"slug,omitempty"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

type MailboxDTO struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	UnreadCount int64  `json:"unread_count"`
}

func (m MailboxDTO) Key() string {
	return m.Type + ":" + m.ID
}

type ConversationDTO struct {
	ID                 string     `json:"id"`
	Mailbox            MailboxDTO `json:"mailbox"`
	OtherParty          PartyDTO   `json:"other_party"`
	LastMessageAt       *time.Time `json:"last_message_at"`
	LastMessagePreview  string     `json:"last_message_preview"`
	UnreadCount         int64      `json:"unread_count"`
	Blocked             bool       `json:"blocked"`
	ReplyAs             PartyDTO   `json:"reply_as"`
}

type MessageDTO struct {
	ID              string     `json:"id"`
	ConversationID  string     `json:"conversation_id"`
	ClientMessageID string     `json:"client_message_id"`
	Sender          PartyDTO   `json:"sender"`
	Content         string     `json:"content"`
	ImageID         *string    `json:"image_id,omitempty"`
	ImageURL        string     `json:"image_url,omitempty"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type SendInput struct {
	ClientMessageID uuid.UUID  `json:"client_message_id" binding:"required"`
	Content         string     `json:"content"`
	ImageID         *uuid.UUID `json:"image_id"`
}
```

- [ ] **Step 4：实现参与方规范化与服务接口**

`policy.go` 只接受真实操作用户，不接受客户端提交的发送身份：

```go
type TargetRef struct {
	Type string
	ID   uuid.UUID
}

type SenderIdentity struct {
	Type        string
	ID          uuid.UUID
	ActorUserID uuid.UUID
}

func NormalizeParties(left, right TargetRef) (TargetRef, TargetRef, error) {
	if left.Type == model.DMPartyChannel && right.Type == model.DMPartyChannel {
		return TargetRef{}, TargetRef{}, ErrPermissionDenied
	}
	if left.Type == model.DMPartyChannel {
		return right, left, nil
	}
	if right.Type == model.DMPartyChannel {
		return left, right, nil
	}
	if strings.Compare(left.ID.String(), right.ID.String()) <= 0 {
		return left, right, nil
	}
	return right, left, nil
}
```

`service.go` 暴露用例级方法：

```go
type Service struct {
	db          *gorm.DB
	repo        *Repo
	policy      *Policy
	images      ImageStore
	publisher   Publisher
	siteUnread  SiteUnreadCounter
	now         func() time.Time
}

type ImageStore interface {
	Put(ctx context.Context, objectKey, contentType string, body io.Reader, size int64) error
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	SignedURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, objectKey string) error
	IsLocal() bool
}

type Publisher interface {
	Push(userID uuid.UUID, event string, payload any)
}

type SiteUnreadCounter interface {
	CountSiteUnread(userID uuid.UUID) (int64, error)
}

func (s *Service) SendToTarget(ctx context.Context, actor authctx.CurrentUser, target TargetRef, input SendInput) (MessageDTO, error)
func (s *Service) SendInConversation(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID, input SendInput) (MessageDTO, error)
func (s *Service) GetTargetConversation(ctx context.Context, actor authctx.CurrentUser, target TargetRef) (*ConversationDTO, error)
```

目标发送只能形成 `actor user -> target user/channel`；已有会话发送由 `Policy.ResolveSender` 根据会话参与方和频道 `UserID` 自动返回个人或频道身份。

- [ ] **Step 5：实现 Repo 的事务原语**

`repo.go` 提供明确的锁定和查询方法，不把 `*gorm.DB` 暴露给 HTTP：

```go
func NewRepo(db *gorm.DB) *Repo
func (r *Repo) ResolveTarget(tx *gorm.DB, target TargetRef) (PartyDTO, uuid.UUID, error)
func (r *Repo) FindConversation(tx *gorm.DB, left, right TargetRef, lock bool) (*model.DMConversation, error)
func (r *Repo) FindOrCreateConversation(tx *gorm.DB, left, right TargetRef) (*model.DMConversation, bool, error)
func (r *Repo) GetConversationForActor(tx *gorm.DB, actorID, conversationID uuid.UUID, lock bool) (*model.DMConversation, error)
func (r *Repo) CreateMessage(tx *gorm.DB, message *model.DMMessage) error
func (r *Repo) FindMessageByClientID(tx *gorm.DB, actorID, clientID uuid.UUID) (*model.DMMessage, error)
func (r *Repo) UpdateConversationPreview(tx *gorm.DB, conversationID uuid.UUID, at time.Time, preview string) error
```

`FindOrCreateConversation` 使用 typed partial unique index处理并发：插入冲突后重新查询；用户频道会话固定 user 为 A、channel 为 B，用户会话按 UUID 排序。

- [ ] **Step 6：实现发送事务和幂等返回**

每次发送都在一个事务内执行：规范化输入、查幂等消息、锁定或创建会话、推导发送身份、校验策略、绑定图片、创建消息、更新摘要。事务提交后才构造 DTO 并推送：

```go
if existing, err := s.repo.FindMessageByClientID(tx, actor.ID, input.ClientMessageID); err == nil {
	message = existing
	return nil
}

message = &model.DMMessage{
	ConversationID: conversation.ID,
	SenderType:      identity.Type,
	SenderID:        identity.ID,
	ActorUserID:     actor.ID,
	ClientMessageID: input.ClientMessageID,
	Content:         strings.TrimSpace(input.Content),
	ImageID:         input.ImageID,
}
```

文本按 Unicode rune 计数限制为 4,000；文本和图片不能同时为空；预览空文本时固定为 `[图片]`。

- [ ] **Step 7：运行身份与幂等测试**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(ServiceIdentity|PolicyResolveSender|SendIdempotency|ConversationNormalization)' -count=1
```

Expected: PASS，且频道回复 DTO 中无 `actor_user_id` 字段。

- [ ] **Step 8：提交 Backend 服务骨架**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/errors.go internal/modules/dm/dto.go internal/modules/dm/repo.go internal/modules/dm/policy.go internal/modules/dm/service.go internal/modules/dm/service_test.go internal/modules/dm/policy_test.go
git commit -m "feat(dm): add standalone dm service"
```

## Task 3：实现邮箱、游标分页、已读和统一未读

**Files:**
- Modify: `Atoman-Backend/internal/modules/dm/dto.go`
- Modify: `Atoman-Backend/internal/modules/dm/repo.go`
- Modify: `Atoman-Backend/internal/modules/dm/service.go`
- Create: `Atoman-Backend/internal/modules/dm/mailbox_test.go`
- Modify: `Atoman-Backend/internal/modules/notification/repo.go`
- Modify: `Atoman-Backend/internal/modules/notification/service.go`
- Modify: `Atoman-Backend/internal/modules/notification/http_test.go`

- [ ] **Step 1：写邮箱与 keyset 分页失败测试**

覆盖个人邮箱、拥有的频道邮箱、越权频道邮箱、会话按 `(last_message_at, id)` 稳定翻页、消息首屏取最新 30 条后正序返回、`before=(created_at,id)` 加载更早数据。至少断言：

```go
require.Equal(t, []string{"user:" + actor.ID.String(), "channel:" + channel.ID.String()}, mailboxKeys(mailboxes))
require.Len(t, firstPage.Items, 30)
require.NotEmpty(t, firstPage.NextCursor)
require.True(t, firstPage.Items[0].CreatedAt.Before(firstPage.Items[29].CreatedAt))
require.Empty(t, intersectMessageIDs(firstPage.Items, olderPage.Items))
require.ErrorIs(t, forbiddenErr, ErrConversationForbidden)
```

- [ ] **Step 2：运行定向测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./internal/modules/notification -run 'Test(Mailboxes|ConversationCursor|MessageCursor|MarkRead|UnreadCounts)' -count=1
```

Expected: FAIL，提示邮箱、游标或 typed DM 未读尚未实现。

- [ ] **Step 3：定义游标与列表响应**

游标使用 base64url 编码 JSON，不接受客户端时间推测：

```go
type Cursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

type PageDTO[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type ReadResultDTO struct {
	ConversationUnread int64 `json:"conversation_unread"`
	MailboxUnread      int64 `json:"mailbox_unread"`
	DMUnread           int64 `json:"dm_unread"`
	TotalUnread        int64 `json:"total_unread"`
}
```

解码失败返回 `validation.invalid_request`；limit 默认 30，范围 1 到 100。

- [ ] **Step 4：实现邮箱与会话查询**

`ListMailboxes` 固定先返回当前用户邮箱，再按频道名称返回当前用户拥有且未删除的频道邮箱。频道未读只统计 `user -> channel` 未读消息；个人邮箱的频道会话只统计 `channel -> user` 未读消息。

```go
func (s *Service) ListMailboxes(ctx context.Context, actor authctx.CurrentUser) ([]MailboxDTO, error)
func (s *Service) ListConversations(ctx context.Context, actor authctx.CurrentUser, mailbox TargetRef, cursor string, limit int) (PageDTO[ConversationDTO], error)
func (s *Service) ListMessages(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID, before string, limit int) (PageDTO[MessageDTO], error)
func (s *Service) MarkRead(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ReadResultDTO, error)
```

会话列表 keyset 条件为：

```sql
WHERE (last_message_at, id) < (?, ?)
ORDER BY last_message_at DESC NULLS LAST, id DESC
LIMIT ?
```

消息查询先按 `created_at DESC, id DESC` 取 `limit + 1` 条，再在内存反转本页，使返回结果始终从旧到新。

- [ ] **Step 5：实现准确已读与统一计数**

已读更新只命中当前邮箱的对方身份：个人会话更新对方 user 消息，用户侧频道会话更新 channel 消息，频道侧更新 user 消息。`notification.Repo.CountUnreadDM` 改为 typed 查询并包含当前用户拥有的频道：

```sql
WHERE dm_messages.read_at IS NULL
AND (
  (c.participant_b_type = 'user' AND (c.participant_a = @user OR c.participant_b = @user) AND dm_messages.sender_id <> @user)
  OR
  (c.participant_b_type = 'channel' AND c.participant_a = @user AND dm_messages.sender_type = 'channel')
  OR
  (c.participant_b_type = 'channel' AND channels.user_id = @user AND dm_messages.sender_type = 'user')
)
```

给 `notification.Service` 增加：

```go
func (s *Service) CountSiteUnread(userID uuid.UUID) (int64, error) {
	counts, err := s.GetUnreadCounts(authctx.CurrentUser{ID: userID})
	return counts.Total, err
}
```

DM Service 通过 `SiteUnreadCounter` 接口调用它，避免 HTTP 层拼装不一致的角标。

- [ ] **Step 6：运行邮箱、分页和未读测试**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./internal/modules/notification -run 'Test(Mailboxes|ConversationCursor|MessageCursor|MarkRead|UnreadCounts)' -count=1
```

Expected: PASS；同一条频道未读不能同时计入个人邮箱与频道邮箱。

- [ ] **Step 7：提交 Backend 邮箱链路**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/dto.go internal/modules/dm/repo.go internal/modules/dm/service.go internal/modules/dm/mailbox_test.go internal/modules/notification/repo.go internal/modules/notification/service.go internal/modules/notification/http_test.go
git commit -m "feat(dm): add mailboxes pagination and unread state"
```

## Task 4：实现权限、拉黑、幂等与限流

**Files:**
- Modify: `Atoman-Backend/internal/modules/dm/policy.go`
- Modify: `Atoman-Backend/internal/modules/dm/repo.go`
- Modify: `Atoman-Backend/internal/modules/dm/service.go`
- Create: `Atoman-Backend/internal/modules/dm/policy_send_test.go`
- Create: `Atoman-Backend/internal/modules/dm/service_postgres_test.go`

- [ ] **Step 1：写策略矩阵失败测试**

用表驱动测试覆盖：用户默认 `one_before_reply`、`following_only`、`anyone`；频道默认 `one_before_reply`、`anyone`、`closed`；任一方向拉黑；回复后解除一条限制；频道所有者的个人回复不能误算为频道回复；每小时第 11 个新目标；每分钟第 31 条消息；重复 `client_message_id` 不重复计数。

```go
tests := []struct {
	name string
	permission string
	wantErr error
}{
	{name: "one message waits for reply", permission: model.DMPermissionOneBeforeReply, wantErr: ErrWaitingReply},
	{name: "closed channel rejects", permission: model.DMPermissionClosed, wantErr: ErrPermissionDenied},
	{name: "blocked conversation is read only", permission: model.DMPermissionAnyone, wantErr: ErrBlocked},
}
```

- [ ] **Step 2：运行策略测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(SendPolicy|BlockPolicy|RateLimits|ConcurrentSend)' -count=1
```

Expected: FAIL，至少有权限默认值、频道规则或限流断言失败。

- [ ] **Step 3：实现收件方权限读取**

```go
func (r *Repo) RecipientPermission(tx *gorm.DB, recipient TargetRef) (string, error) {
	switch recipient.Type {
	case model.DMPartyUser:
		var settings model.UserSettings
		err := tx.FirstOrCreate(&settings, model.UserSettings{UserID: recipient.ID}).Error
		if settings.DMPermission == "" {
			settings.DMPermission = model.DMPermissionOneBeforeReply
		}
		return settings.DMPermission, err
	case model.DMPartyChannel:
		var settings model.DMChannelSettings
		err := tx.FirstOrCreate(&settings, model.DMChannelSettings{ChannelID: recipient.ID}).Error
		if settings.Permission == "" {
			settings.Permission = model.DMPermissionOneBeforeReply
		}
		return settings.Permission, err
	default:
		return "", ErrTargetNotFound
	}
}
```

用户 `following_only` 的方向固定为“收件人关注发件人”；频道不支持该值。

- [ ] **Step 4：实现真实用户拉黑与一条限制**

用户与频道会话都把拉黑映射到两个真实用户：发送操作人和频道所有者。任一方向存在 `user_blocks` 后拒绝发送，但读取、举报和取消拉黑不受影响。

`one_before_reply` 在锁定会话后计算双方显示身份的消息数：发起方已有消息且收件方身份从未回复时返回 `ErrWaitingReply`。频道所有者以个人身份在另一会话发送的消息不能解除频道会话限制。

Service 同时提供会话级拉黑操作，服务端解析真实对方用户，不向前端暴露频道 `ActorUserID`：

```go
func (s *Service) BlockConversation(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ConversationDTO, error)
func (s *Service) UnblockConversation(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ConversationDTO, error)
```

用户会话解析另一用户；用户与频道会话在用户侧解析频道所有者、在频道侧解析会话中的用户。写入或删除 `user_blocks` 后，所有涉及这两个真实用户的个人/频道会话在下次查询时统一显示为只读。

- [ ] **Step 5：实现数据库计数限流**

不引入 Redis。发送事务内调用：

```go
func (r *Repo) CountActorMessagesSince(tx *gorm.DB, actorID uuid.UUID, since time.Time) (int64, error)
func (r *Repo) CountActorInitiatedTargetsSince(tx *gorm.DB, actorID uuid.UUID, since time.Time) (int64, error)
```

第一个按 `actor_user_id` 统计一分钟；第二个只统计“该会话首条消息由 actor 发出”的不同 typed 目标并限定一小时。已有目标不消耗新目标额度；幂等命中在限流前直接返回原消息。

- [ ] **Step 6：验证并发发送**

PostgreSQL 测试同时发起相同 typed 目标，并使用同一个和不同 `client_message_id` 两组场景：同一个 ID 只落一条消息，不同 ID 均落库但只创建一个会话。

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(SendPolicy|BlockPolicy|RateLimits)' -count=1
TEST_DATABASE_URL='postgres://atoman:atoman_secret@localhost:5432/atoman_dev?sslmode=disable' go test ./internal/modules/dm -run TestConcurrentSendPostgres -count=1
```

Expected: 两条命令均 PASS，无重复会话或重复幂等消息。

- [ ] **Step 7：提交 Backend 策略**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/policy.go internal/modules/dm/repo.go internal/modules/dm/service.go internal/modules/dm/policy_send_test.go internal/modules/dm/service_postgres_test.go
git commit -m "feat(dm): enforce messaging policies"
```

## Task 5：实现私有图片存储和旧图片迁移

**Files:**
- Create: `Atoman-Backend/internal/modules/dm/image_store.go`
- Create: `Atoman-Backend/internal/modules/dm/image_store_test.go`
- Modify: `Atoman-Backend/internal/modules/dm/repo.go`
- Modify: `Atoman-Backend/internal/modules/dm/service.go`
- Create: `Atoman-Backend/internal/modules/dm/image_service_test.go`
- Create: `Atoman-Backend/cmd/migrate_dm_images/main.go`
- Create: `Atoman-Backend/cmd/migrate_dm_images/main_test.go`
- Modify: `Atoman-Backend/docker-compose.dev.yml`
- Modify: `Atoman-Backend/.env.example`

- [ ] **Step 1：写上传、越权与单次绑定失败测试**

覆盖 JPEG、PNG、WebP；伪造 MIME；GIF；超过 10 MB；非上传者绑定；一图绑定两条消息；上传者读取未绑定图片；会话参与方读取已绑定图片；陌生用户读取。核心断言：

```go
require.Equal(t, "image/png", image.ContentType)
require.LessOrEqual(t, image.SizeBytes, int64(10*1024*1024))
require.ErrorIs(t, spoofedErr, ErrImageInvalid)
require.ErrorIs(t, reusedErr, ErrImageInvalid)
require.ErrorIs(t, strangerErr, ErrConversationForbidden)
```

- [ ] **Step 2：运行图片测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./cmd/migrate_dm_images -run 'Test(Image|MigrateLegacyDMImage)' -count=1
```

Expected: FAIL，提示 `ImageStore`、上传用例或旧图片迁移命令不存在。

- [ ] **Step 3：定义本地与 S3 共用存储接口**

```go
type StoredImage struct {
	ObjectKey   string
	ContentType string
	SizeBytes   int64
}

type ImageStore interface {
	Put(ctx context.Context, objectKey, contentType string, body io.Reader, size int64) error
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	SignedURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, objectKey string) error
	IsLocal() bool
}
```

该接口已在 Task 2 的 `service.go` 定义，本任务实现它而不另建第二份契约。`NewImageStore` 按 `STORAGE_TYPE` 选择：本地文件写入 `DM_LOCAL_DIR`，默认 `.data/dm`；S3 只使用 `DM_S3_BUCKET`，不得回退到公开的 `S3_BUCKET`。对象 key 固定为 `images/<actor-user-id>/<image-id>.<ext>`。

- [ ] **Step 4：实现受限读取和签名 URL**

上传时先用 `io.LimitReader(maxSize+1)` 读取并以 `http.DetectContentType` 校验实际内容，只允许：

```go
var allowedDMImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}
```

Service 增加：

```go
type ImageDTO struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (s *Service) UploadImage(ctx context.Context, actor authctx.CurrentUser, body io.Reader, declaredType string, declaredSize int64) (ImageDTO, error)
func (s *Service) OpenImage(ctx context.Context, actor authctx.CurrentUser, imageID uuid.UUID) (io.ReadCloser, string, error)
```

本地 DTO URL 为 `/api/v1/dm/images/<id>/content`，该路由先鉴权再读取；S3/R2 DTO URL 使用 `GetObjectRequest(...).Presign(5*time.Minute)`。未绑定图片仅上传者可读，已绑定图片仅会话双方或频道所有者可读。

- [ ] **Step 5：在发送事务中锁定并绑定图片**

```go
func (r *Repo) LockUsableImage(tx *gorm.DB, actorID, imageID uuid.UUID) (*model.DMImage, error) {
	var image model.DMImage
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND uploaded_by_user_id = ? AND deleted_at IS NULL", imageID, actorID).
		First(&image).Error
	if err != nil {
		return nil, ErrImageInvalid
	}
	var used int64
	if err := tx.Model(&model.DMMessage{}).Where("image_id = ? AND deleted_at IS NULL", imageID).Count(&used).Error; err != nil {
		return nil, err
	}
	if used != 0 {
		return nil, ErrImageInvalid
	}
	return &image, nil
}
```

消息 DTO 每次读取时重新解析 URL，数据库只保存 `ImageID` 和私有对象 key。

- [ ] **Step 6：实现旧图片预检和复制命令**

`cmd/migrate_dm_images` 只处理 `image_url <> '' AND image_id IS NULL` 的旧消息。命令先预检全部 URL：

- `/uploads/dm/images/...` 必须能在本地文件目录找到。
- 以 `S3_URL_PREFIX/` 开头的 URL 提取现有对象 key。
- 其他 URL 直接返回包含消息 ID 的错误，在任何数据库更新前中止，避免静默丢图或迁移时访问任意外部地址。

预检通过后，本地来源使用 `os.Open`，旧 S3 来源使用旧公开 Bucket 的 `GetObject`，然后统一调用私有 `ImageStore.Put`；不得假设源对象已在私有 Bucket。创建 `DMImage` 后在单条事务内写入 `dm_messages.image_id`。命令结束时断言旧非空 `image_url` 均已有 `image_id`；旧 `image_url` 列保留但运行时代码不返回。

```bash
cd /root/Atoman/Atoman-Backend
go run ./cmd/migrate_dm_images --env .env.dev
```

Expected: 输出迁移总数和成功总数，不输出图片 URL；遇到不支持的旧 URL 时退出码非 0。

- [ ] **Step 7：配置私有开发 Bucket**

在 `.env.example` 增加：

```dotenv
DM_S3_BUCKET=atoman-dm-dev
DM_LOCAL_DIR=.data/dm
```

`docker-compose.dev.yml` 的 `minio-setup` 创建第二个 Bucket 并保持 private：

```sh
mc mb -p local/atoman-dm-dev || true
mc anonymous set private local/atoman-dm-dev
```

现有 `atoman-dev` 的公开策略不变，避免影响其他模块。

若 `STORAGE_TYPE != local` 但 `DM_S3_BUCKET` 或 S3 client 不可用，`NewImageStoreFromEnv` 返回实现同一接口的 unavailable store：文本私信仍可使用，图片上传/读取返回现有 `system.storage_unavailable` 503。初始化阶段可以 `HeadBucket` 检查私有 Bucket，但不得自动设置公开访问。

- [ ] **Step 8：运行图片全套测试**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./cmd/migrate_dm_images -run 'Test(Image|MigrateLegacyDMImage)' -count=1
```

Expected: PASS；测试对象存储中不存在未授权公开 URL。

- [ ] **Step 9：提交 Backend 图片链路**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/image_store.go internal/modules/dm/image_store_test.go internal/modules/dm/repo.go internal/modules/dm/service.go internal/modules/dm/image_service_test.go cmd/migrate_dm_images/main.go cmd/migrate_dm_images/main_test.go docker-compose.dev.yml .env.example
git commit -m "feat(dm): store message images privately"
```

## Task 6：实现消息举报与管理员处理

**Files:**
- Modify: `Atoman-Backend/internal/modules/dm/dto.go`
- Modify: `Atoman-Backend/internal/modules/dm/repo.go`
- Modify: `Atoman-Backend/internal/modules/dm/service.go`
- Create: `Atoman-Backend/internal/modules/dm/report_test.go`

- [ ] **Step 1：写举报权限和快照失败测试**

覆盖举报对方消息成功、举报自己的消息、陌生人举报、重复举报、图片消息快照、管理员列表、普通用户处理、重复处理。断言举报记录保存 `ReportedActorUserID`，管理员 DTO 不泄漏签名 URL：

```go
require.Equal(t, reportedMessage.ActorUserID, report.ReportedActorUserID)
require.Equal(t, reportedMessage.Content, report.SnapshotContent)
require.Equal(t, image.ObjectKey, report.SnapshotImageKey)
require.ErrorIs(t, ownMessageErr, ErrPermissionDenied)
require.ErrorIs(t, duplicateErr, ErrAlreadyReported)
require.ErrorIs(t, nonAdminErr, ErrPermissionDenied)
```

- [ ] **Step 2：运行举报测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(ReportMessage|ListReports|ReviewReport)' -count=1
```

Expected: FAIL，提示举报 Service 方法尚不存在。

- [ ] **Step 3：定义举报输入与管理员 DTO**

```go
type ReportInput struct {
	Reason string `json:"reason" binding:"required"`
	Detail string `json:"detail"`
}

type ReviewReportInput struct {
	Status string `json:"status" binding:"required"`
}

type ReportDTO struct {
	ID                  string     `json:"id"`
	MessageID           string     `json:"message_id"`
	ReporterUserID      string     `json:"reporter_user_id"`
	ReportedActorUserID string     `json:"reported_actor_user_id"`
	Reason              string     `json:"reason"`
	Detail              string     `json:"detail"`
	SnapshotContent     string     `json:"snapshot_content"`
	HasSnapshotImage    bool       `json:"has_snapshot_image"`
	ConversationContext string     `json:"conversation_context"`
	Status              string     `json:"status"`
	ReviewedByUserID    *string    `json:"reviewed_by_user_id,omitempty"`
	ReviewedAt          *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

type ReportReceiptDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
```

举报原因只接受 `spam`、`harassment`、`illegal`、`privacy`、`other`；detail 最多 1,000 个 Unicode 字符。

- [ ] **Step 4：实现举报与审核事务**

```go
func (s *Service) ReportMessage(ctx context.Context, actor authctx.CurrentUser, messageID uuid.UUID, input ReportInput) (ReportReceiptDTO, error)
func (s *Service) ListReports(ctx context.Context, actor authctx.CurrentUser, cursor string, limit int) (PageDTO[ReportDTO], error)
func (s *Service) ReviewReport(ctx context.Context, actor authctx.CurrentUser, reportID uuid.UUID, input ReviewReportInput) (ReportDTO, error)
```

举报人必须能访问会话，并且 `message.ActorUserID != actor.ID`；这样频道所有者不能举报自己以频道身份发送的消息。频道所有者举报用户消息时，举报人仍记录真实用户。审核仅允许 admin/owner，状态只能从 `pending` 变为 `resolved` 或 `dismissed`，同时写审核人和时间。

- [ ] **Step 5：运行举报测试**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm -run 'Test(ReportMessage|ListReports|ReviewReport)' -count=1
```

Expected: PASS；普通 DTO 中不包含 `reported_actor_user_id`，只有管理员举报 DTO 包含审计字段。

- [ ] **Step 6：提交 Backend 举报链路**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/dto.go internal/modules/dm/repo.go internal/modules/dm/service.go internal/modules/dm/report_test.go
git commit -m "feat(dm): add message reporting workflow"
```

## Task 7：切换新 HTTP 与实时契约并删除旧 Handler

**Files:**
- Create: `Atoman-Backend/internal/modules/dm/realtime.go`
- Create: `Atoman-Backend/internal/modules/dm/realtime_test.go`
- Create: `Atoman-Backend/internal/modules/dm/http.go`
- Create: `Atoman-Backend/internal/modules/dm/http_test.go`
- Modify: `Atoman-Backend/internal/app/router.go`
- Modify: `Atoman-Backend/internal/app/router_test.go`
- Delete: `Atoman-Backend/internal/handlers/dm_handler.go`
- Delete: `Atoman-Backend/internal/handlers/dm_handler_test.go`
- Modify: `Atoman-Backend/internal/handlers/swagger_types.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler.go`
- Modify: `Atoman-Backend/internal/handlers/user_handler_test.go`
- Modify: `Atoman-Backend/docs/swagger.yaml`
- Modify: `Atoman-Backend/docs/swagger.json`
- Modify: `Atoman-Backend/docs/docs.go`

- [ ] **Step 1：写 HTTP 契约和旧路由消失测试**

使用 Cookie Session 测试全部新路由、稳定错误 envelope、204 目标查询、游标 meta、multipart 上传和管理员鉴权。路由测试明确断言：

```go
require.Equal(t, http.StatusOK, request("GET", "/api/v1/dm/mailboxes").Code)
require.Equal(t, http.StatusNoContent, request("GET", "/api/v1/dm/targets/user/"+other.ID.String()+"/conversation").Code)
require.Equal(t, http.StatusNotFound, request("GET", "/api/v1/dm/conversations").Code)
require.Equal(t, http.StatusNotFound, request("POST", "/api/v1/dm/conversations/alice").Code)
require.Equal(t, http.StatusNotFound, request("POST", "/api/v1/dm/upload").Code)
```

- [ ] **Step 2：写实时事件失败测试**

发送首条用户消息和频道回复后，分别断言操作用户与接收用户都收到完整事件；接收频道消息时推给频道所有者：

```go
require.Equal(t, "dm.message.created", event.Event)
require.Equal(t, message.ID, event.Data.Message.ID)
require.Equal(t, conversation.ID, event.Data.Conversation.ID)
require.Equal(t, "channel:"+channel.ID.String(), event.Data.Mailbox.Key())
require.Equal(t, expectedDMUnread, event.Data.DMUnread)
require.Equal(t, expectedTotalUnread, event.Data.TotalUnread)
```

- [ ] **Step 3：运行 HTTP 与实时测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./internal/app -run 'Test(HTTP|Realtime|RegisterV1RoutesDM)' -count=1
```

Expected: FAIL，新路由未注册或仍命中旧 Handler。

- [ ] **Step 4：实现实时 Publisher 适配**

```go
type UserHubPublisher struct {
	Hub *collab.UserHub
}

func (p UserHubPublisher) Push(userID uuid.UUID, event string, payload any) {
	if p.Hub != nil {
		p.Hub.Push(userID, event, payload)
	}
}

type MessageCreatedEventDTO struct {
	Message      MessageDTO      `json:"message"`
	Conversation ConversationDTO `json:"conversation"`
	Mailbox      MailboxDTO      `json:"mailbox"`
	DMUnread     int64           `json:"dm_unread"`
	TotalUnread  int64           `json:"total_unread"`
}

type MessageReadEventDTO struct {
	ConversationID string    `json:"conversation_id"`
	ReadAt         time.Time `json:"read_at"`
	Mailbox        MailboxDTO `json:"mailbox"`
	DMUnread       int64     `json:"dm_unread"`
	TotalUnread    int64     `json:"total_unread"`
}

type MailboxUpdatedEventDTO struct {
	Mailbox     MailboxDTO `json:"mailbox"`
	DMUnread    int64      `json:"dm_unread"`
	TotalUnread int64      `json:"total_unread"`
}
```

事件固定为 `dm.message.created`、`dm.message.read`、`dm.mailbox.updated`。`dm.message.created` 携带 `MessageDTO`、`ConversationDTO`、接收方 `MailboxDTO`、`dm_unread` 和 `total_unread`；事务失败绝不推送，推送失败不回滚已提交消息。

- [ ] **Step 5：实现新路由和错误映射**

`RegisterRoutes(group *gin.RouterGroup, service *Service)` 注册：

```text
GET  /dm/mailboxes
GET  /dm/mailboxes/:type/:id/conversations
GET  /dm/targets/:type/:id/conversation
POST /dm/targets/:type/:id/messages
POST /dm/conversations/:id/messages
GET  /dm/conversations/:id/messages
PUT  /dm/conversations/:id/read
PUT  /dm/conversations/:id/block
DELETE /dm/conversations/:id/block
POST /dm/images
GET  /dm/images/:id/content
POST /dm/messages/:id/reports
GET  /dm/settings
PUT  /dm/settings
GET  /dm/channels/:id/settings
PUT  /dm/channels/:id/settings
GET  /admin/dm/reports
PUT  /admin/dm/reports/:id
```

错误到 HTTP 和稳定 code 的固定映射：

```go
var domainErrors = map[error]struct {
	Status int
	Code   string
}{
	ErrTargetNotFound:        {http.StatusNotFound, "dm.target_not_found"},
	ErrSelfTarget:            {http.StatusBadRequest, "dm.self_target"},
	ErrPermissionDenied:      {http.StatusForbidden, "dm.permission_denied"},
	ErrWaitingReply:          {http.StatusForbidden, "dm.waiting_reply"},
	ErrBlocked:               {http.StatusForbidden, "dm.blocked"},
	ErrConversationForbidden: {http.StatusForbidden, "dm.conversation_forbidden"},
	ErrImageInvalid:          {http.StatusBadRequest, "dm.image_invalid"},
	ErrRateLimited:           {http.StatusTooManyRequests, "dm.rate_limited"},
	ErrMessageNotFound:       {http.StatusNotFound, "dm.message_not_found"},
	ErrAlreadyReported:       {http.StatusConflict, "dm.already_reported"},
}
```

HTTP helper 遍历该表并用 `errors.Is(err, domainErr)` 匹配包装错误。所有其他错误交给 `httpx.Error` 返回通用 500，不返回 SQL、对象 key 或正文日志。

- [ ] **Step 6：实现设置 API 并收回旧所有权**

个人设置只接受 `one_before_reply`、`following_only`、`anyone`；频道设置只接受 `one_before_reply`、`anyone`、`closed`，且仅频道所有者可读写。

从 `handlers.UserSettingsInput` 和对应响应中删除 `DMPermission` 写入逻辑，删除旧测试断言；数据库列继续保留并由 DM 模块管理。

- [ ] **Step 7：在 Router 中完成硬切换**

先构造 notification service，再把它作为 `SiteUnreadCounter` 注入 DM Service：

```go
notificationService := notification.NewService(db)
notification.RegisterRoutes(group, notificationService)

dmService := dm.NewService(
	db,
	dm.NewRepo(db),
	dm.NewImageStoreFromEnv(s3Client),
	dm.UserHubPublisher{Hub: userHub},
	notificationService,
)
dm.RegisterRoutes(group, dmService)
```

删除 `handlers.SetupDMRoutes` 调用与旧文件；不保留重定向、兼容路由或旧 response shape。

- [ ] **Step 8：更新 Swagger 并验证旧路径消失**

为新 Handler 增加完整 godoc 注解后执行仓库现有生成命令：

```bash
cd /root/Atoman/Atoman-Backend
go generate ./cmd/start_server
go test ./internal/modules/dm ./internal/app ./docs -count=1
go build ./...
```

Expected: PASS；`rg -n '^  /api/v1/dm/(conversations|unread-count|upload)' docs/swagger.yaml` 无匹配，新 17 条路由全部存在。

- [ ] **Step 9：提交 Backend API 切换**

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/realtime.go internal/modules/dm/realtime_test.go internal/modules/dm/http.go internal/modules/dm/http_test.go internal/app/router.go internal/app/router_test.go internal/handlers/dm_handler.go internal/handlers/dm_handler_test.go internal/handlers/swagger_types.go internal/handlers/user_handler.go internal/handlers/user_handler_test.go docs/swagger.yaml docs/swagger.json docs/docs.go
git commit -m "feat(dm): switch to dm v2 api"
```

## Task 8：建立前端 typed DM Client 和 normalized Store

**Files:**
- Modify: `Atoman-Frontend/src/api/client.ts`
- Create: `Atoman-Frontend/src/api/dm.ts`
- Create: `Atoman-Frontend/tests/unit/api/dm.spec.ts`
- Rewrite: `Atoman-Frontend/src/stores/dm.ts`
- Rewrite: `Atoman-Frontend/tests/unit/stores/dm.spec.ts`
- Modify: `Atoman-Frontend/src/composables/useApi.ts`
- Modify: `Atoman-Frontend/src/types.ts`

- [ ] **Step 1：写 Client 契约失败测试**

Mock `apiFetch` 并断言所有请求使用 Cookie Session/CSRF transport，无 Authorization/localStorage token；目标查询 204 返回 `null`；发送包含客户端 UUID，不包含 `sender_type`、`sender_id`、`actor_user_id`：

```ts
expect(body).toEqual({
  client_message_id: expect.stringMatching(uuidPattern),
  content: '你好',
  image_id: null,
})
expect(body).not.toHaveProperty('sender_type')
expect(body).not.toHaveProperty('sender_id')
```

- [ ] **Step 2：写 normalized Store 失败测试**

覆盖邮箱归一化、首条发送插入新会话、同一事件按 message id 与 client id 去重、旧消息 prepend、会话切换过期响应隔离、频道 reply-as、已读同步统一 DM 未读。断言：

```ts
expect(store.mailboxOrder).toEqual(['user:me', 'channel:channel-1'])
expect(store.conversationIdsByMailbox['user:me']).toEqual(['conversation-2', 'conversation-1'])
expect(store.messagesByConversation['conversation-1'].map(item => item.id)).toEqual(['older', 'newer'])
expect(store.activeConversation?.reply_as).toMatchObject({ type: 'channel', id: 'channel-1' })
expect(notificationStore.unreadCounts.dm).toBe(0)
```

- [ ] **Step 3：运行前端测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/api/dm.spec.ts tests/unit/stores/dm.spec.ts
```

Expected: FAIL，typed client 和新 Store 字段不存在。

- [ ] **Step 4：给通用 Client 增加 204 支持**

```ts
export async function apiGetOptional<T>(url: string): Promise<T | null> {
  const response = await apiFetch(url, {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (response.status === 204) return null
  return unwrapResponse<T>(response)
}
```

不在 DM Client 中重复错误 envelope 解析。

- [ ] **Step 5：定义前端唯一 DM 契约**

`src/api/dm.ts` 定义：

```ts
export type DMPartyType = 'user' | 'channel'
export type DMPermission = 'one_before_reply' | 'following_only' | 'anyone' | 'closed'

export type DMParty = {
  type: DMPartyType
  id: string
  username?: string
  slug?: string
  display_name: string
  avatar_url?: string
}

export type DMMailbox = {
  type: DMPartyType
  id: string
  display_name: string
  unread_count: number
}

export type DMConversation = {
  id: string
  mailbox: DMMailbox
  other_party: DMParty
  last_message_at: string | null
  last_message_preview: string
  unread_count: number
  blocked: boolean
  reply_as: DMParty
}

export type DMMessage = {
  id: string
  conversation_id: string
  client_message_id: string
  sender: DMParty
  content: string
  image_id?: string
  image_url?: string
  read_at?: string | null
  created_at: string
}

export type DMPaged<T> = { items: T[]; next_cursor?: string }
export type DMTarget = { type: DMPartyType; id: string }

export type DMReadResult = {
  conversation_unread: number
  mailbox_unread: number
  dm_unread: number
  total_unread: number
}

export type DMRealtimeEvent =
  | {
      event: 'dm.message.created'
      data: {
        message: DMMessage
        conversation: DMConversation
        mailbox: DMMailbox
        dm_unread: number
        total_unread: number
      }
    }
  | {
      event: 'dm.message.read'
      data: {
        conversation_id: string
        read_at: string
        mailbox: DMMailbox
        dm_unread: number
        total_unread: number
      }
    }
  | {
      event: 'dm.mailbox.updated'
      data: {
        mailbox: DMMailbox
        dm_unread: number
        total_unread: number
      }
    }
```

导出 `listMailboxes`、`listConversations`、`getTargetConversation`、`listMessages`、`sendToTarget`、`sendInConversation`、`markConversationRead`、`blockConversation`、`unblockConversation`、`uploadDMImage`、设置、举报和管理员处理函数。URL 参数全部 `encodeURIComponent`。

- [ ] **Step 6：重写 Store 状态结构**

```ts
const mailboxesByKey = ref<Record<string, DMMailbox>>({})
const mailboxOrder = ref<string[]>([])
const conversationsById = ref<Record<string, DMConversation>>({})
const conversationIdsByMailbox = ref<Record<string, string[]>>({})
const messagesByConversation = ref<Record<string, DMMessage[]>>({})
const conversationCursorByMailbox = ref<Record<string, string | null>>({})
const messageCursorByConversation = ref<Record<string, string | null>>({})
const activeMailboxKey = ref('')
const activeConversationId = ref('')
const activeTarget = ref<DMTarget | null>(null)
const loadingConversations = ref(false)
const loadingMessages = ref(false)
const requestGeneration = ref(0)

type DMSnapshot = {
  mailboxes: DMMailbox[]
  conversationsByMailbox: Record<string, DMConversation[]>
  activeConversationId: string
  activeMessages: DMMessage[]
}
```

派生 `activeMailbox`、`activeConversation`、`activeMessages`、`canLoadOlderMessages`、`activeConversationBlocked` 和 `replyAsLabel`，不再以 username 作为主键。

- [ ] **Step 7：实现 REST 动作与归并函数**

Store 动作固定为：

```ts
async function bootstrapDM(): Promise<void>
async function selectMailbox(key: string): Promise<void>
async function loadMoreConversations(): Promise<void>
async function openTarget(target: DMTarget): Promise<void>
async function openConversation(id: string): Promise<void>
async function loadOlderMessages(): Promise<void>
async function sendMessage(content: string, imageId?: string): Promise<DMMessage>
async function markRead(): Promise<void>
async function blockActiveConversation(): Promise<void>
async function unblockActiveConversation(): Promise<void>
async function uploadImage(file: File): Promise<{ id: string; url: string }>
async function reportMessage(messageId: string, reason: string, detail: string): Promise<void>
async function reconcileFromServer(): Promise<void>
function receiveEvent(event: DMRealtimeEvent): void
function reconcile(snapshot: DMSnapshot): void
function resetStore(): void
```

每次 `openConversation` 增加 generation；响应只在 generation 与 active id 同时匹配时落地。消息归并优先按 `id`，发送回声再按 `client_message_id` 替换本地记录；列表始终按时间稳定排序。

- [ ] **Step 8：删除旧契约并运行测试**

从 `useApi.ts` 删除旧 `dm.conversations/conversation/markRead/unreadCount/upload`，从 `src/types.ts` 删除旧 `DMConversation`、`DMMessage`、`DMRealtimePayload` 和重复 `DMPermission`；所有新引用只从 `@/api/dm` 导入。

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/api/dm.spec.ts tests/unit/stores/dm.spec.ts
bun run type-check
```

Expected: PASS，且 `rg -n 'other_username|DMRealtimePayload|dm/unread-count|dm/upload' src` 无匹配。

- [ ] **Step 9：提交 Frontend 数据层**

```bash
cd /root/Atoman/Atoman-Frontend
git add src/api/client.ts src/api/dm.ts tests/unit/api/dm.spec.ts src/stores/dm.ts tests/unit/stores/dm.spec.ts src/composables/useApi.ts src/types.ts
git commit -m "feat(dm): add typed dm client and store"
```

## Task 9：实现 WebSocket 指数重连、REST 对账和统一未读

**Files:**
- Modify: `Atoman-Frontend/src/stores/inbox.ts`
- Modify: `Atoman-Frontend/tests/unit/stores/inbox.spec.ts`
- Modify: `Atoman-Frontend/src/stores/notification.ts`
- Modify: `Atoman-Frontend/tests/unit/stores/notification.spec.ts`
- Modify: `Atoman-Frontend/src/stores/dm.ts`
- Modify: `Atoman-Frontend/tests/unit/stores/dm.spec.ts`

- [ ] **Step 1：写重连和对账失败测试**

用 fake timers 和 FakeWebSocket 覆盖：1/2/4/8/16/30 秒退避、成功连接后重置退避、主动断开不重连、旧 socket close 不关闭新 socket、连接成功拉取邮箱/列表/当前消息、轮询 fallback、事件路由。核心断言：

```ts
expect(FakeWebSocket.instances).toHaveLength(1)
await vi.advanceTimersByTimeAsync(999)
expect(FakeWebSocket.instances).toHaveLength(1)
await vi.advanceTimersByTimeAsync(1)
expect(FakeWebSocket.instances).toHaveLength(2)
expect(dmStore.reconcileFromServer).toHaveBeenCalledTimes(1)
```

- [ ] **Step 2：写统一未读失败测试**

断言 REST、实时消息和已读响应都只更新 `notificationStore.unreadCounts.dm`，顶部总角标由现有 computed 自动变化；DM Store 不保留独立 `unreadCount`：

```ts
notificationStore.setDMUnread(4)
expect(notificationStore.unreadCounts.dm).toBe(4)
expect(notificationStore.unreadCount).toBe(otherUnread + 4)
expect('unreadCount' in dmStore).toBe(false)
```

- [ ] **Step 3：运行 Store 测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/stores/inbox.spec.ts tests/unit/stores/notification.spec.ts tests/unit/stores/dm.spec.ts
```

Expected: FAIL，旧 Inbox Store 不主动重连且 DM 未读仍是双状态源。

- [ ] **Step 4：给 Notification Store 增加唯一 DM 写入口**

```ts
function setDMUnread(count: number) {
  unreadCounts.value.dm = Math.max(0, Number.isFinite(count) ? Math.trunc(count) : 0)
}

function applyTotalUnread(items: Partial<Record<InboxTab, number>>) {
  unreadCounts.value = { ...emptyUnreadCounts(), ...unreadCounts.value, ...items }
}
```

`fetchUnreadCounts` 仍可覆盖所有分类；DM Store 的邮箱快照、消息事件和已读响应只调用 `setDMUnread`，不直接修改其他通知分类。

- [ ] **Step 5：重写连接生命周期**

```ts
const reconnectDelays = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000]
let socket: WebSocket | null = null
let reconnectTimer: number | null = null
let reconnectAttempt = 0
let connectionGeneration = 0
let manuallyDisconnected = false

function scheduleReconnect(generation: number) {
  if (manuallyDisconnected || reconnectTimer || generation !== connectionGeneration) return
  const delay = reconnectDelays[Math.min(reconnectAttempt, reconnectDelays.length - 1)]
  reconnectAttempt += 1
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = null
    void connect()
  }, delay)
}
```

`connect()` 只检查 `authStore.isAuthenticated`，不再要求旧 token；URL 使用 `useWebSocketUrl('/ws/user')`。`disconnect()` 增加 generation、清理重连/轮询并关闭当前 socket。

- [ ] **Step 6：实现事件路由与连接后对账**

```ts
socket.onmessage = (event) => {
  const payload = JSON.parse(event.data) as { event: string; data: unknown }
  if (payload.event === 'notification') {
    notificationStore.receiveNotification(payload.data as Notification)
    return
  }
  if (payload.event.startsWith('dm.')) {
    dmStore.receiveEvent(payload as DMRealtimeEvent)
  }
}

socket.onopen = async () => {
  if (generation !== connectionGeneration) return
  connected.value = true
  reconnectAttempt = 0
  stopPolling()
  await Promise.all([
    notificationStore.fetchUnreadCounts(),
    dmStore.reconcileFromServer(),
  ])
}
```

60 秒 fallback 同时刷新通知未读和 DM 邮箱；若当前打开会话，再刷新其最新页。对账以数据库快照覆盖会话摘要和未读，但保留尚未确认且 `client_message_id` 唯一的本地发送记录。

- [ ] **Step 7：运行 Store 测试和类型检查**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/stores/inbox.spec.ts tests/unit/stores/notification.spec.ts tests/unit/stores/dm.spec.ts
bun run type-check
```

Expected: PASS；fake timers 结束后不存在未清理定时器。

- [ ] **Step 8：提交 Frontend 实时链路**

```bash
cd /root/Atoman/Atoman-Frontend
git add src/stores/inbox.ts tests/unit/stores/inbox.spec.ts src/stores/notification.ts tests/unit/stores/notification.spec.ts src/stores/dm.ts tests/unit/stores/dm.spec.ts
git commit -m "feat(dm): reconnect and reconcile realtime inbox"
```

## Task 10：实现 DM 组件、桌面三栏和移动端两级 Inbox

**Files:**
- Create: `Atoman-Frontend/src/components/dm/DMMailboxSelector.vue`
- Create: `Atoman-Frontend/src/components/dm/DMConversationList.vue`
- Create: `Atoman-Frontend/src/components/dm/DMConversationPane.vue`
- Create: `Atoman-Frontend/src/components/dm/DMComposer.vue`
- Create: `Atoman-Frontend/src/components/dm/DMReportModal.vue`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMMailboxSelector.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMConversationList.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMConversationPane.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMComposer.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMReportModal.spec.ts`
- Modify: `Atoman-Frontend/src/views/feed/InboxPage.vue`
- Create: `Atoman-Frontend/tests/unit/views/feed/InboxPage.dm.spec.ts`

- [ ] **Step 1：写组件行为失败测试**

覆盖收件箱选择、会话排序、未读角标、空状态、频道回复提示、文本/单图互补、只读状态、上滚加载、滚动位置保持、举报原因和移动端返回。使用稳定选择器：

```ts
expect(wrapper.get('[data-testid="dm-mailbox-selector"]').text()).toContain('频道：低空飞行')
expect(wrapper.get('[data-testid="dm-reply-as"]').text()).toBe('将以低空飞行回复')
expect(wrapper.get('[data-testid="dm-composer"]').attributes('aria-disabled')).toBe('true')
expect(wrapper.emitted('load-older')).toHaveLength(1)
expect(wrapper.emitted('report')?.[0]).toEqual([{ messageId: 'message-1', reason: 'spam', detail: '' }])
```

- [ ] **Step 2：写桌面与移动页面失败测试**

桌面宽度断言分类、会话、详情同时存在；移动宽度初始只显示邮箱/会话，打开后只显示全屏会话，返回后恢复原收件箱、列表滚动位置和 URL query：

```ts
expect(wrapper.find('[data-testid="dm-conversation-list"]').isVisible()).toBe(true)
expect(wrapper.find('[data-testid="dm-conversation-pane"]').isVisible()).toBe(false)
await wrapper.get('[data-testid="dm-conversation-c1"]').trigger('click')
expect(router.currentRoute.value.query.conversation).toBe('c1')
expect(wrapper.find('[data-testid="dm-conversation-pane"]').isVisible()).toBe(true)
```

- [ ] **Step 3：运行组件测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/components/dm tests/unit/views/feed/InboxPage.dm.spec.ts
```

Expected: FAIL，组件文件和移动端状态尚不存在。

- [ ] **Step 4：实现收件箱和会话列表**

`DMMailboxSelector` 使用原生 select 或现有 `PSelect`，选项文案固定为“我的私信”及“频道：<名称>”。`DMConversationList` 只渲染重复会话项，不套第二层卡片；每项显示对方名称、摘要、时间和未读数。

组件契约：

```ts
defineProps<{
  mailboxes: DMMailbox[]
  activeMailboxKey: string
  conversations: DMConversation[]
  activeConversationId: string
  loading: boolean
  hasMore: boolean
}>()

defineEmits<{
  selectMailbox: [key: string]
  openConversation: [id: string]
  loadMore: []
}>()
```

- [ ] **Step 5：实现消息详情、向上分页与滚动稳定**

`DMConversationPane` 打开后滚到底部；向上加载前记录 `scrollHeight`，发出事件后通过消息数量 watcher 等待异步 prepend 完成，再恢复视觉位置：

```ts
let heightBeforePrepend: number | null = null

function requestOlder() {
  if (!scroller.value || heightBeforePrepend !== null) return
  heightBeforePrepend = scroller.value.scrollHeight
  emit('load-older')
}

watch(() => props.messages.length, async () => {
  if (!scroller.value || heightBeforePrepend === null) return
  const previousHeight = heightBeforePrepend
  heightBeforePrepend = null
  await nextTick()
  scroller.value.scrollTop += scroller.value.scrollHeight - previousHeight
})
```

只有对方消息显示举报菜单。拉黑后保留消息、举报和“取消拉黑”，隐藏发送动作；频道会话头显示“发给 <频道>”。

- [ ] **Step 6：实现 Composer 与举报 Modal**

Composer 使用 textarea、图片按钮和发送按钮；只允许一张图片，选择新图替换未发送图片。按钮使用现有图标库的 Image、Send、X 图标并带 tooltip/aria-label。频道回复只显示一行 `将以 <频道> 回复`，不提供身份选择器。

举报 Modal 只提供原因菜单、可选补充输入、“取消”和“提交举报”，提交中禁用重复操作。

- [ ] **Step 7：重构 InboxPage 的 DM 分支**

URL query 使用：

```text
/inbox?tab=dm&mailbox=user:<uuid>&conversation=<uuid>
/inbox?tab=dm&target_type=user&target_id=<uuid>
/inbox?tab=dm&target_type=channel&target_id=<uuid>
```

目标无既有会话时展示目标信息和空编辑器，首次发送成功后用 `router.replace` 切换为 conversation query。桌面 `min-width: 768px` 保持三栏；移动端一级为收件箱/会话列表，二级为会话详情。固定列表容器尺寸，加载文字、角标和图片预览不能推动列宽。

- [ ] **Step 8：接入拉黑、举报、图片和错误文案**

页面调用 Store 的 `blockActiveConversation`、`unblockActiveConversation`、`reportMessage`、`uploadImage`。稳定错误码映射为用户可行动文案：

```ts
const dmErrorMessages: Record<string, string> = {
  'dm.waiting_reply': '等待对方回复后可继续发送',
  'dm.blocked': '当前会话无法发送消息',
  'dm.permission_denied': '对方暂不接收私信',
  'dm.rate_limited': '发送较频繁，请稍后再试',
  'dm.image_invalid': '请选择 10 MB 以内的 JPEG、PNG 或 WebP 图片',
  'system.storage_unavailable': '图片暂不可用，请稍后再试',
}
```

界面不得显示 SQL、ActorUserID、对象 key、内部状态名或研发说明。

- [ ] **Step 9：运行组件测试和类型检查**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/components/dm tests/unit/views/feed/InboxPage.dm.spec.ts tests/unit/views/feed/InboxPage.notifications.spec.ts tests/unit/views/feed/InboxPage.forum-routing.spec.ts
bun run type-check
```

Expected: PASS；原通知和论坛分类行为无回归。

- [ ] **Step 10：提交 Frontend Inbox 界面**

```bash
cd /root/Atoman/Atoman-Frontend
git add src/components/dm/DMMailboxSelector.vue src/components/dm/DMConversationList.vue src/components/dm/DMConversationPane.vue src/components/dm/DMComposer.vue src/components/dm/DMReportModal.vue tests/unit/components/dm src/views/feed/InboxPage.vue tests/unit/views/feed/InboxPage.dm.spec.ts
git commit -m "feat(dm): build responsive dm inbox"
```

## Task 11：增加用户/频道入口、权限设置和管理员举报区

**Files:**
- Create: `Atoman-Frontend/src/components/dm/DMSettingsPanel.vue`
- Create: `Atoman-Frontend/src/components/dm/DMAdminReportsPanel.vue`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMSettingsPanel.spec.ts`
- Create: `Atoman-Frontend/tests/unit/components/dm/DMAdminReportsPanel.spec.ts`
- Modify: `Atoman-Frontend/src/views/blog/ProfileView.vue`
- Create: `Atoman-Frontend/tests/unit/views/blog/ProfileView.spec.ts`
- Modify: `Atoman-Frontend/src/views/blog/ChannelView.vue`
- Modify: `Atoman-Frontend/tests/unit/views/blog/ChannelView.spec.ts`
- Modify: `Atoman-Frontend/src/components/user/UserBlogSettingsPanel.vue`
- Modify: `Atoman-Frontend/src/views/blog/UserManagementView.vue`
- Modify: `Atoman-Frontend/src/views/studio/StudioSettingsView.vue`
- Modify: `Atoman-Frontend/tests/unit/views/studio/StudioSettingsView.spec.ts`
- Modify: `Atoman-Frontend/src/views/setting/SettingCommunityView.vue`
- Modify: `Atoman-Frontend/tests/unit/views/setting/SettingCommunityView.spec.ts`

- [ ] **Step 1：写入口可见性失败测试**

登录用户查看他人主页和非自有频道时显示“私信”；查看自己或自己的频道时不显示。点击只进入统一 Inbox：

```ts
expect(profileWrapper.get('[data-testid="message-user"]').attributes('href')).toBe('/inbox?tab=dm&target_type=user&target_id=user-2')
expect(channelWrapper.get('[data-testid="message-channel"]').attributes('href')).toBe('/inbox?tab=dm&target_type=channel&target_id=channel-2')
expect(ownProfileWrapper.find('[data-testid="message-user"]').exists()).toBe(false)
```

- [ ] **Step 2：写设置和举报管理失败测试**

个人设置只有三项且默认一条；频道设置只有 `one_before_reply/anyone/closed`；非所有者不能加载频道设置；管理员可以分页、处理完成或驳回，普通用户不渲染管理区。

- [ ] **Step 3：运行定向测试并确认红灯**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/components/dm/DMSettingsPanel.spec.ts tests/unit/components/dm/DMAdminReportsPanel.spec.ts tests/unit/views/blog/ProfileView.spec.ts tests/unit/views/blog/ChannelView.spec.ts tests/unit/views/studio/StudioSettingsView.spec.ts tests/unit/views/setting/SettingCommunityView.spec.ts
```

Expected: FAIL，入口和设置组件不存在。

- [ ] **Step 4：实现上下文明确的入口**

用户主页按钮使用 User UUID；频道页按钮使用 Channel UUID，不使用 username/slug 作为 API 主键。按钮使用现有 MessageCircle 图标和“私信”文案。未登录用户点击走现有认证导航，不能先创建空会话。

- [ ] **Step 5：实现复用设置组件**

```ts
const props = defineProps<{
  subject: { type: 'user'; id: string } | { type: 'channel'; id: string }
}>()

const options = computed(() => props.subject.type === 'user'
  ? [
      { value: 'one_before_reply', label: '陌生人仅可发一条' },
      { value: 'following_only', label: '仅我关注的人' },
      { value: 'anyone', label: '允许连续发送' },
    ]
  : [
      { value: 'one_before_reply', label: '陌生人仅可发一条' },
      { value: 'anyone', label: '允许连续发送' },
      { value: 'closed', label: '关闭频道私信' },
    ])
```

`UserBlogSettingsPanel` 改用用户 DM settings API；`UserManagementView` 删除重复的 `dm_permission` 表单和旧用户设置请求；`StudioSettingsView` 在当前频道设置内渲染同一组件。

- [ ] **Step 6：实现管理员举报区**

`DMAdminReportsPanel` 加入 `SettingCommunityView` 的管理员区域，行内显示原因、快照、双方 ID、频道上下文、时间和状态，只提供“处理完成”“驳回”两个动作。图片只显示“含图片”，不直接渲染对象 key或长期 URL。

- [ ] **Step 7：运行定向测试和类型检查**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/components/dm/DMSettingsPanel.spec.ts tests/unit/components/dm/DMAdminReportsPanel.spec.ts tests/unit/views/blog/ProfileView.spec.ts tests/unit/views/blog/ChannelView.spec.ts tests/unit/views/studio/StudioSettingsView.spec.ts tests/unit/views/setting/SettingCommunityView.spec.ts
bun run type-check
```

Expected: PASS；`rg -n 'dm_permission' src/views/blog/UserManagementView.vue src/components/user/UserBlogSettingsPanel.vue` 只允许新 DM Client 类型，不再出现旧用户设置 payload。

- [ ] **Step 8：提交 Frontend 入口与管理界面**

```bash
cd /root/Atoman/Atoman-Frontend
git add src/components/dm/DMSettingsPanel.vue src/components/dm/DMAdminReportsPanel.vue tests/unit/components/dm/DMSettingsPanel.spec.ts tests/unit/components/dm/DMAdminReportsPanel.spec.ts src/views/blog/ProfileView.vue tests/unit/views/blog/ProfileView.spec.ts src/views/blog/ChannelView.vue tests/unit/views/blog/ChannelView.spec.ts src/components/user/UserBlogSettingsPanel.vue src/views/blog/UserManagementView.vue src/views/studio/StudioSettingsView.vue tests/unit/views/studio/StudioSettingsView.spec.ts src/views/setting/SettingCommunityView.vue tests/unit/views/setting/SettingCommunityView.spec.ts
git commit -m "feat(dm): add messaging entry points and settings"
```

## Task 12：完成真实双用户 E2E、全量验证和发布硬切换检查

**Files:**
- Create: `Atoman-Frontend/tests/e2e/specs/dm.real.spec.ts`
- Modify: `Atoman-Backend/internal/modules/dm/http_test.go`
- Modify: `Atoman-Backend/internal/modules/dm/service_postgres_test.go`

- [ ] **Step 1：写受环境保护的真实 E2E**

测试仅在以下条件满足时运行：

```ts
const enabled = process.env.DM_REAL_E2E === '1'
test.skip(!enabled, 'requires DM_REAL_E2E=1 and local PostgreSQL/MinIO')

function requireLocalEnvironment() {
  const baseURL = new URL(process.env.PLAYWRIGHT_BASE_URL ?? '')
  if (!['localhost', '127.0.0.1', '0.0.0.0'].includes(baseURL.hostname)) {
    throw new Error(`拒绝对非本地地址执行 DM fixture：${baseURL.origin}`)
  }
  if (process.env.DM_E2E_LOCAL_DB_CLEANUP !== '1') {
    throw new Error('真实 DM 测试需要显式启用本地清理')
  }
}
```

fixture 创建 sender、recipient/channel owner、admin 和 recipient 拥有的频道；每个浏览器 context 通过 `/api/v1/auth/login` 获取独立 Cookie Session，结束时按外键顺序删除消息、举报、图片、会话、频道和临时用户。

- [ ] **Step 2：实现个人私信与实时插入场景**

场景顺序：sender 从他人主页进入 Inbox；发送首条文本；recipient 无刷新看到新会话和未读；recipient 打开并已读；sender 收到 read 事件；重复提交相同 client ID 后数据库只有一条消息。

```ts
await expect(recipientPage.getByTestId('dm-conversation-list')).toContainText(sender.username)
await recipientPage.getByText(sender.username, { exact: true }).click()
await expect(recipientPage.getByTestId('dm-message')).toContainText('真实个人私信')
await expect(recipientPage.getByTestId('dm-unread-count')).toHaveCount(0)
```

- [ ] **Step 3：实现频道自动身份、图片和分页场景**

sender 从频道页发信；owner 在频道邮箱打开会话，页面只显示“将以 <频道> 回复”；owner 回复后 sender 看到 sender.type 为 channel。上传一张内存 PNG 并发送，未登录 context 请求本地图片内容返回 401。通过 API 写入超过 30 条消息后，浏览器向上滚动能加载更早消息且当前位置不跳变。

- [ ] **Step 4：实现一条限制、拉黑和举报场景**

新目标第二条在对方回复前显示等待文案；回复后可继续。sender 拉黑频道会话后相关个人和频道会话都只读，取消拉黑恢复。sender 举报频道回复，admin 在社区设置中处理完成，数据库快照、ReportedActorUserID 和审核人正确。

- [ ] **Step 5：先运行真实 E2E 并修正发现的问题**

先启动开发依赖、Backend 和 Frontend，再运行：

```bash
cd /root/Atoman/Atoman-Frontend
DM_REAL_E2E=1 \
DM_E2E_LOCAL_DB_CLEANUP=1 \
PLAYWRIGHT_BASE_URL=http://localhost:5173 \
bun run test:e2e -- tests/e2e/specs/dm.real.spec.ts
```

Expected: PASS，测试结束后临时 DB 行和私有图片存储测试对象（本地目录或 MinIO Bucket）均为 0。

- [ ] **Step 6：运行 Backend 全量验证**

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/dm ./internal/modules/notification ./internal/app ./internal/migrations ./cmd/migrate ./cmd/migrate_dm_images -count=1
go test ./...
go build ./...
```

Expected: 全部 PASS，build 退出码 0。

- [ ] **Step 7：运行 Frontend 全量验证**

```bash
cd /root/Atoman/Atoman-Frontend
bun run type-check
bun run test:unit
bun run build
```

Expected: 全部 PASS，无 TypeScript 错误或构建警告升级为错误。

- [ ] **Step 8：执行硬切换静态检查**

```bash
cd /root/Atoman
rg -n 'SetupDMRoutes|dmHandler|/dm/unread-count|/dm/upload|/dm/conversations/:username|other_username|DMRealtimePayload' Atoman-Backend Atoman-Frontend
rg -n 'actor_user_id|ActorUserID' Atoman-Backend/internal/modules/dm/http.go Atoman-Backend/internal/modules/dm/dto.go Atoman-Frontend/src
rg -n 'S3_BUCKET' Atoman-Backend/internal/modules/dm Atoman-Backend/cmd/migrate_dm_images
```

Expected: 第一条无匹配；第二条只在 Backend 内部管理员 DTO/测试出现，Frontend 无匹配；第三条 DM 运行时代码只读取 `DM_S3_BUCKET`，不读取公开 Bucket。

- [ ] **Step 9：提交真实 E2E 和最终修正**

先提交 Backend 中仅属于 DM 的修正：

```bash
cd /root/Atoman/Atoman-Backend
git add internal/modules/dm/http_test.go internal/modules/dm/service_postgres_test.go
git commit -m "test(dm): verify dm v2 end to end"
```

若 Backend 没有新改动则不创建空提交。再提交 Frontend：

```bash
cd /root/Atoman/Atoman-Frontend
git add tests/e2e/specs/dm.real.spec.ts
git commit -m "test(dm): cover real messaging workflow"
```

## 最终自检

- [ ] 旧用户私信文本与图片均完成迁移；存在不支持的旧图片 URL 时发布会中止，不会静默丢失。
- [ ] 仅允许 `user <-> user`、`user <-> channel`，频道不能主动发起，频道所有者回复自动显示为频道。
- [ ] `SenderType/SenderID` 与 `ActorUserID` 同时保存，普通用户 DTO 不暴露真实频道操作人。
- [ ] 默认权限、following、closed、一条限制、拉黑、10 新目标/小时、30 消息/分钟和幂等均有测试。
- [ ] 文本、单张私有图片、首屏最新消息、向上分页、已读和统一未读均有 Backend 与 Frontend 测试。
- [ ] WebSocket 断线主动重连，连接成功执行 REST 对账，新会话能实时插入列表。
- [ ] 桌面三栏、移动端两级、个人/频道入口、权限设置、举报和管理员处理均可用。
- [ ] 旧 Handler、旧路由、旧前端 endpoint、旧 DTO 和重复私信设置调用全部删除。
- [ ] Backend `go test ./...`、`go build ./...`，Frontend `type-check`、unit、build 和真实 DM Playwright 全部通过。
