package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zerr0-C00L/StreamArr/internal/database"
	"github.com/Zerr0-C00L/StreamArr/internal/models"
	"github.com/Zerr0-C00L/StreamArr/internal/services/streams"
	isettings "github.com/Zerr0-C00L/StreamArr/internal/settings"
)

const (
	dmmHashlistsTreeURL             = "https://api.github.com/repos/debridmediamanager/hashlists/git/trees/main?recursive=1"
	dmmHashlistsRawBaseURL          = "https://raw.githubusercontent.com/debridmediamanager/hashlists/main"
	dmmHashlistFilesPerRun          = 250
	dmmHashlistMaxCandidatesPerRun  = 300
	dmmHashlistMaxImportsPerRun     = dmmHashlistMaxCandidatesPerRun
	dmmHashlistMaxHTMLBytes         = 5 << 20
	dmmHashlistSource               = "dmm_hashlist"
	dmmHashlistIndexer              = "DMM-Hashlist"
	dmmHashlistRequestUserAgent     = "StreamArr-Pro DMM Hashlist Importer"
	dmmHashlistStateFilename        = "state.json"
	dmmHashlistMovieSimilarityFloor = 0.86
	dmmHashlistShowSimilarityFloor  = 0.88
)

var (
	dmmIframeHashPattern = regexp.MustCompile(`(?i)src=["']https?://debridmediamanager\.com/hashlist#([^"']+)["']`)
	dmmEpisodePatterns   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bS(\d{1,2})\s*E(\d{1,3})\b`),
		regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`),
		regexp.MustCompile(`(?i)\bseason[\s._-]*(\d{1,2}).{0,40}\bepisode[\s._-]*(\d{1,3})\b`),
	}
	dmmYearPattern = regexp.MustCompile(`\b(19[0-9]{2}|20[0-4][0-9])\b`)
)

// DMMHashlistImporter turns the public DMM hashlists repository into a
// conservative library source. It imports only items that can be matched to
// TMDB with a year/title check and whose torrent hash is still cached on the
// configured Real-Debrid account.
type DMMHashlistImporter struct {
	movieStore       *database.MovieStore
	seriesStore      *database.SeriesStore
	episodeStore     *database.EpisodeStore
	streamCacheStore *database.StreamCacheStore
	tmdbClient       *TMDBClient
	settingsGetter   func() *isettings.Settings
	httpClient       *http.Client
	cacheDir         string
	streamScorer     *streams.StreamService
	mu               sync.Mutex
}

type DMMHashlistImportSummary struct {
	FilesTotal        int `json:"files_total"`
	FilesProcessed    int `json:"files_processed"`
	TorrentsSeen      int `json:"torrents_seen"`
	CandidatesMatched int `json:"candidates_matched"`
	CachedCandidates  int `json:"cached_candidates"`
	MoviesImported    int `json:"movies_imported"`
	SeriesImported    int `json:"series_imported"`
	StreamsCached     int `json:"streams_cached"`
	Skipped           int `json:"skipped"`
	Errors            int `json:"errors"`
	NextCursor        int `json:"next_cursor"`
	NextTorrentOffset int `json:"next_torrent_offset"`
}

