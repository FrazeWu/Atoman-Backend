# Unified Comment Core Design

Date: 2026-07-14
Status: awaiting user specification review
Scope: Atoman Backend and Web

## 1. Goal

Build one comment core for every Atoman content module. The core owns threading, mentions, rendering, attachments, likes, reports, notifications, sorting, counters, and moderation. Modules add domain behavior through target adapters and typed extension tables.

The same core appears differently by module:

- Blog, video, podcast, and RSS use comment language.
- Artist, album, and song pages use discussion language.
- Forum topics call the unique marked root comment the best answer.
- Debate topics add argument metadata and keep their structured presentation.
- Timeline events and people use root comments as Wiki revision proposals and child comments as proposal discussion.

## 2. Non-goals

- Do not add comment reading or writing to iOS in this project.
- Do not preserve old comment, discussion, forum reply, or argument APIs.
- Do not migrate old interaction rows because the current database is empty.
- Do not support anonymous comments.
- Do not add a generic author-controlled close-comments setting. Domain lifecycle rules, such as a concluded debate rejecting new arguments, remain module policy.
- Do not build arbitrary rich text, headings, lists, tables, fenced code blocks, raw HTML, or Markdown images.
- Do not implement infinitely nested replies.

The existing iOS blog publishing request may continue sending `allow_comments`; Backend ignores the deprecated field so publishing does not fail. Discussion is always open for visible content. Removing the iOS toggle is deferred.

## 3. Confirmed Product Rules

### 3.1 Threading

- A discussion consists of root comments (floors) and child comments (replies inside a floor).
- A child comment stores both `root_id` and `reply_to_id`.
- Replying to another child keeps the same `root_id` and changes `reply_to_id` to the selected child.
- The UI never adds indentation beyond root plus one child level.
- Root floors receive immutable, monotonically increasing floor numbers.
- Root comments default to oldest-first order, 20 roots per page.
- Users can switch to newest and hottest sorting.
- Each root initially shows three child comments; the rest load on expansion.
- Public counts include roots and children. Floor numbers count roots only.
- `active` and `auto_folded` comments count publicly; `moderator_hidden` comments do not.
- A marked root appears first on every sort without changing its immutable floor number; remaining roots follow the selected sort.

### 3.2 Content and Mentions

- A comment contains at most 2,000 Unicode code points after NFC normalization.
- A comment may contain text, one to four images, or both. Empty text is valid only when at least one image exists.
- Comment images reuse the current media upload policy: verified JPEG, PNG, GIF, or WebP, at most 10 MB per asset.
- Allowed Markdown is bold, italic, inline code, links, blockquotes, and line breaks.
- Lists, headings, tables, and fenced-code markers render as escaped plain text. Raw HTML and Markdown image syntax are rejected.
- Replying automatically targets the selected author.
- The composer supports searching registered active users and inserting structured mentions.
- Mentions reference immutable user IDs; display names are presentation data.
- Mention ranges use zero-based Unicode code-point offsets, not UTF-8 bytes or JavaScript UTF-16 indexes.
- One action that is both a reply and an explicit mention produces one notification for that recipient.

### 3.3 Media Time Anchors

- There is no separate timestamp-comment type.
- Video, podcast episode, and song comments automatically detect every valid `M:SS` or `H:MM:SS` token.
- The composer offers an Insert current time action that only inserts formatted text.
- The server stores all valid anchors with their text spans and second offsets.
- A token beyond known media duration remains ordinary text.
- Clicking a valid anchor seeks the active player.
- `Song` gains `duration_sec` so server validation matches video and podcast behavior.

### 3.4 Editing and Deletion

- Authors can edit their own comments.
- Edited comments display the exact edit timestamp.
- Deleting a root comment deletes the complete floor and all dependent relations in one transaction.
- Deleting a child comment removes only that child and does not leave a placeholder.
- Deletion also removes notifications whose source is a deleted comment, preventing dead links.
- Content authors can delete comments under their content.
- Administrators can hide or delete comments across the site.
- Moderator actions remain auditable.

### 3.5 Likes, Ranking, and Unique Marking

