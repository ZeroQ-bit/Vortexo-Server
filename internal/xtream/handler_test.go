package xtream

import (
	"strings"
	"testing"
)

func TestDecodeMoviePlaybackIDSupportsEncodedQualityVariant(t *testing.T) {
	streamID := encodeMovieQualityStreamID(1174240, "1080p")

	tmdbID, quality, err := decodeMoviePlaybackID("900011742402")
	if err != nil {
		t.Fatalf("expected encoded stream id to decode, got error: %v", err)
	}
	if streamID != 900011742402 {
		t.Fatalf("test fixture drifted, got encoded id %d", streamID)
	}
	if tmdbID != 1174240 || quality != "1080p" {
		t.Fatalf("expected tmdb 1174240 quality 1080p, got tmdb=%d quality=%q", tmdbID, quality)
	}
}

func TestDecodeMoviePlaybackIDSupportsSuffixQualityVariant(t *testing.T) {
	tmdbID, quality, err := decodeMoviePlaybackID("1174240_720p")
	if err != nil {
		t.Fatalf("expected suffixed stream id to decode, got error: %v", err)
	}
	if tmdbID != 1174240 || quality != "720p" {
		t.Fatalf("expected tmdb 1174240 quality 720p, got tmdb=%d quality=%q", tmdbID, quality)
	}
}

func TestMovieDirectSourcesUsesStoredVODLinks(t *testing.T) {
	sources := movieDirectSources(map[string]interface{}{
		"iptv_vod_sources": []interface{}{
			map[string]interface{}{"name": "Xtream Provider", "url": " https://cdn.example/movie.m3u8 ", "quality": "1080p"},
			map[string]interface{}{"name": "Duplicate", "url": "https://cdn.example/movie.m3u8"},
		},
		"balkan_vod_streams": []map[string]interface{}{
			{"name": "Balkan", "url": "https://cdn.example/movie.mp4", "quality": "HD"},
		},
	})

	if len(sources) != 2 {
		t.Fatalf("expected 2 unique direct sources, got %#v", sources)
	}
	if sources[0].Name != "Xtream Provider" || sources[0].URL != "https://cdn.example/movie.m3u8" || sources[0].Quality != "1080p" {
		t.Fatalf("unexpected first source: %#v", sources[0])
	}
	if sources[1].Name != "Balkan" || sources[1].URL != "https://cdn.example/movie.mp4" || sources[1].Quality != "HD" {
		t.Fatalf("unexpected second source: %#v", sources[1])
	}
}

func TestEpisodeDirectSourcesFindsMatchingBalkanEpisode(t *testing.T) {
	sources := episodeDirectSources(map[string]interface{}{
		"balkan_vod_seasons": []interface{}{
			map[string]interface{}{
				"number": float64(2),
				"episodes": []interface{}{
					map[string]interface{}{"episode": float64(4), "title": "Wrong", "url": "https://cdn.example/wrong.mp4"},
					map[string]interface{}{"episode": float64(5), "title": "Episode 5", "url": "https://cdn.example/s02e05.mp4", "quality": "720p"},
				},
			},
		},
	}, 2, 5)

	if len(sources) != 1 {
		t.Fatalf("expected 1 matching source, got %#v", sources)
	}
	if sources[0].Name != "Episode 5" || sources[0].URL != "https://cdn.example/s02e05.mp4" || sources[0].Quality != "720p" {
		t.Fatalf("unexpected source: %#v", sources[0])
	}
}

func TestWriteMovieDirectSourcePlaylistEntriesUsesRealURLs(t *testing.T) {
	sources := []xtreamDirectSource{
		{Name: "One", URL: "https://cdn.example/one.mp4", Quality: "1080p"},
		{Name: "Two", URL: "https://cdn.example/two.mp4", Quality: "720p"},
	}

	var single strings.Builder
	count := writeMovieDirectSourcePlaylistEntries(&single, 123, "Movie", " (2024)", "poster.jpg", sources, false, nil)
	if count != 1 {
		t.Fatalf("expected one playlist entry, got %d", count)
	}
	if got := single.String(); !strings.Contains(got, "https://cdn.example/one.mp4") || strings.Contains(got, "[1080P]") || strings.Contains(got, "[720P]") {
		t.Fatalf("single-source playlist should contain the real URL without hardcoded quality variants, got:\n%s", got)
	}

	var duplicated strings.Builder
	count = writeMovieDirectSourcePlaylistEntries(&duplicated, 123, "Movie", " (2024)", "poster.jpg", sources, true, nil)
	if count != 2 {
		t.Fatalf("expected two playlist entries, got %d", count)
	}
	if got := duplicated.String(); !strings.Contains(got, "Movie (2024) [One]") || !strings.Contains(got, "https://cdn.example/two.mp4") {
		t.Fatalf("duplicated playlist should preserve source names and URLs, got:\n%s", got)
	}
}
