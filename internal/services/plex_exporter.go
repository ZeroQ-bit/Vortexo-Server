package services

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Zerr0-C00L/StreamArr/internal/database"
	"github.com/Zerr0-C00L/StreamArr/internal/models"
	"github.com/Zerr0-C00L/StreamArr/internal/settings"
)

type PlexExporter struct {
	movieStore      *database.MovieStore
	seriesStore     *database.SeriesStore
	streamCache     *database.StreamCacheStore
	settingsManager *settings.Manager
	rdClient        *RealDebridClient
	httpClient      *http.Client
	stopChan        chan struct{}
}

type plexExportStats struct {
	pendingCount     int
	exportedCount    int
	failedCount      int
	missingRDCount   int
	missingFileCount int
	resetRDCount     int
	retiredCount     int
}

type sourceResolutionError struct {
	cause      error
	rdFilename string
	rdStatus   string
	rdLinks    int
}

func (e *sourceResolutionError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *sourceResolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func NewPlexExporter(
	movieStore *database.MovieStore,
	seriesStore *database.SeriesStore,
	streamCache *database.StreamCacheStore,
	settingsManager *settings.Manager,
) *PlexExporter {
	return &PlexExporter{
		movieStore:      movieStore,
		seriesStore:     seriesStore,
		streamCache:     streamCache,
		settingsManager: settingsManager,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		stopChan: make(chan struct{}),
	}
}

func (p *PlexExporter) Start() {
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()

		for {
			select {
			case <-p.stopChan:
				return
			case <-timer.C:
			}

			cfg := p.settingsManager.Get()
			interval := time.Duration(maxInt(cfg.PlexExportIntervalMinutes, 1)) * time.Minute

			if cfg.PlexExportEnabled {
				GlobalScheduler.MarkRunning(ServicePlexExport)
				err := p.ExportPending(context.Background())
				GlobalScheduler.MarkComplete(ServicePlexExport, err, interval)
				if err != nil {
					log.Printf("[PLEX-EXPORT] Export run failed: %v", err)
				}
			}

			timer.Reset(interval)
		}
	}()
}

func (p *PlexExporter) Stop() {
	close(p.stopChan)
}

