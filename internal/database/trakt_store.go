package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type TraktStore struct {
	db *sql.DB
}

type TraktAuth struct {
	UserID       int        `json:"user_id"`
	AccessToken  string     `json:"-"`
	RefreshToken string     `json:"-"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Scope        string     `json:"scope,omitempty"`
	LastSyncAt   *time.Time `json:"last_sync_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type ExternalWatchHistory struct {
	UserID        int       `json:"user_id"`
	Source        string    `json:"source"`
	MediaType     string    `json:"media_type"`
	TMDBID        int       `json:"tmdb_id,omitempty"`
	IMDBID        string    `json:"imdb_id,omitempty"`
	TraktID       int       `json:"trakt_id,omitempty"`
	SeasonNumber  int       `json:"season_number,omitempty"`
	EpisodeNumber int       `json:"episode_number,omitempty"`
	Title         string    `json:"title"`
	Year          int       `json:"year,omitempty"`
	WatchedAt     time.Time `json:"watched_at"`
	Plays         int       `json:"plays,omitempty"`
}

func NewTraktStore(db *sql.DB) (*TraktStore, error) {
	store := &TraktStore{db: db}
	if err := store.initTables(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *TraktStore) initTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS user_trakt_auth (
			user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
			access_token TEXT NOT NULL,
			refresh_token TEXT NOT NULL,
			expires_at TIMESTAMPTZ,
			scope TEXT,
			last_sync_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS user_external_watch_history (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_external_watch_history_user ON user_external_watch_history(user_id, watched_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_user_external_watch_history_identity ON user_external_watch_history(user_id, source, media_type, tmdb_id, season_number, episode_number)`,
	}

	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			return fmt.Errorf("failed to initialize Trakt tables: %w", err)
		}
	}
	return nil
}

func (s *TraktStore) SaveAuth(ctx context.Context, auth TraktAuth) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_trakt_auth (user_id, access_token, refresh_token, expires_at, scope, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			expires_at = EXCLUDED.expires_at,
			scope = EXCLUDED.scope,
			updated_at = NOW()
	`, auth.UserID, auth.AccessToken, auth.RefreshToken, auth.ExpiresAt, auth.Scope)
	return err
}

func (s *TraktStore) GetAuth(ctx context.Context, userID int) (*TraktAuth, error) {
	var auth TraktAuth
	var expiresAt sql.NullTime
	var lastSyncAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, access_token, refresh_token, expires_at, COALESCE(scope, ''), last_sync_at, created_at, updated_at
		FROM user_trakt_auth
		WHERE user_id = $1
	`, userID).Scan(
		&auth.UserID,
		&auth.AccessToken,
		&auth.RefreshToken,
		&expiresAt,
		&auth.Scope,
		&lastSyncAt,
		&auth.CreatedAt,
		&auth.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		auth.ExpiresAt = &expiresAt.Time
	}
	if lastSyncAt.Valid {
		auth.LastSyncAt = &lastSyncAt.Time
	}
	return &auth, nil
}

func (s *TraktStore) DeleteAuth(ctx context.Context, userID int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_trakt_auth WHERE user_id = $1`, userID)
	return err
}

func (s *TraktStore) UpdateLastSync(ctx context.Context, userID int, syncedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE user_trakt_auth
		SET last_sync_at = $2, updated_at = NOW()
		WHERE user_id = $1
	`, userID, syncedAt)
	return err
}

func (s *TraktStore) UpsertExternalWatchHistory(ctx context.Context, item ExternalWatchHistory) error {
	tmdbID := nullableInt(item.TMDBID)
	traktID := nullableInt(item.TraktID)
	season := nullableInt(item.SeasonNumber)
	episode := nullableInt(item.EpisodeNumber)
	year := nullableInt(item.Year)
	if item.Plays <= 0 {
		item.Plays = 1
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE user_external_watch_history
		SET
			imdb_id = NULLIF($7, ''),
			trakt_id = $8,
			title = $9,
			year = $10,
			watched_at = GREATEST(watched_at, $11),
			plays = GREATEST(plays, $12),
			updated_at = NOW()
		WHERE user_id = $1
			AND source = $2
			AND media_type = $3
			AND COALESCE(tmdb_id, 0) = $4
			AND COALESCE(season_number, 0) = $5
			AND COALESCE(episode_number, 0) = $6
	`, item.UserID, item.Source, item.MediaType, item.TMDBID, item.SeasonNumber, item.EpisodeNumber, item.IMDBID, traktID, item.Title, year, item.WatchedAt, item.Plays)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO user_external_watch_history (
			user_id, source, media_type, tmdb_id, imdb_id, trakt_id, season_number, episode_number,
			title, year, watched_at, plays
		)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11, $12)
	`, item.UserID, item.Source, item.MediaType, tmdbID, item.IMDBID, traktID, season, episode, item.Title, year, item.WatchedAt, item.Plays)
	return err
}

func (s *TraktStore) GetExternalWatchHistory(ctx context.Context, userID, limit int) ([]ExternalWatchHistory, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			user_id,
			source,
			media_type,
			COALESCE(tmdb_id, 0),
			COALESCE(imdb_id, ''),
			COALESCE(trakt_id, 0),
			COALESCE(season_number, 0),
			COALESCE(episode_number, 0),
			title,
			COALESCE(year, 0),
			watched_at,
			plays
		FROM user_external_watch_history
		WHERE user_id = $1
		ORDER BY watched_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []ExternalWatchHistory
	for rows.Next() {
		var item ExternalWatchHistory
		if err := rows.Scan(
			&item.UserID,
			&item.Source,
			&item.MediaType,
			&item.TMDBID,
			&item.IMDBID,
			&item.TraktID,
			&item.SeasonNumber,
			&item.EpisodeNumber,
			&item.Title,
			&item.Year,
			&item.WatchedAt,
			&item.Plays,
		); err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	return history, rows.Err()
}

func nullableInt(value int) interface{} {
	if value == 0 {
		return nil
	}
	return value
}
