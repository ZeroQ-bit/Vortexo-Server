package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/providers"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services/debrid"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services/streams"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/settings"
)

// CacheScanner handles automatic cache maintenance and upgrades
type CacheScanner struct {
	movieStore      *database.MovieStore
	seriesStore     *database.SeriesStore
	episodeStore    *database.EpisodeStore
	cacheStore      *database.StreamCacheStore
	streamService   *streams.StreamService
	provider        *providers.MultiProvider
	providerMu      sync.RWMutex
	rdSyncMu        sync.Mutex
	rdSyncActive    bool
	rdSyncCooldown  time.Time
	debridService   debrid.DebridService
	settingsManager *settings.Manager
	ticker          *time.Ticker
	stopChan        chan bool
}

const (
	realDebridLibrarySyncDefaultCooldown = 30 * time.Minute
	realDebridLibrarySyncMinCooldown     = 5 * time.Minute
	realDebridLibrarySyncMaxCooldown     = 2 * time.Hour
)

// RealDebridLibrarySyncRateLimitError tells scheduler callers to back off the
// RD library sync worker without losing the normal service interval.
type RealDebridLibrarySyncRateLimitError struct {
	RetryAfter time.Duration
	RetryAt    time.Time
}

func (e *RealDebridLibrarySyncRateLimitError) Error() string {
	if e == nil {
		return "Real-Debrid library sync paused by rate limit"
	}
	if !e.RetryAt.IsZero() {
		return fmt.Sprintf("Real-Debrid library sync paused by rate limit; retry after %s", e.RetryAt.Local().Format("15:04:05"))
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("Real-Debrid library sync paused by rate limit; retry in %s", e.RetryAfter.Round(time.Second))
	}
	return "Real-Debrid library sync paused by rate limit"
}

func RealDebridLibrarySyncRetryDelay(err error) time.Duration {
	var rateErr *RealDebridLibrarySyncRateLimitError
	if errors.As(err, &rateErr) && rateErr.RetryAfter > 0 {
		return rateErr.RetryAfter
	}
	return 0
}

func (cs *CacheScanner) SetProvider(provider *providers.MultiProvider) {
	cs.providerMu.Lock()
	defer cs.providerMu.Unlock()
	cs.provider = provider
}

func (cs *CacheScanner) getProvider() *providers.MultiProvider {
	cs.providerMu.RLock()
	defer cs.providerMu.RUnlock()
	return cs.provider
}

// NewCacheScanner creates a new cache scanner
func NewCacheScanner(
	movieStore *database.MovieStore,
	seriesStore *database.SeriesStore,
	episodeStore *database.EpisodeStore,
	cacheStore *database.StreamCacheStore,
	streamService *streams.StreamService,
	provider *providers.MultiProvider,
	debridService debrid.DebridService,
	settingsManager *settings.Manager,
) *CacheScanner {
	return &CacheScanner{
		movieStore:      movieStore,
		seriesStore:     seriesStore,
		episodeStore:    episodeStore,
		cacheStore:      cacheStore,
		streamService:   streamService,
		provider:        provider,
		debridService:   debridService,
		settingsManager: settingsManager,
		stopChan:        make(chan bool),
	}
}

// Start begins the automatic 24-hour scan cycle
func (cs *CacheScanner) Start() {
	cs.ticker = time.NewTicker(24 * time.Hour)
	go func() {
		// Run once on startup after 5 minutes
		time.Sleep(5 * time.Minute)
		log.Println("[CACHE-SCANNER] Running initial scan...")
		cs.ScanAndUpgrade(context.Background())

		// Then run every 24 hours
		for {
			select {
			case <-cs.ticker.C:
				log.Println("[CACHE-SCANNER] Running scheduled 24-hour scan...")
				cs.ScanAndUpgrade(context.Background())
			case <-cs.stopChan:
				return
			}
		}
	}()
}

// Stop stops the automatic scanning
func (cs *CacheScanner) Stop() {
	if cs.ticker != nil {
		cs.ticker.Stop()
	}
	close(cs.stopChan)
}

