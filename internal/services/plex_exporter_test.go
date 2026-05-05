package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Zerr0-C00L/StreamArr/internal/models"
	"github.com/Zerr0-C00L/StreamArr/internal/settings"
)

func TestPathWithinRootRejectsSiblingPrefix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "movies")
	sibling := filepath.Join(base, "movies-other", "item.mkv")

	if !pathWithinRoot(filepath.Join(root, "Movie", "Movie.mkv"), root) {
		t.Fatal("expected child path to be inside root")
	}
	if pathWithinRoot(sibling, root) {
		t.Fatal("expected sibling with shared prefix to be outside root")
	}
	if pathWithinRoot(root, root) {
		t.Fatal("expected root itself to be rejected as a managed export file")
	}
}

func TestPlexExportPathNeedsRefreshDetectsBrokenSymlink(t *testing.T) {
	base := t.TempDir()
	linkPath := filepath.Join(base, "movie.mkv")
	targetPath := filepath.Join(base, "missing.mkv")

	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	needsRefresh, err := plexExportPathNeedsRefresh(linkPath)
	if err != nil {
		t.Fatalf("check symlink: %v", err)
	}
	if !needsRefresh {
		t.Fatal("expected broken symlink to need refresh")
	}
}

func TestRemoveManagedExportSymlinkOnlyRemovesSymlinksUnderRoots(t *testing.T) {
	base := t.TempDir()
	moviesRoot := filepath.Join(base, "movies")
	outsideRoot := filepath.Join(base, "outside")
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("create movies root: %v", err)
	}
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("create outside root: %v", err)
	}

	target := filepath.Join(base, "target.mkv")
	if err := os.WriteFile(target, []byte("video"), 0o644); err != nil {
		t.Fatalf("create target: %v", err)
	}

	managedLink := filepath.Join(moviesRoot, "movie.mkv")
	if err := os.Symlink(target, managedLink); err != nil {
		t.Fatalf("create managed symlink: %v", err)
	}

	regularFile := filepath.Join(moviesRoot, "regular.mkv")
	if err := os.WriteFile(regularFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("create regular file: %v", err)
	}

	outsideLink := filepath.Join(outsideRoot, "movie.mkv")
	if err := os.Symlink(target, outsideLink); err != nil {
		t.Fatalf("create outside symlink: %v", err)
	}

	exporter := &PlexExporter{}
	cfg := &settings.Settings{PlexExportMoviesPath: moviesRoot}

	removed, err := exporter.removeManagedExportSymlink(cfg, managedLink)
	if err != nil {
		t.Fatalf("remove managed symlink: %v", err)
	}
	if !removed {
		t.Fatal("expected managed symlink to be removed")
	}
	if _, err := os.Lstat(managedLink); !os.IsNotExist(err) {
		t.Fatalf("expected managed symlink to be gone, got err=%v", err)
	}

	removed, err = exporter.removeManagedExportSymlink(cfg, regularFile)
	if err != nil {
		t.Fatalf("regular file check: %v", err)
	}
	if removed {
		t.Fatal("expected regular file to be left alone")
	}
	if _, err := os.Lstat(regularFile); err != nil {
		t.Fatalf("expected regular file to remain: %v", err)
	}

	removed, err = exporter.removeManagedExportSymlink(cfg, outsideLink)
	if err != nil {
		t.Fatalf("outside symlink check: %v", err)
	}
	if removed {
		t.Fatal("expected outside symlink to be left alone")
	}
	if _, err := os.Lstat(outsideLink); err != nil {
		t.Fatalf("expected outside symlink to remain: %v", err)
	}
}

func TestResolveCandidatePathSeriesDirectoryUsesRequestedEpisode(t *testing.T) {
	base := t.TempDir()
	seasonPack := filepath.Join(base, "Dandelion.S01.1080p.WEB-DL")
	wrongEpisode := filepath.Join(seasonPack, "Dandelion.S01E07.1080p.mkv")
	rightEpisode := filepath.Join(seasonPack, "Dandelion.S01E01.1080p.mkv")
	writeTestVideo(t, wrongEpisode, 32)
	writeTestVideo(t, rightEpisode, 8)

	resolved, ok := resolveCandidatePath(seasonPack, &models.CachedStream{
		MediaType: "series",
		Season:    1,
		Episode:   1,
	})
	if !ok {
		t.Fatal("expected series directory to resolve the requested episode")
	}
	if resolved != rightEpisode {
		t.Fatalf("expected %s, got %s", rightEpisode, resolved)
	}
}

func TestResolveCandidatePathSeriesDirectoryRejectsWrongEpisode(t *testing.T) {
	base := t.TempDir()
	seasonPack := filepath.Join(base, "Dandelion.S01.1080p.WEB-DL")
	writeTestVideo(t, filepath.Join(seasonPack, "Dandelion.S01E07.1080p.mkv"), 32)

	resolved, ok := resolveCandidatePath(seasonPack, &models.CachedStream{
		MediaType: "series",
		Season:    1,
		Episode:   1,
	})
	if ok || resolved != "" {
		t.Fatalf("expected no match for missing requested episode, got ok=%v path=%s", ok, resolved)
	}
}

