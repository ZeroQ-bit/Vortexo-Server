package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/providers"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
)

func TestRealDebridInfringingFileBlocksVortexoSource(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"

	resetVortexoBlockedSourcesForTest()

	err := fmt.Errorf("failed to unrestrict link: %w", &services.RealDebridAPIError{
		StatusCode: 403,
		ErrorName:  "infringing_file",
		ErrorCode:  35,
	})
	if !isRealDebridBlockedPlaybackError(err) {
		t.Fatal("expected infringing_file Real-Debrid error to be classified as blocked")
	}

	markVortexoSourceBlocked(hash, "infringing_file")
	if !isVortexoSourceBlocked(hash) {
		t.Fatal("expected hash to be hidden after Real-Debrid rejected it")
	}
}

func TestExpiredVortexoBlockedSourceIsPruned(t *testing.T) {
	const hash = "fedcba9876543210fedcba9876543210fedcba98"

	resetVortexoBlockedSourcesForTest()
	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash[hash] = vortexoBlockedSource{
		Reason:    "infringing_file",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	vortexoBlockedSources.Unlock()

	if isVortexoSourceBlocked(hash) {
		t.Fatal("expected expired blocked source to be playable again")
	}

	vortexoBlockedSources.RLock()
	_, exists := vortexoBlockedSources.byHash[hash]
	vortexoBlockedSources.RUnlock()
	if exists {
		t.Fatal("expected expired blocked source to be pruned")
	}
}

func TestSortMediaVideosPrefersHighestOfficialYouTubeTrailer(t *testing.T) {
	videos := []services.Video{
		{Key: "low", Site: "YouTube", Type: "Trailer", Official: true, Size: 360, Published: "2024-01-01T00:00:00.000Z"},
		{Key: "clip", Site: "YouTube", Type: "Clip", Official: true, Size: 1080, Published: "2024-01-02T00:00:00.000Z"},
		{Key: "high", Site: "YouTube", Type: "Trailer", Official: true, Size: 1080, Published: "2024-01-03T00:00:00.000Z"},
		{Key: "vimeo", Site: "Vimeo", Type: "Trailer", Official: true, Size: 2160, Published: "2024-01-04T00:00:00.000Z"},
	}

	got := sortMediaVideos(videos)
	if got[0].Key != "high" {
		t.Fatalf("sortMediaVideos first key = %q, want high; sorted=%#v", got[0].Key, got)
	}
}

func TestYouTubeTrailerFormatAttemptsPreferHLS(t *testing.T) {
	attempts := youtubeTrailerFormatAttempts()
	if len(attempts) != 2 {
		t.Fatalf("youtubeTrailerFormatAttempts returned %d attempts, want 2", len(attempts))
	}
	if attempts[0].Name != "hls" {
		t.Fatalf("first trailer format attempt = %q, want hls", attempts[0].Name)
	}
	if attempts[1].Name != "progressive" {
		t.Fatalf("second trailer format attempt = %q, want progressive", attempts[1].Name)
	}
}

func TestBuildVortexoSourcesPrefersDirectURLWhenHashPresent(t *testing.T) {
	const directURL = "https://example.com/realdebrid/stream.mp4"
	handler := &Handler{}

	sources := handler.buildVortexoSources([]providers.TorrentioStream{{
		Name:     "RD+",
		Title:    "Movie.2026.1080p.WEB-DL",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		URL:      directURL,
		Cached:   true,
		Source:   "Torrentio",
	}}, vortexoSourcesRequest{Type: "movie", Title: "Movie", Year: 2026})

	if len(sources) != 1 {
		t.Fatalf("buildVortexoSources returned %d sources, want 1", len(sources))
	}

	token, err := decodeVortexoPlayToken(sources[0].ID)
	if err != nil {
		t.Fatalf("decodeVortexoPlayToken failed: %v", err)
	}
	if token.URL != directURL {
		t.Fatalf("token.URL = %q, want %q", token.URL, directURL)
	}
	if token.Hash != "" {
		t.Fatalf("token.Hash = %q, want empty when direct URL is available", token.Hash)
	}
	if sources[0].DirectURL != directURL {
		t.Fatalf("source.DirectURL = %q, want %q", sources[0].DirectURL, directURL)
	}
	if sources[0].DownloadURL != directURL {
		t.Fatalf("source.DownloadURL = %q, want %q", sources[0].DownloadURL, directURL)
	}
}

func TestBuildVortexoSourcesResolvesTorrentioURLThroughRealDebrid(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	const resolverURL = "https://torrentio.strem.fun/resolve/realdebrid/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/" + hash + "/null/1/Memory.of.a.Killer.S01E03.720p.HEVC.x265-MeGusta.mkv"
	handler := &Handler{}

	sources := handler.buildVortexoSources([]providers.TorrentioStream{{
		Name:   "RD+",
		Title:  "Memory.of.a.Killer.S01E03.720p.HEVC.x265-MeGusta.mkv",
		URL:    resolverURL,
		Cached: true,
		Source: "Torrentio",
	}}, vortexoSourcesRequest{Type: "episode", Title: "Memory of a Killer", Year: 2026, Season: 1, Episode: 3})

	if len(sources) != 1 {
		t.Fatalf("buildVortexoSources returned %d sources, want 1", len(sources))
	}

	token, err := decodeVortexoPlayToken(sources[0].ID)
	if err != nil {
		t.Fatalf("decodeVortexoPlayToken failed: %v", err)
	}
	if token.Hash != hash {
		t.Fatalf("token.Hash = %q, want %q", token.Hash, hash)
	}
	if token.URL != "" {
		t.Fatalf("token.URL = %q, want empty so /vortexo/play resolves RD download URL", token.URL)
	}
	if sources[0].DirectURL != "" || sources[0].DownloadURL != "" {
		t.Fatalf("source direct URLs = %q/%q, want empty resolver fields", sources[0].DirectURL, sources[0].DownloadURL)
	}
}

func TestBuildVortexoSourcesPrioritizesRealDebridLibraryTorrent(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	handler := &Handler{}

	sources := handler.buildVortexoSources([]providers.TorrentioStream{{
		Name:      "M.I.A. 2026 S01E01 720p - PW.mkv",
		Title:     "M.I.A. 2026 S01E01 720p - PW.mkv",
		InfoHash:  hash,
		TorrentID: "rd-torrent-1",
		FileIdx:   2,
		URL:       "magnet:?xt=urn:btih:" + hash,
		Cached:    true,
		Source:    vortexoRealDebridLibrarySource,
		Size:      3_200_000_000,
	}}, vortexoSourcesRequest{Type: "episode", Title: "M.I.A.", Year: 2026, Season: 1, Episode: 1})

	if len(sources) != 1 {
		t.Fatalf("buildVortexoSources returned %d sources, want 1", len(sources))
	}
	if sources[0].Priority != vortexoRealDebridLibraryPriority {
		t.Fatalf("priority = %d, want %d", sources[0].Priority, vortexoRealDebridLibraryPriority)
	}

	token, err := decodeVortexoPlayToken(sources[0].ID)
	if err != nil {
		t.Fatalf("decodeVortexoPlayToken failed: %v", err)
	}
	if token.TorrentID != "rd-torrent-1" {
		t.Fatalf("token.TorrentID = %q, want rd-torrent-1", token.TorrentID)
	}
	if token.Hash != hash {
		t.Fatalf("token.Hash = %q, want %q", token.Hash, hash)
	}
	if token.FileIdx != 2 {
		t.Fatalf("token.FileIdx = %d, want 2", token.FileIdx)
	}
}

func TestBuildVortexoSourcesUsesRDWebDAVLocalFileToken(t *testing.T) {
	const localPath = "/app/rd-library/TV/M.I.A (2026) {tmdb-262388}/Season 01/M.I.A - S01E01 - Revenge {tmdb-6061110}.mkv"
	handler := &Handler{}

	sources := handler.buildVortexoSources([]providers.TorrentioStream{{
		Name:   "M.I.A - S01E01 - Revenge {tmdb-6061110}.mkv",
		Title:  "M.I.A - S01E01 - Revenge {tmdb-6061110}.mkv",
		URL:    encodeVortexoLocalFileStreamURL(localPath),
		Cached: true,
		Source: vortexoRDWebDAVLibrarySource,
		Size:   3_200_000_000,
	}}, vortexoSourcesRequest{Type: "episode", Title: "M.I.A.", Year: 2026, Season: 1, Episode: 1})

	if len(sources) != 1 {
		t.Fatalf("buildVortexoSources returned %d sources, want 1", len(sources))
	}
	if sources[0].Priority != vortexoRDWebDAVLibraryPriority {
		t.Fatalf("priority = %d, want %d", sources[0].Priority, vortexoRDWebDAVLibraryPriority)
	}
	if !strings.HasPrefix(sources[0].DirectURL, "/api/v1/vortexo/file/") {
		t.Fatalf("direct URL = %q, want local file endpoint", sources[0].DirectURL)
	}

	token, err := decodeVortexoPlayToken(sources[0].ID)
	if err != nil {
		t.Fatalf("decodeVortexoPlayToken failed: %v", err)
	}
	if token.LocalPath != localPath {
		t.Fatalf("token.LocalPath = %q, want %q", token.LocalPath, localPath)
	}
	if token.Hash != "" || token.URL != "" || token.TorrentID != "" {
		t.Fatalf("local file token should not carry remote resolver fields: %#v", token)
	}
}

func TestVortexoCachedStreamToTorrentioKeepsSavedRealDebridOption(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"

	stream := vortexoCachedStreamToTorrentio(models.CachedStream{
		StreamTitle:    "M.I.A. 2026 S01 720p - PW",
		StreamURL:      "magnet:?xt=urn:btih:" + hash,
		StreamHash:     hash,
		Resolution:     "720p",
		FileSizeGB:     3.2,
		Indexer:        vortexoRealDebridLibrarySource,
		RDLibraryAdded: true,
		RDTorrentID:    "rd-torrent-1",
		RDFileID:       10,
		IsAvailable:    true,
	}, vortexoSourcesRequest{Type: "episode", Title: "M.I.A.", Year: 2026, Season: 1, Episode: 1})

	if stream.Title != "M.I.A. 2026 S01 720p - PW" {
		t.Fatalf("title = %q, want saved stream title", stream.Title)
	}
	if stream.Source != vortexoRealDebridLibrarySource {
		t.Fatalf("source = %q, want Real-Debrid Library", stream.Source)
	}
	if stream.TorrentID != "rd-torrent-1" {
		t.Fatalf("torrent ID = %q, want rd-torrent-1", stream.TorrentID)
	}
	if stream.FileIdx != 10 {
		t.Fatalf("file index = %d, want 10", stream.FileIdx)
	}
	if stream.Quality != "720p" {
		t.Fatalf("quality = %q, want 720p", stream.Quality)
	}
}

func TestPrependVortexoPreferredStreamsKeepsRealDebridLibraryFirst(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	preferred := []providers.TorrentioStream{{
		Title:     "M.I.A. 2026 S01E01 720p - PW.mkv",
		InfoHash:  hash,
		TorrentID: "rd-torrent-1",
		FileIdx:   1,
		Source:    vortexoRealDebridLibrarySource,
	}}
	existing := []providers.TorrentioStream{
		{Title: "M.I.A. S01E01 2160p WEB-DL", InfoHash: hash, FileIdx: 1, Source: "DMM"},
		{Title: "M.I.A. S01E01 1080p WEB-DL", InfoHash: "fedcba9876543210fedcba9876543210fedcba98", Source: "DMM"},
	}

	got := prependVortexoPreferredStreams(preferred, existing)
	if len(got) != 2 {
		t.Fatalf("combined streams = %d, want 2: %#v", len(got), got)
	}
	if got[0].Source != vortexoRealDebridLibrarySource {
		t.Fatalf("first source = %q, want Real-Debrid Library", got[0].Source)
	}
	if got[0].TorrentID != "rd-torrent-1" {
		t.Fatalf("first torrent ID = %q, want rd-torrent-1", got[0].TorrentID)
	}
}

func TestVortexoRealDebridEpisodeFileMatchesExactEpisodeReference(t *testing.T) {
	files := []services.RealDebridTorrentFile{
		{ID: 1, Path: "/M.I.A. 2026 S01/Extras/sample.mkv", Bytes: 200_000},
		{ID: 2, Path: "/M.I.A. 2026 S01/M.I.A.S01E01.Revenge.720p.mkv", Bytes: 3_200_000_000},
		{ID: 3, Path: "/M.I.A. 2026 S01/M.I.A.S01E02.Splash.720p.mkv", Bytes: 3_100_000_000},
	}

	file, ok := vortexoRealDebridEpisodeFile(files, 1, 1, false)
	if !ok {
		t.Fatal("expected exact S01E01 file to match")
	}
	if file.ID != 2 {
		t.Fatalf("matched file ID = %d, want 2", file.ID)
	}
}

func TestVortexoRealDebridEpisodeFileFallsBackToSeasonPackOrdinal(t *testing.T) {
	files := []services.RealDebridTorrentFile{
		{ID: 10, Path: "/M.I.A. 2026 S01/01 - Revenge.mkv", Bytes: 3_200_000_000},
		{ID: 11, Path: "/M.I.A. 2026 S01/02 - Splash.mkv", Bytes: 3_150_000_000},
	}

	file, ok := vortexoRealDebridEpisodeFile(files, 1, 1, true)
	if !ok {
		t.Fatal("expected ordinal season-pack file to match")
	}
	if file.ID != 10 {
		t.Fatalf("matched file ID = %d, want 10", file.ID)
	}
}

func TestVortexoRealDebridEpisodeFileDoesNotUseOrdinalWithoutSeasonPackSignal(t *testing.T) {
	files := []services.RealDebridTorrentFile{
		{ID: 10, Path: "/Downloads/01 - Revenge.mkv", Bytes: 3_200_000_000},
	}

	if file, ok := vortexoRealDebridEpisodeFile(files, 1, 1, false); ok {
		t.Fatalf("unexpected ordinal fallback file: %#v", file)
	}
}

func TestVortexoRealDebridLibrarySourceCacheKeepsSourcesVisible(t *testing.T) {
	resetVortexoRealDebridLibrarySourceCacheForTest()

	req := vortexoSourcesRequest{Type: "episode", Title: "M.I.A.", Year: 2026, Season: 1, Episode: 1}
	cacheKey := vortexoRealDebridLibrarySourceCacheKey(req, 2026)
	streams := []providers.TorrentioStream{{
		Title:     "M.I.A. 2026 S01/01 - Revenge.mkv",
		InfoHash:  "0123456789abcdef0123456789abcdef01234567",
		TorrentID: "rd-torrent-1",
		FileIdx:   10,
		Source:    vortexoRealDebridLibrarySource,
	}}

	cacheVortexoRealDebridLibrarySources(cacheKey, streams)
	streams[0].Title = "mutated outside cache"

	cached, ok := cachedVortexoRealDebridLibrarySources(cacheKey)
	if !ok {
		t.Fatal("expected cached Real-Debrid library sources")
	}
	if len(cached) != 1 {
		t.Fatalf("cached source count = %d, want 1", len(cached))
	}
	if cached[0].Title != "M.I.A. 2026 S01/01 - Revenge.mkv" {
		t.Fatalf("cached title = %q, want original title", cached[0].Title)
	}
}

func TestVortexoRealDebridLibrarySearchFiltersIncludePunctuationVariants(t *testing.T) {
	filters := vortexoRealDebridLibrarySearchFilters("M.I.A.")
	got := strings.ToLower(strings.Join(filters, "|"))
	for _, want := range []string{"m.i.a.", "m i a", "mia", "m.i.a", "m_i_a"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filters %q missing %q", got, want)
		}
	}
}

