package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	isettings "github.com/ZeroQ-bit/Vortexo-Server/internal/settings"
)

const (
	rdWebDAVLibrarySource = "rd_webdav"
	rdWebDAVRemoteName    = "rdwebdav"
	rdWebDAVMinVideoBytes = 100 << 20
	rdWebDAVMountTimeout  = 30 * time.Second
)

var (
	rdWebDAVTMDBPattern        = regexp.MustCompile(`(?i)[\[\{\(]tmdb[-_:\s]?(\d+)[\]\}\)]`)
	rdWebDAVUnsafePathPattern  = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	rdWebDAVMultiSpacePattern  = regexp.MustCompile(`\s+`)
	rdWebDAVVideoExtensions    = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true}
	rdWebDAVScannerSkipFolders = map[string]bool{".cache": true, ".git": true, ".trash": true, "@eadir": true, "sample": true, "samples": true}
)

type RDWebDAVLibraryBuilder struct {
	movieStore     *database.MovieStore
	seriesStore    *database.SeriesStore
	episodeStore   *database.EpisodeStore
	tmdbClient     *TMDBClient
	settingsGetter func() *isettings.Settings

	mu       sync.Mutex
	mountCmd *exec.Cmd
}

type RDWebDAVLibrarySummary struct {
	FilesSeen        int `json:"files_seen"`
	VideoFiles       int `json:"video_files"`
	MoviesAdded      int `json:"movies_added"`
	MoviesUpdated    int `json:"movies_updated"`
	SeriesAdded      int `json:"series_added"`
	SeriesUpdated    int `json:"series_updated"`
	EpisodesAdded    int `json:"episodes_added"`
	EpisodesUpdated  int `json:"episodes_updated"`
	SymlinksCreated  int `json:"symlinks_created"`
	SymlinksUpdated  int `json:"symlinks_updated"`
	StaleLinksPruned int `json:"stale_links_pruned"`
	Skipped          int `json:"skipped"`
	Errors           int `json:"errors"`
}