// ScanAndUpgrade scans all movies for cache upgrades and empty entries
func (cs *CacheScanner) ScanAndUpgrade(ctx context.Context) error {
	log.Println("[CACHE-SCANNER] Starting library scan for upgrades and empty cache...")

	if cs.shouldAutoAddBestStreamsToRealDebrid() {
		if err := cs.syncPendingRealDebridLibraryAdds(ctx); err != nil {
			log.Printf("[CACHE-SCANNER] Warning: failed to sync existing cached streams to Real-Debrid: %v", err)
		}
	}

	upgraded := 0
	cached := 0
	skipped := 0
	errors := 0
	totalProcessed := 0

	// Process in batches of 5000 to handle large libraries (100k+ movies)
	batchSize := 5000
	offset := 0

	for {
		// Get batch of movies
		movies, err := cs.movieStore.List(ctx, offset, batchSize, nil)
		if err != nil {
			log.Printf("[CACHE-SCANNER] Error getting movies at offset %d: %v", offset, err)
			return err
		}

		if len(movies) == 0 {
			break // No more movies
		}

		log.Printf("[CACHE-SCANNER] Processing batch %d-%d (%d movies)...", offset, offset+len(movies), len(movies))

		for _, movie := range movies {
			// Log progress every 100 movies
			if totalProcessed > 0 && totalProcessed%100 == 0 {
				log.Printf("[CACHE-SCANNER] Progress: %d movies scanned (%d cached, %d upgraded, %d skipped)",
					totalProcessed, cached, upgraded, skipped)
			}
			totalProcessed++

			// Get IMDB ID - log if missing but don't skip
			imdbID, ok := movie.Metadata["imdb_id"].(string)
			if !ok || imdbID == "" {
				log.Printf("[CACHE-SCANNER] ⚠️  Movie %d (%s) missing IMDB ID - skipping", movie.ID, movie.Title)
				skipped++
				continue
			}

			// Check existing cache
			existingCache, err := cs.cacheStore.GetCachedStream(ctx, int(movie.ID))
			if err != nil {
				log.Printf("[CACHE-SCANNER] Error checking cache for movie %d: %v", movie.ID, err)
				errors++
				continue
			}

			// If movie already has cached streams, check if we should upgrade
			if existingCache != nil {
				log.Printf("[CACHE-SCANNER] Movie %d (%s) already cached, skipping (upgrade scanning coming soon)", movie.ID, movie.Title)
				skipped++
				continue
			}

			log.Printf("[CACHE-SCANNER] Movie %d (%s) has no cache, attempting to populate...", movie.ID, movie.Title)

			// Get release year for Torrentio
			releaseYear := 0
			if movie.ReleaseDate != nil && !movie.ReleaseDate.IsZero() {
				releaseYear = movie.ReleaseDate.Year()
			}

			// Use Torrentio with RD integration (pre-filtered for RD availability)
			// Note: RD's instant availability API is currently disabled, so we must use Torrentio
			// which handles RD checking internally via their proxy
			time.Sleep(2 * time.Second) // Rate limit protection

			provider := cs.getProvider()
			if provider == nil {
				log.Printf("[CACHE-SCANNER] No stream provider configured, skipping %s", movie.Title)
				skipped++
				continue
			}

			providerStreams, err := provider.GetMovieStreamsWithYear(imdbID, releaseYear)
			if err != nil {
				log.Printf("[CACHE-SCANNER] Error fetching streams for %s (%s): %v", movie.Title, imdbID, err)
				if isProviderRateLimitError(err) {
					log.Printf("[CACHE-SCANNER] Provider rate limit hit; pausing this cache scan so playback requests can recover")
					return nil
				}
				time.Sleep(5 * time.Second)
				errors++
				continue
			}

			if len(providerStreams) == 0 {
				continue
			}

			log.Printf("[CACHE-SCANNER] Found %d RD-cached streams for %s", len(providerStreams), movie.Title)

			// Apply local quality exclusion filters from settings
			excludedQualities := cs.settingsManager.Get().ExcludedQualities
			if excludedQualities != "" {
				var filteredStreams []providers.TorrentioStream
				for _, stream := range providerStreams {
					parsed := cs.streamService.ParseStreamFromTorrentName(stream.Title, stream.InfoHash, stream.Source, 0)
					if !cs.streamService.ShouldExcludeByQualityType(stream.Title, parsed.Resolution, parsed.HDRType, excludedQualities) {
						filteredStreams = append(filteredStreams, stream)
					} else {
						log.Printf("[CACHE-SCANNER] 🚫 Filtered out stream: %s (excluded quality type)", stream.Title)
					}
				}
				if len(filteredStreams) < len(providerStreams) {
					log.Printf("[CACHE-SCANNER] Filtered %d/%d streams based on quality exclusions", len(providerStreams)-len(filteredStreams), len(providerStreams))
				}
				providerStreams = filteredStreams
			}

			if len(providerStreams) == 0 {
				log.Printf("[CACHE-SCANNER] No streams remaining after quality filtering for %s", movie.Title)
				continue
			}

			// Addon URL already filters content - accept whatever it returns
			log.Printf("[CACHE-SCANNER] Processing %d streams from addon (addon-level filtering already applied)", len(providerStreams))

			// Find best stream (no existing cache since we skip those above)
			var bestStream *providers.TorrentioStream
			bestScore := 0

			for i := range providerStreams {
				// Parse and score
				parsed := cs.streamService.ParseStreamFromTorrentName(
					providerStreams[i].Title,
					providerStreams[i].InfoHash,
					providerStreams[i].Source,
					0,
				)

				quality := streams.StreamQuality{
					Resolution:  parsed.Resolution,
					HDRType:     parsed.HDRType,
					AudioFormat: parsed.AudioFormat,
					Source:      parsed.Source,
					Codec:       parsed.Codec,
					SizeGB:      parsed.SizeGB,
				}
				score := streams.CalculateScore(quality).TotalScore

				// Accept any stream with positive score
				if score > bestScore {
					bestScore = score
					bestStream = &providerStreams[i]
				}
			}

			// Cache or upgrade if we found a better stream
			if bestStream != nil {
				// Extract hash from URL if needed
				hash := bestStream.InfoHash
				if !isValidHash(hash) && bestStream.URL != "" {
					hash = extractHashFromURL(bestStream.URL)
				}
				if !isValidHash(hash) {
					log.Printf("[CACHE-SCANNER] Skipping cache for %s because no valid torrent hash was found in source %q", movie.Title, bestStream.Source)
					continue
				}

				stream := models.TorrentStream{
					Hash:        hash,
					Title:       bestStream.Name,
					TorrentName: bestStream.Title,
					Resolution:  bestStream.Quality,
					SizeGB:      float64(bestStream.Size) / (1024 * 1024 * 1024),
					Indexer:     bestStream.Source,
				}

				// Parse for quality details
				parsed := cs.streamService.ParseStreamFromTorrentName(stream.TorrentName, stream.Hash, stream.Indexer, 0)
				quality := streams.StreamQuality{
					Resolution:  parsed.Resolution,
					HDRType:     parsed.HDRType,
					AudioFormat: parsed.AudioFormat,
					Source:      parsed.Source,
					Codec:       parsed.Codec,
					SizeGB:      parsed.SizeGB,
				}
				stream.QualityScore = streams.CalculateScore(quality).TotalScore
				stream.Resolution = parsed.Resolution
				stream.HDRType = parsed.HDRType
				stream.AudioFormat = parsed.AudioFormat
				stream.Source = parsed.Source
				stream.Codec = parsed.Codec

				// Save to cache
				if err := cs.cacheStore.CacheStream(ctx, int(movie.ID), stream, bestStream.URL); err != nil {
					log.Printf("[CACHE-SCANNER] ❌ Error caching stream for movie %d (%s): %v", movie.ID, movie.Title, err)
					errors++
				} else {
					cached++
					if err := cs.maybeAddCachedStreamToRealDebrid(
						ctx,
						movie.Title,
						stream.Hash,
						bestStream.FileIdx,
						func(torrentID string) error {
							return cs.cacheStore.MarkRealDebridLibraryAddedForMovie(ctx, int(movie.ID), torrentID)
						},
						false,
					); err != nil {
						log.Printf("[CACHE-SCANNER] Warning: failed to add %s to Real-Debrid library: %v", movie.Title, err)
					}
					log.Printf("[CACHE-SCANNER] ✅ Cached: %s | %s | Score: %d", movie.Title, stream.Resolution, stream.QualityScore)
				}
			}
		}

		// Move to next batch
		offset += batchSize

		// Short break between batches to avoid overwhelming the system
		time.Sleep(2 * time.Second)
	}

	log.Printf("[CACHE-SCANNER] Movies scan complete: %d total movies processed, %d newly cached, %d skipped, %d errors",
		totalProcessed, cached, skipped, errors)

	log.Println("[CACHE-SCANNER] Starting series episode scan...")
	seriesScanned, episodesCached, seriesErrors := cs.scanSeries(ctx)
	log.Printf("[CACHE-SCANNER] Series episode scan complete: %d series scanned, %d episodes cached, %d errors",
		seriesScanned, episodesCached, seriesErrors)

	log.Printf("[CACHE-SCANNER] === FULL SCAN COMPLETE ===")
	log.Printf("[CACHE-SCANNER] Movies: %d processed, %d newly cached, %d skipped", totalProcessed, cached, skipped)
	log.Printf("[CACHE-SCANNER] Series: %d scanned, %d episodes cached", seriesScanned, episodesCached)
	log.Printf("[CACHE-SCANNER] Total errors: %d", errors+seriesErrors)

	return nil
}