func TestDirectVortexoPlaybackURLKeepsRealDebridDownloadURL(t *testing.T) {
	const directURL = "https://syd5-4.download.real-debrid.com/d/QZBMYCQLC2DZG107/Memory.of.a.Killer.S01E03.720p.HEVC.x265-MeGusta%5BEZTVx.to%5D.mkv"

	if got := directVortexoPlaybackURL(directURL); got != directURL {
		t.Fatalf("directVortexoPlaybackURL = %q, want %q", got, directURL)
	}
}

func TestDirectVortexoPlaybackURLRejectsRealDebridStreamingPage(t *testing.T) {
	const streamingURL = "https://real-debrid.com/streaming-QZBMYCQLC2DZG"

	if got := directVortexoPlaybackURL(streamingURL); got != "" {
		t.Fatalf("directVortexoPlaybackURL = %q, want empty for streaming page", got)
	}
}

func TestFilterVortexoMovieStreamsRejectsAdultAndLooseTitleMatches(t *testing.T) {
	streams := []providers.TorrentioStream{
		{Title: "Normal 2025 2160p AMZN WEB-DL DD+ 5.1 H.265-SCOPE", Name: "Normal 2025", Source: "DMM"},
		{Title: "Blacked 20 11 14 Little Caprice The New Normal XXX 2160p MP4 P2", Name: "Blacked", Source: "DMM"},
		{Title: "Normal People - Season 1 - Mp4 x264 AC3 1080p", Name: "Normal People", Source: "DMM"},
		{Title: "A Normal Woman", Name: "A Normal Woman", Source: "DMM"},
	}

	filtered := filterVortexoMovieStreams(streams, "Normal", 2026)
	if len(filtered) != 1 {
		t.Fatalf("filterVortexoMovieStreams kept %d streams, want 1: %#v", len(filtered), filtered)
	}
	if filtered[0].Title != streams[0].Title {
		t.Fatalf("filterVortexoMovieStreams kept %q, want %q", filtered[0].Title, streams[0].Title)
	}
}