type rdWebDAVMediaCandidate struct {
	MediaType  string
	Title      string
	Year       int
	Season     int
	Episode    int
	TMDBID     int
	SourcePath string
	SizeBytes  int64
	Ext        string
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len()+len(p) > 16*1024 {
		b.buf.Reset()
	}
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func NewRDWebDAVLibraryBuilder(
	movieStore *database.MovieStore,
	seriesStore *database.SeriesStore,
	episodeStore *database.EpisodeStore,
	tmdbClient *TMDBClient,
	settingsGetter func() *isettings.Settings,
) *RDWebDAVLibraryBuilder {
	return &RDWebDAVLibraryBuilder{
		movieStore:     movieStore,
		seriesStore:    seriesStore,
		episodeStore:   episodeStore,
		tmdbClient:     tmdbClient,
		settingsGetter: settingsGetter,
	}
}

func (b *RDWebDAVLibraryBuilder) UpdateTMDBClient(tmdbClient *TMDBClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tmdbClient = tmdbClient
}

func (b *RDWebDAVLibraryBuilder) Build(ctx context.Context) (*RDWebDAVLibrarySummary, error) {
	summary := &RDWebDAVLibrarySummary{}
	if b == nil || b.movieStore == nil || b.seriesStore == nil || b.episodeStore == nil {
		return summary, fmt.Errorf("RD WebDAV library builder is not initialized")
	}
	cfg := b.currentSettings()
	if cfg == nil || !cfg.RDWebDAVLibraryEnabled {
		GlobalScheduler.UpdateProgress(ServiceRDWebDAVLibrary, 0, 0, "RD WebDAV library scan disabled")
		return summary, nil
	}
	if b.tmdbClient == nil || strings.TrimSpace(cfg.TMDBAPIKey) == "" {
		return summary, fmt.Errorf("TMDB API key is required for RD WebDAV library matching")
	}

	mountPath := cleanConfiguredPath(cfg.RDWebDAVMountPath, "/mnt/rd")
	libraryPath := cleanConfiguredPath(cfg.RDWebDAVLibraryPath, "/app/rd-library")
	if cfg.RDWebDAVMountEnabled {
		if err := b.ensureRcloneMount(ctx, cfg, mountPath); err != nil {
			return summary, err
		}
	}
	if err := ensureReadableDir(mountPath); err != nil {
		return summary, fmt.Errorf("RD WebDAV mount path is not readable: %w", err)
	}
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		return summary, fmt.Errorf("create clean library path: %w", err)
	}

	files, err := collectRDWebDAVVideoFiles(ctx, mountPath, libraryPath, summary)
	if err != nil {
		return summary, err
	}
	summary.VideoFiles = len(files)
	if len(files) == 0 {
		GlobalScheduler.UpdateProgress(ServiceRDWebDAVLibrary, 0, 0, "No video files found in RD WebDAV mount")
		return summary, nil
	}

	for idx, sourcePath := range files {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}

		GlobalScheduler.UpdateProgress(ServiceRDWebDAVLibrary, idx+1, len(files), fmt.Sprintf("Matching %s", filepath.Base(sourcePath)))
		candidate, ok := parseRDWebDAVMediaFile(sourcePath)
		if !ok {
			summary.Skipped++
			continue
		}
		if info, err := os.Stat(sourcePath); err == nil {
			candidate.SizeBytes = info.Size()
		}

		switch candidate.MediaType {
		case "episode":
			created, updated, linkState, err := b.importWebDAVEpisode(ctx, cfg, candidate, libraryPath)
			if err != nil {
				summary.Errors++
				log.Printf("[RD WebDAV] Episode import failed for %s: %v", sourcePath, err)
				continue
			}
			if created {
				summary.SeriesAdded++
			} else {
				summary.SeriesUpdated++
			}
			if updated {
				summary.EpisodesUpdated++
			} else {
				summary.EpisodesAdded++
			}
			recordSymlinkState(summary, linkState)
		case "movie":
			created, linkState, err := b.importWebDAVMovie(ctx, cfg, candidate, libraryPath)
			if err != nil {
				summary.Errors++
				log.Printf("[RD WebDAV] Movie import failed for %s: %v", sourcePath, err)
				continue
			}
			if created {
				summary.MoviesAdded++
			} else {
				summary.MoviesUpdated++
			}
			recordSymlinkState(summary, linkState)
		default:
			summary.Skipped++
		}
	}

	if cfg.RDWebDAVCleanStaleSymlinks {
		pruned, err := pruneDeadSymlinks(libraryPath)
		if err != nil {
			summary.Errors++
			log.Printf("[RD WebDAV] Stale symlink cleanup failed: %v", err)
		}
		summary.StaleLinksPruned = pruned
	}

	GlobalScheduler.UpdateProgress(
		ServiceRDWebDAVLibrary,
		len(files),
		len(files),
		fmt.Sprintf("RD WebDAV scan complete: %d movies, %d episodes, %d symlinks", summary.MoviesAdded+summary.MoviesUpdated, summary.EpisodesAdded+summary.EpisodesUpdated, summary.SymlinksCreated+summary.SymlinksUpdated),
	)
	log.Printf("[RD WebDAV] Scan complete: %+v", summary)
	return summary, nil
}

func (b *RDWebDAVLibraryBuilder) currentSettings() *isettings.Settings {
	if b.settingsGetter == nil {
		return nil
	}
	return b.settingsGetter()
}

