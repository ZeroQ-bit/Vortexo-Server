package services

import (
	"testing"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

func TestDecodeDMMHashlistHTML(t *testing.T) {
	const compressed = "N4IgLglmA2CmIC4QBVYGcwAIAyEMgBpwB7AJ1NgDsw1EBtUAMwjkoEMBbeJAUQA9OABzgA6ALLEAbhFgiATAAY5AFhEBGBQA4FgkQHUeAIQC0AEWwiOAa0mEQACzZp7iEGwBGAYwAmsRmrkAZmUAVgA2AHZNAE4FDx8-AODwqNj43385O3cATzB0RCTQyJiFAF8AXTKgA"
	html := `<iframe src="https://debridmediamanager.com/hashlist#` + compressed + `"></iframe>`

	torrents, err := decodeDMMHashlistHTML(html)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("expected one torrent, got %d", len(torrents))
	}
	if torrents[0].Filename != "Example.Movie.2024.1080p.WEB-DL.mkv" {
		t.Fatalf("unexpected filename: %q", torrents[0].Filename)
	}
	if torrents[0].Hash != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Fatalf("unexpected hash: %q", torrents[0].Hash)
	}
	if torrents[0].Bytes != 1234567890 {
		t.Fatalf("unexpected byte size: %d", torrents[0].Bytes)
	}
}

func TestParseDMMFilenameMovie(t *testing.T) {
	candidate, ok := parseDMMFilename("Example.Movie.2024.1080p.WEB-DL.x265-GROUP.mkv")
	if !ok {
		t.Fatal("expected movie candidate")
	}
	if candidate.MediaType != "movie" || candidate.Title != "example movie" || candidate.Year != 2024 {
		t.Fatalf("unexpected candidate: %+v", candidate)
	}
}

func TestParseDMMFilenameSeriesEpisode(t *testing.T) {
	candidate, ok := parseDMMFilename("Example.Show.2022.S02E07.1080p.WEB-DL.mkv")
	if !ok {
		t.Fatal("expected series candidate")
	}
	if candidate.MediaType != "series" || candidate.Title != "example show" || candidate.Year != 2022 || candidate.Season != 2 || candidate.Episode != 7 {
		t.Fatalf("unexpected candidate: %+v", candidate)
	}
}

func TestBestDMMCandidatesByGroupTrustsHashlistsWhenAvailabilityDisabled(t *testing.T) {
	grouped := map[string][]dmmCandidate{
		"movie:example:2024:0:0": {
			{
				MediaType: "movie",
				Title:     "Example",
				Year:      2024,
				Torrent:   DMMHashlistTorrent{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 1},
				Stream:    testDMMStream(10),
			},
			{
				MediaType: "movie",
				Title:     "Example",
				Year:      2024,
				Torrent:   DMMHashlistTorrent{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Bytes: 2},
				Stream:    testDMMStream(50),
			},
		},
	}

	best := bestDMMCandidatesByGroup(grouped, nil)
	if len(best) != 1 {
		t.Fatalf("expected one trusted candidate, got %d", len(best))
	}
	if best[0].Torrent.Hash != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("expected highest score candidate, got %s", best[0].Torrent.Hash)
	}
}

func testDMMStream(score int) models.TorrentStream {
	return models.TorrentStream{QualityScore: score}
}