// scanSeries scans monitored series and caches streams for monitored aired episodes.
func (cs *CacheScanner) scanSeries(ctx context.Context) (int, int, int) {
	scanned := 0
	cached := 0
	errors := 0

	batchSize := 5000
	offset := 0
	monitored := true

	for {
		series, err := cs.seriesStore.List(ctx, offset, batchSize, &monitored)
		if err != nil {
			log.Printf("[CACHE-SCANNER] Error getting series at offset %d: %v", offset, err)
			return scanned, cached, errors + 1
		}

		if len(series) == 0 {
			break
		}

		log.Printf("[CACHE-SCANNER] Processing series batch %d-%d (%d series)...", offset, offset+len(series), len(series))

		for _, s := range series {
			scanned++

			if scanned > 0 && scanned%100 == 0 {
				log.Printf("[CACHE-SCANNER] Series progress: %d scanned, %d cached", scanned, cached)
			}

			imdbID, ok := s.Metadata["imdb_id"].(string)
			if !ok || imdbID == "" {
				continue
			}

			episodes, err := cs.seriesEpisodesToScan(ctx, s)
			if err != nil {
				log.Printf("[CACHE-SCANNER] Error getting episodes for %s: %v", s.Title, err)
				errors++
				continue
			}
			if len(episodes) == 0 {
				continue
			}

			log.Printf("[CACHE-SCANNER] Scanning %d episodes for %s", len(episodes), s.Title)
			for _, ep := range episodes {
				if err := ctx.Err(); err != nil {
					log.Printf("[CACHE-SCANNER] Series episode scan cancelled: %v", err)
					return scanned, cached, errors
				}

				cachedEpisode, err := cs.scanSeriesEpisode(ctx, s, imdbID, ep.SeasonNumber, ep.EpisodeNumber)
				if err != nil {
					log.Printf("[CACHE-SCANNER] Error scanning %s S%02dE%02d: %v", s.Title, ep.SeasonNumber, ep.EpisodeNumber, err)
					errors++
					if isProviderRateLimitError(err) {
						log.Printf("[CACHE-SCANNER] Provider rate limit hit; pausing series scan")
						return scanned, cached, errors
					}
					continue
				}
				if cachedEpisode {
					cached++
				}
			}
		}

		offset += batchSize
		time.Sleep(2 * time.Second)
	}

	return scanned, cached, errors
}

