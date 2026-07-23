# 私信模块重构设计

## 状态

- 日期：2026-07-23
- 范围：Backend 与 Web Frontend
- 目标：可上线的 Web 私信 MVP
- 决策：重写为独立 DM 模块，不兼容旧 HTTP 接口；迁移并保留现有私信数据

## 当前进度与问题

现有后端已经实现用户间会话、文本与图片消息、已读、未读计数、私信权限、双向拉黑和 UserHub WebSocket 推送。定向的 Handler、通知和 WebSocket 鉴权测试可以通过。

现有 Web 已有 DM Store、统一 Inbox、图片上传、权限设置和实时消息入口，但仍不满足上线要求：

- Cookie Session 迁移后，DM 单测仍依赖旧 localStorage token，定向测试存在失败。
- 首条实时消息不会把新会话加入列表。
- DM Store 与统一通知未读数不是同一状态源，已读和实时消息会造成角标延迟。
- WebSocket 断开后只退化为轮询，不主动重连和补拉消息。
- 消息分页从最早一页开始，前端没有加载更早消息。
- 移动端把会话列表和详情纵向堆叠。
- 没有跨 Backend、PostgreSQL 和浏览器的真实私信 E2E。
- 图片消息可接受任意 URL，不符合私信资源的隐私边界。

当前工作区没有 iOS 工程，因此本设计不包含 iOS。

## 目标

1. 用户可以主动向用户或频道发起一对一会话。
2. 频道只能回复用户先发来的频道私信，不能主动创建新会话。
3. 频道所有者在频道会话内自动以频道身份回复，不出现身份选择器。
4. 系统记录实际操作用户，确保权限、举报和审计可追溯。
5. 支持文本与单张私有图片、稳定分页、实时更新、断线对账和统一未读数。
6. 支持拉黑、单条消息举报和最小管理处理流程。
7. 迁移现有用户私信历史，移除旧 Handler、旧路由和旧前端调用。

## 非目标

- 群聊、群发或频道间私信
- 频道主动向用户创建新会话
- 频道成员、客服分配或多人共享处理
- 撤回、编辑、删除、输入状态和已读回执展示
- 多图、通用文件附件、Web Push 和邮件提醒
- 消息队列、outbox 或投递 ACK
- iOS 客户端

## 核心角色

### 用户

用户是认证主体、实际操作人和个人收件箱所有者。拉黑、限流、举报和审计最终都绑定真实用户。

### 频道

频道是可联系的消息身份和独立收件箱，不是登录账号。当前频道只有一个 `UserID` 所有者，只有该所有者可以读取频道收件箱并代表频道回复。

### 会话

会话由两个带类型的参与方构成，只允许：

```text
user:<id> <-> user:<id>
user:<id> <-> channel:<id>
```

禁止 `channel <-> channel`。用户不能给自己或自己拥有的频道发私信。

用户间会话按 UUID 规范化排序。用户与频道会话固定用户为 A、频道为 B。同一组用户可以同时存在个人会话和不同频道会话，互不合并。

## 数据模型

### DMConversation

在现有参与者 UUID 上增加类型，不建立通用多参与者表：

```go
type DMConversation struct {
    model.Base
    ParticipantAType string
    ParticipantA     uuid.UUID
    ParticipantBType string
    ParticipantB     uuid.UUID

    LastMessageAt      *time.Time
    LastMessagePreview string
}
```

数据库约束：

- 参与方类型只能是 `user` 或 `channel`。
- A 必须是 `user`。
- B 可以是 `user` 或 `channel`。
- `(participant_a_type, participant_a_id, participant_b_type, participant_b_id)` 唯一。
- 软删除记录不参与活动会话唯一性。

### DMMessage

消息分离显示身份与实际操作人：

```go
type DMMessage struct {
    model.Base
    ConversationID uuid.UUID

    SenderType string
    SenderID   uuid.UUID
    ActorUserID uuid.UUID

    ClientMessageID uuid.UUID
    Content         string
    ImageID         *uuid.UUID
    ReadAt          *time.Time
}
```

规则：

- `SenderType` 和 `SenderID` 必须匹配会话一方。
- 个人发送时 `SenderType=user` 且 `ActorUserID=SenderID`。
- 频道回复时 `SenderType=channel`，`ActorUserID` 必须是当前频道所有者。
- 客户端不提交 `SenderType` 或 `SenderID`。
- `(actor_user_id, client_message_id)` 唯一，用于发送幂等。
- `ActorUserID` 不返回给普通收件人，只用于内部权限、举报与审计。

