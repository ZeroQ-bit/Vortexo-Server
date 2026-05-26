package services

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

func TestParseRDWebDAVMediaFileEpisodeWithYear(t *testing.T) {
	candidate, ok := parseRDWebDAVMediaFile("/mnt/rd/shows/M.I.A.2026.S01/M.I.A.2026.S01E01.Revenge.720p-PW.mkv")
	if !ok {
		t.Fatal("expected parser to match episode file")
	}
	if candidate.MediaType != "episode" {
		t.Fatalf("expected episode media type, got %q", candidate.MediaType)
	}
	if candidate.Title != "m i a" {
		t.Fatalf("expected title M I A, got %q", candidate.Title)
	}
	if candidate.Year != 2026 || candidate.Season != 1 || candidate.Episode != 1 {
		t.Fatalf("unexpected episode parse: year=%d season=%d episode=%d", candidate.Year, candidate.Season, candidate.Episode)
	}
}

func TestParseRDWebDAVMediaFileMovie(t *testing.T) {
	candidate, ok := parseRDWebDAVMediaFile("/mnt/rd/movies/The.Muppet.Movie.1979.1080p.BluRay.mkv")
	if !ok {
		t.Fatal("expected parser to match movie file")
	}
	if candidate.MediaType != "movie" {
		t.Fatalf("expected movie media type, got %q", candidate.MediaType)
	}
	if candidate.Title != "the muppet movie" || candidate.Year != 1979 {
		t.Fatalf("unexpected movie parse: title=%q year=%d", candidate.Title, candidate.Year)
	}
}

func TestParseRDWebDAVMediaFileKeepsTMDBID(t *testing.T) {
	candidate, ok := parseRDWebDAVMediaFile("/mnt/rd/movies/Backrooms.(2026).{tmdb-1083381}/Backrooms.2026.1080p.mkv")
	if !ok {
		t.Fatal("expected parser to match movie with TMDB id")
	}
	if candidate.TMDBID != 1083381 {
		t.Fatalf("expected TMDB id 1083381, got %d", candidate.TMDBID)
	}
}

func TestEnsureSymlinkCreatesAndUpdates(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.mkv")
	second := filepath.Join(dir, "second.mkv")
	link := filepath.Join(dir, "library", "movie.mkv")
	if err := os.WriteFile(first, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := ensureSymlink(first, link)
	if err != nil {
		t.Fatal(err)
	}
	if state != "created" {
		t.Fatalf("expected created, got %q", state)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != first {
		t.Fatalf("expected first target, got %q", target)
	}

	state, err = ensureSymlink(second, link)
	if err != nil {
		t.Fatal(err)
	}
	if state != "updated" {
		t.Fatalf("expected updated, got %q", state)
	}
	target, err = os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != second {
		t.Fatalf("expected second target, got %q", target)
	}
}

func TestBuildRDWebDAVRcloneMountArgsIncludesAllowOther(t *testing.T) {
	args := buildRDWebDAVRcloneMountArgs("/app/cache/rclone.conf", "/mnt/rd")
	if !slices.Contains(args, "--allow-other") {
		t.Fatalf("expected rclone mount args to include --allow-other, got %v", args)
	}
}

func TestEpisodeSymlinkPathIncludesTMDBIDs(t *testing.T) {
	series := &models.Series{TMDBID: 262388, Title: "M.I.A.", Year: 2026}
	episode := &models.Episode{TMDBID: 6061110, Title: "Revenge"}
	candidate := rdWebDAVMediaCandidate{Season: 1, Episode: 1, Ext: ".mkv"}

	path := episodeSymlinkPath("/app/rd-library", series, episode, candidate)
	if filepath.Base(path) != "M.I.A - S01E01 - Revenge {tmdb-6061110}.mkv" {
		t.Fatalf("unexpected episode symlink filename: %s", filepath.Base(path))
	}
}