func (b *RDWebDAVLibraryBuilder) ensureRcloneMount(ctx context.Context, cfg *isettings.Settings, mountPath string) error {
	if isRDWebDAVRcloneMountedPath(mountPath) {
		return nil
	}
	if strings.TrimSpace(cfg.RDWebDAVUsername) == "" || strings.TrimSpace(cfg.RDWebDAVPassword) == "" {
		return fmt.Errorf("RD WebDAV rclone mount is enabled but username/password are missing")
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		return fmt.Errorf("rclone is not installed in this container: %w", err)
	}
	if err := ensureFuseDeviceReady(); err != nil {
		return err
	}
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return fmt.Errorf("create rclone mount path: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mountCmd != nil && b.mountCmd.Process != nil {
		if isRDWebDAVRcloneMountedPath(mountPath) {
			return nil
		}
		return fmt.Errorf("rclone mount process is running but %s is not mounted yet", mountPath)
	}

	obscuredPass, err := obscureRclonePassword(ctx, cfg.RDWebDAVPassword)
	if err != nil {
		return err
	}
	configPath := filepath.Join("/app/cache", "rclone-rd-webdav.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create rclone config dir: %w", err)
	}
	config := fmt.Sprintf("[%s]\ntype = webdav\nurl = %s\nvendor = other\nuser = %s\npass = %s\n", rdWebDAVRemoteName, strings.TrimSpace(cfg.RDWebDAVURL), strings.TrimSpace(cfg.RDWebDAVUsername), obscuredPass)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write rclone config: %w", err)
	}

	args := buildRDWebDAVRcloneMountArgs(configPath, mountPath)
	cmd := exec.CommandContext(context.Background(), "rclone", args...)
	var rcloneOutput synchronizedBuffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &rcloneOutput)
	cmd.Stderr = io.MultiWriter(os.Stderr, &rcloneOutput)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start rclone mount: %w", err)
	}
	b.mountCmd = cmd
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		done <- err
		if err != nil {
			log.Printf("[RD WebDAV] rclone mount exited: %v output=%s", err, compactRcloneOutput(rcloneOutput.String()))
		} else {
			log.Printf("[RD WebDAV] rclone mount exited normally")
		}
		b.mu.Lock()
		if b.mountCmd == cmd {
			b.mountCmd = nil
		}
		b.mu.Unlock()
	}()

	deadline := time.Now().Add(rdWebDAVMountTimeout)
	for time.Now().Before(deadline) {
		if isRDWebDAVRcloneMountedPath(mountPath) {
			log.Printf("[RD WebDAV] rclone mounted %s", mountPath)
			return nil
		}
		select {
		case err := <-done:
			output := compactRcloneOutput(rcloneOutput.String())
			if err != nil {
				return fmt.Errorf("rclone mount exited before %s became ready: %w: %s", mountPath, err, output)
			}
			return fmt.Errorf("rclone mount exited before %s became ready: %s", mountPath, output)
		default:
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("rclone mount did not become ready at %s after %s; recent output: %s", mountPath, rdWebDAVMountTimeout, compactRcloneOutput(rcloneOutput.String()))
}

func buildRDWebDAVRcloneMountArgs(configPath, mountPath string) []string {
	return []string{
		"--config", configPath,
		"mount", rdWebDAVRemoteName + ":", mountPath,
		"--read-only",
		"--allow-other",
		"--allow-non-empty",
		"--dir-cache-time", "30s",
		"--poll-interval", "0",
		"--vfs-cache-mode", "off",
		"--umask", "002",
		"--log-level", "INFO",
	}
}

func obscureRclonePassword(ctx context.Context, password string) (string, error) {
	cmd := exec.CommandContext(ctx, "rclone", "obscure", password)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rclone obscure failed: %w", err)
	}
	obscured := strings.TrimSpace(string(output))
	if obscured == "" {
		return "", fmt.Errorf("rclone obscure returned an empty password")
	}
	return obscured, nil
}

func ensureFuseDeviceReady() error {
	info, err := os.Stat("/dev/fuse")
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("FUSE device /dev/fuse is not available; restart the container with /dev/fuse mounted and SYS_ADMIN capability, or disable server-managed rclone mount and mount Real-Debrid externally")
		}
		return fmt.Errorf("check FUSE device /dev/fuse: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("FUSE device /dev/fuse is a directory, expected a character device")
	}
	return nil
}

func compactRcloneOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return "no rclone output"
	}
	output = rdWebDAVMultiSpacePattern.ReplaceAllString(output, " ")
	const maxLen = 600
	if len(output) > maxLen {
		return output[len(output)-maxLen:]
	}
	return output
}

