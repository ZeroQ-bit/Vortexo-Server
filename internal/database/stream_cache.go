package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Zerr0-C00L/StreamArr/internal/models"
	"github.com/lib/pq"
)

// StreamCacheStore manages media_streams table operations
type StreamCacheStore struct {
	db *sql.DB
}

const cachedStreamSelectColumns = `
	media_streams.id, media_streams.media_type, media_streams.media_id, media_streams.movie_id, media_streams.series_id, media_streams.season, media_streams.episode, media_streams.stream_url, media_streams.stream_hash, media_streams.quality_score,
	media_streams.resolution, media_streams.hdr_type, media_streams.audio_format, media_streams.source_type, media_streams.file_size_gb,
	media_streams.codec, media_streams.indexer, media_streams.cached_at, media_streams.last_checked, media_streams.check_count,
	media_streams.is_available, media_streams.upgrade_available, media_streams.rd_library_added, media_streams.rd_torrent_id, media_streams.rd_library_added_at,
	media_streams.next_check_at, media_streams.created_at, media_streams.updated_at
`

// NewStreamCacheStore creates a new stream cache store
func NewStreamCacheStore(db *sql.DB) *StreamCacheStore {
	return &StreamCacheStore{db: db}
}

// GetDB returns the database connection for advanced queries
func (s *StreamCacheStore) GetDB() *sql.DB {
	return s.db
}