type dmmHashlistState struct {
	TreeSHA        string    `json:"tree_sha"`
	Cursor         int       `json:"cursor"`
	TorrentOffset  int       `json:"torrent_offset"`
	ProcessedFiles int       `json:"processed_files"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type dmmTreeResponse struct {
	SHA       string        `json:"sha"`
	Tree      []dmmTreeFile `json:"tree"`
	Truncated bool          `json:"truncated"`
}

type dmmTreeFile struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type dmmHashlistPayload struct {
	Title    string               `json:"title"`
	Torrents []DMMHashlistTorrent `json:"torrents"`
}

type DMMHashlistTorrent struct {
	Filename string `json:"filename"`
	Hash     string `json:"hash"`
	Bytes    int64  `json:"bytes"`
}

type dmmCandidate struct {
	MediaType string
	Title     string
	Year      int
	Season    int
	Episode   int
	Torrent   DMMHashlistTorrent
	Stream    models.TorrentStream
}

func NewDMMHashlistImporter(
	movieStore *database.MovieStore,
	seriesStore *database.SeriesStore,
	episodeStore *database.EpisodeStore,
	streamCacheStore *database.StreamCacheStore,
	tmdbClient *TMDBClient,
	settingsGetter func() *isettings.Settings,
	cacheDir string,
) *DMMHashlistImporter {
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = filepath.Join(".", "cache", "dmm")
	}

	return &DMMHashlistImporter{
		movieStore:       movieStore,
		seriesStore:      seriesStore,
		episodeStore:     episodeStore,
		streamCacheStore: streamCacheStore,
		tmdbClient:       tmdbClient,
		settingsGetter:   settingsGetter,
		cacheDir:         cacheDir,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		streamScorer:     streams.NewStreamService(nil, slog.Default()),
	}
}

func (i *DMMHashlistImporter) UpdateTMDBClient(tmdbClient *TMDBClient) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tmdbClient = tmdbClient
}

func (i *DMMHashlistImporter) Import(ctx context.Context) (*DMMHashlistImportSummary, error) {
	summary := &DMMHashlistImportSummary{}
	if i == nil || i.settingsGetter == nil {
		return summary, fmt.Errorf("DMM hashlist importer not initialized")
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	cfg := i.settingsGetter()
	if cfg == nil || !cfg.DMMLibraryImportEnabled {
		GlobalScheduler.UpdateProgress(ServiceDMMHashlistImport, 0, 0, "DMM full library import disabled")
		return summary, nil
	}
	if i.tmdbClient == nil || strings.TrimSpace(cfg.TMDBAPIKey) == "" {
		return summary, fmt.Errorf("TMDB API key is required for DMM hashlist matching")
	}
	if strings.TrimSpace(cfg.RealDebridAPIKey) == "" {
		return summary, fmt.Errorf("Real-Debrid API key is required to verify DMM hashes are cached")
	}

	state, err := i.loadState()
	if err != nil {
		return summary, err
	}

	tree, treeSHA, err := i.fetchTree(ctx)
	if err != nil {
		return summary, err
	}
	if len(tree) == 0 {
		GlobalScheduler.UpdateProgress(ServiceDMMHashlistImport, 0, 0, "No DMM hashlists found")
		return summary, nil
	}
	summary.FilesTotal = len(tree)

	if state.TreeSHA != treeSHA || state.Cursor >= len(tree) || state.Cursor < 0 {
		state.TreeSHA = treeSHA
		state.Cursor = 0
		state.TorrentOffset = 0
	}
	if state.TorrentOffset < 0 {
		state.TorrentOffset = 0
	}

	start := state.Cursor
	end := start + dmmHashlistFilesPerRun
	if end > len(tree) {
		end = len(tree)
	}

	candidates := make([]dmmCandidate, 0, 256)
	nextCursor := start
	nextTorrentOffset := state.TorrentOffset
	candidateLimitReached := false
	for idx := start; idx < end; idx++ {
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		file := tree[idx]
		GlobalScheduler.UpdateProgress(
			ServiceDMMHashlistImport,
			idx-start,
			end-start,
			fmt.Sprintf("Scanning DMM hashlist %d/%d", idx+1, len(tree)),
		)

		torrents, err := i.fetchAndDecodeHashlist(ctx, file.Path)
		if err != nil {
			summary.Errors++
			log.Printf("[DMM Hashlists] Failed to decode %s: %v", file.Path, err)
			continue
		}

		summary.FilesProcessed++
		torrentStart := 0
		if idx == start {
			torrentStart = state.TorrentOffset
			if torrentStart > len(torrents) {
				torrentStart = len(torrents)
			}
		}
		summary.TorrentsSeen += len(torrents) - torrentStart
		for torrentIdx := torrentStart; torrentIdx < len(torrents); torrentIdx++ {
			if len(candidates) >= dmmHashlistMaxCandidatesPerRun {
				nextCursor = idx
				nextTorrentOffset = torrentIdx
				candidateLimitReached = true
				break
			}
			torrent := torrents[torrentIdx]
			candidate, ok := i.candidateFromTorrent(torrent, cfg)
			if !ok {
				summary.Skipped++
				continue
			}
			candidates = append(candidates, candidate)
		}
		if candidateLimitReached {
			break
		}
		nextCursor = idx + 1
		nextTorrentOffset = 0
	}

	state.Cursor = nextCursor
	state.TorrentOffset = nextTorrentOffset
	if state.Cursor >= len(tree) {
		state.Cursor = 0
		state.TorrentOffset = 0
	}
	state.ProcessedFiles += summary.FilesProcessed
	state.UpdatedAt = time.Now()
	summary.NextCursor = state.Cursor
	summary.NextTorrentOffset = state.TorrentOffset

	if len(candidates) == 0 {
		if err := i.saveState(state); err != nil {
			log.Printf("[DMM Hashlists] Failed to save state: %v", err)
		}
		GlobalScheduler.UpdateProgress(ServiceDMMHashlistImport, 0, 0, "No high-confidence DMM candidates in this batch")
		return summary, nil
	}
	summary.CandidatesMatched = len(candidates)

	cachedCandidates, err := i.filterCachedCandidates(ctx, candidates, cfg.RealDebridAPIKey)
	if err != nil {
		return summary, err
	}
	summary.CachedCandidates = len(cachedCandidates)
	if len(cachedCandidates) == 0 {
		if err := i.saveState(state); err != nil {
			log.Printf("[DMM Hashlists] Failed to save state: %v", err)
		}
		GlobalScheduler.UpdateProgress(ServiceDMMHashlistImport, 0, 0, "No currently cached Real-Debrid hashes in this batch")
		return summary, nil
	}

	imported := 0
	for idx, candidate := range cachedCandidates {
		if imported >= dmmHashlistMaxImportsPerRun {
			break
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		GlobalScheduler.UpdateProgress(
			ServiceDMMHashlistImport,
			idx+1,
			len(cachedCandidates),
			fmt.Sprintf("Importing %s", candidate.Title),
		)

		switch candidate.MediaType {
		case "movie":
			added, cached, err := i.importMovieCandidate(ctx, cfg, candidate)
			if err != nil {
				summary.Errors++
				log.Printf("[DMM Hashlists] Movie import failed for %s (%d): %v", candidate.Title, candidate.Year, err)
				continue
			}
			if added {
				summary.MoviesImported++
			}
			if cached {
				summary.StreamsCached++
			}
			imported++
		case "series":
			added, cached, err := i.importSeriesCandidate(ctx, cfg, candidate)
			if err != nil {
				summary.Errors++
				log.Printf("[DMM Hashlists] Series import failed for %s S%02dE%02d: %v", candidate.Title, candidate.Season, candidate.Episode, err)
				continue
			}
			if added {
				summary.SeriesImported++
			}
			if cached {
				summary.StreamsCached++
			}
			imported++
		}

		time.Sleep(120 * time.Millisecond)
	}

	if err := i.saveState(state); err != nil {
		log.Printf("[DMM Hashlists] Failed to save state: %v", err)
	}

	GlobalScheduler.UpdateProgress(
		ServiceDMMHashlistImport,
		imported,
		len(cachedCandidates),
		fmt.Sprintf("DMM import complete: +%d movies, +%d series, %d cached streams",
			summary.MoviesImported, summary.SeriesImported, summary.StreamsCached),
	)
	log.Printf("[DMM Hashlists] Import batch complete: files=%d/%d torrents=%d candidates=%d cached=%d movies=%d series=%d streams=%d skipped=%d errors=%d next_cursor=%d next_torrent_offset=%d",
		summary.FilesProcessed, summary.FilesTotal, summary.TorrentsSeen, summary.CandidatesMatched, summary.CachedCandidates,
		summary.MoviesImported, summary.SeriesImported, summary.StreamsCached, summary.Skipped, summary.Errors, summary.NextCursor, summary.NextTorrentOffset)

	return summary, nil
}

func (i *DMMHashlistImporter) fetchTree(ctx context.Context) ([]dmmTreeFile, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dmmHashlistsTreeURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", dmmHashlistRequestUserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("GitHub tree API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var treeResp dmmTreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&treeResp); err != nil {
		return nil, "", err
	}
	if treeResp.Truncated {
		return nil, "", fmt.Errorf("GitHub tree response was truncated")
	}

	files := make([]dmmTreeFile, 0, len(treeResp.Tree))
	for _, file := range treeResp.Tree {
		if file.Type == "blob" && strings.HasSuffix(strings.ToLower(file.Path), ".html") {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files, treeResp.SHA, nil
}

func (i *DMMHashlistImporter) fetchAndDecodeHashlist(ctx context.Context, repoPath string) ([]DMMHashlistTorrent, error) {
	rawURL := dmmRawURL(repoPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", dmmHashlistRequestUserAgent)

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("raw hashlist returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, dmmHashlistMaxHTMLBytes))
	if err != nil {
		return nil, err
	}
	return decodeDMMHashlistHTML(string(data))
}

func decodeDMMHashlistHTML(html string) ([]DMMHashlistTorrent, error) {
	match := dmmIframeHashPattern.FindStringSubmatch(html)
	if len(match) < 2 {
		return nil, fmt.Errorf("hashlist iframe fragment not found")
	}

	jsonString, err := decompressLZStringFromEncodedURIComponent(match[1])
	if err != nil {
		return nil, err
	}
	jsonString = strings.TrimSpace(jsonString)
	if jsonString == "" {
		return nil, nil
	}

	if strings.HasPrefix(jsonString, "[") {
		var torrents []DMMHashlistTorrent
		if err := json.Unmarshal([]byte(jsonString), &torrents); err != nil {
			return nil, err
		}
		return normalizeDMMTorrents(torrents), nil
	}

	var payload dmmHashlistPayload
	if err := json.Unmarshal([]byte(jsonString), &payload); err != nil {
		return nil, err
	}
	return normalizeDMMTorrents(payload.Torrents), nil
}

func normalizeDMMTorrents(torrents []DMMHashlistTorrent) []DMMHashlistTorrent {
	normalized := make([]DMMHashlistTorrent, 0, len(torrents))
	for _, torrent := range torrents {
		torrent.Filename = strings.TrimSpace(torrent.Filename)
		torrent.Hash = normalizeDMMHash(torrent.Hash)
		if torrent.Filename == "" || !isValidDMMHash(torrent.Hash) {
			continue
		}
		normalized = append(normalized, torrent)
	}
	return normalized
}

func (i *DMMHashlistImporter) candidateFromTorrent(torrent DMMHashlistTorrent, cfg *isettings.Settings) (dmmCandidate, bool) {
	if !i.torrentAllowedByReleaseFilters(torrent, cfg) {
		return dmmCandidate{}, false
	}

	parsed, ok := parseDMMFilename(torrent.Filename)
	if !ok {
		return dmmCandidate{}, false
	}
	parsed.Torrent = torrent

	stream := i.streamScorer.ParseStreamFromTorrentName(torrent.Filename, torrent.Hash, dmmHashlistIndexer, 0)
	if torrent.Bytes > 0 {
		stream.SizeGB = float64(torrent.Bytes) / (1024 * 1024 * 1024)
	}
	quality := streams.StreamQuality{
		Resolution:  stream.Resolution,
		HDRType:     stream.HDRType,
		AudioFormat: stream.AudioFormat,
		Source:      stream.Source,
		Codec:       stream.Codec,
		SizeGB:      stream.SizeGB,
		Seeders:     stream.Seeders,
	}
	stream.QualityScore = streams.CalculateScore(quality).TotalScore
	if stream.QualityScore == 0 {
		stream.QualityScore = 1
	}
	stream.Hash = torrent.Hash
	stream.Title = torrent.Filename
	stream.TorrentName = torrent.Filename
	stream.Indexer = dmmHashlistIndexer
	parsed.Stream = stream

	return parsed, true
}

func (i *DMMHashlistImporter) torrentAllowedByReleaseFilters(torrent DMMHashlistTorrent, cfg *isettings.Settings) bool {
	if cfg == nil {
		return true
	}

	parsed := i.streamScorer.ParseStreamFromTorrentName(torrent.Filename, torrent.Hash, dmmHashlistIndexer, 0)
	if cfg.ExcludedQualities != "" && i.streamScorer.ShouldExcludeByQualityType(torrent.Filename, parsed.Resolution, parsed.HDRType, cfg.ExcludedQualities) {
		return false
	}

	if cfg.EnableReleaseFilters {
		stream := models.TorrentStream{
			Title:       torrent.Filename,
			TorrentName: torrent.Filename,
			Resolution:  parsed.Resolution,
			Indexer:     dmmHashlistIndexer,
		}
		if i.streamScorer.ShouldFilterStream(stream, cfg.ExcludedReleaseGroups, "", cfg.ExcludedLanguageTags) {
			return false
		}
		if customPatternMatches(torrent.Filename, cfg.CustomExcludePatterns) {
			return false
		}
	}

	return true
}

func (i *DMMHashlistImporter) filterCachedCandidates(ctx context.Context, candidates []dmmCandidate, rdAPIKey string) ([]dmmCandidate, error) {
	grouped := make(map[string][]dmmCandidate)
	hashes := make([]string, 0, len(candidates))
	seenHash := make(map[string]struct{})
	for _, candidate := range candidates {
		grouped[candidate.matchKey()] = append(grouped[candidate.matchKey()], candidate)
		if _, seen := seenHash[candidate.Torrent.Hash]; !seen {
			seenHash[candidate.Torrent.Hash] = struct{}{}
			hashes = append(hashes, candidate.Torrent.Hash)
		}
	}

	availability, err := NewRealDebridClient(rdAPIKey).CheckInstantAvailability(ctx, hashes)
	if err != nil {
		return nil, err
	}

	best := make([]dmmCandidate, 0, len(grouped))
	for _, group := range grouped {
		sort.Slice(group, func(a, b int) bool {
			if group[a].Stream.QualityScore == group[b].Stream.QualityScore {
				return group[a].Torrent.Bytes > group[b].Torrent.Bytes
			}
			return group[a].Stream.QualityScore > group[b].Stream.QualityScore
		})
		for _, candidate := range group {
			if availability[candidate.Torrent.Hash] {
				best = append(best, candidate)
				break
			}
		}
	}

	sort.Slice(best, func(a, b int) bool {
		if best[a].MediaType == best[b].MediaType {
			return best[a].Title < best[b].Title
		}
		return best[a].MediaType < best[b].MediaType
	})

	return best, nil
}

func (candidate dmmCandidate) matchKey() string {
	return fmt.Sprintf("%s:%s:%d:%d:%d", candidate.MediaType, normalizeTitle(candidate.Title), candidate.Year, candidate.Season, candidate.Episode)
}

func (i *DMMHashlistImporter) importMovieCandidate(ctx context.Context, cfg *isettings.Settings, candidate dmmCandidate) (bool, bool, error) {
	movie, err := i.matchMovie(ctx, candidate)
	if err != nil {
		return false, false, err
	}
	if movie == nil {
		return false, false, nil
	}

	opts := contentFilterOptionsFromSettings(cfg)
	if allowed, reason := MovieAllowedByContentFilters(movie, opts); !allowed {
		return false, false, fmt.Errorf("%w: %s", ErrBlockedContentFilter, reason)
	}

	added := false
	existing, err := i.movieStore.GetByTMDBID(ctx, movie.TMDBID)
	if err == nil && existing != nil {
		movie = existing
	} else {
		movie.Monitored = true
		movie.Available = true
		movie.QualityProfile = "1080p"
		if movie.Metadata == nil {
			movie.Metadata = models.Metadata{}
		}
		movie.Metadata["source"] = dmmHashlistSource
		movie.Metadata["dmm_hashlist"] = true
		movie.Metadata["dmm_hashlist_filename"] = candidate.Torrent.Filename
		movie.Metadata["dmm_hashlist_hash"] = candidate.Torrent.Hash

		if err := i.movieStore.Add(ctx, movie); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "already exists") && !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return false, false, err
			}
			movie, err = i.movieStore.GetByTMDBID(ctx, movie.TMDBID)
			if err != nil {
				return false, false, err
			}
		} else {
			added = true
		}
	}

	i.updateMovieIMDbID(ctx, movie)
	cached, err := i.cacheMovieCandidateStream(ctx, movie.ID, candidate)
	if err != nil {
		return added, false, err
	}
	return added, cached, nil
}

func (i *DMMHashlistImporter) importSeriesCandidate(ctx context.Context, cfg *isettings.Settings, candidate dmmCandidate) (bool, bool, error) {
	series, err := i.matchSeries(ctx, candidate)
	if err != nil {
		return false, false, err
	}
	if series == nil {
		return false, false, nil
	}

	opts := contentFilterOptionsFromSettings(cfg)
	if allowed, reason := SeriesAllowedByContentFilters(series, opts); !allowed {
		return false, false, fmt.Errorf("%w: %s", ErrBlockedContentFilter, reason)
	}

	added := false
	existing, err := i.seriesStore.GetByTMDBID(ctx, series.TMDBID)
	if err == nil && existing != nil {
		series = existing
	} else {
		series.Monitored = true
		series.QualityProfile = "1080p"
		if series.Metadata == nil {
			series.Metadata = models.Metadata{}
		}
		if series.IMDBID == "" {
			if imdbID, ok := series.Metadata["imdb_id"].(string); ok {
				series.IMDBID = imdbID
			}
		}
		series.Metadata["source"] = dmmHashlistSource
		series.Metadata["dmm_hashlist"] = true
		series.Metadata["dmm_hashlist_filename"] = candidate.Torrent.Filename
		series.Metadata["dmm_hashlist_hash"] = candidate.Torrent.Hash
		series.Metadata["number_of_seasons"] = series.Seasons

		if err := i.seriesStore.Add(ctx, series); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return false, false, err
			}
			series, err = i.seriesStore.GetByTMDBID(ctx, series.TMDBID)
			if err != nil {
				return false, false, err
			}
		} else {
			added = true
		}
	}

	i.updateSeriesIMDbID(ctx, series)
	cached, err := i.cacheSeriesCandidateStream(ctx, series.ID, candidate)
	if err != nil {
		return added, false, err
	}
	return added, cached, nil
}

func (i *DMMHashlistImporter) matchMovie(ctx context.Context, candidate dmmCandidate) (*models.Movie, error) {
	results, err := i.tmdbClient.SearchMovies(ctx, candidate.Title, 1)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	bestScore := -1.0
	var best *models.Movie
	for _, result := range results {
		score := dmmMovieMatchScore(candidate, result)
		if score > bestScore {
			bestScore = score
			best = result
		}
	}
	if best == nil || bestScore < 1.0 {
		return nil, nil
	}

	full, err := i.tmdbClient.GetMovie(ctx, best.TMDBID)
	if err != nil {
		return nil, err
	}
	if dmmMovieMatchScore(candidate, full) < 1.0 {
		return nil, nil
	}
	return full, nil
}

func (i *DMMHashlistImporter) matchSeries(ctx context.Context, candidate dmmCandidate) (*models.Series, error) {
	results, err := i.tmdbClient.SearchSeries(ctx, candidate.Title, 1)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	bestScore := -1.0
	var best *models.Series
	for _, result := range results {
		score := dmmSeriesMatchScore(candidate, result)
		if score > bestScore {
			bestScore = score
			best = result
		}
	}
	if best == nil || bestScore < 1.0 {
		return nil, nil
	}

	full, err := i.tmdbClient.GetSeries(ctx, best.TMDBID)
	if err != nil {
		return nil, err
	}
	if dmmSeriesMatchScore(candidate, full) < 1.0 {
		return nil, nil
	}
	if full.IMDBID == "" {
		if imdbID, ok := full.Metadata["imdb_id"].(string); ok {
			full.IMDBID = imdbID
		}
	}
	return full, nil
}

func dmmMovieMatchScore(candidate dmmCandidate, movie *models.Movie) float64 {
	if movie == nil || candidate.Year == 0 {
		return 0
	}
	year := MovieReleaseYear(movie)
	if year != candidate.Year {
		return 0
	}
	similarity := dmmTitleSimilarity(candidate.Title, movie.Title)
	if similarity < dmmHashlistMovieSimilarityFloor {
		return 0
	}
	return similarity + 0.2
}

func dmmSeriesMatchScore(candidate dmmCandidate, series *models.Series) float64 {
	if series == nil {
		return 0
	}
	similarity := dmmTitleSimilarity(candidate.Title, series.Title)
	if similarity < dmmHashlistShowSimilarityFloor {
		return 0
	}
	if candidate.Year > 0 {
		year := SeriesReleaseYear(series)
		if year > 0 && abs(year-candidate.Year) > 1 {
			return 0
		}
		return similarity + 0.1
	}
	return similarity
}

func dmmTitleSimilarity(a, b string) float64 {
	a = normalizeTitle(a)
	b = normalizeTitle(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0
	}
	return 1 - (float64(levenshtein(a, b)) / float64(maxLen))
}

func parseDMMFilename(filename string) (dmmCandidate, bool) {
	cleaned := cleanDMMFilename(filename)
	if cleaned == "" || shouldSkipDMMFilename(cleaned) {
		return dmmCandidate{}, false
	}

	for _, pattern := range dmmEpisodePatterns {
		loc := pattern.FindStringSubmatchIndex(cleaned)
		if len(loc) >= 6 {
			season, _ := strconv.Atoi(cleaned[loc[2]:loc[3]])
			episode, _ := strconv.Atoi(cleaned[loc[4]:loc[5]])
			if season <= 0 || episode <= 0 {
				return dmmCandidate{}, false
			}

			prefix := strings.TrimSpace(cleaned[:loc[0]])
			title, year := titleAndYearFromPrefix(prefix)
			title = cleanupDMMTitle(title)
			if title == "" {
				return dmmCandidate{}, false
			}
			return dmmCandidate{
				MediaType: "series",
				Title:     title,
				Year:      year,
				Season:    season,
				Episode:   episode,
			}, true
		}
	}

	title, year := titleAndYearFromPrefix(cleaned)
	title = cleanupDMMTitle(title)
	if title == "" || year == 0 {
		return dmmCandidate{}, false
	}
	return dmmCandidate{MediaType: "movie", Title: title, Year: year}, true
}

func cleanDMMFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if unescaped, err := url.PathUnescape(filename); err == nil {
		filename = unescaped
	}
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = path.Base(filename)
	filename = strings.TrimSuffix(filename, filepath.Ext(filename))
	replacer := strings.NewReplacer(".", " ", "_", " ", "-", " ", "[", " ", "]", " ", "(", " ", ")", " ")
	filename = replacer.Replace(filename)
	return strings.Join(strings.Fields(filename), " ")
}

func shouldSkipDMMFilename(filename string) bool {
	lower := strings.ToLower(filename)
	skipTerms := []string{" sample ", " trailer ", " extras ", " featurette ", " deleted scene ", " behind the scenes "}
	padded := " " + lower + " "
	for _, term := range skipTerms {
		if strings.Contains(padded, term) {
			return true
		}
	}
	return false
}

func titleAndYearFromPrefix(prefix string) (string, int) {
	match := dmmYearPattern.FindStringSubmatchIndex(prefix)
	if len(match) >= 4 {
		year, _ := strconv.Atoi(prefix[match[2]:match[3]])
		return strings.TrimSpace(prefix[:match[0]]), year
	}
	return strings.TrimSpace(prefix), 0
}

func cleanupDMMTitle(title string) string {
	title = stripQualityTags(title)
	stopWords := []string{" complete ", " season ", " multi ", " proper ", " repack "}
	padded := " " + strings.ToLower(title) + " "
	for _, word := range stopWords {
		padded = strings.ReplaceAll(padded, word, " ")
	}
	return strings.Join(strings.Fields(padded), " ")
}

func (i *DMMHashlistImporter) cacheMovieCandidateStream(ctx context.Context, movieID int64, candidate dmmCandidate) (bool, error) {
	if i.streamCacheStore == nil || movieID <= 0 {
		return false, nil
	}
	streamURL := magnetURL(candidate.Torrent.Hash)
	query := `
		INSERT INTO media_streams (
			media_type, media_id, movie_id, stream_url, stream_hash,
			quality_score, resolution, hdr_type, audio_format,
			source_type, file_size_gb, codec, indexer,
			cached_at, last_checked, check_count, is_available, upgrade_available,
			next_check_at, created_at, updated_at
		) VALUES (
			'movie', $1, $1, $2, $3,
			$4, $5, $6, $7,
			$8, $9, $10, $11,
			NOW(), NOW(), 0, true, false,
			NOW() + INTERVAL '7 days', NOW(), NOW()
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
		WHERE media_streams.quality_score IS NULL OR EXCLUDED.quality_score >= media_streams.quality_score
	`
	result, err := i.streamCacheStore.GetDB().ExecContext(ctx, query,
		movieID,
		streamURL,
		candidate.Torrent.Hash,
		candidate.Stream.QualityScore,
		candidate.Stream.Resolution,
		candidate.Stream.HDRType,
		candidate.Stream.AudioFormat,
		candidate.Stream.Source,
		candidate.Stream.SizeGB,
		candidate.Stream.Codec,
		dmmHashlistIndexer,
	)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		_, _ = i.movieStore.GetDB().ExecContext(ctx, `UPDATE library_movies SET available = true WHERE id = $1`, movieID)
	}
	return rows > 0, nil
}

func (i *DMMHashlistImporter) cacheSeriesCandidateStream(ctx context.Context, seriesID int64, candidate dmmCandidate) (bool, error) {
	if i.streamCacheStore == nil || seriesID <= 0 || candidate.Season <= 0 || candidate.Episode <= 0 {
		return false, nil
	}
	streamURL := magnetURL(candidate.Torrent.Hash)
	query := `
		INSERT INTO media_streams (
			media_type, media_id, series_id, season, episode, stream_url, stream_hash,
			quality_score, resolution, hdr_type, audio_format,
			source_type, file_size_gb, codec, indexer,
			cached_at, last_checked, check_count, is_available, upgrade_available,
			next_check_at, created_at, updated_at
		) VALUES (
			'series', $1, $1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			NOW(), NOW(), 0, true, false,
			NOW() + INTERVAL '7 days', NOW(), NOW()
		)
		ON CONFLICT (series_id, season, episode) WHERE series_id IS NOT NULL DO UPDATE SET
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
		WHERE media_streams.quality_score IS NULL OR EXCLUDED.quality_score >= media_streams.quality_score
	`
	result, err := i.streamCacheStore.GetDB().ExecContext(ctx, query,
		seriesID,
		candidate.Season,
		candidate.Episode,
		streamURL,
		candidate.Torrent.Hash,
		candidate.Stream.QualityScore,
		candidate.Stream.Resolution,
		candidate.Stream.HDRType,
		candidate.Stream.AudioFormat,
		candidate.Stream.Source,
		candidate.Stream.SizeGB,
		candidate.Stream.Codec,
		dmmHashlistIndexer,
	)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

func (i *DMMHashlistImporter) updateMovieIMDbID(ctx context.Context, movie *models.Movie) {
	if movie == nil || movie.ID == 0 || movie.Metadata == nil {
		return
	}
	imdbID, _ := movie.Metadata["imdb_id"].(string)
	if imdbID == "" {
		return
	}
	_, _ = i.movieStore.GetDB().ExecContext(ctx,
		`UPDATE library_movies SET imdb_id = $2 WHERE id = $1 AND COALESCE(imdb_id, '') = ''`,
		movie.ID, imdbID)
}

func (i *DMMHashlistImporter) updateSeriesIMDbID(ctx context.Context, series *models.Series) {
	if series == nil || series.ID == 0 {
		return
	}
	imdbID := series.IMDBID
	if imdbID == "" && series.Metadata != nil {
		imdbID, _ = series.Metadata["imdb_id"].(string)
	}
	if imdbID == "" {
		return
	}
	_, _ = i.seriesStore.GetDB().ExecContext(ctx,
		`UPDATE library_series SET imdb_id = $2 WHERE id = $1 AND COALESCE(imdb_id, '') = ''`,
		series.ID, imdbID)
}

func contentFilterOptionsFromSettings(cfg *isettings.Settings) ContentFilterOptions {
	if cfg == nil {
		return ContentFilterOptions{}
	}
	return ContentFilterOptions{
		MinYear:             cfg.MinYear,
		MinRuntime:          cfg.MinRuntime,
		IncludeAdultVOD:     cfg.IncludeAdultVOD,
		OnlyReleasedContent: cfg.OnlyReleasedContent,
		BlockBollywood:      cfg.BlockBollywood,
	}
}

func customPatternMatches(value, patterns string) bool {
	value = strings.TrimSpace(value)
	patterns = strings.TrimSpace(patterns)
	if value == "" || patterns == "" {
		return false
	}
	for _, pattern := range strings.FieldsFunc(patterns, func(r rune) bool {
		return r == '\n' || r == ','
	}) {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if re, err := regexp.Compile("(?i)" + pattern); err == nil {
			if re.MatchString(value) {
				return true
			}
			continue
		}
		if strings.Contains(strings.ToLower(value), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func dmmRawURL(repoPath string) string {
	parts := strings.Split(repoPath, "/")
	for idx, part := range parts {
		parts[idx] = url.PathEscape(part)
	}
	return dmmHashlistsRawBaseURL + "/" + strings.Join(parts, "/")
}

func magnetURL(hash string) string {
	return "magnet:?xt=urn:btih:" + normalizeDMMHash(hash)
}

func normalizeDMMHash(hash string) string {
	hash = strings.TrimSpace(strings.ToLower(hash))
	hash = strings.TrimPrefix(hash, "urn:btih:")
	hash = strings.TrimPrefix(hash, "btih:")
	return hash
}

func isValidDMMHash(hash string) bool {
	hash = normalizeDMMHash(hash)
	if len(hash) != 40 {
		return false
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func (i *DMMHashlistImporter) loadState() (dmmHashlistState, error) {
	var state dmmHashlistState
	data, err := os.ReadFile(filepath.Join(i.cacheDir, dmmHashlistStateFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return dmmHashlistState{}, err
	}
	return state, nil
}

func (i *DMMHashlistImporter) saveState(state dmmHashlistState) error {
	if err := os.MkdirAll(i.cacheDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(i.cacheDir, dmmHashlistStateFilename), data, 0644)
}