func (cs *CacheScanner) seriesEpisodesToScan(ctx context.Context, s *models.Series) ([]*models.Episode, error) {
	if cs.episodeStore == nil {
		return []*models.Episode{{
			SeriesID:      s.ID,
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Monitored:     true,
		}}, nil
	}

	episodes, err := cs.episodeStore.ListBySeries(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if len(episodes) == 0 {
		log.Printf("[CACHE-SCANNER] No episode metadata for %s; falling back to S01E01", s.Title)
		return []*models.Episode{{
			SeriesID:      s.ID,
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Monitored:     true,
		}}, nil
	}

	return filterSeriesEpisodesForCacheScan(episodes, time.Now()), nil
}

func filterSeriesEpisodesForCacheScan(episodes []*models.Episode, now time.Time) []*models.Episode {
	filtered := make([]*models.Episode, 0, len(episodes))
	for _, ep := range episodes {
		if ep == nil {
			continue
		}
		if !ep.Monitored {
			continue
		}
		if ep.SeasonNumber <= 0 || ep.EpisodeNumber <= 0 {
			continue
		}
		if ep.AirDate != nil && ep.AirDate.After(now) {
			continue
		}
		filtered = append(filtered, ep)
	}
	return filtered
}

func (cs *CacheScanner) scanSeriesEpisode(ctx context.Context, s *models.Series, imdbID string, season, episode int) (bool, error) {
	existsQuery := `SELECT COUNT(*) FROM media_streams WHERE series_id = $1 AND season = $2 AND episode = $3`
	var count int
	if err := cs.cacheStore.GetDB().QueryRowContext(ctx, existsQuery, s.ID, season, episode).Scan(&count); err != nil {
		return false, fmt.Errorf("check existing cache: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	provider := cs.getProvider()
	if provider == nil {
		return false, nil
	}

	time.Sleep(2 * time.Second)
	providerStreams, err := provider.GetSeriesStreams(imdbID, season, episode)
	if err != nil {
		if !isProviderRateLimitError(err) {
			time.Sleep(5 * time.Second)
		}
		return false, fmt.Errorf("fetch streams: %w", err)
	}
	if len(providerStreams) == 0 {
		return false, nil
	}

	excludedQualities := cs.settingsManager.Get().ExcludedQualities
	if excludedQualities != "" {
		var filteredStreams []providers.TorrentioStream
		for _, stream := range providerStreams {
			parsed := cs.streamService.ParseStreamFromTorrentName(stream.Title, stream.InfoHash, stream.Source, 0)
			if !cs.streamService.ShouldExcludeByQualityType(stream.Title, parsed.Resolution, parsed.HDRType, excludedQualities) {
				filteredStreams = append(filteredStreams, stream)
			}
		}
		providerStreams = filteredStreams
	}

	if len(providerStreams) == 0 {
		return false, nil
	}

	for i := range providerStreams {
		hash := providerStreams[i].InfoHash
		if !isValidHash(hash) && providerStreams[i].URL != "" {
			hash = extractHashFromURL(providerStreams[i].URL)
			if isValidHash(hash) {
				providerStreams[i].InfoHash = hash
			}
		}
	}

	bestStream, bestScore := bestTorrentioStream(cs.streamService, providerStreams)
	if bestStream == nil {
		return false, nil
	}

	hash := bestStream.InfoHash
	if !isValidHash(hash) && bestStream.URL != "" {
		hash = extractHashFromURL(bestStream.URL)
	}
	if !isValidHash(hash) {
		log.Printf("[CACHE-SCANNER] Skipping cache for %s S%02dE%02d because no valid torrent hash was found in source %q", s.Title, season, episode, bestStream.Source)
		return false, nil
	}

	parsed := cs.streamService.ParseStreamFromTorrentName(bestStream.Title, hash, bestStream.Source, 0)
	quality := streams.StreamQuality{
		Resolution:  parsed.Resolution,
		HDRType:     parsed.HDRType,
		AudioFormat: parsed.AudioFormat,
		Source:      parsed.Source,
		Codec:       parsed.Codec,
		SizeGB:      parsed.SizeGB,
	}
	qualityScore := streams.CalculateScore(quality).TotalScore
	if qualityScore == 0 {
		qualityScore = bestScore
	}

	insertQuery := `
		INSERT INTO media_streams (
			media_type, media_id, series_id, season, episode, stream_url, stream_hash,
			quality_score, resolution, hdr_type, audio_format,
			source_type, file_size_gb, codec, indexer
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (series_id, season, episode) WHERE series_id IS NOT NULL
		DO UPDATE SET
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
			updated_at = NOW()
	`

	_, err = cs.cacheStore.GetDB().ExecContext(ctx, insertQuery,
		"series", s.ID, s.ID, season, episode, bestStream.URL, hash,
		qualityScore, parsed.Resolution, parsed.HDRType, parsed.AudioFormat,
		parsed.Source, parsed.SizeGB, parsed.Codec, bestStream.Source,
	)
	if err != nil {
		return false, fmt.Errorf("cache stream: %w", err)
	}

	seriesLabel := s.Title + " S" + twoDigit(season) + "E" + twoDigit(episode)
	if err := cs.maybeAddCachedStreamToRealDebrid(
		ctx,
		seriesLabel,
		hash,
		bestStream.FileIdx,
		func(torrentID string) error {
			return cs.cacheStore.MarkRealDebridLibraryAddedForSeriesEpisode(ctx, int(s.ID), season, episode, torrentID)
		},
		false,
	); err != nil {
		log.Printf("[CACHE-SCANNER] Warning: failed to add %s to Real-Debrid library: %v", seriesLabel, err)
	}
	log.Printf("[CACHE-SCANNER] Cached series: %s S%02dE%02d | %s | Score: %d",
		s.Title, season, episode, parsed.Resolution, qualityScore)

	return true, nil
}

func isProviderRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "429") ||
		strings.Contains(message, "too_many_requests") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "rate limited")
}