func TestFilterVortexoEpisodeStreamsRejectsLooseMiamiSportsMatch(t *testing.T) {
	streams := []providers.TorrentioStream{
		{Title: "M.I.A. S01E01 2160p WEB-DL DD+ 5.1 H.265-GRACE", Name: "M.I.A.", Source: "DMM"},
		{Title: "F1.2026.R04.Miami.Grand.Prix.SkyUHD.2160P", Name: "Formula 1", Source: "DMM"},
		{Title: "M I A S01E01 Revenge 2160p PCOK WEB-DL DDP5 1 Atmos H 265-Kitsune", Name: "M I A", Source: "DMM"},
		{Title: "Neon.Genesis.Evangelion.S01.1080p.BluRay.Remux.Dual-Audio.FLAC5.1.H.264", Name: "Neon Genesis Evangelion", Source: "DMM"},
		{Title: "M.I.A.S01.1080p", Name: "M.I.A.", Source: "DMM"},
	}

	filtered := filterVortexoEpisodeStreams(streams, "M.I.A.", 1, 1, 2026)
	if len(filtered) != 3 {
		t.Fatalf("filterVortexoEpisodeStreams kept %d streams, want 3: %#v", len(filtered), filtered)
	}

	for _, stream := range filtered {
		if strings.Contains(stream.Title, "Miami") || strings.Contains(stream.Title, "Evangelion") {
			t.Fatalf("kept unrelated stream: %q", stream.Title)
		}
	}
}

