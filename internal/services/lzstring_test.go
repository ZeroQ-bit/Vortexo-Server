package services

import "testing"

func TestDecompressLZStringFromEncodedURIComponent(t *testing.T) {
	const compressed = "N4IgLglmA2CmIC4QBVYGcwAIAyEMgBpwB7AJ1NgDsw1EBtUAMwjkoEMBbeJAUQA9OABzgA6ALLEAbhFgiATAAY5AFhEBGBQA4FgkQHUeAIQC0AEWwiOAa0mEQACzZp7iEGwBGAYwAmsRmrkAZmUAVgA2AHZNAE4FDx8-AODwqNj43385O3cATzB0RCTQyJiFAF8AXTKgA"
	const expected = `{"title":"Test List","torrents":[{"filename":"Example.Movie.2024.1080p.WEB-DL.mkv","hash":"abcdef1234567890abcdef1234567890abcdef12","bytes":1234567890}]}`

	got, err := decompressLZStringFromEncodedURIComponent(compressed)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if got != expected {
		t.Fatalf("decoded payload mismatch:\nwant %s\ngot  %s", expected, got)
	}
}
