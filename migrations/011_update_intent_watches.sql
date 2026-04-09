ALTER TABLE session_watches
    ADD COLUMN IF NOT EXISTS source TEXT,
    ADD COLUMN IF NOT EXISTS subject_ref TEXT;