### DMImage

图片使用独立所有权记录，不接受任意外部 URL：

```go
type DMImage struct {
    model.Base
    UploadedByUserID uuid.UUID
    ObjectKey        string
    ContentType      string
    SizeBytes        int64
}
```

`DMMessage.ImageID` 是唯一关联方向，并建立非空唯一索引。发送消息时锁定图片记录，验证图片由当前操作用户上传且没有被其他消息引用。消息响应将 `ImageID` 转换为短期签名 URL。

### DMMessageReport

```go
type DMMessageReport struct {
    model.Base
    MessageID          uuid.UUID
    ReporterUserID     uuid.UUID
    ReportedActorUserID uuid.UUID
    Reason             string
    Detail             string
    SnapshotContent    string
    SnapshotImageKey   string
    Status             string
    ReviewedByUserID   *uuid.UUID
    ReviewedAt         *time.Time
}
```

同一用户只能举报同一消息一次。举报人必须能访问会话，并且只能举报对方消息。

## 权限模型

个人私信权限：

- `one_before_reply`：默认值；收件人回复前，陌生发送者只能发一条。
- `following_only`：只有收件人关注的用户可发起。
- `anyone`：可以连续发送。

频道私信权限：

- `one_before_reply`：默认值。
- `anyone`：可以连续发送。
- `closed`：关闭频道私信。

个人权限归 DM 模块的 `/dm/settings` 管理；频道权限归 `/dm/channels/:id/settings` 管理。现有用户设置中的明确值保留，空值使用 `one_before_reply`。新频道默认 `one_before_reply`。

拉黑继续绑定真实用户。发送者与频道所有者任一方拉黑对方后，他们的个人会话和所有相关频道会话均变为只读；历史仍可查看、举报和用于取消拉黑。

## API

所有接口使用 Cookie Session 和 CSRF，返回公开 DTO，不直接序列化 GORM Model。

### 收件箱与会话

```text
GET /api/v1/dm/mailboxes
GET /api/v1/dm/mailboxes/:type/:id/conversations?cursor=<cursor>&limit=30
GET /api/v1/dm/targets/:type/:id/conversation
```

`/mailboxes` 返回个人收件箱和当前用户拥有的频道收件箱，以及各自未读数。目标会话查询无结果时返回 `204`，不创建空会话。

### 发送与读取

```text
POST /api/v1/dm/targets/:type/:id/messages
POST /api/v1/dm/conversations/:id/messages
GET  /api/v1/dm/conversations/:id/messages?before=<cursor>&limit=30
PUT  /api/v1/dm/conversations/:id/read
```

首次发送通过目标接口在同一事务内创建或查找会话并写入消息。已有会话发送由服务端根据当前用户权限自动确定用户或频道发送身份。

消息首屏返回最新 30 条并按时间正序排列。向上滚动使用由 `created_at + id` 生成的不透明游标加载更早消息。

已读响应返回会话未读、当前收件箱未读和全站总未读，前端立即更新全部角标。

### 图片、举报与设置

```text
POST /api/v1/dm/images
POST /api/v1/dm/messages/:id/reports
GET  /api/v1/dm/settings
PUT  /api/v1/dm/settings
GET  /api/v1/dm/channels/:id/settings
PUT  /api/v1/dm/channels/:id/settings
```

管理员提供最小举报处理接口：

```text
GET /api/v1/admin/dm/reports
PUT /api/v1/admin/dm/reports/:id
```

管理员可以查看必要快照并将状态更新为 `resolved` 或 `dismissed`。

## 实时数据流

继续复用 `/ws/user`，不引入队列。提交顺序是：数据库事务成功、构造公开 DTO、向相关用户尽力推送。

事件名称：

```text
dm.message.created
dm.message.read
dm.mailbox.updated
```

`dm.message.created` 携带完整消息 DTO、会话摘要、所属收件箱以及最新收件箱未读和总未读。新会话因此可以实时插入列表。

客户端按消息 ID 和 `client_message_id` 去重。WebSocket 使用指数退避重连；每次连接成功重新获取收件箱、会话列表和当前会话最新页，以数据库状态覆盖本地推测。WebSocket 只负责降低延迟，不承担可靠存储。

## Web 交互

统一 `/inbox` 继续承载通知与私信。私信分类内部增加收件箱选择器：

```text
我的私信
频道：低空飞行
频道：原子乐队
```