func (p *PlexExporter) ExportPending(ctx context.Context) error {
	if p.streamCache == nil || p.settingsManager == nil {
		return fmt.Errorf("plex exporter dependencies not initialized")
	}

	cfg := p.settingsManager.Get()
	if !cfg.PlexExportEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.PlexExportMode) == "" {
		cfg.PlexExportMode = "symlink"
	}
	if !strings.EqualFold(cfg.PlexExportMode, "symlink") {
		return fmt.Errorf("unsupported plex export mode %q", cfg.PlexExportMode)
	}

	apiKey := strings.TrimSpace(cfg.RealDebridAPIKey)
	if apiKey == "" {
		return fmt.Errorf("real-debrid api key is required for plex export")
	}
	p.rdClient = NewRealDebridClient(apiKey)

	pending, err := p.streamCache.GetPendingPlexExports(ctx, 100)
	if err != nil {
		return err
	}
	log.Printf("[PLEX-EXPORT] Pending export candidates: %d", len(pending))
	if len(pending) == 0 {
		GlobalScheduler.UpdateProgress(ServicePlexExport, 0, 0, "No pending Plex exports")
		return nil
	}

	GlobalScheduler.UpdateProgress(ServicePlexExport, 0, len(pending), "Exporting cached items to Plex")

	stats := plexExportStats{pendingCount: len(pending)}
	var serviceErr error
	for i, cached := range pending {
		label := fmt.Sprintf("%s #%d", cached.MediaType, cached.MediaID)
		if cached.MediaType == "movie" {
			if movie, getErr := p.movieStore.Get(ctx, int64(cached.MovieID)); getErr == nil {
				label = movie.Title
			}
		} else if cached.MediaType == "series" {
			if series, getErr := p.seriesStore.Get(ctx, int64(cached.SeriesID)); getErr == nil {
				label = fmt.Sprintf("%s S%02dE%02d", series.Title, cached.Season, cached.Episode)
			}
		}

		GlobalScheduler.UpdateProgress(ServicePlexExport, i, len(pending), fmt.Sprintf("Exporting %s", label))
		log.Printf("[PLEX-EXPORT] Candidate %d/%d: %s (cache_id=%d rd_added=%v rd_torrent_id=%q exported=%v)",
			i+1, len(pending), label, cached.ID, cached.RDLibraryAdded, cached.RDTorrentID, cached.PlexExported)

		exportPath, exportErr := p.exportSingle(ctx, cfg, cached)
		if exportErr != nil {
			stats.failedCount++
			if strings.Contains(exportErr.Error(), "missing rd torrent id") {
				stats.missingRDCount++
			}
			if strings.Contains(exportErr.Error(), "could not locate mounted RD file") || strings.Contains(exportErr.Error(), "rd mount path unavailable") {
				stats.missingFileCount++
			}
			log.Printf("[PLEX-EXPORT] Failed to export %s: %v", label, exportErr)
			handled, handleErr := p.handleExportFailure(ctx, cached, label, exportErr)
			if handleErr != nil {
				if serviceErr == nil {
					serviceErr = handleErr
				}
				log.Printf("[PLEX-EXPORT] Failed to persist export failure state for %s: %v", label, handleErr)
			}
			switch handled {
			case "reset-rd":
				stats.resetRDCount++
			case "retired":
				stats.retiredCount++
			}
			if serviceErr == nil && isServiceLevelPlexExportError(exportErr) {
				serviceErr = exportErr
			}
			continue
		}

		if err := p.streamCache.MarkPlexExportedByID(ctx, cached.ID, exportPath); err != nil {
			if serviceErr == nil {
				serviceErr = err
			}
			log.Printf("[PLEX-EXPORT] Failed to mark export complete for %s: %v", label, err)
			continue
		}

		stats.exportedCount++
		log.Printf("[PLEX-EXPORT] Exported %s -> %s", label, exportPath)
		GlobalScheduler.UpdateProgress(ServicePlexExport, i+1, len(pending), fmt.Sprintf("Exported %s", label))
	}

	log.Printf("[PLEX-EXPORT] Run summary: pending=%d exported=%d failed=%d missing_rd_torrent_id=%d missing_source=%d reset_rd=%d retired=%d",
		stats.pendingCount, stats.exportedCount, stats.failedCount, stats.missingRDCount, stats.missingFileCount, stats.resetRDCount, stats.retiredCount)

	if serviceErr != nil {
		return serviceErr
	}

	if stats.failedCount > 0 {
		log.Printf("[PLEX-EXPORT] Completed with item-level skips/failures; returning success so the scheduler reflects exporter health")
		return nil
	}

	return nil
}

func (p *PlexExporter) exportSingle(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream) (string, error) {
	if strings.TrimSpace(cached.RDTorrentID) == "" {
		return "", fmt.Errorf("missing rd torrent id")
	}
	if err := p.validateExportEligibility(ctx, cached); err != nil {
		return "", err
	}

	sourcePath, err := p.resolveSourcePath(ctx, cfg, cached)
	if err != nil {
		return "", err
	}
	log.Printf("[PLEX-EXPORT] Resolved source path for cache_id=%d: %s", cached.ID, sourcePath)

	destPath, sectionID, err := p.buildDestinationPath(ctx, cfg, cached, sourcePath)
	if err != nil {
		return "", err
	}
	log.Printf("[PLEX-EXPORT] Computed destination for cache_id=%d: %s", cached.ID, destPath)

	if err := ensureDir(filepath.Dir(destPath)); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	if err := p.ensureSymlink(sourcePath, destPath); err != nil {
		return "", err
	}

	if cfg.PlexRefreshEnabled {
		if refreshErr := p.refreshPlexPath(ctx, cfg, sectionID, filepath.Dir(destPath)); refreshErr != nil {
			log.Printf("[PLEX-EXPORT] Plex refresh warning for %s: %v", destPath, refreshErr)
		}
	}

	return destPath, nil
}

