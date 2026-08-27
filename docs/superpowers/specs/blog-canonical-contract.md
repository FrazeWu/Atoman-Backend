# Blog Canonical Contract

## Scope

This document freezes the Blog API contract for unified-content cutover. `ContentEntry.ID` is the only article identity. The legacy `Post`, `Collection`, `PostCollection`, and `LegacyCollectionMapping` types and identifiers are not runtime Blog API inputs or outputs.

The physical legacy tables may remain temporarily for non-Blog migration dependencies. Blog runtime queries and writes must not read or write them.

## Public API

Blog routes remain under `/api/v1/blog` and retain article-oriented URLs such as `/posts/{content_id}`. Route names describe product resources, not database tables.

All article IDs in requests and responses are `content_id`. All collection IDs are `content_collection_id`. The following names are retired from Blog API payloads:

- `post_id`
- `source_post_id`
- `collection_id` when it denotes a legacy collection
- `model.Post`, `model.Collection`, and `model.BlogPostVersion` response schemas

Canonical response shapes use these concepts:

- `BlogContent`: shared `ContentEntry` fields, Blog extension fields, canonical author/channel, and canonical collection memberships.
- `BlogCollection`: `ContentCollection` metadata and channel ownership.
- `BlogContentVersion`: immutable Blog version with `content_id` and `content_collection_id`.
- `BlogBookmark`: bookmark metadata with `content_id` and optional `content`.
- `BlogDraft`: current draft metadata with optional `content_id` and `content_collection_id`.

## Runtime Rules

- `content_entries` plus `content_blog_extensions` are the only Blog article read/write source.
- `content_collections` plus `content_collection_memberships` are the only Blog collection source.
- Content references, comments, likes, ratings, bookmarks, reading lists, subscriptions, Feed, Portal, SEO, RSS, and recommendations resolve Blog articles through `ContentEntry.ID`.
- Public visibility treats both empty visibility and `public` as public. Owners and administrators retain existing access semantics.
- A missing canonical mapping is an error. No request may silently fall back to a legacy Blog record.

## Cutover Validation

Before removal of the legacy migration audit data, verify canonical Blog entry, extension, collection, membership, interaction, reference, Feed, RSS, and SEO counts; report every mismatch as a failure list.
