-- Read-only recommendation feedback checks for the last 30 days.
-- Run with: psql "$DATABASE_URL" -f scripts/music-recommendation-funnel.sql

-- 1. Event volume and unique users/requests.
SELECT
  event,
  COUNT(*) AS event_count,
  COUNT(DISTINCT user_id) AS user_count,
  COUNT(DISTINCT request_id) AS request_count,
  MIN(created_at) AS first_seen_at,
  MAX(created_at) AS last_seen_at
FROM music_recommendation_events
WHERE created_at >= NOW() - INTERVAL '30 days'
GROUP BY event
ORDER BY event;

-- 2. Request-level funnel. Playback events use song entities while
-- impressions/clicks use album entities, so the request_id is the join key.
WITH request_funnel AS (
  SELECT
    user_id,
    request_id,
    COUNT(*) FILTER (
      WHERE event = 'impression' AND entity_type = 'album'
    ) AS impressions,
    COUNT(*) FILTER (
      WHERE event = 'click' AND entity_type = 'album'
    ) AS clicks,
    COUNT(*) FILTER (
      WHERE event = 'play_start' AND entity_type = 'song'
    ) AS play_starts,
    COUNT(*) FILTER (
      WHERE event = 'play_complete' AND entity_type = 'song'
    ) AS play_completes,
    COUNT(*) FILTER (
      WHERE event = 'skip' AND entity_type = 'song'
    ) AS skips
  FROM music_recommendation_events
  WHERE created_at >= NOW() - INTERVAL '30 days'
  GROUP BY user_id, request_id
)
SELECT
  COUNT(*) AS recommendation_requests,
  COUNT(*) FILTER (WHERE impressions > 0) AS requests_with_impressions,
  COUNT(*) FILTER (WHERE clicks > 0) AS requests_with_clicks,
  COUNT(*) FILTER (WHERE play_starts > 0) AS requests_with_play_starts,
  COUNT(*) FILTER (WHERE play_completes > 0) AS requests_with_play_completes,
  COUNT(*) FILTER (WHERE skips > 0) AS requests_with_skips,
  COALESCE(SUM(impressions), 0) AS impressions,
  COALESCE(SUM(clicks), 0) AS clicks,
  COALESCE(SUM(play_starts), 0) AS play_starts,
  COALESCE(SUM(play_completes), 0) AS play_completes,
  COALESCE(SUM(skips), 0) AS skips
FROM request_funnel;

-- 3. Potential duplicate writes. The current contract has no event_id,
-- so this reports repeated logical events without deleting or collapsing them.
SELECT
  user_id,
  request_id,
  surface,
  event,
  entity_type,
  entity_id,
  position,
  reason,
  COUNT(*) AS duplicate_count,
  MIN(created_at) AS first_seen_at,
  MAX(created_at) AS last_seen_at
FROM music_recommendation_events
WHERE created_at >= NOW() - INTERVAL '30 days'
GROUP BY
  user_id,
  request_id,
  surface,
  event,
  entity_type,
  entity_id,
  position,
  reason
HAVING COUNT(*) > 1
ORDER BY duplicate_count DESC, last_seen_at DESC
LIMIT 100;

-- 4. Invalid event/entity combinations and impossible positions.
SELECT
  COUNT(*) FILTER (
    WHERE event NOT IN ('impression', 'click', 'play_start', 'play_complete', 'skip')
  ) AS invalid_events,
  COUNT(*) FILTER (
    WHERE entity_type NOT IN ('album', 'song')
  ) AS invalid_entity_types,
  COUNT(*) FILTER (
    WHERE (event IN ('impression', 'click') AND entity_type <> 'album')
       OR (event IN ('play_start', 'play_complete', 'skip') AND entity_type <> 'song')
  ) AS invalid_event_entity_pairs,
  COUNT(*) FILTER (WHERE position < 1) AS invalid_positions,
  COUNT(*) FILTER (WHERE surface = '') AS empty_surfaces
FROM music_recommendation_events
WHERE created_at >= NOW() - INTERVAL '30 days';

-- 5. Requests with playback but no album exposure. These should be reviewed
-- before using the playback rows for recommendation model training.
WITH request_events AS (
  SELECT
    user_id,
    request_id,
    BOOL_OR(event = 'impression' AND entity_type = 'album') AS has_impression,
    BOOL_OR(event IN ('play_start', 'play_complete', 'skip') AND entity_type = 'song') AS has_playback
  FROM music_recommendation_events
  WHERE created_at >= NOW() - INTERVAL '30 days'
  GROUP BY user_id, request_id
)
SELECT COUNT(*) AS playback_requests_without_impression
FROM request_events
WHERE has_playback AND NOT has_impression;