// GetCachedStream retrieves the cached stream for a movie
// Returns nil if no cached stream exists
func (s *StreamCacheStore) GetCachedStream(ctx context.Context, movieID int) (*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		WHERE movie_id = $1
	`, cachedStreamSelectColumns)

	cached := &models.CachedStream{}
	err := scanCachedStream(s.db.QueryRowContext(ctx, query, movieID), cached)

	if err == sql.ErrNoRows {
		return nil, nil // No cached stream
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cached stream: %w", err)
	}

	return cached, nil
}

// CacheStream stores or updates the cached stream for a media item
// Replaces existing stream if one exists (one stream per media)
func (s *StreamCacheStore) CacheStream(ctx context.Context, movieID int, stream models.TorrentStream, streamURL string) error {
	query := `
		INSERT INTO media_streams (
			media_type, media_id, movie_id, stream_url, stream_hash, quality_score,
			resolution, hdr_type, audio_format, source_type, file_size_gb,
			codec, indexer, cached_at, last_checked, check_count,
			is_available, upgrade_available, next_check_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			NOW(), NOW(), 0, true, false, NOW() + INTERVAL '7 days', NOW(), NOW()
		)
		ON CONFLICT (movie_id) WHERE movie_id IS NOT NULL AND series_id IS NULL DO UPDATE SET
			media_type = EXCLUDED.media_type,
			media_id = EXCLUDED.media_id,
			stream_url = EXCLUDED.stream_url,
			stream_hash = EXCLUDED.stream_hash,
			quality_score = EXCLUDED.quality_score,
			resolution = EXCLUDED.resolution,
			hdr_type = EXCLUDED.hdr_type,
			audio_format = EXCLUDED.audio_format,
			source_type = EXCLUDED.source_type,
			file_size_gb = EXCLUDED.file_size_gb,
			codec = EXCLUDED.codec,
			indexer = EXCLUDED.indexer,
			cached_at = NOW(),
			last_checked = NOW(),
			check_count = 0,
			is_available = true,
			upgrade_available = false,
			rd_library_added = CASE
				WHEN media_streams.stream_hash IS DISTINCT FROM EXCLUDED.stream_hash THEN false
				ELSE media_streams.rd_library_added
			END,
			rd_torrent_id = CASE
				WHEN media_streams.stream_hash IS DISTINCT FROM EXCLUDED.stream_hash THEN NULL
				ELSE media_streams.rd_torrent_id
			END,
			rd_library_added_at = CASE
				WHEN media_streams.stream_hash IS DISTINCT FROM EXCLUDED.stream_hash THEN NULL
				ELSE media_streams.rd_library_added_at
			END,
			next_check_at = NOW() + INTERVAL '7 days',
			updated_at = NOW()
	`

	_, err := s.db.ExecContext(ctx, query,
		"movie",
		movieID,
		movieID,
		streamURL,
		stream.Hash,
		stream.QualityScore,
		stream.Resolution,
		stream.HDRType,
		stream.AudioFormat,
		stream.Source,
		stream.SizeGB,
		stream.Codec,
		stream.Indexer,
	)

	if err != nil {
		return fmt.Errorf("failed to cache stream: %w", err)
	}

	return nil
}

// MarkUnavailable marks a stream as unavailable (debrid cache expired)
func (s *StreamCacheStore) MarkUnavailable(ctx context.Context, movieID int) error {
	query := `
		UPDATE media_streams
		SET is_available = false,
		    last_checked = NOW(),
		    check_count = check_count + 1,
		    next_check_at = NOW() + INTERVAL '1 day',
		    updated_at = NOW()
		WHERE movie_id = $1
	`

	result, err := s.db.ExecContext(ctx, query, movieID)
	if err != nil {
		return fmt.Errorf("failed to mark unavailable: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no stream found for movie_id %d", movieID)
	}

	return nil
}

// MarkUpgradeAvailable marks that a better quality stream is available
func (s *StreamCacheStore) MarkUpgradeAvailable(ctx context.Context, movieID int, available bool) error {
	query := `
		UPDATE media_streams
		SET upgrade_available = $1,
		    updated_at = NOW()
		WHERE movie_id = $2
	`

	_, err := s.db.ExecContext(ctx, query, available, movieID)
	if err != nil {
		return fmt.Errorf("failed to mark upgrade available: %w", err)
	}

	return nil
}

// UpdateNextCheck schedules the next availability check
func (s *StreamCacheStore) UpdateNextCheck(ctx context.Context, movieID int, daysUntilCheck int) error {
	query := `
		UPDATE media_streams
		SET last_checked = NOW(),
		    check_count = check_count + 1,
		    next_check_at = NOW() + INTERVAL '1 day' * $1,
		    updated_at = NOW()
		WHERE movie_id = $2
	`

	_, err := s.db.ExecContext(ctx, query, daysUntilCheck, movieID)
	if err != nil {
		return fmt.Errorf("failed to update next check: %w", err)
	}

	return nil
}

// GetStreamsDueForCheck retrieves streams that need availability checking
func (s *StreamCacheStore) GetStreamsDueForCheck(ctx context.Context, limit int) ([]*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		WHERE next_check_at <= NOW()
		  AND is_available = true
		ORDER BY last_checked ASC
		LIMIT $1
	`, cachedStreamSelectColumns)

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get streams for check: %w", err)
	}
	defer rows.Close()

	var streams []*models.CachedStream
	for rows.Next() {
		cached := &models.CachedStream{}
		err := scanCachedStream(rows, cached)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stream: %w", err)
		}
		streams = append(streams, cached)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating streams: %w", err)
	}

	return streams, nil
}

// GetStreamsByQualityScore retrieves streams with quality score below threshold
// Useful for finding upgrade candidates
func (s *StreamCacheStore) GetStreamsByQualityScore(ctx context.Context, maxScore int, limit int) ([]*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		WHERE quality_score <= $1
		  AND is_available = true
		ORDER BY quality_score ASC
		LIMIT $2
	`, cachedStreamSelectColumns)

	rows, err := s.db.QueryContext(ctx, query, maxScore, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get streams by score: %w", err)
	}
	defer rows.Close()

	var streams []*models.CachedStream
	for rows.Next() {
		cached := &models.CachedStream{}
		err := scanCachedStream(rows, cached)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stream: %w", err)
		}
		streams = append(streams, cached)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating streams: %w", err)
	}

	return streams, nil
}

// GetUnavailableStreams retrieves streams marked as unavailable
func (s *StreamCacheStore) GetUnavailableStreams(ctx context.Context, limit int) ([]*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		WHERE is_available = false
		ORDER BY last_checked ASC
		LIMIT $1
	`, cachedStreamSelectColumns)

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get unavailable streams: %w", err)
	}
	defer rows.Close()

	var streams []*models.CachedStream
	for rows.Next() {
		cached := &models.CachedStream{}
		err := scanCachedStream(rows, cached)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stream: %w", err)
		}
		streams = append(streams, cached)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating streams: %w", err)
	}

	return streams, nil
}