func TestApplyVortexoCacheAvailabilityOverridesProviderCachedFlag(t *testing.T) {
	const unavailableHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const cachedHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	streams := []providers.TorrentioStream{
		{Title: "Normal 2026 2160p", InfoHash: unavailableHash, Cached: true, Source: "DMM"},
		{Title: "Normal 2026 1080p", InfoHash: cachedHash, Cached: false, Source: "DMM"},
	}

	filtered := applyVortexoCacheAvailability(streams, map[string]bool{
		unavailableHash: false,
		cachedHash:      true,
	}, true, true)

	if len(filtered) != 1 {
		t.Fatalf("applyVortexoCacheAvailability kept %d streams, want 1: %#v", len(filtered), filtered)
	}
	if filtered[0].InfoHash != cachedHash {
		t.Fatalf("kept hash = %q, want %q", filtered[0].InfoHash, cachedHash)
	}
	if !filtered[0].Cached {
		t.Fatal("expected kept stream to be marked cached")
	}
}

func TestEncodeDecodeVortexoSubtitleToken(t *testing.T) {
	want := vortexoSourcesRequest{
		Type:    "movie",
		Title:   "Lee Cronin's The Mummy",
		Year:    2026,
		TMDBID:  1304313,
		IMDBID:  "tt1234567",
		Season:  0,
		Episode: 0,
	}

	token, err := encodeVortexoSubtitleToken(want)
	if err != nil {
		t.Fatalf("encodeVortexoSubtitleToken failed: %v", err)
	}

	got, err := decodeVortexoSubtitleToken(token)
	if err != nil {
		t.Fatalf("decodeVortexoSubtitleToken failed: %v", err)
	}

	if got.Type != want.Type ||
		got.Title != want.Title ||
		got.Year != want.Year ||
		got.TMDBID != want.TMDBID ||
		got.IMDBID != want.IMDBID {
		t.Fatalf("decoded token = %#v, want %#v", got, want)
	}
}

