DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_quota_policies'
          AND column_name = 'window'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_quota_policies'
          AND column_name = 'window_key'
    ) THEN
        ALTER TABLE usage_quota_policies RENAME COLUMN "window" TO window_key;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_buckets'
          AND column_name = 'window'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_buckets'
          AND column_name = 'window_key'
    ) THEN
        ALTER TABLE usage_buckets RENAME COLUMN "window" TO window_key;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_events'
          AND column_name = 'window'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'usage_events'
          AND column_name = 'window_key'
    ) THEN
        ALTER TABLE usage_events RENAME COLUMN "window" TO window_key;
    END IF;
END $$;

DROP INDEX IF EXISTS usage_buckets_summary_idx;
CREATE INDEX IF NOT EXISTS usage_buckets_summary_idx
ON usage_buckets(scope_kind, scope_id, metric, window_key, window_start DESC);