// GetCacheStats returns statistics about cached streams
func (s *StreamCacheStore) GetCacheStats(ctx context.Context) (map[string]interface{}, error) {
	query := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE is_available = true) as available,
			COUNT(*) FILTER (WHERE is_available = false) as unavailable,
			COUNT(*) FILTER (WHERE upgrade_available = true) as upgrades_available,
			AVG(quality_score) as avg_score,
			COUNT(*) FILTER (WHERE resolution = '2160p' OR resolution = '4K') as count_4k,
			COUNT(*) FILTER (WHERE resolution = '1080p') as count_1080p,
			COUNT(*) FILTER (WHERE resolution = '720p') as count_720p,
			COUNT(*) FILTER (WHERE hdr_type = 'DV') as count_dolby_vision,
			COUNT(*) FILTER (WHERE source_type = 'REMUX') as count_remux
		FROM media_streams
	`

	var stats struct {
		Total             int
		Available         int
		Unavailable       int
		UpgradesAvailable int
		AvgScore          sql.NullFloat64
		Count4K           int
		Count1080p        int
		Count720p         int
		CountDolbyVision  int
		CountRemux        int
	}

	err := s.db.QueryRowContext(ctx, query).Scan(
		&stats.Total,
		&stats.Available,
		&stats.Unavailable,
		&stats.UpgradesAvailable,
		&stats.AvgScore,
		&stats.Count4K,
		&stats.Count1080p,
		&stats.Count720p,
		&stats.CountDolbyVision,
		&stats.CountRemux,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get cache stats: %w", err)
	}

	avgScore := 0.0
	if stats.AvgScore.Valid {
		avgScore = stats.AvgScore.Float64
	}

	return map[string]interface{}{
		"total":                stats.Total,
		"available":            stats.Available,
		"unavailable":          stats.Unavailable,
		"upgrades_available":   stats.UpgradesAvailable,
		"avg_quality_score":    avgScore,
		"4k_streams":           stats.Count4K,
		"1080p_streams":        stats.Count1080p,
		"720p_streams":         stats.Count720p,
		"dolby_vision_streams": stats.CountDolbyVision,
		"remux_streams":        stats.CountRemux,
	}, nil
}

// DeleteCachedStream removes a cached stream
func (s *StreamCacheStore) DeleteCachedStream(ctx context.Context, movieID int) error {
	query := `DELETE FROM media_streams WHERE movie_id = $1`

	_, err := s.db.ExecContext(ctx, query, movieID)
	if err != nil {
		return fmt.Errorf("failed to delete cached stream: %w", err)
	}

	return nil
}

// MarkRealDebridLibraryAddedByID records that the cached stream has been added
// to the user's Real-Debrid account so background scans can avoid duplicate adds.
func (s *StreamCacheStore) MarkRealDebridLibraryAddedByID(ctx context.Context, cacheID int, torrentID string) error {
	query := `
		UPDATE media_streams
		SET rd_library_added = true,
		    rd_torrent_id = NULLIF($2, ''),
		    rd_library_added_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := s.db.ExecContext(ctx, query, cacheID, torrentID)
	if err != nil {
		return fmt.Errorf("failed to mark Real-Debrid library add: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no stream found for cache id %d", cacheID)
	}

	return nil
}