func TestWantsVortexoPlayJSON(t *testing.T) {
	req, err := http.NewRequest("GET", "/api/v1/vortexo/play/token", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Accept", "application/json,video/*,*/*;q=0.8")
	if !wantsVortexoPlayJSON(req) {
		t.Fatal("expected JSON accept header to request JSON playback response")
	}

	req, err = http.NewRequest("GET", "/api/v1/vortexo/play/token?format=json", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	if !wantsVortexoPlayJSON(req) {
		t.Fatal("expected format=json to request JSON playback response")
	}
}

func TestLegacyAPIStreamObjectsPreserveRDWebDAVLocalFileStreams(t *testing.T) {
	const localPath = "/app/rd-library/TV/M.I.A (2026) {tmdb-262388}/Season 01/M.I.A - S01E01 - Revenge HEVC {tmdb-6061110}.mkv"

	streams := legacyAPIStreamObjects([]providers.TorrentioStream{{
		Name:   "M.I.A - S01E01 - Revenge HEVC {tmdb-6061110}.mkv",
		Title:  "M.I.A - S01E01 - Revenge HEVC {tmdb-6061110}.mkv",
		URL:    encodeVortexoLocalFileStreamURL(localPath),
		Cached: true,
		Source: vortexoRDWebDAVLibrarySource,
		Size:   3_200_000_000,
	}})

	if len(streams) != 1 {
		t.Fatalf("legacyAPIStreamObjects returned %d streams, want 1", len(streams))
	}
	if got := streams[0]["source"]; got != vortexoRDWebDAVLibrarySource {
		t.Fatalf("source = %#v, want %q", got, vortexoRDWebDAVLibrarySource)
	}
	if got := streams[0]["url"]; got != encodeVortexoLocalFileStreamURL(localPath) {
		t.Fatalf("url = %#v, want encoded local file stream URL", got)
	}
	if got := streams[0]["filename"]; got != "M.I.A - S01E01 - Revenge HEVC {tmdb-6061110}.mkv" {
		t.Fatalf("filename = %#v, want file name", got)
	}
	if got := streams[0]["codec"]; got != "HEVC" {
		t.Fatalf("codec = %#v, want HEVC", got)
	}
}

func resetVortexoBlockedSourcesForTest() {
	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash = make(map[string]vortexoBlockedSource)
	vortexoBlockedSources.Unlock()
}

func resetVortexoRealDebridLibrarySourceCacheForTest() {
	vortexoRealDebridLibrarySourceCache.Lock()
	vortexoRealDebridLibrarySourceCache.byKey = make(map[string]vortexoRealDebridLibrarySourceCacheEntry)
	vortexoRealDebridLibrarySourceCache.Unlock()
}
