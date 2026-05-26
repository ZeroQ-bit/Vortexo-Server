-- Add episode fields used by current library/import code.
-- Older installs created library_episodes before these columns existed.

ALTER TABLE library_episodes
    ADD COLUMN IF NOT EXISTS tmdb_id INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS runtime INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS stream_url TEXT,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_episodes_tmdb_id
    ON library_episodes(tmdb_id)
    WHERE tmdb_id > 0;
