# Music Album Multipart Upload Design

## Goal

Allow the music album creation flow to upload zip archives up to 2GB through Cloudflare Pages without hitting request body limits. The browser uploads archive parts directly to R2. The Go backend owns authentication, upload state, multipart completion, zip extraction, and final album import payload generation.

This design only covers music album zip import. It does not change generic media uploads, cover uploads, audio uploads, or other modules.

## User Behavior

The user selects a `.zip` archive in the existing album creation flow. Files larger than 2GB are rejected before upload with a short message telling the user to convert or compress the archive before uploading.

If upload fails or the page refreshes, the user can select the same file again. The frontend resumes already completed parts when the file name and file size match the stored upload task. The first version does not resume without reselecting the local file.

After all parts finish, the backend combines the R2 multipart upload, extracts the archive, fills album title, cover, and tracks, then deletes the original zip object from R2.

## Backend API

The existing album import session remains the workflow owner. Multipart upload state is stored in `music_album_import_sessions.payload_json` to avoid introducing a second task table for this one-purpose flow.

New endpoints:

```text
POST /api/v1/music/imports/albums/:sessionId/multipart
POST /api/v1/music/imports/albums/:sessionId/multipart/parts/:partNumber
POST /api/v1/music/imports/albums/:sessionId/multipart/parts/:partNumber/complete
POST /api/v1/music/imports/albums/:sessionId/multipart/complete
```

`POST /multipart` creates or restores a multipart task. The request includes `fileName`, `fileSize`, and `contentType`. The backend validates `.zip`, 2GB maximum size, login, session existence, and session status. If an unfinished task exists for the same file name and size, the response returns the saved upload state. Otherwise, the backend creates a new R2 multipart upload and stores its state.

`POST /multipart/parts/:partNumber` returns one presigned R2 upload URL for the requested part. Signing one part at a time keeps URL expiry and retry behavior simple.

`POST /multipart/parts/:partNumber/complete` records the part `ETag` after the browser successfully uploads it to R2. The backend stores `partNumber`, `etag`, and `size`.

`POST /multipart/complete` completes the R2 multipart upload using the stored parts sorted by part number. It then reads the combined zip from R2, extracts it, updates the import session to `ready`, and deletes the original zip object.

The existing full-body upload endpoint stays in place for compatibility during rollout. The frontend creation flow should use multipart by default.

## Session State

Import status values:

```text
pending_upload
uploading
uploaded
extracting
ready
failed
committed
```

State transitions:

```text
pending_upload -> uploading -> uploaded -> extracting -> ready -> committed
pending_upload -> uploading -> failed
uploaded -> extracting -> failed
```

Payload fields added for multipart:

```json
{
  "archive_name": "album.zip",
  "archive_size": 123456,
  "archive_key": "imports/albums/<sessionId>/album.zip",
  "multipart_upload_id": "...",
  "multipart_part_size": 16777216,
  "multipart_parts": [
    { "part_number": 1, "etag": "\"...\"", "size": 16777216 }
  ],
  "upload_progress": 35,
  "upload_speed": 0,
  "error_message": ""
}
```

The recommended part size is 16MB. A 2GB archive produces at most 128 parts, which is small enough for simple JSON state and short enough for practical retry behavior.

## Frontend Flow

The existing `MusicCreationAlbumSeedStep.vue` remains the file entry point.

On file selection:

1. Validate `.zip` and `file.size <= 2GB`.
2. Create or reuse the album import session.
3. Call the multipart init endpoint with file metadata.
4. Slice the file with the returned `partSize`.
5. Skip parts already listed in `completedParts`.
6. Upload missing parts to R2 with `PUT` against presigned URLs.
7. Read the response `ETag` header and report it to the backend.
8. Update progress and speed after each completed part.
9. Call multipart complete.
10. Apply the returned import snapshot to fill album details and tracks.

Upload concurrency is 3 parts. Each failed part retries twice. If a part still fails, the flow stops and shows a retryable error. The user can reselect the same file to resume.

The UI stays close to the current design: upload progress, upload speed, extracting state, and a short retryable failure message. It does not add a separate upload task list.

## Zip Extraction

The backend must not read a 2GB zip fully into memory. After multipart completion, it streams the R2 object into a temporary local file and uses a zip reader that can work from that file. Archive entries are processed one by one.

The existing import derivation rules stay the same:

- Ignore hidden files and `__MACOSX`.
- Derive tracks from audio files.
- Prefer cover-like image names for the album cover.
- Upload extracted audio and cover files to the normal music R2 paths.

Reading each extracted audio file into memory before `PutObject` is acceptable for the first version. The large object risk is the zip archive itself, not normal single-track audio files.

After successful extraction:

- Set session status to `ready`.
- Store derived album title, cover, tracks, progress, and archive name.
- Delete the original zip object from R2.

After failure:

- Set session status to `failed`.
- Store a short user-facing error message.
- Abort unfinished multipart upload when possible, or delete the combined zip if it already exists.

## Cloudflare R2 Requirements

R2 CORS must allow browser `PUT` requests from the Pages origin and expose the `ETag` response header. Without exposed `ETag`, the frontend cannot report completed parts to the backend.

Required CORS behavior:

- Allow origin: production Pages site and local development origin.
- Allow method: `PUT`.
- Allow request headers used by presigned uploads.
- Expose response header: `ETag`.

## Tests

Backend tests:

- Multipart init rejects non-zip files and files over 2GB.
- Multipart init restores an unfinished task for the same file name and size.
- Part completion saves `ETag` and part metadata.
- Multipart complete sorts parts by number before completing R2 upload.
- Zip extraction via temporary file preserves current import behavior.

Frontend tests:

- API adapter sends the expected multipart requests.
- File selection rejects archives over 2GB with the conversion/compression message.
- Upload skips already completed parts.
- Upload reports part `ETag`s and calls final complete.
- Existing creation flow still applies the ready import snapshot.

## Rollout

Deploy backend first, then frontend. Confirm R2 CORS before using the production Pages flow. Keep the old full-body archive upload endpoint until multipart import has worked in production.