func (p *PlexExporter) validateExportEligibility(ctx context.Context, cached *models.CachedStream) error {
	now := time.Now()

	switch cached.MediaType {
	case "movie":
		movie, err := p.movieStore.Get(ctx, int64(cached.MovieID))
		if err != nil {
			return fmt.Errorf("load movie metadata: %w", err)
		}
		if movie.ReleaseDate != nil && movie.ReleaseDate.After(now) {
			return fmt.Errorf("movie is unreleased until %s", movie.ReleaseDate.Format("2006-01-02"))
		}
	case "series":
		series, err := p.seriesStore.Get(ctx, int64(cached.SeriesID))
		if err != nil {
			return fmt.Errorf("load series metadata: %w", err)
		}
		if series.FirstAirDate != nil && series.FirstAirDate.After(now) {
			return fmt.Errorf("series is unreleased until %s", series.FirstAirDate.Format("2006-01-02"))
		}
	}

	return nil
}

func (p *PlexExporter) resolveSourcePath(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream) (string, error) {
	rdMount := filepath.Clean(strings.TrimSpace(cfg.PlexExportRDMountPath))
	if rdMount == "." || rdMount == "" {
		return "", fmt.Errorf("plex export rd mount path is required")
	}
	roots := candidateMountRoots(rdMount)
	log.Printf("[PLEX-EXPORT] Looking for source file under RD mount roots %v for cache_id=%d torrent_id=%q", roots, cached.ID, cached.RDTorrentID)

	info, err := p.rdClient.GetTorrentInfo(ctx, cached.RDTorrentID)
	if err != nil {
		return "", &sourceResolutionError{
			cause: fmt.Errorf("load rd torrent info: %w", err),
		}
	}
	log.Printf("[PLEX-EXPORT] RD torrent info for cache_id=%d: filename=%q status=%q links=%d",
		cached.ID, info.Filename, info.Status, len(info.Links))

	availableRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if stat, statErr := os.Stat(root); statErr == nil && stat.IsDir() {
			availableRoots = append(availableRoots, root)
		}
	}
	if len(availableRoots) == 0 {
		if _, statErr := os.Stat(rdMount); statErr != nil {
			return "", &sourceResolutionError{
				cause:      fmt.Errorf("rd mount path unavailable: %w", statErr),
				rdFilename: info.Filename,
				rdStatus:   info.Status,
				rdLinks:    len(info.Links),
			}
		}
		return "", &sourceResolutionError{
			cause:      fmt.Errorf("rd mount path is not a directory"),
			rdFilename: info.Filename,
			rdStatus:   info.Status,
			rdLinks:    len(info.Links),
		}
	}

	hintTitle, hintYear := p.buildMediaSearchHints(ctx, cached)

	for _, root := range availableRoots {
		var candidates []string
		if name := strings.TrimSpace(info.Filename); name != "" {
			candidates = append(candidates,
				filepath.Join(root, name),
				filepath.Join(root, filepath.Base(name)),
			)
		}

		for _, candidate := range candidates {
			resolved, ok := resolveCandidatePath(candidate)
			if ok {
				log.Printf("[PLEX-EXPORT] Matched direct candidate for cache_id=%d under %s: %s", cached.ID, root, resolved)
				return resolved, nil
			}
		}

		match, matchErr := findBestMountedMatch(root, info.Filename, cached, hintTitle, hintYear)
		if matchErr != nil {
			return "", matchErr
		}
		if match != "" {
			log.Printf("[PLEX-EXPORT] Matched fallback candidate for cache_id=%d under %s: %s", cached.ID, root, match)
			return match, nil
		}

		log.Printf("[PLEX-EXPORT] No source match found under %s for cache_id=%d; top-level entries: %s", root, cached.ID, summarizeTopLevelEntries(root, 12))
	}

	return "", &sourceResolutionError{
		cause:      fmt.Errorf("could not locate mounted RD file for torrent %s", cached.RDTorrentID),
		rdFilename: info.Filename,
		rdStatus:   info.Status,
		rdLinks:    len(info.Links),
	}
}

func (p *PlexExporter) buildMediaSearchHints(ctx context.Context, cached *models.CachedStream) (string, int) {
	switch cached.MediaType {
	case "movie":
		if cached.MovieID > 0 {
			if movie, err := p.movieStore.Get(ctx, int64(cached.MovieID)); err == nil && movie != nil {
				year := movie.Year
				if year == 0 && movie.ReleaseDate != nil {
					year = movie.ReleaseDate.Year()
				}
				return movie.Title, year
			}
		}
	case "series":
		if cached.SeriesID > 0 {
			if series, err := p.seriesStore.Get(ctx, int64(cached.SeriesID)); err == nil && series != nil {
				year := series.Year
				if year == 0 && series.FirstAirDate != nil {
					year = series.FirstAirDate.Year()
				}
				return series.Title, year
			}
		}
	}
	return "", 0
}