func bestTorrentioStream(streamService *streams.StreamService, providerStreams []providers.TorrentioStream) (*providers.TorrentioStream, int) {
	var bestStream *providers.TorrentioStream
	bestScore := 0

	for i := range providerStreams {
		if !isValidHash(providerStreams[i].InfoHash) {
			continue
		}
		parsed := streamService.ParseStreamFromTorrentName(
			providerStreams[i].Title,
			providerStreams[i].InfoHash,
			providerStreams[i].Source,
			0,
		)
		quality := streams.StreamQuality{
			Resolution:  parsed.Resolution,
			HDRType:     parsed.HDRType,
			AudioFormat: parsed.AudioFormat,
			Source:      parsed.Source,
			Codec:       parsed.Codec,
			SizeGB:      parsed.SizeGB,
		}
		score := streams.CalculateScore(quality).TotalScore

		if score > bestScore {
			bestScore = score
			bestStream = &providerStreams[i]
		}
	}

	return bestStream, bestScore
}

// isValidHash validates that a string is a 40-character hex v1 torrent hash.
func isValidHash(hash string) bool {
	hash = normalizeTorrentHash(hash)
	if len(hash) != 40 {
		return false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func normalizeTorrentHash(hash string) string {
	hash = strings.TrimSpace(hash)
	hash = strings.TrimPrefix(strings.ToLower(hash), "urn:btih:")
	hash = strings.TrimPrefix(hash, "btih:")
	return hash
}

// CleanupUnreleasedCache removes cached streams for unreleased movies
func (cs *CacheScanner) CleanupUnreleasedCache(ctx context.Context) (int, error) {
	log.Println("[CACHE-SCANNER] Starting cleanup of unreleased content cache...")

	// Query to delete streams for movies with future release dates
	query := `
		DELETE FROM media_streams
		WHERE movie_id IN (
			SELECT id FROM library_movies
			WHERE metadata->>'release_date' IS NOT NULL
			AND (metadata->>'release_date')::date > CURRENT_DATE
		)
	`

	result, err := cs.cacheStore.GetDB().ExecContext(ctx, query)
	if err != nil {
		log.Printf("[CACHE-SCANNER] ❌ Error cleaning unreleased cache: %v", err)
		return 0, err
	}

	rowsDeleted, err := result.RowsAffected()
	if err != nil {
		log.Printf("[CACHE-SCANNER] ❌ Error getting rows affected: %v", err)
		return 0, err
	}

	log.Printf("[CACHE-SCANNER] ✅ Cleaned up %d cached streams for unreleased movies", rowsDeleted)
	return int(rowsDeleted), nil
}

func (cs *CacheScanner) shouldAutoAddBestStreamsToRealDebrid() bool {
	if cs.settingsManager == nil || cs.debridService == nil {
		return false
	}

	current := cs.settingsManager.Get()
	return current != nil && current.UseRealDebrid && current.AutoAddBestStreamsToRealDebrid
}

func (cs *CacheScanner) maybeAddCachedStreamToRealDebrid(ctx context.Context, label, hash string, fileIdx int, markAdded func(string) error, force bool) error {
	hash = normalizeTorrentHash(hash)
	if (!force && !cs.shouldAutoAddBestStreamsToRealDebrid()) || hash == "" {
		return nil
	}
	if !isValidHash(hash) {
		return fmt.Errorf("invalid torrent hash %q", truncateForLog(hash, 12))
	}

	torrentID, err := cs.debridService.AddToLibrary(ctx, hash, fileIdx)
	if err != nil {
		if isRealDebridDuplicateError(err) {
			log.Printf("[CACHE-SCANNER] %s already appears to exist in Real-Debrid, marking as synced locally", label)
			return markAdded("")
		}
		return err
	}

	if err := markAdded(torrentID); err != nil {
		return err
	}

	log.Printf("[CACHE-SCANNER] Added %s to Real-Debrid library (torrent ID: %s)", label, torrentID)
	return nil
}

func realDebridLibrarySyncRateLimitDelay(err error) time.Duration {
	delay := debrid.RateLimitRetryAfter(err)
	if delay <= 0 {
		delay = realDebridLibrarySyncDefaultCooldown
	}
	if delay < realDebridLibrarySyncMinCooldown {
		delay = realDebridLibrarySyncMinCooldown
	}
	if delay > realDebridLibrarySyncMaxCooldown {
		delay = realDebridLibrarySyncMaxCooldown
	}
	return delay
}

func isRealDebridLibraryRateLimitError(err error) bool {
	if debrid.IsRateLimitError(err) {
		return true
	}
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "too_many_requests") ||
		strings.Contains(errText, "error_code\": 34") ||
		strings.Contains(errText, "status 429") ||
		strings.Contains(errText, "rate limited") ||
		strings.Contains(errText, "rate limit")
}

func isRealDebridRejectedSourceError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "status 451") ||
		strings.Contains(errText, "infringing_file") ||
		strings.Contains(errText, "copyright") ||
		strings.Contains(errText, "unavailable for legal reasons")
}

