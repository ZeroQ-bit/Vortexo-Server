-- Cache public Plex Discover artwork lookups for Vortexo clients.

CREATE TABLE IF NOT EXISTS plex_artwork_cache (
    id BIGSERIAL PRIMARY KEY,
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'tv')),
    tmdb_id INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    year INTEGER,
    source_page TEXT,
    artwork JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'ok',
    error TEXT,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (media_type, tmdb_id)
);

CREATE INDEX IF NOT EXISTS idx_plex_artwork_cache_lookup
    ON plex_artwork_cache(media_type, tmdb_id);

CREATE INDEX IF NOT EXISTS idx_plex_artwork_cache_status_fetched
    ON plex_artwork_cache(status, fetched_at);