- Roots and children can be liked.
- A user can like a comment at most once.
- Hottest sorting ranks roots using root likes, child likes, reply activity, and time decay.
- The initial score is `(root likes * 3 + child likes + active child count * 2) / (age in hours + 2)^1.2`; a child like or reply recomputes its root score.
- Each target can mark at most one active root comment.
- For ordinary content, only the content author can mark it and the UI calls it pinned.
- For forum topics, only the topic author can mark it and the UI calls it best answer.
- Targets without a local content author, including external RSS items and community-owned music records, do not expose marking.
- Administrators moderate content but do not choose pinned comments or best answers for authors.

### 3.6 Reports and Automatic Folding

- A user can report a comment once and selects a reason.
- The fourth report from a distinct user changes the comment to `auto_folded`.
- Folded content keeps a visible placeholder and can be expanded by readers.
- Moderators can restore or delete folded content.
- Pending and upheld reports count toward folding. Rejected reports do not. Moderation restores an automatically folded comment when fewer than four counted reports remain.

### 3.7 Notifications

- Replies, mentions, and unique marking create immediate notifications.
- Likes update an aggregated unread notification instead of creating one row per like.
- Users never receive notifications for their own actions.
- Recipients are deduplicated across reply and mention causes.
- Editing does not repeat reply notifications. A newly added mention not previously notified for that comment produces one mention notification.

### 3.8 Abuse Limits

- Only authenticated, active users can publish, reply, like, report, or mark.
- A user can create at most five comments per rolling minute.
- The same user cannot send an identical normalized body plus identical image set to the same target within five minutes.

## 4. Architecture

### 4.1 Package Boundary

Create `internal/modules/comment` with focused units:

- `http.go`: unified HTTP routes and request decoding.
- `service.go`: product rules and transactions.
- `repo.go`: comment-core persistence only.
- `dto.go`: stable request and response types.
- `markdown.go`: Markdown allowlist parsing and safe rendering.
- `mention.go`: mention validation and recipient extraction.
- `time_anchor.go`: media token detection and duration validation.
- `ranking.go`: deterministic hot score calculation.
- `notification.go`: recipient deduplication and like aggregation.
- `target.go`: target adapter contract and registry.

The core imports no blog, video, music, forum, debate, or timeline service. Each module registers an adapter that resolves its own model.

### 4.2 Target Adapter Contract

Given the current user and a public `{kind, resource_id}`, an adapter returns:

- a stable internal target key;
- whether the resource exists and is visible to the viewer;
- the content author's user ID;
- optional media duration;
- the module presentation policy;
- optional hooks for typed extension validation and post-create actions.

The core uses the result to authorize reads and writes. It does not duplicate module visibility rules.

Initial target kinds are `blog_post`, `video`, `podcast_episode`, `feed_article`, `music_artist`, `music_album`, `music_song`, `forum_topic`, `debate`, `timeline_event`, and `timeline_person`. The registry rejects unknown kinds.

RSS accepts a `FeedItem.id`, normalizes the item's original URL, and uses that normalized URL as the stable target key. Items from mirror feeds therefore share one discussion target.

## 5. Data Model

### 5.1 Core Tables

`discussion_targets`

- `id`, `kind`, `resource_key`, `owner_id`
- `comment_count`, `root_count`, `next_floor`
- `pinned_comment_id`, timestamps
- unique `(kind, resource_key)`

`comments`

- `id`, `target_id`, `author_id`
- nullable `root_id`, nullable `reply_to_id`
- nullable `floor_number`
- `content`, `status`, `edited_at`
- cached `like_count`, `reply_count`, `report_count`, `hot_score`
- timestamps; user deletion physically removes rows, while moderator hide/delete actions write the existing audit log

Root comments have no `root_id` or `reply_to_id` and have a floor number. Child comments have both relation fields and no floor number. Service validation guarantees that target, root, and reply target all belong to the same floor.

`comment_mentions`

- `comment_id`, `user_id`, zero-based Unicode code-point start/end positions
- unique occurrence identity; one user may appear more than once but receives one notification

`comment_attachments`