func (s *StreamCacheStore) MarkRealDebridLibraryAddedForMovie(ctx context.Context, movieID int, torrentID string) error {
	query := `
		UPDATE media_streams
		SET rd_library_added = true,
		    rd_torrent_id = NULLIF($2, ''),
		    rd_library_added_at = NOW(),
		    updated_at = NOW()
		WHERE movie_id = $1
	`

	result, err := s.db.ExecContext(ctx, query, movieID, torrentID)
	if err != nil {
		return fmt.Errorf("failed to mark Real-Debrid movie add: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no movie stream found for movie_id %d", movieID)
	}

	return nil
}

func (s *StreamCacheStore) MarkRealDebridLibraryAddedForSeriesEpisode(ctx context.Context, seriesID, season, episode int, torrentID string) error {
	query := `
		UPDATE media_streams
		SET rd_library_added = true,
		    rd_torrent_id = NULLIF($4, ''),
		    rd_library_added_at = NOW(),
		    updated_at = NOW()
		WHERE series_id = $1
		  AND season = $2
		  AND episode = $3
	`

	result, err := s.db.ExecContext(ctx, query, seriesID, season, episode, torrentID)
	if err != nil {
		return fmt.Errorf("failed to mark Real-Debrid series add: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no series stream found for series_id %d S%02dE%02d", seriesID, season, episode)
	}

	return nil
}

// GetPendingRealDebridLibraryAdds retrieves cached streams that have not yet
// been added to the user's Real-Debrid account.
func (s *StreamCacheStore) GetPendingRealDebridLibraryAdds(ctx context.Context, limit int) ([]*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		LEFT JOIN library_movies m ON m.id = media_streams.movie_id
		LEFT JOIN library_series srs ON srs.id = media_streams.series_id
		WHERE is_available = true
		  AND COALESCE(stream_hash, '') <> ''
		  AND rd_library_added = false
		  AND (
			media_type <> 'movie'
			OR COALESCE(NULLIF(m.metadata->>'release_date', '')::timestamptz <= NOW(), true)
		  )
		  AND (
			media_type <> 'series'
			OR COALESCE(NULLIF(srs.metadata->>'first_air_date', '')::timestamptz <= NOW(), true)
		  )
		ORDER BY updated_at ASC
		LIMIT $1
	`, cachedStreamSelectColumns)

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending Real-Debrid library adds: %w", err)
	}
	defer rows.Close()

	var streams []*models.CachedStream
	for rows.Next() {
		cached := &models.CachedStream{}
		if err := scanCachedStream(rows, cached); err != nil {
			return nil, fmt.Errorf("failed to scan pending Real-Debrid add: %w", err)
		}
		streams = append(streams, cached)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pending Real-Debrid adds: %w", err)
	}

	return streams, nil
}

func (s *StreamCacheStore) ResetRDLibraryByCacheID(ctx context.Context, cacheID int, _ string) error {
	query := `
		UPDATE media_streams
		SET rd_library_added = false,
		    rd_torrent_id = NULL,
		    rd_library_added_at = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := s.db.ExecContext(ctx, query, cacheID)
	if err != nil {
		return fmt.Errorf("failed to reset RD library state: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no stream found for cache id %d", cacheID)
	}

	return nil
}

// ResetRDLibraryMissingFrom clears local Real-Debrid library state for cached
// streams whose tracked RD torrent ID is no longer present in the account.
func (s *StreamCacheStore) ResetRDLibraryMissingFrom(ctx context.Context, validTorrentIDs map[string]struct{}, _ string) (int64, error) {
	baseSet := `
		UPDATE media_streams
		SET rd_library_added = false,
		    rd_torrent_id = NULL,
		    rd_library_added_at = NULL,
		    updated_at = NOW()
		WHERE rd_library_added = true
	`

	var (
		result sql.Result
		err    error
	)

	if len(validTorrentIDs) == 0 {
		result, err = s.db.ExecContext(ctx, baseSet)
	} else {
		ids := make([]string, 0, len(validTorrentIDs))
		for id := range validTorrentIDs {
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			result, err = s.db.ExecContext(ctx, baseSet)
		} else {
			result, err = s.db.ExecContext(ctx, baseSet+`
		  AND (
		    COALESCE(rd_torrent_id, '') = ''
		    OR NOT (rd_torrent_id = ANY($1))
		  )
		`, pq.Array(ids))
		}
	}

	if err != nil {
		return 0, fmt.Errorf("failed to reset missing RD library state: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count missing RD library resets: %w", err)
	}
	return rows, nil
}