func (p *PlexExporter) buildDestinationPath(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream, sourcePath string) (string, string, error) {
	ext := filepath.Ext(sourcePath)
	if ext == "" {
		ext = ".mkv"
	}

	switch cached.MediaType {
	case "movie":
		root := filepath.Clean(strings.TrimSpace(cfg.PlexExportMoviesPath))
		if root == "." || root == "" {
			return "", "", fmt.Errorf("plex movies path is required")
		}
		movie, err := p.movieStore.Get(ctx, int64(cached.MovieID))
		if err != nil {
			return "", "", fmt.Errorf("load movie metadata: %w", err)
		}
		title := sanitizePathComponent(movie.Title)
		year := movie.Year
		if year == 0 && movie.ReleaseDate != nil {
			year = movie.ReleaseDate.Year()
		}
		folderBase := title
		if year > 0 {
			folderBase = fmt.Sprintf("%s (%d)", title, year)
		}
		folderName := folderBase
		if movie.TMDBID > 0 {
			folderName = fmt.Sprintf("%s {tmdb-%d}", folderBase, movie.TMDBID)
		}
		fileName := folderBase
		return filepath.Join(root, folderName, fileName+ext), strings.TrimSpace(cfg.PlexMoviesSectionID), nil

	case "series":
		root := filepath.Clean(strings.TrimSpace(cfg.PlexExportShowsPath))
		if root == "." || root == "" {
			return "", "", fmt.Errorf("plex shows path is required")
		}
		series, err := p.seriesStore.Get(ctx, int64(cached.SeriesID))
		if err != nil {
			return "", "", fmt.Errorf("load series metadata: %w", err)
		}
		title := sanitizePathComponent(series.Title)
		year := series.Year
		if year == 0 && series.FirstAirDate != nil {
			year = series.FirstAirDate.Year()
		}
		showBase := title
		if year > 0 {
			showBase = fmt.Sprintf("%s (%d)", title, year)
		}
		showFolder := showBase
		if tvdbID := p.resolveSeriesTVDBID(ctx, cfg, series); tvdbID > 0 {
			showFolder = fmt.Sprintf("%s {tvdb-%d}", showBase, tvdbID)
		}
		seasonFolder := fmt.Sprintf("Season %02d", cached.Season)
		fileName := fmt.Sprintf("%s - s%02de%02d%s", title, cached.Season, cached.Episode, ext)
		return filepath.Join(root, showFolder, seasonFolder, fileName), strings.TrimSpace(cfg.PlexShowsSectionID), nil

	default:
		return "", "", fmt.Errorf("unsupported media type %q", cached.MediaType)
	}
}

func (p *PlexExporter) resolveSeriesTVDBID(ctx context.Context, cfg *settings.Settings, series *models.Series) int {
	if series == nil {
		return 0
	}
	if series.Metadata != nil {
		if tvdbID := metadataInt(series.Metadata["tvdb_id"]); tvdbID > 0 {
			return tvdbID
		}
	}
	if series.TMDBID <= 0 {
		return 0
	}
	apiKey := strings.TrimSpace(cfg.TMDBAPIKey)
	if apiKey == "" {
		return 0
	}
	tmdbClient := NewTMDBClient(apiKey)
	externalIDs, err := tmdbClient.GetSeriesExternalIDs(ctx, series.TMDBID)
	if err != nil {
		log.Printf("[PLEX-EXPORT] Could not resolve TVDB ID for %s: %v", series.Title, err)
		return 0
	}
	return externalIDs.TVDBID
}

