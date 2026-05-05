package api

import "testing"

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
