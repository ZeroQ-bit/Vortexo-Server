package api

import (
	"testing"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/settings"
)

func TestExtractHashFromURLResolveSkipsDebridToken(t *testing.T) {
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw := "/resolve/realdebrid/" + token + "/" + hash + "/null/1/movie.mkv"

	got := extractHashFromURL(raw)
	if got != hash {
		t.Fatalf("expected torrent hash %q, got %q", hash, got)
	}
}

func TestExtractHashFromURLRejectsBareHexWithoutContext(t *testing.T) {
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw := "https://example.invalid/path/" + token + "/movie.mkv"

	if got := extractHashFromURL(raw); got != "" {
		t.Fatalf("expected no hash from context-free token, got %q", got)
	}
}

func TestExtractHashFromURLAcceptsMagnetBTIH(t *testing.T) {
	hash := "ABCDEF1234567890ABCDEF1234567890ABCDEF12"
	raw := "magnet:?xt=urn:btih:" + hash + "&dn=movie"

	got := extractHashFromURL(raw)
	if got != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Fatalf("expected normalized hash, got %q", got)
	}
}

func TestExtractHashFromURLAcceptsHashQuery(t *testing.T) {
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw := "https://example.invalid/stream?api_key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&infoHash=" + hash

	got := extractHashFromURL(raw)
	if got != hash {
		t.Fatalf("expected infoHash query value, got %q", got)
	}
}

func TestExtractHashFromURLAcceptsFileIndexContext(t *testing.T) {
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw := "https://example.invalid/stream/movie/tt1234567/" + hash + "/2/file.mkv"

	got := extractHashFromURL(raw)
	if got != hash {
		t.Fatalf("expected hash before file index, got %q", got)
	}
}

func TestFilterSeriesEpisodesForCacheScan(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	past := now.AddDate(0, 0, -1)
	future := now.AddDate(0, 0, 1)

	episodes := []*models.Episode{
		nil,
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: &past, Monitored: true},
		{SeasonNumber: 1, EpisodeNumber: 2, Monitored: true},
		{SeasonNumber: 1, EpisodeNumber: 3, AirDate: &future, Monitored: true},
		{SeasonNumber: 1, EpisodeNumber: 4, AirDate: &past, Monitored: false},
		{SeasonNumber: 0, EpisodeNumber: 1, AirDate: &past, Monitored: true},
		{SeasonNumber: 1, EpisodeNumber: 0, AirDate: &past, Monitored: true},
	}

	got := filterSeriesEpisodesForCacheScan(episodes, now)
	if len(got) != 2 {
		t.Fatalf("expected 2 scan candidates, got %d", len(got))
	}
	if got[0].SeasonNumber != 1 || got[0].EpisodeNumber != 1 {
		t.Fatalf("expected first scan candidate to be S01E01, got S%02dE%02d", got[0].SeasonNumber, got[0].EpisodeNumber)
	}
	if got[1].SeasonNumber != 1 || got[1].EpisodeNumber != 2 {
		t.Fatalf("expected second scan candidate to be S01E02, got S%02dE%02d", got[1].SeasonNumber, got[1].EpisodeNumber)
	}
}

func TestTorBoxLibraryAutoAddEnabledUsesAPIKeyWithoutProviderToggle(t *testing.T) {
	if !TorBoxLibraryAutoAddEnabled(&settings.Settings{
		UseTorBox:                  false,
		AutoAddBestStreamsToTorBox: true,
		TorBoxAPIKey:               "tb-key",
	}) {
		t.Fatal("expected TorBox library auto-add to use the API key even when TorBox is not the default provider")
	}

	if TorBoxLibraryAutoAddEnabled(&settings.Settings{
		UseTorBox:                  true,
		AutoAddBestStreamsToTorBox: false,
		TorBoxAPIKey:               "tb-key",
	}) {
		t.Fatal("expected TorBox library auto-add to stay disabled when auto-add is off")
	}

	if TorBoxLibraryAutoAddEnabled(&settings.Settings{
		UseTorBox:                  true,
		AutoAddBestStreamsToTorBox: true,
	}) {
		t.Fatal("expected TorBox library auto-add to require a TorBox API key")
	}
}