func collectRDWebDAVVideoFiles(ctx context.Context, mountPath, libraryPath string, summary *RDWebDAVLibrarySummary) ([]string, error) {
	var files []string
	mountPath, _ = filepath.Abs(mountPath)
	libraryPath, _ = filepath.Abs(libraryPath)
	err := filepath.WalkDir(mountPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if summary != nil {
				summary.Errors++
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if sameOrDescendant(path, libraryPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := strings.ToLower(d.Name())
			if path != mountPath && (strings.HasPrefix(name, ".") || rdWebDAVScannerSkipFolders[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if summary != nil {
			summary.FilesSeen++
		}
		if !isRDWebDAVVideoFile(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if summary != nil {
				summary.Errors++
			}
			return nil
		}
		if info.Size() > 0 && info.Size() < rdWebDAVMinVideoBytes {
			if summary != nil {
				summary.Skipped++
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func parseRDWebDAVMediaFile(sourcePath string) (rdWebDAVMediaCandidate, bool) {
	base := filepath.Base(sourcePath)
	parent := filepath.Base(filepath.Dir(sourcePath))
	grandparent := filepath.Base(filepath.Dir(filepath.Dir(sourcePath)))
	tmdbID := extractRDWebDAVTMDBID(base + " " + parent + " " + grandparent)

	candidates := []string{base, parent + " " + base, grandparent + " " + parent + " " + base}
	for _, name := range candidates {
		parsed, ok := parseDMMFilename(name)
		if !ok {
			continue
		}
		mediaType := parsed.MediaType
		if mediaType == "series" {
			mediaType = "episode"
		}
		out := rdWebDAVMediaCandidate{
			MediaType:  mediaType,
			Title:      parsed.Title,
			Year:       parsed.Year,
			Season:     parsed.Season,
			Episode:    parsed.Episode,
			TMDBID:     tmdbID,
			SourcePath: sourcePath,
			Ext:        strings.ToLower(filepath.Ext(sourcePath)),
		}
		if out.MediaType != "" && out.Title != "" {
			return out, true
		}
	}
	return rdWebDAVMediaCandidate{}, false
}

func (b *RDWebDAVLibraryBuilder) importWebDAVMovie(ctx context.Context, cfg *isettings.Settings, candidate rdWebDAVMediaCandidate, libraryPath string) (bool, string, error) {
	movie, err := b.matchWebDAVMovie(ctx, candidate)
	if err != nil {
		return false, "", err
	}
	if movie == nil {
		return false, "", fmt.Errorf("no TMDB movie match for %q (%d)", candidate.Title, candidate.Year)
	}
	opts := contentFilterOptionsFromSettings(cfg)
	if allowed, reason := MovieAllowedByContentFilters(movie, opts); !allowed {
		return false, "", fmt.Errorf("%w: %s", ErrBlockedContentFilter, reason)
	}

	linkPath := movieSymlinkPath(libraryPath, movie, candidate)
	linkState, err := ensureSymlink(candidate.SourcePath, linkPath)
	if err != nil {
		return false, "", err
	}

	added := false
	existing, err := b.movieStore.GetByTMDBID(ctx, movie.TMDBID)
	if err == nil && existing != nil {
		movie = existing
	} else {
		movie.Monitored = true
		movie.Available = true
		movie.QualityProfile = "1080p"
		added = true
	}
	movie.Metadata = mergeRDWebDAVMetadata(movie.Metadata, candidate, linkPath)
	if added {
		if err := b.movieStore.Add(ctx, movie); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate") && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return false, linkState, err
			}
			movie, err = b.movieStore.GetByTMDBID(ctx, movie.TMDBID)
			if err != nil {
				return false, linkState, err
			}
			added = false
		}
	} else if err := b.movieStore.Update(ctx, movie); err != nil {
		return false, linkState, err
	}
	_, _ = b.movieStore.GetDB().ExecContext(ctx, `UPDATE library_movies SET available = true WHERE id = $1`, movie.ID)
	return added, linkState, nil
}

func (b *RDWebDAVLibraryBuilder) importWebDAVEpisode(ctx context.Context, cfg *isettings.Settings, candidate rdWebDAVMediaCandidate, libraryPath string) (bool, bool, string, error) {
	series, err := b.matchWebDAVSeries(ctx, candidate)
	if err != nil {
		return false, false, "", err
	}
	if series == nil {
		return false, false, "", fmt.Errorf("no TMDB series match for %q", candidate.Title)
	}
	opts := contentFilterOptionsFromSettings(cfg)
	if allowed, reason := SeriesAllowedByContentFilters(series, opts); !allowed {
		return false, false, "", fmt.Errorf("%w: %s", ErrBlockedContentFilter, reason)
	}

	seriesAdded := false
	existingSeries, err := b.seriesStore.GetByTMDBID(ctx, series.TMDBID)
	if err == nil && existingSeries != nil {
		series = existingSeries
	} else {
		series.Monitored = true
		series.QualityProfile = "1080p"
		markRDWebDAVSeriesMetadata(series, candidate)
		seriesAdded = true
		if err := b.seriesStore.Add(ctx, series); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return false, false, "", err
			}
			series, err = b.seriesStore.GetByTMDBID(ctx, series.TMDBID)
			if err != nil {
				return false, false, "", err
			}
			seriesAdded = false
		}
	}
	if !seriesAdded && markRDWebDAVSeriesMetadata(series, candidate) {
		if err := b.seriesStore.Update(ctx, series); err != nil {
			return false, false, "", err
		}
	}

	episode := b.tmdbEpisodeForCandidate(ctx, series.ID, series.TMDBID, candidate)
	linkPath := episodeSymlinkPath(libraryPath, series, episode, candidate)
	linkState, err := ensureSymlink(candidate.SourcePath, linkPath)
	if err != nil {
		return seriesAdded, false, "", err
	}

	episodeUpdated := false
	existingEpisode, err := b.episodeStore.GetBySeriesAndNumber(ctx, series.ID, candidate.Season, candidate.Episode)
	if err == nil && existingEpisode != nil {
		episode.ID = existingEpisode.ID
		episode.CreatedAt = existingEpisode.CreatedAt
		if episode.Title == "" {
			episode.Title = existingEpisode.Title
		}
		episode.Metadata = mergeRDWebDAVMetadataMaps(existingEpisode.Metadata, episode.Metadata)
		episodeUpdated = true
	}
	episode.SeriesID = series.ID
	episode.SeasonNumber = candidate.Season
	episode.EpisodeNumber = candidate.Episode
	episode.Monitored = true
	episode.Available = true
	episode.Metadata = mergeRDWebDAVMetadata(episode.Metadata, candidate, linkPath)
	now := time.Now()
	episode.LastChecked = &now

	if episodeUpdated {
		if err := b.episodeStore.Update(ctx, episode); err != nil {
			return seriesAdded, false, linkState, err
		}
	} else if err := b.episodeStore.Add(ctx, episode); err != nil {
		return seriesAdded, false, linkState, err
	}
	return seriesAdded, episodeUpdated, linkState, nil
}

func (b *RDWebDAVLibraryBuilder) matchWebDAVMovie(ctx context.Context, candidate rdWebDAVMediaCandidate) (*models.Movie, error) {
	if candidate.TMDBID > 0 {
		return b.tmdbClient.GetMovie(ctx, candidate.TMDBID)
	}
	results, err := b.tmdbClient.SearchMovies(ctx, candidate.Title, 1)
	if err != nil {
		return nil, err
	}
	var best *models.Movie
	bestScore := 0.0
	for _, result := range results {
		score := dmmTitleSimilarity(candidate.Title, result.Title)
		year := MovieReleaseYear(result)
		if candidate.Year > 0 && year > 0 {
			switch delta := abs(year - candidate.Year); {
			case delta == 0:
				score += 0.25
			case delta == 1:
				score += 0.1
			default:
				score -= 0.5
			}
		}
		if score > bestScore {
			bestScore = score
			best = result
		}
	}
	if best == nil || bestScore < 0.88 {
		return nil, nil
	}
	return b.tmdbClient.GetMovie(ctx, best.TMDBID)
}

func (b *RDWebDAVLibraryBuilder) matchWebDAVSeries(ctx context.Context, candidate rdWebDAVMediaCandidate) (*models.Series, error) {
	if candidate.TMDBID > 0 {
		return b.tmdbClient.GetSeries(ctx, candidate.TMDBID)
	}
	results, err := b.tmdbClient.SearchSeries(ctx, candidate.Title, 1)
	if err != nil {
		return nil, err
	}
	var best *models.Series
	bestScore := 0.0
	for _, result := range results {
		score := dmmTitleSimilarity(candidate.Title, result.Title)
		year := SeriesReleaseYear(result)
		if candidate.Year > 0 && year > 0 {
			switch delta := abs(year - candidate.Year); {
			case delta == 0:
				score += 0.2
			case delta == 1:
				score += 0.08
			default:
				score -= 0.4
			}
		}
		if score > bestScore {
			bestScore = score
			best = result
		}
	}
	if best == nil || bestScore < 0.82 {
		return nil, nil
	}
	return b.tmdbClient.GetSeries(ctx, best.TMDBID)
}

func (b *RDWebDAVLibraryBuilder) tmdbEpisodeForCandidate(ctx context.Context, seriesID int64, tmdbSeriesID int, candidate rdWebDAVMediaCandidate) *models.Episode {
	episode := &models.Episode{
		SeriesID:      seriesID,
		SeasonNumber:  candidate.Season,
		EpisodeNumber: candidate.Episode,
		Title:         fmt.Sprintf("Episode %d", candidate.Episode),
		Metadata:      models.Metadata{},
	}
	season, err := b.tmdbClient.GetSeason(ctx, tmdbSeriesID, candidate.Season)
	if err != nil {
		return episode
	}
	for _, tmdbEpisode := range season.Episodes {
		if tmdbEpisode.EpisodeNumber == candidate.Episode {
			return b.tmdbClient.convertEpisode(seriesID, &tmdbEpisode)
		}
	}
	return episode
}

func movieSymlinkPath(libraryPath string, movie *models.Movie, candidate rdWebDAVMediaCandidate) string {
	year := MovieReleaseYear(movie)
	if year == 0 {
		year = candidate.Year
	}
	folder := fmt.Sprintf("%s (%d) {tmdb-%d}", safePathComponent(movie.Title), year, movie.TMDBID)
	file := fmt.Sprintf("%s (%d) {tmdb-%d}%s", safePathComponent(movie.Title), year, movie.TMDBID, candidate.Ext)
	return filepath.Join(libraryPath, "Movies", folder, file)
}

func episodeSymlinkPath(libraryPath string, series *models.Series, episode *models.Episode, candidate rdWebDAVMediaCandidate) string {
	year := SeriesReleaseYear(series)
	seriesFolder := safePathComponent(series.Title)
	if year > 0 {
		seriesFolder = fmt.Sprintf("%s (%d) {tmdb-%d}", seriesFolder, year, series.TMDBID)
	} else {
		seriesFolder = fmt.Sprintf("%s {tmdb-%d}", seriesFolder, series.TMDBID)
	}
	episodeTitle := safePathComponent(episode.Title)
	if episodeTitle == "" {
		episodeTitle = fmt.Sprintf("Episode %d", candidate.Episode)
	}
	file := fmt.Sprintf("%s - S%02dE%02d - %s", safePathComponent(series.Title), candidate.Season, candidate.Episode, episodeTitle)
	if episode.TMDBID > 0 {
		file += fmt.Sprintf(" {tmdb-%d}", episode.TMDBID)
	}
	file += candidate.Ext
	return filepath.Join(libraryPath, "TV", seriesFolder, fmt.Sprintf("Season %02d", candidate.Season), file)
}

func ensureSymlink(sourcePath, linkPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return "", err
	}
	current, err := os.Lstat(linkPath)
	if err == nil {
		if current.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("clean library path exists and is not a symlink: %s", linkPath)
		}
		target, err := os.Readlink(linkPath)
		if err == nil && target == sourcePath {
			return "unchanged", nil
		}
		if err := os.Remove(linkPath); err != nil {
			return "", err
		}
		if err := os.Symlink(sourcePath, linkPath); err != nil {
			return "", err
		}
		return "updated", nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(sourcePath, linkPath); err != nil {
		return "", err
	}
	return "created", nil
}

func pruneDeadSymlinks(root string) (int, error) {
	pruned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return nil
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			if removeErr := os.Remove(path); removeErr == nil {
				pruned++
			}
		}
		return nil
	})
	removeEmptyDirs(root)
	return pruned, err
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}