func (cs *CacheScanner) setRealDebridLibrarySyncCooldown(delay time.Duration) time.Time {
	if delay <= 0 {
		delay = realDebridLibrarySyncDefaultCooldown
	}
	retryAt := time.Now().Add(delay)
	cs.rdSyncMu.Lock()
	cs.rdSyncCooldown = retryAt
	cs.rdSyncMu.Unlock()
	return retryAt
}

func realDebridLibraryRateLimitMessage(retryAt time.Time) string {
	if retryAt.IsZero() {
		return "Paused by Real-Debrid rate limit"
	}
	return fmt.Sprintf("Paused by Real-Debrid rate limit; retry after %s", retryAt.Local().Format("15:04:05"))
}

func (cs *CacheScanner) syncPendingRealDebridLibraryAdds(ctx context.Context) error {
	return cs.syncPendingRealDebridLibraryAddsWithMode(ctx, false)
}

func (cs *CacheScanner) SyncPendingRealDebridLibraryAddsNow(ctx context.Context) error {
	return cs.syncPendingRealDebridLibraryAddsWithMode(ctx, true)
}

type pendingRealDebridLibraryAdd struct {
	stream *models.CachedStream
	option bool
}

func (cs *CacheScanner) pendingRealDebridLibraryAdds(ctx context.Context, limit int) ([]pendingRealDebridLibraryAdd, error) {
	if limit <= 0 {
		limit = 100
	}
	legacy, err := cs.cacheStore.GetPendingRealDebridLibraryAdds(ctx, limit)
	if err != nil {
		return nil, err
	}

	pending := make([]pendingRealDebridLibraryAdd, 0, limit)
	for _, stream := range legacy {
		pending = append(pending, pendingRealDebridLibraryAdd{stream: stream})
	}

	remaining := limit - len(pending)
	if remaining <= 0 {
		return pending, nil
	}

	options, err := cs.cacheStore.GetPendingRealDebridStreamOptions(ctx, remaining)
	if err != nil {
		return nil, err
	}
	for _, stream := range options {
		pending = append(pending, pendingRealDebridLibraryAdd{stream: stream, option: true})
	}

	return pending, nil
}

