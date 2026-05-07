package xtream

import "testing"

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
