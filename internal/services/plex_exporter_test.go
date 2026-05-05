package services

import (
	"os"
	"path/filepath"
	"testing"

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
