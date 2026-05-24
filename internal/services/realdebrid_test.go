package services

import (
	"errors"
	"fmt"
	"testing"
)

func TestRealDebridAPIErrorDisabledEndpoint(t *testing.T) {
	err := &RealDebridAPIError{
		StatusCode: 403,
		ErrorName:  "disabled_endpoint",
		ErrorCode:  37,
	}

	if !errors.Is(err, ErrRealDebridDisabledEndpoint) {
		t.Fatal("expected disabled endpoint sentinel to match")
	}
}

func TestRealDebridDisabledEndpointWrapped(t *testing.T) {
	err := fmt.Errorf("failed to check availability: %w", &RealDebridAPIError{
		StatusCode: 403,
		ErrorName:  "disabled_endpoint",
		ErrorCode:  37,
	})

	if !isRealDebridDisabledEndpointError(err) {
		t.Fatal("expected wrapped disabled endpoint error to match")
	}
}

func TestChooseRDTorrentFileIDPrefersMatchingVideoName(t *testing.T) {
	info := &rdTorrentInfo{
		Files: []rdTorrentFile{
			{ID: 1, Path: "/Sample/sample.mkv", Bytes: 100},
			{ID: 2, Path: "/Show.Name.S01E02.1080p.mkv", Bytes: 5_000},
			{ID: 3, Path: "/extras.txt", Bytes: 10_000},
		},
	}

	got := chooseRDTorrentFileID(info, 0, "Show.Name.S01E02.1080p.mkv")
	if got != 2 {
		t.Fatalf("expected matching video file id 2, got %d", got)
	}
}

func TestChooseRDDownloadLinkMapsSelectedFileToLink(t *testing.T) {
	info := &rdTorrentInfo{
		Files: []rdTorrentFile{
			{ID: 1, Path: "/Show.Name.S01E01.mkv", Bytes: 4_000, Selected: 1},
			{ID: 2, Path: "/Show.Name.S01E02.mkv", Bytes: 5_000, Selected: 1},
		},
		Links: []string{"https://rd.example/episode-1", "https://rd.example/episode-2"},
	}

	got, err := chooseRDDownloadLink(info, 2, 0, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://rd.example/episode-2" {
		t.Fatalf("expected second selected file link, got %q", got)
	}
}