func (s *StreamCacheStore) MarkUnavailableByCacheID(ctx context.Context, cacheID int, _ string) error {
	query := `
		UPDATE media_streams
		SET is_available = false,
		    rd_library_added = false,
		    rd_torrent_id = NULL,
		    rd_library_added_at = NULL,
		    last_checked = NOW(),
		    check_count = check_count + 1,
		    next_check_at = NOW() + INTERVAL '1 day',
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := s.db.ExecContext(ctx, query, cacheID)
	if err != nil {
		return fmt.Errorf("failed to mark cache entry unavailable: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no stream found for cache id %d", cacheID)
	}

	return nil
}

// GetStreamsWithUpgradesAvailable retrieves streams that have upgrades available
func (s *StreamCacheStore) GetStreamsWithUpgradesAvailable(ctx context.Context, limit int) ([]*models.CachedStream, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM media_streams
		WHERE upgrade_available = true
		  AND is_available = true
		ORDER BY quality_score ASC
		LIMIT $1
	`, cachedStreamSelectColumns)

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get streams with upgrades: %w", err)
	}
	defer rows.Close()

	var streams []*models.CachedStream
	for rows.Next() {
		cached := &models.CachedStream{}
		err := scanCachedStream(rows, cached)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stream: %w", err)
		}
		streams = append(streams, cached)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating streams: %w", err)
	}

	return streams, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCachedStream(scanner rowScanner, cached *models.CachedStream) error {
	var movieID sql.NullInt64
	var seriesID sql.NullInt64
	var season sql.NullInt64
	var episode sql.NullInt64
	var rdTorrentID sql.NullString
	var rdLibraryAddedAt sql.NullTime

	if err := scanner.Scan(
		&cached.ID,
		&cached.MediaType,
		&cached.MediaID,
		&movieID,
		&seriesID,
		&season,
		&episode,
		&cached.StreamURL,
		&cached.StreamHash,
		&cached.QualityScore,
		&cached.Resolution,
		&cached.HDRType,
		&cached.AudioFormat,
		&cached.SourceType,
		&cached.FileSizeGB,
		&cached.Codec,
		&cached.Indexer,
		&cached.CachedAt,
		&cached.LastChecked,
		&cached.CheckCount,
		&cached.IsAvailable,
		&cached.UpgradeAvailable,
		&cached.RDLibraryAdded,
		&rdTorrentID,
		&rdLibraryAddedAt,
		&cached.NextCheckAt,
		&cached.CreatedAt,
		&cached.UpdatedAt,
	); err != nil {
		return err
	}

	if rdTorrentID.Valid {
		cached.RDTorrentID = rdTorrentID.String
	} else {
		cached.RDTorrentID = ""
	}

	if rdLibraryAddedAt.Valid {
		cached.RDLibraryAddedAt = &rdLibraryAddedAt.Time
	} else {
		cached.RDLibraryAddedAt = nil
	}

	if movieID.Valid {
		cached.MovieID = int(movieID.Int64)
	} else {
		cached.MovieID = 0
	}

	if seriesID.Valid {
		cached.SeriesID = int(seriesID.Int64)
	} else {
		cached.SeriesID = 0
	}

	if season.Valid {
		cached.Season = int(season.Int64)
	} else {
		cached.Season = 0
	}

	if episode.Valid {
		cached.Episode = int(episode.Int64)
	} else {
		cached.Episode = 0
	}

	return nil
}