func (cs *CacheScanner) syncPendingRealDebridLibraryAddsWithMode(ctx context.Context, force bool) error {
	const (
		batchSize     = 10
		perItemDelay  = 5 * time.Second
		progressEvery = 10
	)

	cs.rdSyncMu.Lock()
	if cs.rdSyncActive {
		cs.rdSyncMu.Unlock()
		log.Printf("[CACHE-SCANNER] Real-Debrid library sync already running, skipping duplicate request")
		return nil
	}
	now := time.Now()
	if !cs.rdSyncCooldown.IsZero() {
		if delay := time.Until(cs.rdSyncCooldown); delay > 0 {
			retryAt := cs.rdSyncCooldown
			cs.rdSyncMu.Unlock()
			message := realDebridLibraryRateLimitMessage(retryAt)
			services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, 0, 0, message)
			return &RealDebridLibrarySyncRateLimitError{RetryAfter: delay, RetryAt: retryAt}
		}
		if now.After(cs.rdSyncCooldown) {
			cs.rdSyncCooldown = time.Time{}
		}
	}
	cs.rdSyncActive = true
	cs.rdSyncMu.Unlock()
	defer func() {
		cs.rdSyncMu.Lock()
		cs.rdSyncActive = false
		cs.rdSyncMu.Unlock()
	}()

	if !force && !cs.shouldAutoAddBestStreamsToRealDebrid() {
		return nil
	}

	totalSynced := 0
	rdAttempts := 0
	syncedHashTorrentIDs := make(map[string]string)
	waitForRealDebridSlot := func() error {
		if rdAttempts == 0 {
			rdAttempts++
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(perItemDelay):
			rdAttempts++
			return nil
		}
	}

	for {
		pending, err := cs.pendingRealDebridLibraryAdds(ctx, batchSize)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			if totalSynced == 0 {
				log.Printf("[CACHE-SCANNER] Real-Debrid library sync found no pending cached streams")
				services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, 0, 0, "No pending cached streams to add to Real-Debrid")
			}
			if totalSynced > 0 {
				log.Printf("[CACHE-SCANNER] Real-Debrid library sync complete: %d cached streams added", totalSynced)
				services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced, fmt.Sprintf("Added %d cached streams to Real-Debrid", totalSynced))
			}
			return nil
		}

		services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced+len(pending), fmt.Sprintf("Syncing %d cached streams to Real-Debrid", len(pending)))

		syncedThisBatch := 0
		retiredThisBatch := 0
		for _, item := range pending {
			stream := item.stream
			if stream == nil {
				continue
			}
			label := stream.MediaType + " " + strconv.Itoa(stream.MediaID)
			if strings.TrimSpace(stream.StreamTitle) != "" {
				label = stream.StreamTitle
			}
			hash := normalizeTorrentHash(stream.StreamHash)
			if safeHash := extractHashFromURL(stream.StreamURL); isValidHash(safeHash) && safeHash != hash {
				hash = safeHash
				log.Printf("[CACHE-SCANNER] Recovered valid hash from stream URL for %s", label)
			}
			if !isValidHash(hash) {
				log.Printf("[CACHE-SCANNER] Skipping cached stream %s %d because no valid torrent hash is available", stream.MediaType, stream.MediaID)
				markUnavailable := cs.cacheStore.MarkUnavailableByCacheID
				if item.option {
					markUnavailable = cs.cacheStore.MarkStreamOptionUnavailableByID
				}
				if markErr := markUnavailable(ctx, stream.ID, "retired cached stream with no valid torrent hash"); markErr != nil {
					log.Printf("[CACHE-SCANNER] Failed to retire cached stream %s %d with invalid hash: %v", stream.MediaType, stream.MediaID, markErr)
				} else {
					retiredThisBatch++
				}
				continue
			}
			fileIdx := firstNonZero(stream.RDFileID, extractFileIndexFromStreamURL(stream.StreamURL))
			markAdded := cs.cacheStore.MarkRealDebridLibraryAddedByID
			if item.option {
				markAdded = cs.cacheStore.MarkStreamOptionRealDebridAddedByID
			}
			markUnavailable := cs.cacheStore.MarkUnavailableByCacheID
			if item.option {
				markUnavailable = cs.cacheStore.MarkStreamOptionUnavailableByID
			}
			if torrentID, ok := syncedHashTorrentIDs[hash]; ok {
				if markErr := markAdded(ctx, stream.ID, torrentID); markErr != nil {
					log.Printf("[CACHE-SCANNER] Failed to mark duplicate cached stream %s %d as Real-Debrid synced: %v", stream.MediaType, stream.MediaID, markErr)
					continue
				}
				if markErr := cs.cacheStore.MarkRealDebridLibraryAddedByHash(ctx, hash, torrentID); markErr != nil {
					log.Printf("[CACHE-SCANNER] Failed to mark duplicate hash %s as Real-Debrid synced: %v", truncateForLog(hash, 12), markErr)
				}
				totalSynced++
				syncedThisBatch++
				continue
			}
			if err := waitForRealDebridSlot(); err != nil {
				return err
			}
			addedTorrentID := ""
			if err := cs.maybeAddCachedStreamToRealDebrid(
				ctx,
				label,
				hash,
				fileIdx,
				func(torrentID string) error {
					addedTorrentID = torrentID
					if err := markAdded(ctx, stream.ID, torrentID); err != nil {
						return err
					}
					return cs.cacheStore.MarkRealDebridLibraryAddedByHash(ctx, hash, torrentID)
				},
				force,
			); err != nil {
				log.Printf("[CACHE-SCANNER] Failed to sync cached stream %s %d to Real-Debrid: %v", stream.MediaType, stream.MediaID, err)
				if isRealDebridLibraryRateLimitError(err) {
					delay := realDebridLibrarySyncRateLimitDelay(err)
					retryAt := cs.setRealDebridLibrarySyncCooldown(delay)
					log.Printf("[CACHE-SCANNER] Real-Debrid rate limit reached, pausing library sync so we can resume cleanly later")
					services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced+len(pending), realDebridLibraryRateLimitMessage(retryAt))
					return &RealDebridLibrarySyncRateLimitError{RetryAfter: delay, RetryAt: retryAt}
				}
				if isRealDebridInvalidMagnetError(err) {
					message := fmt.Sprintf("retired invalid magnet source: %v", err)
					if markErr := markUnavailable(ctx, stream.ID, truncateForLog(message, 500)); markErr != nil {
						log.Printf("[CACHE-SCANNER] Failed to retire invalid magnet source %s %d: %v", stream.MediaType, stream.MediaID, markErr)
					} else {
						retiredThisBatch++
						log.Printf("[CACHE-SCANNER] Retired invalid magnet source %s %d after Real-Debrid rejected it", stream.MediaType, stream.MediaID)
					}
					continue
				}
				if isRealDebridRejectedSourceError(err) {
					message := fmt.Sprintf("retired Real-Debrid rejected source: %v", err)
					if markErr := markUnavailable(ctx, stream.ID, truncateForLog(message, 500)); markErr != nil {
						log.Printf("[CACHE-SCANNER] Failed to retire Real-Debrid rejected source %s %d: %v", stream.MediaType, stream.MediaID, markErr)
					} else {
						retiredThisBatch++
						log.Printf("[CACHE-SCANNER] Retired Real-Debrid rejected source %s %d", stream.MediaType, stream.MediaID)
					}
					continue
				}
				continue
			}

			syncedHashTorrentIDs[hash] = addedTorrentID
			totalSynced++
			syncedThisBatch++
			services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced+len(pending)-syncedThisBatch, fmt.Sprintf("Added %s to Real-Debrid", label))
			if totalSynced%progressEvery == 0 {
				log.Printf("[CACHE-SCANNER] Real-Debrid library sync progress: %d streams added", totalSynced)
			}
		}

		if syncedThisBatch == 0 && retiredThisBatch == 0 {
			log.Printf("[CACHE-SCANNER] Real-Debrid library sync paused: no items from the current batch could be added")
			services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced+len(pending), "No items from the current batch could be added")
			return nil
		}
		if syncedThisBatch == 0 {
			log.Printf("[CACHE-SCANNER] Real-Debrid library sync retired %d rejected streams; continuing to next batch", retiredThisBatch)
			services.GlobalScheduler.UpdateProgress(services.ServiceRDLibrarySync, totalSynced, totalSynced+len(pending), fmt.Sprintf("Retired %d rejected Real-Debrid streams", retiredThisBatch))
		}
	}
}

