CREATE TABLE IF NOT EXISTS user_trakt_auth (
    user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    scope TEXT,
    last_sync_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_external_watch_history (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source VARCHAR(50) NOT NULL,
    media_type VARCHAR(50) NOT NULL,
    tmdb_id INTEGER,
    imdb_id VARCHAR(32),
    trakt_id INTEGER,
    season_number INTEGER,
    episode_number INTEGER,
    title VARCHAR(500) NOT NULL,
    year INTEGER,
    watched_at TIMESTAMPTZ NOT NULL,
    plays INTEGER NOT NULL DEFAULT 1,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_external_watch_history_user
    ON user_external_watch_history(user_id, watched_at DESC);

CREATE INDEX IF NOT EXISTS idx_user_external_watch_history_identity
    ON user_external_watch_history(user_id, source, media_type, tmdb_id, season_number, episode_number);
