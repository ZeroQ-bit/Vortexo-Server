DROP INDEX IF EXISTS idx_episodes_tmdb_id;

ALTER TABLE library_episodes
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS stream_url,
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS runtime,
    DROP COLUMN IF EXISTS tmdb_id;
