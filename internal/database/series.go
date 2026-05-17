package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

type SeriesStore struct {
	db *sql.DB
}

func NewSeriesStore(db *sql.DB) *SeriesStore {
	return &SeriesStore{db: db}
}

// GetDB returns the underlying database connection
func (s *SeriesStore) GetDB() *sql.DB {
	return s.db
}

// Add adds a new series to the library
func (s *SeriesStore) Add(ctx context.Context, series *models.Series) error {
	// Build metadata JSON with all series details
	metadata := map[string]interface{}{
		"original_title":  series.OriginalTitle,
		"overview":        series.Overview,
		"poster_path":     series.PosterPath,
		"backdrop_path":   series.BackdropPath,
		"first_air_date":  series.FirstAirDate,
		"status":          series.Status,
		"seasons":         series.Seasons,
		"total_episodes":  series.TotalEpisodes,
		"genres":          series.Genres,
		"quality_profile": series.QualityProfile,
	}
	// Merge with existing metadata
	for k, v := range series.Metadata {
		metadata[k] = v
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Extract year from first air date
	var year *int
	if series.FirstAirDate != nil {
		y := series.FirstAirDate.Year()
		year = &y
	}

	// Generate clean title for searching
	cleanTitle := series.Title

	query := `
		INSERT INTO library_series (
			tmdb_id, imdb_id, title, year, monitored, clean_title, metadata, added_at, preferred_quality
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, added_at
	`

	// Use NULL for empty IMDB ID
	var imdbID *string
	if series.IMDBID != "" {
		imdbID = &series.IMDBID
	}

	err = s.db.QueryRowContext(
		ctx, query,
		series.TMDBID, imdbID, series.Title, year, series.Monitored,
		cleanTitle, metadataJSON, time.Now(), series.QualityProfile,
	).Scan(&series.ID, &series.AddedAt)

	if err != nil {
		return fmt.Errorf("failed to add series: %w", err)
	}

	return nil
}

type seriesRowScanner interface {
	Scan(dest ...any) error
}

func scanSeries(scanner seriesRowScanner) (*models.Series, error) {
	var series models.Series
	var metadataJSON []byte
	var imdbID sql.NullString
	var year sql.NullInt32
	var lastChecked sql.NullTime
	var preferredQuality sql.NullString

	err := scanner.Scan(
		&series.ID, &series.TMDBID, &imdbID, &series.Title, &year,
		&series.Monitored, &series.CleanTitle, &metadataJSON,
		&series.AddedAt, &lastChecked, &preferredQuality,
	)
	if err != nil {
		return nil, err
	}

	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &series.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}
	if series.Metadata == nil {
		series.Metadata = models.Metadata{}
	}

	if imdbID.Valid && imdbID.String != "" {
		series.IMDBID = imdbID.String
		series.Metadata["imdb_id"] = imdbID.String
	} else if value, ok := series.Metadata["imdb_id"].(string); ok {
		series.IMDBID = value
	}
	if year.Valid {
		series.Year = int(year.Int32)
		series.Metadata["year"] = series.Year
	}
	if lastChecked.Valid {
		series.LastChecked = &lastChecked.Time
	}
	if preferredQuality.Valid {
		series.QualityProfile = preferredQuality.String
	}

	applySeriesMetadata(&series)
	return &series, nil
}