func isRealDebridDuplicateError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "exists") ||
		strings.Contains(msg, "active torrent")
}

func isRealDebridInvalidMagnetError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid magnet") ||
		strings.Contains(msg, "magnet_error") ||
		strings.Contains(msg, "invalid torrent hash")
}

func twoDigit(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func extractFileIndexFromStreamURL(streamURL string) int {
	parts := strings.Split(streamURL, "/")
	for i, part := range parts {
		if !isValidHash(part) {
			continue
		}

		if i+2 < len(parts) && strings.EqualFold(parts[i+1], "null") {
			if idx, err := strconv.Atoi(parts[i+2]); err == nil && idx > 0 {
				return idx
			}
		}

		if i+1 < len(parts) {
			if idx, err := strconv.Atoi(parts[i+1]); err == nil && idx > 0 {
				return idx
			}
		}
	}

	return 0
}

func extractHashFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if isValidHash(raw) {
		return normalizeTorrentHash(raw)
	}

	if parsed, err := url.Parse(raw); err == nil {
		for _, key := range []string{"xt", "btih", "infohash", "info_hash", "hash"} {
			for _, value := range parsed.Query()[key] {
				if hash := hashFromValue(value); hash != "" {
					return hash
				}
			}
		}
	}

	if hash := hashFromValue(raw); hash != "" {
		return hash
	}

	tokens := tokenizeURLForHash(raw)
	for i, token := range tokens {
		hash := normalizeTorrentHash(token)
		if !isValidHash(hash) || !hasSafeHashContext(tokens, i) {
			continue
		}
		return hash
	}

	return ""
}

func hashFromValue(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if idx := strings.Index(lower, "urn:btih:"); idx >= 0 {
		start := idx + len("urn:btih:")
		if start+40 <= len(value) {
			hash := value[start : start+40]
			if isValidHash(hash) {
				return normalizeTorrentHash(hash)
			}
		}
	}
	if isValidHash(value) {
		return normalizeTorrentHash(value)
	}
	return ""
}

func tokenizeURLForHash(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '/', '?', '&', '=', ':', '#', '|', ',', ';':
			return true
		default:
			return false
		}
	})
}

func hasSafeHashContext(tokens []string, index int) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if index > 0 && isHashContextToken(tokens[index-1]) {
		return true
	}
	if index > 1 && isHashContextToken(tokens[index-2]) {
		return true
	}
	if index+1 < len(tokens) && (strings.EqualFold(tokens[index+1], "null") || isPositiveInt(tokens[index+1])) {
		return true
	}
	if index+2 < len(tokens) && strings.EqualFold(tokens[index+1], "null") && isPositiveInt(tokens[index+2]) {
		return true
	}
	if index >= 3 && strings.EqualFold(tokens[index-3], "resolve") && isDebridToken(tokens[index-2]) {
		return true
	}
	return false
}

func isHashContextToken(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "btih", "infohash", "info_hash", "hash", "torrent_hash", "magnet":
		return true
	default:
		return false
	}
}

func isDebridToken(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "realdebrid", "real-debrid", "rd", "alldebrid", "all-debrid", "premiumize", "debridlink", "debrid-link":
		return true
	default:
		return false
	}
}

func isPositiveInt(value string) bool {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil && parsed > 0
}

func truncateForLog(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}
