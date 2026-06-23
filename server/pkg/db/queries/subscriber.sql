-- name: AddIssueSubscriber :exec
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, user_type, user_id) DO NOTHING;

-- name: RemoveIssueSubscriber :exec
DELETE FROM issue_subscriber
WHERE issue_id = $1 AND user_type = $2 AND user_id = $3;

-- name: ListIssueSubscribers :many
SELECT * FROM issue_subscriber
WHERE issue_id = $1
ORDER BY created_at;

-- name: ListIssueSubscriberMemberEmails :many
SELECT
    s.issue_id,
    s.user_id,
    u.email,
    COALESCE(np.preferences, '{}'::jsonb) AS preferences
FROM issue_subscriber s
JOIN "user" u ON u.id = s.user_id
LEFT JOIN notification_preference np
    ON np.workspace_id = $2
   AND np.user_id = s.user_id
WHERE s.issue_id = $1
  AND s.user_type = 'member'
ORDER BY s.created_at;

-- name: IsIssueSubscriber :one
SELECT EXISTS(
    SELECT 1 FROM issue_subscriber
    WHERE issue_id = $1 AND user_type = $2 AND user_id = $3
) AS subscribed;