func mergeRDWebDAVMetadata(metadata models.Metadata, candidate rdWebDAVMediaCandidate, linkPath string) models.Metadata {
	if metadata == nil {
		metadata = models.Metadata{}
	}
	metadata["source"] = rdWebDAVLibrarySource
	metadata["rd_webdav"] = true
	metadata["rd_webdav_source_path"] = candidate.SourcePath
	metadata["rd_webdav_symlink_path"] = linkPath
	metadata["rd_webdav_size_bytes"] = candidate.SizeBytes
	metadata["rd_webdav_last_seen"] = time.Now().Format(time.RFC3339)
	return metadata
}

func markRDWebDAVSeriesMetadata(series *models.Series, candidate rdWebDAVMediaCandidate) bool {
	if series == nil {
		return false
	}
	if series.Metadata == nil {
		series.Metadata = models.Metadata{}
	}
	changed := false
	updates := map[string]interface{}{
		"source":                     rdWebDAVLibrarySource,
		"rd_webdav":                  true,
		"rd_webdav_last_source_path": candidate.SourcePath,
		"rd_webdav_last_seen":        time.Now().Format(time.RFC3339),
	}
	for key, value := range updates {
		if series.Metadata[key] != value {
			series.Metadata[key] = value
			changed = true
		}
	}
	return changed
}