- `comment_id`, `media_asset_id`, `position`
- width, height, MIME type, and storage metadata come from the existing media asset

`comment_likes`

- unique `(comment_id, user_id)`

`comment_reports`

- unique `(comment_id, reporter_id)`
- reason, moderation status, reviewer, review timestamp

`comment_time_anchors`

- `comment_id`, zero-based Unicode code-point start/end positions, `seconds`

### 5.2 Typed Extensions

`timeline_revision_proposals`

- one-to-one `comment_id`
- target kind and target entity ID
- structured before/after field changes, source/evidence, status, reviewer, applied revision ID

`debate_argument_details`

- one-to-one `comment_id`
- argument type, source URL, source title, source excerpt, conclusion fields

`debate_argument_references` and `debate_argument_debate_refs`

- preserve argument-to-argument and argument-to-debate references using core comment IDs
- debate votes reference the core comment ID that owns `debate_argument_details`

Forum best-answer state and ordinary pin state reuse `discussion_targets.pinned_comment_id`; the adapter supplies the label and owner rule. Music needs no comment extension. Media time anchors are a shared capability rather than separate video, podcast, and song tables.

## 6. API

All modules use the same public core routes:

```text
GET    /api/v1/discussions/{kind}/{resource_id}/comments
POST   /api/v1/discussions/{kind}/{resource_id}/comments
GET    /api/v1/comments/{root_comment_id}/replies
PATCH  /api/v1/comments/{comment_id}
DELETE /api/v1/comments/{comment_id}
PUT    /api/v1/comments/{comment_id}/like
DELETE /api/v1/comments/{comment_id}/like
PUT    /api/v1/comments/{comment_id}/report
PUT    /api/v1/discussions/{kind}/{resource_id}/pinned-comment
DELETE /api/v1/discussions/{kind}/{resource_id}/pinned-comment
GET    /api/v1/admin/comment-reports
PUT    /api/v1/admin/comments/{comment_id}/moderation
```

List query parameters are `sort=oldest|newest|hot`, `page`, and `page_size`. Root page size defaults to and is capped at 20. The response includes target summary, roots, the oldest three active children for each root, child totals, viewer state, and pagination metadata. `GET /comments/{root_comment_id}/replies` loads that floor's children oldest-first with `page` and a maximum `page_size` of 50.

Create accepts:

```json
{
  "content": "1:24 这里需要补充",
  "reply_to_id": null,
  "mentions": [{ "user_id": "uuid", "start": 11, "end": 15 }],
  "attachment_ids": []
}
```

Clients never send `root_id`, `floor_number`, `comment_type`, or timestamp fields. The service derives them.

The existing `GET /api/v1/users/search?scope=mention` becomes the mention picker source and returns active registered users matching username or display name; it is no longer limited to followed users.

Typed module actions complement, rather than duplicate, the core routes:

```text
POST /api/v1/debates/{id}/arguments
PATCH /api/v1/debate-arguments/{comment_id}
POST /api/v1/timeline/events/{id}/revision-proposals
POST /api/v1/timeline/persons/{id}/revision-proposals
PUT  /api/v1/timeline/revision-proposals/{comment_id}/decision
```

These handlers validate typed domain fields and call the comment service in the same transaction. Ordinary child discussion under an argument or Wiki proposal uses the core create route.

Likes, unlike, reports, and unique marking are idempotent. Stable application error codes accompany these HTTP statuses:

- `400`: invalid content, Markdown, mention, relation, or attachment;
- `401`: login required;
- `403`: invisible target or insufficient permission;
- `404`: missing target or comment;
- `429`: publish rate exceeded.

An out-of-duration time token is not an error; it remains plain text.

Moderation accepts only `restore`, `hide`, `delete`, `uphold_report`, and `reject_report` actions. Listing reports is restricted to moderator-or-higher roles. Timeline proposal decisions are restricted to the target creator or moderator-or-higher roles.

## 7. Transaction and Data Flow

Creating a root comment performs the following in one database transaction:

