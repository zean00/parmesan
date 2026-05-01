UPDATE tool_runs
SET idempotency_key = id
WHERE idempotency_key = '';

WITH ranked_tool_runs AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY idempotency_key
               ORDER BY
                   CASE status WHEN 'succeeded' THEN 0 WHEN 'running' THEN 1 ELSE 2 END,
                   created_at DESC,
                   id DESC
           ) AS rn
    FROM tool_runs
    WHERE idempotency_key <> ''
)
DELETE FROM tool_runs
WHERE id IN (SELECT id FROM ranked_tool_runs WHERE rn > 1);

CREATE UNIQUE INDEX IF NOT EXISTS tool_runs_idempotency_key_idx
ON tool_runs(idempotency_key);

UPDATE delivery_attempts
SET idempotency_key = id
WHERE idempotency_key = '';

WITH ranked_delivery_attempts AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY idempotency_key
               ORDER BY created_at DESC, id DESC
           ) AS rn
    FROM delivery_attempts
    WHERE idempotency_key <> ''
)
DELETE FROM delivery_attempts
WHERE id IN (SELECT id FROM ranked_delivery_attempts WHERE rn > 1);

CREATE UNIQUE INDEX IF NOT EXISTS delivery_attempts_idempotency_key_idx
ON delivery_attempts(idempotency_key);