桌面端保持分类、会话列表、消息详情三栏。进入频道收件箱后，会话头部显示“发给某频道”，编辑器只显示“将以某频道回复”，不显示身份选择器。

移动端使用两级页面：收件箱与会话列表为一级，全屏会话为二级。返回时保留收件箱、会话列表和滚动位置。打开会话后滚动到最新消息，向上滚动加载更早消息。

拉黑后编辑器变为只读，历史、举报和取消拉黑仍可用。单条对方消息提供举报入口。

管理员增加最小 DM 举报页，显示举报原因、必要消息快照、双方 ID 和频道上下文，只提供“处理完成”和“驳回”操作。

## 安全与限制

- 文本最多 4,000 个 Unicode 字符。
- 文本和图片不能同时为空。
- 每条消息最多一张图片，最大 10 MB，只允许 JPEG、PNG、WebP，并校验实际内容。
- DM 图片使用独立私有 Bucket；生产返回短期签名 URL，本地开发通过鉴权接口读取。
- 每个用户每小时最多联系 10 个新目标。
- 每个用户每分钟最多发送 30 条消息。
- 日志仅记录用户、频道、会话和消息 ID，不记录正文和图片 URL。

稳定错误码：

```text
dm.target_not_found
dm.self_target
dm.permission_denied
dm.waiting_reply
dm.blocked
dm.conversation_forbidden
dm.image_invalid
dm.rate_limited
dm.message_not_found
dm.already_reported
```

前端错误只说明用户可以采取的修正动作，不展示数据库或权限内部原因。

## 迁移与发布

旧 HTTP 接口和旧 Handler 不提供兼容层。数据迁移采用扩展、迁移、切换顺序：

1. 备份 PostgreSQL。
2. 增加参与者类型、发送者类型、操作人、幂等、图片和举报结构。
3. 将现有会话标记为 `user <-> user`。
4. 将现有消息标记为 `SenderType=user`，将 `ActorUserID` 填为原 `SenderID`，并使用原消息 ID 填充 `ClientMessageID`。
5. 构建 Backend 与 Frontend 产物。
6. 在同一发布窗口部署新 Backend 与 Frontend，刷新 Cloudflare Pages 缓存。
7. 验证个人会话、频道会话、图片、已读、WebSocket 和举报。
8. 删除旧 Handler、旧路由、旧前端调用、旧测试和失效 API 文档。

现有 UUID 数据列在首次切换时不删除，新 Model 直接复用，降低数据回滚风险；运行时代码只使用新 DM 模块。

## 测试策略

### Backend

- Service 测试：用户与频道身份、权限、拉黑、首条限制、图片所有权和举报。
- HTTP 契约测试：鉴权、DTO、游标、稳定错误码和管理员处理。
- 真实 PostgreSQL 测试：并发会话创建、幂等发送、游标稳定性、唯一索引和旧数据迁移。
- Realtime 测试：双方事件、频道所有者事件、新会话摘要和未读数。
- 存储测试：私有对象、文件类型、大小和越权读取。

### Frontend

- dmClient：Cookie Session、CSRF、请求与 DTO 映射。
- DM Store：新会话实时插入、事件去重、统一未读、断线重连、对账和过期请求隔离。
- 组件：收件箱切换、桌面三栏、移动端两级导航、自动频道身份、加载更早、拉黑、举报和管理员处理。
- Playwright 真实链路：个人私信、频道私信与自动频道回复、图片、已读、拉黑和举报。
- 完成前运行 `bun run type-check`。

### Backend 验证

完成前运行：

```bash
go test ./internal/modules/dm/... ./internal/migrations/... ./internal/collab/...
go build ./...
```

## 上线验收标准

1. 用户可以从用户主页和频道页进入正确目标会话。
2. 频道所有者无需选择身份即可自动以频道回复。
3. 旧个人私信历史完整迁移且顺序正确。
4. 首屏显示最新消息，向上分页无重复或遗漏。
5. REST 与 WebSocket 重复到达不会产生重复消息。
6. 新会话、已读和重连后，列表与所有未读角标一致。
7. 拉黑立即禁止双方在全部相关会话继续发送，历史仍可查看。
8. 私有图片无法被非会话参与者读取。
9. 举报进入管理员列表并可完成处理。
10. Backend 构建、定向测试、Frontend 类型检查和真实 Playwright 链路全部通过。

## 观测

记录发送延迟与失败率、权限拒绝、限流、WebSocket 在线数与重连次数、图片上传失败、举报量和迁移统计。所有观测数据禁止包含私信正文。