func (p *PlexExporter) ensureSymlink(sourcePath, destPath string) error {
	if existingInfo, err := os.Lstat(destPath); err == nil {
		if existingInfo.Mode()&os.ModeSymlink != 0 {
			currentTarget, readErr := os.Readlink(destPath)
			if readErr == nil {
				absCurrent, _ := filepath.Abs(currentTarget)
				absSource, _ := filepath.Abs(sourcePath)
				if absCurrent == absSource || currentTarget == sourcePath {
					log.Printf("[PLEX-EXPORT] Existing symlink already correct: %s -> %s", destPath, sourcePath)
					return nil
				}
			}
			if removeErr := os.Remove(destPath); removeErr != nil {
				return fmt.Errorf("replace existing symlink: %w", removeErr)
			}
			log.Printf("[PLEX-EXPORT] Replacing existing symlink at %s", destPath)
		} else {
			log.Printf("[PLEX-EXPORT] Destination already exists as regular file, leaving untouched: %s", destPath)
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination path: %w", err)
	}

	if err := os.Symlink(sourcePath, destPath); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	log.Printf("[PLEX-EXPORT] Created symlink: %s -> %s", destPath, sourcePath)
	return nil
}

func (p *PlexExporter) refreshPlexPath(ctx context.Context, cfg *settings.Settings, sectionID, targetPath string) error {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.PlexURL), "/")
	token := strings.TrimSpace(cfg.PlexToken)
	if baseURL == "" || token == "" || sectionID == "" {
		return nil
	}

	refreshURL, err := url.Parse(baseURL + "/library/sections/" + sectionID + "/refresh")
	if err != nil {
		return err
	}
	query := refreshURL.Query()
	query.Set("path", targetPath)
	query.Set("X-Plex-Token", token)
	refreshURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refreshURL.String(), nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plex refresh returned %s", resp.Status)
	}
	return nil
}

func resolveCandidatePath(path string) (string, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if stat.IsDir() {
		file, err := findLargestVideoFile(path)
		if err != nil || file == "" {
			return "", false
		}
		return file, true
	}
	return path, true
}

func findBestMountedMatch(root, torrentName string, cached *models.CachedStream, mediaTitle string, mediaYear int) (string, error) {
	torrentBase := strings.ToLower(filepath.Base(strings.TrimSpace(torrentName)))
	normalizedTorrentBase := normalizeMatchString(torrentBase)
	normalizedMediaTitle := normalizeMatchString(mediaTitle)
	yearToken := ""
	if mediaYear > 0 {
		yearToken = strconv.Itoa(mediaYear)
	}
	episodeToken := ""
	if cached.MediaType == "series" {
		episodeToken = fmt.Sprintf("s%02de%02d", cached.Season, cached.Episode)
	}

	type candidate struct {
		path  string
		score int
		size  int64
	}

	var matches []candidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := strings.ToLower(d.Name())
		normalizedName := normalizeMatchString(name)
		score := 0
		torrentSignal := false
		titleSignal := false
		yearSignal := false
		hashSignal := false
		episodeSignal := false

		if torrentBase != "" && name == torrentBase {
			score += 100
			torrentSignal = true
		}
		if torrentBase != "" && strings.Contains(name, torrentBase) {
			score += 50
			torrentSignal = true
		}
		if normalizedTorrentBase != "" && normalizedName == normalizedTorrentBase {
			score += 90
			torrentSignal = true
		}
		if normalizedTorrentBase != "" && strings.Contains(normalizedName, normalizedTorrentBase) {
			score += 40
			torrentSignal = true
		}
		if normalizedMediaTitle != "" && strings.Contains(normalizedName, normalizedMediaTitle) {
			score += 35
			titleSignal = true
		}
		if yearToken != "" && strings.Contains(normalizedName, yearToken) {
			score += 10
			yearSignal = true
		}
		if cached.StreamHash != "" && strings.Contains(name, strings.ToLower(cached.StreamHash)) {
			score += 25
			hashSignal = true
		}
		if episodeToken != "" && strings.Contains(name, episodeToken) {
			score += 20
			episodeSignal = true
		}

		strongSignal := torrentSignal || hashSignal || episodeSignal || (titleSignal && (yearToken == "" || yearSignal))
		if score == 0 || !strongSignal {
			return nil
		}

		if d.IsDir() {
			videoPath, fileErr := findLargestVideoFile(path)
			if fileErr == nil && videoPath != "" {
				if info, statErr := os.Stat(videoPath); statErr == nil {
					matches = append(matches, candidate{path: videoPath, score: score, size: info.Size()})
				}
			}
			return nil
		}

		if !isVideoFile(path) {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		matches = append(matches, candidate{path: path, score: score, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", nil
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].size > matches[j].size
		}
		return matches[i].score > matches[j].score
	})

	return matches[0].path, nil
}