func applySeriesMetadata(series *models.Series) {
	if series == nil || series.Metadata == nil {
		return
	}

	if value, ok := series.Metadata["original_title"].(string); ok {
		series.OriginalTitle = value
	}
	if value, ok := series.Metadata["overview"].(string); ok {
		series.Overview = value
	}
	if value, ok := series.Metadata["poster_path"].(string); ok {
		series.PosterPath = value
	}
	if value, ok := series.Metadata["backdrop_path"].(string); ok {
		series.BackdropPath = value
	}
	if value, ok := series.Metadata["first_air_date"].(string); ok {
		if parsed, ok := parseMetadataTime(value); ok {
			series.FirstAirDate = &parsed
		}
	}
	if value, ok := series.Metadata["status"].(string); ok {
		series.Status = value
	}
	if value := metadataInt(series.Metadata["seasons"]); value > 0 {
		series.Seasons = value
	}
	if value := metadataInt(series.Metadata["total_episodes"]); value > 0 {
		series.TotalEpisodes = value
	}
	if value, ok := series.Metadata["genres"].([]interface{}); ok {
		series.Genres = make([]string, 0, len(value))
		for _, genre := range value {
			if text, ok := genre.(string); ok {
				series.Genres = append(series.Genres, text)
			}
		}
	}
	if value := metadataFloat(series.Metadata["vote_average"]); value > 0 {
		series.VoteAverage = value
	}
	if value := metadataInt(series.Metadata["vote_count"]); value > 0 {
		series.VoteCount = value
	}
	if value, ok := series.Metadata["original_language"].(string); ok {
		series.OriginalLang = value
	}
	if value, ok := series.Metadata["quality_profile"].(string); ok && series.QualityProfile == "" {
		series.QualityProfile = value
	}
	if value, ok := series.Metadata["search_status"].(string); ok {
		series.SearchStatus = value
	}
}

func metadataInt(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func metadataFloat(value interface{}) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}