func TestFindBestMountedMatchSeriesRejectsEpisodeOnlyUnrelatedTitle(t *testing.T) {
	root := t.TempDir()
	writeTestVideo(t, filepath.Join(root, "__all__", "We Are All Trying Here S01E01 1080p.mkv"), 32)

	match, err := findBestMountedMatch(root,
		"Absolute Value of Romance S01E01 MULTI 1080p WEB H264-HiggsBoson.mkv",
		&models.CachedStream{
			MediaType: "series",
			Season:    1,
			Episode:   1,
		},
		"Absolute Value of Romance",
		2026,
	)
	if err != nil {
		t.Fatalf("find mounted match: %v", err)
	}
	if match != "" {
		t.Fatalf("expected unrelated S01E01 file to be ignored, got %s", match)
	}
}

func TestFindBestMountedMatchSeriesDirectoryUsesRequestedEpisode(t *testing.T) {
	root := t.TempDir()
	seasonPack := filepath.Join(root, "Dandelion.S01.1080p.WEB-DL")
	wrongEpisode := filepath.Join(seasonPack, "Dandelion.S01E07.1080p.mkv")
	rightEpisode := filepath.Join(seasonPack, "Dandelion.S01E01.1080p.mkv")
	writeTestVideo(t, wrongEpisode, 32)
	writeTestVideo(t, rightEpisode, 8)

	match, err := findBestMountedMatch(root,
		"Dandelion.S01.1080p.WEB-DL",
		&models.CachedStream{
			MediaType: "series",
			Season:    1,
			Episode:   1,
		},
		"Dandelion",
		2025,
	)
	if err != nil {
		t.Fatalf("find mounted match: %v", err)
	}
	if match != rightEpisode {
		t.Fatalf("expected %s, got %s", rightEpisode, match)
	}
}

func TestFindBestMountedMatchSeriesDirectoryRejectsWrongEpisode(t *testing.T) {
	root := t.TempDir()
	seasonPack := filepath.Join(root, "Dandelion.S01.1080p.WEB-DL")
	writeTestVideo(t, filepath.Join(seasonPack, "Dandelion.S01E07.1080p.mkv"), 32)

	match, err := findBestMountedMatch(root,
		"Dandelion.S01.1080p.WEB-DL",
		&models.CachedStream{
			MediaType: "series",
			Season:    1,
			Episode:   1,
		},
		"Dandelion",
		2025,
	)
	if err != nil {
		t.Fatalf("find mounted match: %v", err)
	}
	if match != "" {
		t.Fatalf("expected missing requested episode to be ignored, got %s", match)
	}
}

func TestSeriesExportSymlinkNeedsRefreshAcceptsExpectedTarget(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "rd", "Dandelion.S01E01.1080p.mkv")
	exportPath := filepath.Join(base, "shows", "Dandelion - s01e01.mkv")
	writeTestVideo(t, source, 32)
	writeTestSymlink(t, source, exportPath)

	needsRefresh, err := seriesExportSymlinkNeedsRefresh(&models.CachedStream{
		MediaType: "series",
		Season:    1,
		Episode:   1,
	}, exportPath, "Dandelion.S01.1080p.WEB-DL", "Dandelion")
	if err != nil {
		t.Fatalf("verify symlink target: %v", err)
	}
	if needsRefresh {
		t.Fatal("expected matching series symlink target to remain valid")
	}
}

func TestSeriesExportSymlinkNeedsRefreshRejectsWrongEpisodeTarget(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "rd", "Dandelion.S01E07.1080p.mkv")
	exportPath := filepath.Join(base, "shows", "Dandelion - s01e01.mkv")
	writeTestVideo(t, source, 32)
	writeTestSymlink(t, source, exportPath)

	needsRefresh, err := seriesExportSymlinkNeedsRefresh(&models.CachedStream{
		MediaType: "series",
		Season:    1,
		Episode:   1,
	}, exportPath, "Dandelion.S01.1080p.WEB-DL", "Dandelion")
	if err != nil {
		t.Fatalf("verify symlink target: %v", err)
	}
	if !needsRefresh {
		t.Fatal("expected wrong episode symlink target to need refresh")
	}
}

func TestSeriesExportSymlinkNeedsRefreshRejectsUnrelatedSameEpisodeTarget(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "rd", "We Are All Trying Here S01E01 1080p.mkv")
	exportPath := filepath.Join(base, "shows", "Absolute Value of Romance - s01e01.mkv")
	writeTestVideo(t, source, 32)
	writeTestSymlink(t, source, exportPath)

	needsRefresh, err := seriesExportSymlinkNeedsRefresh(&models.CachedStream{
		MediaType: "series",
		Season:    1,
		Episode:   1,
	}, exportPath, "Absolute Value of Romance S01E01 MULTI 1080p WEB H264-HiggsBoson.mkv", "Absolute Value of Romance")
	if err != nil {
		t.Fatalf("verify symlink target: %v", err)
	}
	if !needsRefresh {
		t.Fatal("expected unrelated same-episode symlink target to need refresh")
	}
}

func writeTestVideo(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create test media directory: %v", err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("create test video: %v", err)
	}
}

func writeTestSymlink(t *testing.T, target, linkPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("create symlink directory: %v", err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
}