1. Resolve and authorize the target.
2. Enforce rate and duplicate-content rules.
3. Parse and sanitize Markdown, mentions, attachments, and time anchors.
4. Lock the `discussion_targets` row and allocate `next_floor`.
5. Insert the comment and relations.
6. Update target counters and cached score inputs.
7. Insert deduplicated notifications.

Creating a child follows the same flow but locks and validates the target floor, derives `root_id`, and increments root and target counters. Parent deletion cascades the floor and adjusts all counters in the same transaction.

## 8. Module Integration

### 8.1 Shared Web Components

Create a shared comment surface rather than copying the current blog and video components:

- `CommentSection`: target loading, sorting, paging, root composer, and states.
- `CommentThread`: root plus child preview and expansion.
- `CommentItem`: author, floor, content, attachments, time anchors, counts, and actions.
- `CommentComposer`: restricted Markdown, mention search, image upload, reply target, and optional current-time insertion.
- `CommentReportDialog`: reason selection and idempotent submission.

Components receive a target descriptor and presentation policy. Slots or small policy props change labels and domain controls; they do not fork core interaction logic.

### 8.2 Per-module Form

- Blog: replace `components/blog/CommentSection.vue`; remove Web controls for `allow_comments` and `comment_mode`.
- Video: replace `VideoCommentSection`; keep seek behavior through shared time anchors.
- Podcast: add the shared section to episode detail and connect seek behavior.
- RSS: add the shared section to feed-item detail and article sheet; both resolve through `FeedItem.id` to the canonical discussion target.
- Music: replace album drawer discussion logic and add the same discussion surface to artist and song views. Use discussion wording.
- Forum: replace reply persistence and interaction logic with the core while keeping forum layout, drafts, best-answer label, and topic-specific controls.
- Debate: store each argument as a core comment plus `debate_argument_details`; preserve the argument-node UI, voting, references, folding, and conclusions as debate extensions.
- Timeline: event and person detail surfaces show Wiki revision proposals rather than ordinary comments. A root proposal contains a typed diff and evidence; children discuss it. Applying a proposal creates the existing revision/audit record.

## 9. Rendering and Safety

- Parse Markdown with an allowlist-capable library; do not render raw HTML.
- Allow only `http` and `https` links and add safe external-link attributes.
- Validate attachment ownership, MIME type, upload completion, and maximum count before association.
- Mention and time-anchor positions are server-validated against normalized content.
- A target adapter must enforce resource visibility on every list and mutation request.
- Comments disappear with deleted targets through explicit service cleanup or foreign-key cascade, depending on the module lifecycle.

## 10. Testing

Backend tests cover:

- concurrent floor allocation without duplicate floor numbers;
- root and child relation invariants;
- root cascade deletion and child-only deletion;
- Markdown allowlist and XSS rejection;
- mention search, validation, notification deduplication, and self-notification filtering;
- multiple media anchors and duration bounds;
- image-only comments and four-image limit;
- like/report idempotency and fourth-distinct-report folding;
- pin/best-answer owner rules and one-root limit;
- oldest/newest/hot sorting and root pagination;
- rate limiting and duplicate-content rejection;
- visibility enforcement for every target adapter;
- Wiki proposal application and debate extension persistence.

Frontend unit tests cover composer rules, reply targeting, child expansion, time seeking, folded-content expansion, role-based actions, and module labels. Playwright covers one ordinary comment flow, one media time-anchor flow, forum best answer, and timeline proposal acceptance.

Required verification before completion:

```bash
cd Atoman-Backend && go build ./... && go test ./...
cd Atoman-Frontend && bun run type-check && bun run test:unit && bun run build
```

Focused Playwright scenarios run after the local Backend dependencies and both development servers are available.

## 11. Delivery Order

The code can ship as one coordinated Backend/Web change, but implementation remains incremental:

1. Core schema, parser, target registry, service, and HTTP contract.
2. Shared Web comment components.
3. Blog and video replacement.
4. Podcast, RSS, and music integration.
5. Forum and debate extensions.
6. Timeline Wiki revision proposals.
7. Remove obsolete models, handlers, components, settings, and generated API documentation; regenerate Swagger.

No compatibility forwarding layer and no data migration job are added.