func mergeRDWebDAVMetadataMaps(base, overlay models.Metadata) models.Metadata {
	if base == nil {
		base = models.Metadata{}
	}
	for key, value := range overlay {
		base[key] = value
	}
	return base
}

func recordSymlinkState(summary *RDWebDAVLibrarySummary, state string) {
	switch state {
	case "created":
		summary.SymlinksCreated++
	case "updated":
		summary.SymlinksUpdated++
	}
}

func cleanConfiguredPath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return value
}

func ensureReadableDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	_, err = os.ReadDir(path)
	return err
}

func extractRDWebDAVTMDBID(value string) int {
	match := rdWebDAVTMDBPattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return 0
	}
	id, _ := strconv.Atoi(match[1])
	return id
}

func isRDWebDAVVideoFile(path string) bool {
	return rdWebDAVVideoExtensions[strings.ToLower(filepath.Ext(path))]
}

func safePathComponent(value string) string {
	value = strings.TrimSpace(value)
	value = rdWebDAVUnsafePathPattern.ReplaceAllString(value, " ")
	value = rdWebDAVMultiSpacePattern.ReplaceAllString(value, " ")
	value = strings.Trim(value, " .")
	if value == "" {
		return "Unknown"
	}
	return value
}

func sameOrDescendant(path, root string) bool {
	path, _ = filepath.Abs(path)
	root, _ = filepath.Abs(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func isRDWebDAVRcloneMountedPath(path string) bool {
	mounts, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	path, _ = filepath.Abs(path)
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountPoint := unescapeProcMountPath(fields[1])
		mountPoint, _ = filepath.Abs(mountPoint)
		if mountPoint == path && isRDWebDAVRcloneMountEntry(fields) {
			return true
		}
	}
	return false
}

func isRDWebDAVRcloneMountEntry(fields []string) bool {
	if len(fields) < 3 {
		return false
	}
	source := strings.ToLower(unescapeProcMountPath(fields[0]))
	fsType := strings.ToLower(fields[2])
	return strings.Contains(fsType, "fuse") ||
		strings.Contains(source, "rclone") ||
		strings.HasPrefix(source, strings.ToLower(rdWebDAVRemoteName)+":")
}

func unescapeProcMountPath(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}
