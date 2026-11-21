-- name: CreateAnnouncement :one
INSERT INTO announcements (id, title, body, level, starts_at, ends_at, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateAnnouncement :one
UPDATE announcements SET title = $2, body = $3, level = $4, starts_at = $5, ends_at = $6, enabled = $7, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetAnnouncement :one
SELECT * FROM announcements WHERE id = $1;

-- name: DeleteAnnouncement :execrows
DELETE FROM announcements WHERE id = $1;

-- name: ListAnnouncements :many
SELECT * FROM announcements ORDER BY created_at DESC, id DESC;

-- name: ListActiveAnnouncements :many
-- Enabled, inside their time window and not dismissed by the user.
SELECT a.* FROM announcements a
WHERE a.enabled
  AND (a.starts_at IS NULL OR a.starts_at <= sqlc.arg('now')::timestamptz)
  AND (a.ends_at IS NULL OR a.ends_at > sqlc.arg('now')::timestamptz)
  AND NOT EXISTS (SELECT 1 FROM announcement_dismissals d WHERE d.announcement_id = a.id AND d.user_id = sqlc.arg('user_id'))
ORDER BY (a.level = 'warning') DESC, a.created_at DESC;

-- name: DismissAnnouncement :exec
INSERT INTO announcement_dismissals (announcement_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;
