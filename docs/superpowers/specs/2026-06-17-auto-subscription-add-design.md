# 自动检测来源添加订阅设计

Date: 2026-06-17
Scope: Feed 添加订阅入口、后端来源预检与统一添加接口、前端单输入框自动检测体验

## 背景

Feed 现在已经有三种添加订阅能力：直接 RSS URL、普通网站发现 feed 候选、RSSHub provider 创建来源。前端 `SubscriptionAddSheet` 通过“发现 / RSS / RSSHub”三个模式暴露这些能力，用户需要先理解来源类型再选择路径。

本次目标是把三种方式合并成一个添加入口：用户输入 URL 或平台链接后，系统先自动检测来源，像用户名唯一性校验一样在添加前展示状态；用户确认后再完成订阅创建。

## 目标

1. 前端只保留一个主输入框，不再要求用户选择“发现 / RSS / RSSHub”模式。
2. 后端提供统一的预检接口，能判断输入是否已订阅、数据库是否已有来源、是否可新增、是否需要候选选择。
3. 后端提供统一的添加接口，最终创建或复用来源并建立当前用户订阅关系。
4. 添加前能提示“已订阅”“来源已存在，可直接添加”“可添加新来源”“请选择候选”“无法识别”。
5. 先覆盖当前已有能力：RSS/Atom URL、普通网站 feed discovery、GitHub 仓库链接到 RSSHub `github/repo`。

## 非目标

1. 不迁移或删除现有 `/api/v1/feed/subscriptions`、`/api/v1/feed/discover`、`/api/v1/feed/sources/create-from-provider`。
2. 不扩大 RSSHub 模板覆盖面；第一阶段只保留 GitHub 仓库。
3. 不做复杂智能归一或跨站点相似来源合并。
4. 不重构 feed legacy compat 与新 subscription module 的边界。

## 后端设计

在现有 feed 模块新增两个受登录保护的接口。

### POST `/api/v1/feed/subscriptions/resolve`

该接口只检测，不创建来源和订阅。

请求体：

```json
{
  "input": "https://github.com/DIYgod/RSSHub"
}
```

响应体：

```json
{
  "status": "existing_source",
  "source": {
    "provider": "rsshub",
    "source_type": "external_rss",
    "title": "RSSHub",
    "rss_url": "https://rsshub.app/github/repo/DIYgod/RSSHub",
    "site_url": "https://github.com/DIYgod/RSSHub",
    "canonical_url": "https://rsshub.app/github/repo/DIYgod/RSSHub"
  },
  "subscription": null,
  "candidates": [],
  "message": "来源已存在，可添加到你的订阅"
}
```

`status` 枚举：

- `already_subscribed`: 当前用户已经订阅该来源。前端禁用添加按钮。
- `existing_source`: 数据库已有来源，但当前用户未订阅。前端提示可直接添加。
- `new_source`: 输入能解析为确定来源，但数据库暂无来源。前端提示将添加新来源。
- `multiple_candidates`: 普通网站发现到多个候选。前端要求用户选择一个候选。
- `not_found`: 输入有效，但未找到可订阅来源。
- `invalid`: 输入为空或不是合法 http/https URL。

检测顺序：

1. 清理输入空白，校验必须是 http/https URL。
2. 如果匹配 `https://github.com/{owner}/{repo}`，构造 RSSHub provider 目标：
   - `provider = rsshub`
   - `template_key = github/repo`
   - `params.owner = owner`
   - `params.repo = repo`
   - `rss_url = service.BuildRSSHubFeedURL(...)`
   - `site_url = 原 GitHub 仓库 URL`
3. 如果输入看起来是直接 RSS/Atom/feed URL，作为 `external_rss` 目标。
4. 对确定目标使用现有 canonical URL 归一逻辑查询 `feed_sources`。
5. 如果来源存在，再查询当前用户是否已订阅。
6. 如果不是确定目标，把输入当普通网站 URL 调用现有 discovery：
   - 没有候选返回 `not_found`。
   - 一个候选返回该候选对应的 `existing_source` 或 `new_source`。
   - 多个候选返回 `multiple_candidates`，候选中包含 feed URL、标题、kind、默认标记，以及每个候选的数据库/订阅状态。

### POST `/api/v1/feed/subscriptions/auto-add`

该接口创建或复用来源，并为当前用户创建订阅关系。它必须重新解析和校验输入，不信任前端的 resolve 结果。

请求体：

```json
{
  "input": "https://example.com",
  "candidate_feed_url": "https://example.com/feed.xml",
  "title": "可选标题",
  "group_id": "可选分组"
}
```

