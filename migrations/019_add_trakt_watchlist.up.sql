CREATE TABLE IF NOT EXISTS user_external_watchlist (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source VARCHAR(50) NOT NULL,
    media_type VARCHAR(50) NOT NULL,
    tmdb_id INTEGER,
    imdb_id VARCHAR(32),
    trakt_id INTEGER,
    title VARCHAR(500) NOT NULL,
    year INTEGER,
    listed_at TIMESTAMPTZ NOT NULL,
    rank INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_external_watchlist_user
    ON user_external_watchlist(user_id, listed_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_external_watchlist_identity
    ON user_external_watchlist(user_id, source, media_type, tmdb_id);