func findLargestVideoFile(root string) (string, error) {
	type candidate struct {
		path string
		size int64
	}

	var matches []candidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isVideoFile(path) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		matches = append(matches, candidate{path: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].size > matches[j].size
	})
	return matches[0].path, nil
}

func isVideoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts":
		return true
	default:
		return false
	}
}

func isKnownNonMediaFilename(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".txt", ".jpg", ".jpeg", ".png", ".gif", ".nfo", ".srt", ".sub", ".ass", ".ssa", ".sfv":
		return true
	default:
		return false
	}
}

func isServiceLevelPlexExportError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "rd mount path unavailable") || strings.Contains(err.Error(), "rd mount path is not a directory") {
		return true
	}
	return false
}

func (p *PlexExporter) handleExportFailure(ctx context.Context, cached *models.CachedStream, label string, exportErr error) (string, error) {
	action := "failed"
	message := truncateString(exportErr.Error(), 500)

	var srcErr *sourceResolutionError
	if errors.As(exportErr, &srcErr) {
		if shouldRetireUnplayableSource(srcErr) {
			action = "retired"
			message = truncateString(fmt.Sprintf("retired unplayable rd source: %s", exportErr.Error()), 500)
			if err := p.streamCache.MarkUnavailableByCacheID(ctx, cached.ID, message); err != nil {
				return action, err
			}
			log.Printf("[PLEX-EXPORT] Retired %s from export queue because RD torrent payload is not playable", label)
			return action, nil
		}
		if shouldResetRDLibraryState(srcErr) {
			action = "reset-rd"
			message = truncateString(fmt.Sprintf("reset rd state after export failure: %s", exportErr.Error()), 500)
			if err := p.streamCache.ResetRDLibraryByCacheID(ctx, cached.ID, message); err != nil {
				return action, err
			}
			log.Printf("[PLEX-EXPORT] Reset RD library state for %s so a future sync can rebuild it cleanly", label)
			return action, nil
		}
	}

	if err := p.streamCache.MarkPlexExportFailedByID(ctx, cached.ID, message); err != nil {
		return action, err
	}
	return action, nil
}

func shouldRetireUnplayableSource(err *sourceResolutionError) bool {
	if err == nil {
		return false
	}
	if isKnownNonMediaFilename(err.rdFilename) {
		return true
	}
	if strings.Contains(err.Error(), "could not locate mounted RD file") &&
		strings.EqualFold(strings.TrimSpace(err.rdStatus), "downloaded") &&
		err.rdLinks > 0 &&
		isKnownNonMediaFilename(err.rdFilename) {
		return true
	}
	return false
}

func shouldResetRDLibraryState(err *sourceResolutionError) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown_ressource") || strings.Contains(msg, "status 404") {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(err.rdStatus))
	if status == "error" {
		return true
	}
	return false
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	replacer := strings.NewReplacer(
		"/", " ",
		"\\", " ",
		":", " -",
		"*", "",
		"?", "",
		"\"", "'",
		"<", "",
		">", "",
		"|", "",
	)
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimSpace(value)
}

func normalizeMatchString(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastWasSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastWasSpace = false
			continue
		}
		if !lastWasSpace {
			b.WriteByte(' ')
			lastWasSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func candidateMountRoots(primary string) []string {
	seen := map[string]struct{}{}
	var roots []string
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}

	add(primary)
	switch primary {
	case "/mnt/rd":
		add("/mount/rd")
		add("/mount")
	case "/mount":
		add("/mount/rd")
		add("/mnt/rd")
	case "/mount/rd":
		add("/mnt/rd")
		add("/mount")
	}
	return roots
}

func summarizeTopLevelEntries(root string, limit int) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Sprintf("unreadable (%v)", err)
	}
	if len(entries) == 0 {
		return "(empty)"
	}
	if limit <= 0 {
		limit = 10
	}
	names := make([]string, 0, minInt(len(entries), limit))
	for i, entry := range entries {
		if i >= limit {
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if len(entries) > limit {
		names = append(names, fmt.Sprintf("...+%d more", len(entries)-limit))
	}
	return strings.Join(names, ", ")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateString(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func metadataInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func parseSectionID(value string) int {
	id, _ := strconv.Atoi(strings.TrimSpace(value))
	return id
}
