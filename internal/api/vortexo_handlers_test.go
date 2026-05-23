package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

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

func resetVortexoBlockedSourcesForTest() {
	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash = make(map[string]vortexoBlockedSource)
	vortexoBlockedSources.Unlock()
}