func parseMetadataTime(value string) (time.Time, bool) {
	formats := []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05Z"}
	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// Get retrieves a series by ID
func (s *SeriesStore) Get(ctx context.Context, id int64) (*models.Series, error) {
	query := `
		SELECT id, tmdb_id, imdb_id, title, year, monitored, clean_title,
			metadata, added_at, last_checked, preferred_quality
		FROM library_series
		WHERE id = $1
	`

	series, err := scanSeries(s.db.QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("series not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get series: %w", err)
	}

	return series, nil
}

// GetByTMDBID retrieves a series by TMDB ID
func (s *SeriesStore) GetByTMDBID(ctx context.Context, tmdbID int) (*models.Series, error) {
	query := `
		SELECT id, tmdb_id, imdb_id, title, year, monitored, clean_title,
			metadata, added_at, last_checked, preferred_quality
		FROM library_series
		WHERE tmdb_id = $1
	`

	series, err := scanSeries(s.db.QueryRowContext(ctx, query, tmdbID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("series not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get series: %w", err)
	}

	return series, nil
}

// GetByIMDBID retrieves a series by IMDB ID
func (s *SeriesStore) GetByIMDBID(ctx context.Context, imdbID string) (*models.Series, error) {
	query := `
		SELECT id, tmdb_id, imdb_id, title, year, monitored, clean_title,
			metadata, added_at, last_checked, preferred_quality
		FROM library_series
		WHERE imdb_id = $1
	`

	series, err := scanSeries(s.db.QueryRowContext(ctx, query, imdbID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("series not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get series: %w", err)
	}

	return series, nil
}

// List returns paginated series with optional filtering
func (s *SeriesStore) List(ctx context.Context, offset, limit int, monitored *bool) ([]*models.Series, error) {
	query := `
		SELECT id, tmdb_id, imdb_id, title, year, monitored, clean_title,
			metadata, added_at, last_checked, preferred_quality
		FROM library_series
		WHERE ($1::boolean IS NULL OR monitored = $1)
		ORDER BY added_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := s.db.QueryContext(ctx, query, monitored, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list series: %w", err)
	}
	defer rows.Close()

	var seriesList []*models.Series
	for rows.Next() {
		series, err := scanSeries(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan series: %w", err)
		}
		seriesList = append(seriesList, series)
	}

	return seriesList, nil
}

// Search performs full-text search on series
func (s *SeriesStore) Search(ctx context.Context, query string, limit int) ([]*models.Series, error) {
	searchQuery := `
		SELECT id, tmdb_id, imdb_id, title, year, monitored, clean_title,
			metadata, added_at, last_checked, preferred_quality
		FROM library_series
		WHERE title_vector @@ plainto_tsquery('english', $1)
		ORDER BY ts_rank(title_vector, plainto_tsquery('english', $1)) DESC
		LIMIT $2
	`

	rows, err := s.db.QueryContext(ctx, searchQuery, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search series: %w", err)
	}
	defer rows.Close()

	var seriesList []*models.Series
	for rows.Next() {
		series, err := scanSeries(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan series: %w", err)
		}
		seriesList = append(seriesList, series)
	}

	return seriesList, nil
}

// Update updates an existing series
func (s *SeriesStore) Update(ctx context.Context, series *models.Series) error {
	if series.Metadata == nil {
		series.Metadata = models.Metadata{}
	}
	series.Metadata["quality_profile"] = series.QualityProfile
	if series.SearchStatus != "" {
		series.Metadata["search_status"] = series.SearchStatus
	} else {
		delete(series.Metadata, "search_status")
	}

	metadataJSON, err := json.Marshal(series.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		UPDATE library_series
		SET monitored = $1,
			preferred_quality = $2,
			metadata = $3,
			last_checked = $4
		WHERE id = $5
	`

	result, err := s.db.ExecContext(
		ctx, query,
		series.Monitored, series.QualityProfile, metadataJSON, time.Now(), series.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update series: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("series not found")
	}

	return nil
}

// Delete removes a series from the library
func (s *SeriesStore) Delete(ctx context.Context, id int64) error {
	query := `DELETE FROM library_series WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete series: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("series not found")
	}

	return nil
}

// GetMonitored returns all monitored series
func (s *SeriesStore) GetMonitored(ctx context.Context) ([]*models.Series, error) {
	monitored := true
	return s.List(ctx, 0, 10000, &monitored)
}

// UpdateSearchStatus updates the search status for a series
func (s *SeriesStore) UpdateSearchStatus(ctx context.Context, id int64, status string) error {
	query := `
		UPDATE library_series
		SET metadata = jsonb_set(metadata, '{search_status}', to_jsonb($1::text), true),
		    last_checked = $2
		WHERE id = $3
	`

	now := time.Now()
	result, err := s.db.ExecContext(ctx, query, status, now, id)
	if err != nil {
		return fmt.Errorf("failed to update search status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("series not found")
	}

	return nil
}

// Count returns the total number of series
func (s *SeriesStore) Count(ctx context.Context, monitored *bool) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM library_series
		WHERE ($1::boolean IS NULL OR monitored = $1)
	`

	var count int
	err := s.db.QueryRowContext(ctx, query, monitored).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count series: %w", err)
	}

	return count, nil
}

// CountEpisodes returns the total number of episodes across all series
func (s *SeriesStore) CountEpisodes(ctx context.Context) (int, error) {
	query := `
		WITH metadata_total AS (
			SELECT COALESCE(SUM(
				CASE
					WHEN metadata->>'total_episodes' ~ '^[0-9]+$'
					THEN (metadata->>'total_episodes')::integer
					ELSE 0
				END
			), 0) AS count
			FROM library_series
		), episode_total AS (
			SELECT COUNT(*) AS count
			FROM library_episodes
		)
		SELECT GREATEST(metadata_total.count, episode_total.count)
		FROM metadata_total, episode_total
	`

	var count int
	err := s.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// DeleteAll removes all series from the library
func (s *SeriesStore) DeleteAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM library_series")
	return err
}

// ResetStatus resets the search status for all series
func (s *SeriesStore) ResetStatus(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_series 
		SET metadata = metadata - 'search_status',
		    last_checked = NULL
	`)
	return err
}

// DeleteBySource removes all series from a specific source
func (s *SeriesStore) DeleteBySource(ctx context.Context, source string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM library_series WHERE metadata->>'source' = $1",
		source)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
