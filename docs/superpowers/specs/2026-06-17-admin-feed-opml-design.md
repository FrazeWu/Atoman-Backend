# Admin Feed OPML Design

## Goal

Add OPML import and export controls to the admin subscription source management flow for global `external_rss` feed sources.

## Scope

- Admin import manages global feed sources only. It creates or reuses `feed_sources` rows and must not create user `subscriptions`.
- Admin export downloads all global external RSS sources as OPML.
- The frontend exposes the feature from the existing admin "订阅源管理" panel.
- Personal user OPML import/export remains unchanged.

## Backend Design

The backend already has `POST /api/v1/feed/sources/opml/import`, which imports OPML into global feed sources and is protected by auth plus admin middleware. Keep that route and implementation as the import path.

Add `GET /api/v1/feed/sources/opml/export`, also behind auth plus admin middleware. The handler queries `feed_sources` where `source_type = external_rss` and `rss_url` is not blank, orders sources by title and creation time, then returns OPML 2.0:

```xml
<opml version="2.0">
  <head><title>Atoman Feed Sources</title></head>
  <body>
    <outline text="Example" title="Example" type="rss" xmlUrl="https://example.com/feed.xml"></outline>
  </body>
</opml>
```

Use `application/x-opml+xml` and `Content-Disposition: attachment; filename="atoman-feed-sources.opml"`.

## Frontend Design

Extend `SettingFeedSourcePanel.vue` with a compact OPML action row:

- `导入 OPML`: opens a hidden file input accepting `.opml,.xml`; uploads the selected file to the admin global import endpoint using multipart form data.
- `导出 OPML`: calls the export endpoint, creates an object URL for the returned blob, and triggers a download.

The panel shows import counts as `导入 X，复用 Y，失败 Z`, refreshes the source list after successful import, and keeps the current manual add/edit/sync behavior unchanged.

## API Surface

- Existing: `POST /api/v1/feed/sources/opml/import`
- New: `GET /api/v1/feed/sources/opml/export`

The frontend API helper should expose both under the admin feed namespace, even though the backend route is currently rooted under `/feed/sources`.

## Error Handling

- Missing file, oversized file, and invalid OPML keep returning `400` from import.
- Export returns `500` only when sources cannot be read or XML cannot be generated.
- Frontend uses the existing panel message/error pattern.

## Testing

- Backend unit tests cover global export, excluding internal sources and blank RSS URLs.
- Backend route coverage verifies admin import and export are protected by admin middleware.
- Frontend unit tests cover import upload, source refresh after import, and export download invocation.
- Final verification runs backend tests for feed OPML handlers plus frontend component tests and standard build/type checks required by repo instructions.