规则：

1. 如果传入 `candidate_feed_url`，优先把它作为最终 feed URL，但仍校验其必须是 http/https URL。
2. 如果没有候选，按 resolve 的同一检测顺序得到确定目标。
3. 如果 resolve 结果是 `multiple_candidates` 且没有 `candidate_feed_url`，返回 `400`，提示需要选择候选。
4. 如果当前用户已订阅，返回 `409 subscription.already_exists`。
5. 如果来源已存在，复用 `feed_sources`；否则创建来源。
6. 创建订阅后，如果传入有效 `group_id`，把订阅移动到指定分组；移动失败时返回明确错误。
7. 响应沿用现有订阅创建响应结构，返回 `data: subscription`。

## 前端设计

`SubscriptionAddSheet.vue` 从三模式表单改为单输入表单。

保留字段：

- 来源输入：必填，接受 RSS URL、普通网站 URL、GitHub 仓库 URL。
- 自定义名称：可选，只在最终添加时提交。
- 分组：可选，只在最终添加时提交。

移除显式模式切换：

- 不再展示“发现 / RSS / RSSHub”三个 `PClip`。
- 不再让用户填写 RSSHub owner/repo 字段。

自动检测流程：

1. 用户输入停止一小段时间后触发 resolve。建议 500ms debounce。
2. 输入为空时不请求。
3. 每次输入变化清理旧候选和旧错误，并取消过期结果的展示。
4. resolve 期间显示轻量“检测中”状态。
5. 根据 resolve 状态更新 UI：
   - `already_subscribed`: 显示“你已订阅此来源”，确认按钮禁用。
   - `existing_source`: 显示“来源已存在，可添加到你的订阅”，确认按钮可用。
   - `new_source`: 显示将添加的标题和 URL，确认按钮可用。
   - `multiple_candidates`: 展示候选列表，用户选择后确认按钮可用。
   - `not_found` / `invalid`: 显示错误，确认按钮禁用。
6. 点击确认调用 store 的统一 `autoAddSubscription` action。
7. 成功后复用现有行为：关闭抽屉、重置表单、刷新订阅列表与时间线、完成 onboarding 订阅步骤。

## Store 设计

`src/stores/feed.ts` 增加：

- `resolveSubscriptionInput(input: string): Promise<ResolvedSubscriptionInput | null>`
- `autoAddSubscription(payload): Promise<boolean>`

`SubscriptionAddSheet` 只 emit 一个提交事件，例如：

```ts
{
  input: string
  candidate_feed_url?: string
  title?: string
  group_id?: string
}
```

`FeedView.vue` 和 `OrbitView.vue` 改为使用统一提交处理函数，不再区分 RSS、discovered、provider。

## 错误处理

1. resolve 的网络错误只显示“检测失败，请稍后重试”，不创建订阅。
2. auto-add 返回已订阅时，前端显示“你已订阅此来源”并刷新订阅列表。
3. auto-add 返回需要候选时，前端保留抽屉并提示选择候选。
4. 分组移动失败时，沿用现有文案“订阅已添加，但移动分组失败”。

## 测试计划

后端单元测试：

1. resolve GitHub 仓库 URL 返回 RSSHub 目标。
2. resolve 已订阅来源返回 `already_subscribed`。
3. resolve 数据库已有但当前用户未订阅返回 `existing_source`。
4. resolve 直接 RSS URL 返回 `new_source` 或 `existing_source`。
5. resolve 普通网站多个候选返回 `multiple_candidates`。
6. auto-add GitHub 仓库 URL 创建 RSSHub 来源和订阅。
7. auto-add 数据库已有来源时复用来源。
8. auto-add 已订阅来源返回 conflict。
9. auto-add 多候选但未传 candidate 时返回 bad request。
10. auto-add 带 candidate_feed_url 时重新校验候选 URL。

前端单元测试：

1. 输入后自动调用 resolve，并显示检测状态结果。
2. `already_subscribed` 禁用确认按钮。
3. `existing_source` 显示可直接添加并允许提交。
4. `multiple_candidates` 要求选择候选后才允许提交。
5. 确认提交调用 `autoAddSubscription`，不再调用旧的 RSS/discovered/provider 提交事件。
6. 成功后表单重置并关闭抽屉。

## 兼容性

旧接口继续存在，现有管理页、OPML、历史入口不受影响。新前端添加入口只使用 resolve 和 auto-add。后续如果迁移到新 subscription module，可以保持这两个 API 合约不变，只替换内部实现。
