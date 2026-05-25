package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

type PlexArtworkCacheStore struct {
	db *sql.DB
}

func NewPlexArtworkCacheStore(db *sql.DB) *PlexArtworkCacheStore {
	return &PlexArtworkCacheStore{db: db}
}

func (s *PlexArtworkCacheStore) Get(ctx context.Context, mediaType string, tmdbID int) (*models.PlexArtworkCacheRecord, error) {
	query := `
		SELECT media_type, tmdb_id, title, COALESCE(year, 0), COALESCE(source_page, ''),
			artwork, status, COALESCE(error, ''), fetched_at, updated_at
		FROM plex_artwork_cache
		WHERE media_type = $1 AND tmdb_id = $2
	`

	var record models.PlexArtworkCacheRecord
	var artworkJSON []byte
	err := s.db.QueryRowContext(ctx, query, normalizePlexArtworkMediaType(mediaType), tmdbID).Scan(
		&record.MediaType,
		&record.TMDBID,
		&record.Title,
		&record.Year,
		&record.SourcePage,
		&artworkJSON,
		&record.Status,
		&record.Error,
		&record.FetchedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	record.Version = 1
	if len(artworkJSON) > 0 {
		if err := json.Unmarshal(artworkJSON, &record.Artwork); err != nil {
			return nil, fmt.Errorf("failed to unmarshal plex artwork: %w", err)
		}
	}

	return &record, nil
}

func (s *PlexArtworkCacheStore) Upsert(ctx context.Context, entry *models.PlexArtworkEntry, status, errorMessage string) error {
	if entry == nil {
		return fmt.Errorf("plex artwork entry is nil")
	}

	artworkJSON, err := json.Marshal(entry.Artwork)
	if err != nil {
		return fmt.Errorf("failed to marshal plex artwork: %w", err)
	}

	if status == "" {
		status = "ok"
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO plex_artwork_cache (
			media_type, tmdb_id, title, year, source_page, artwork, status, error, fetched_at, updated_at
		) VALUES ($1, $2, $3, NULLIF($4, 0), $5, $6, $7, NULLIF($8, ''), NOW(), NOW())
		ON CONFLICT (media_type, tmdb_id) DO UPDATE SET
			title = EXCLUDED.title,
			year = EXCLUDED.year,
			source_page = EXCLUDED.source_page,
			artwork = EXCLUDED.artwork,
			status = EXCLUDED.status,
			error = EXCLUDED.error,
			fetched_at = NOW(),
			updated_at = NOW()
	`

	_, err = s.db.ExecContext(
		ctx,
		query,
		normalizePlexArtworkMediaType(entry.MediaType),
		entry.TMDBID,
		entry.Title,
		entry.Year,
		entry.SourcePage,
		artworkJSON,
		status,
		errorMessage,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert plex artwork cache: %w", err)
	}

	return nil
}

func (s *PlexArtworkCacheStore) ListLibrarySeedItems(ctx context.Context, limit int, staleBefore time.Time) ([]models.PlexArtworkSeedItem, error) {
	if limit <= 0 {
		limit = 2000
	}

	query := `
		WITH library_items AS (
			SELECT 'movie'::text AS media_type, tmdb_id, title, COALESCE(year, 0) AS year, added_at
			FROM library_movies
			WHERE tmdb_id > 0
			UNION ALL
			SELECT 'tv'::text AS media_type, tmdb_id, title, COALESCE(year, 0) AS year, added_at
			FROM library_series
			WHERE tmdb_id > 0
		)
		SELECT i.media_type, i.tmdb_id, i.title, i.year
		FROM library_items i
		LEFT JOIN plex_artwork_cache c
			ON c.media_type = i.media_type AND c.tmdb_id = i.tmdb_id
		WHERE c.tmdb_id IS NULL
			OR c.status <> 'ok'
			OR c.fetched_at < $2
		ORDER BY
			CASE
				WHEN c.tmdb_id IS NULL THEN 0
				WHEN c.status <> 'ok' THEN 1
				ELSE 2
			END,
			i.added_at DESC
		LIMIT $1
	`

	rows, err := s.db.QueryContext(ctx, query, limit, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("failed to list plex artwork seed items: %w", err)
	}
	defer rows.Close()

	var items []models.PlexArtworkSeedItem
	for rows.Next() {
		var item models.PlexArtworkSeedItem
		if err := rows.Scan(&item.MediaType, &item.TMDBID, &item.Title, &item.Year); err != nil {
			return nil, fmt.Errorf("failed to scan plex artwork seed item: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate plex artwork seed items: %w", err)
	}

	return items, nil
}

func (s *PlexArtworkCacheStore) FindLibrarySeedItem(ctx context.Context, mediaType string, tmdbID int) (*models.PlexArtworkSeedItem, error) {
	normalized := normalizePlexArtworkMediaType(mediaType)
	table := "library_movies"
	if normalized == "tv" {
		table = "library_series"
	}

	query := fmt.Sprintf(
		`SELECT $1::text AS media_type, tmdb_id, title, COALESCE(year, 0) AS year FROM %s WHERE tmdb_id = $2 LIMIT 1`,
		table,
	)

	var item models.PlexArtworkSeedItem
	if err := s.db.QueryRowContext(ctx, query, normalized, tmdbID).Scan(&item.MediaType, &item.TMDBID, &item.Title, &item.Year); err != nil {
		return nil, err
	}

	return &item, nil
}

func normalizePlexArtworkMediaType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "show" || normalized == "series" {
		return "tv"
	}
	return normalized
}
