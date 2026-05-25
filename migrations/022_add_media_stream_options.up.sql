-- Store multiple selectable Real-Debrid library streams per media item.
-- media_streams remains the single "best stream" cache used by existing
-- scanners; this table backs instant source picker options.

CREATE TABLE IF NOT EXISTS media_stream_options (
    id                  BIGSERIAL PRIMARY KEY,
    media_type          VARCHAR(10) NOT NULL,
    media_id            BIGINT NOT NULL,
    movie_id            BIGINT REFERENCES library_movies(id) ON DELETE CASCADE,
    series_id           BIGINT REFERENCES library_series(id) ON DELETE CASCADE,
    season              INTEGER,
    episode             INTEGER,
    stream_title        TEXT NOT NULL DEFAULT '',
    stream_url          TEXT NOT NULL DEFAULT '',
    stream_hash         VARCHAR(64) NOT NULL DEFAULT '',
    quality_score       INTEGER DEFAULT 0,
    resolution          VARCHAR(20) DEFAULT '',
    hdr_type            VARCHAR(20) DEFAULT '',
    audio_format        VARCHAR(50) DEFAULT '',
    source_type         VARCHAR(20) DEFAULT '',
    file_size_gb        DECIMAL(10,2) DEFAULT 0,
    codec               VARCHAR(20) DEFAULT '',
    indexer             VARCHAR(50) DEFAULT '',
    rd_torrent_id       TEXT NOT NULL DEFAULT '',
    rd_file_id          INTEGER NOT NULL DEFAULT 0,
    rd_library_added    BOOLEAN NOT NULL DEFAULT TRUE,
    cached_at           TIMESTAMPTZ DEFAULT NOW(),
    last_checked        TIMESTAMPTZ DEFAULT NOW(),
    is_available        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT check_media_stream_options_target CHECK (
        (movie_id IS NOT NULL AND series_id IS NULL AND season IS NULL AND episode IS NULL) OR
        (movie_id IS NULL AND series_id IS NOT NULL AND season IS NOT NULL AND episode IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS unique_movie_stream_option_idx
ON media_stream_options (movie_id, stream_hash, rd_file_id)
WHERE movie_id IS NOT NULL AND series_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS unique_episode_stream_option_idx
ON media_stream_options (series_id, season, episode, stream_hash, rd_file_id)
WHERE series_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_media_stream_options_movie_lookup
ON media_stream_options (movie_id, is_available, quality_score DESC, updated_at DESC)
WHERE movie_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_media_stream_options_episode_lookup
ON media_stream_options (series_id, season, episode, is_available, quality_score DESC, updated_at DESC)
WHERE series_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_media_stream_options_hash
ON media_stream_options (stream_hash)
WHERE stream_hash <> '';

INSERT INTO schema_migrations (version, applied_at)
VALUES (22, NOW())
ON CONFLICT (version) DO NOTHING;
