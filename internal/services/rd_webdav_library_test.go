package services

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	isettings "github.com/ZeroQ-bit/Vortexo-Server/internal/settings"
)

func TestParseRDWebDAVMediaFileEpisodeWithYear(t *testing.T) {
	candidate, ok := parseRDWebDAVMediaFile("/mnt/rd/shows/M.I.A.2026.S01/M.I.A.2026.S01E01.Revenge.720p.WEB-DL.x264-PW.mkv")
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
	expectedTags := []string{"720p", "WEB-DL", "AVC"}
	if !slices.Equal(candidate.QualityTags, expectedTags) {
		t.Fatalf("expected quality tags %v, got %v", expectedTags, candidate.QualityTags)
	}
}

func TestParseRDWebDAVMediaFileMovie(t *testing.T) {
	candidate, ok := parseRDWebDAVMediaFile("/mnt/rd/movies/The.Muppet.Movie.1979.1080p.BluRay.x265.mkv")
	if !ok {
		t.Fatal("expected parser to match movie file")
	}
	if candidate.MediaType != "movie" {
		t.Fatalf("expected movie media type, got %q", candidate.MediaType)
	}
	if candidate.Title != "the muppet movie" || candidate.Year != 1979 {
		t.Fatalf("unexpected movie parse: title=%q year=%d", candidate.Title, candidate.Year)
	}
	expectedTags := []string{"1080p", "BluRay", "HEVC"}
	if !slices.Equal(candidate.QualityTags, expectedTags) {
		t.Fatalf("expected quality tags %v, got %v", expectedTags, candidate.QualityTags)
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
	if !slices.Contains(args, "--allow-non-empty") {
		t.Fatalf("expected rclone mount args to include --allow-non-empty, got %v", args)
	}
}

func TestRDWebDAVMountFingerprintNormalizesTrailingSlash(t *testing.T) {
	first := rdWebDAVMountFingerprint(&isettings.Settings{
		RDWebDAVURL:      "https://webdav.torbox.app/",
		RDWebDAVUsername: "user@example.com",
		RDWebDAVPassword: "secret",
	})
	second := rdWebDAVMountFingerprint(&isettings.Settings{
		RDWebDAVURL:      "https://webdav.torbox.app",
		RDWebDAVUsername: "user@example.com",
		RDWebDAVPassword: "secret",
	})
	if first == "" || first != second {
		t.Fatalf("expected equivalent fingerprints, got %q and %q", first, second)
	}
	third := rdWebDAVMountFingerprint(&isettings.Settings{
		RDWebDAVURL:      "https://dav.real-debrid.com",
		RDWebDAVUsername: "user@example.com",
		RDWebDAVPassword: "secret",
	})
	if third == first {
		t.Fatal("expected different WebDAV URLs to produce different fingerprints")
	}
}

func TestRDWebDAVMountMarkerMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mount.marker")
	if rdWebDAVMountMarkerMatches(path, "abc") {
		t.Fatal("missing marker should not match")
	}
	if err := os.WriteFile(path, []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !rdWebDAVMountMarkerMatches(path, "abc") {
		t.Fatal("expected marker to match trimmed fingerprint")
	}
	if rdWebDAVMountMarkerMatches(path, "def") {
		t.Fatal("unexpected marker match")
	}
}

func TestRDWebDAVMountEntryRejectsSharedBindMount(t *testing.T) {
	fields := []string{"host/path", "/mnt/rd", "ext4", "rw,relatime"}
	if isRDWebDAVRcloneMountEntry(fields) {
		t.Fatal("expected ext4 bind mount to not count as a ready RD WebDAV rclone mount")
	}
}

func TestRDWebDAVMountEntryAcceptsRcloneFuseMount(t *testing.T) {
	fields := []string{"rdwebdav:", "/mnt/rd", "fuse.rclone", "rw,nosuid,nodev,relatime"}
	if !isRDWebDAVRcloneMountEntry(fields) {
		t.Fatal("expected rclone FUSE mount to count as ready")
	}
}

func TestRecoverableRDWebDAVReadError(t *testing.T) {
	recoverable := []error{
		os.ErrPermission,
	}
	if isRecoverableRDWebDAVReadError(recoverable[0]) {
		t.Fatal("plain permission errors should not trigger a WebDAV remount")
	}
	for _, errText := range []string{
		"readdirent /mnt/rd: input/output error",
		"stat /mnt/rd: transport endpoint is not connected",
		"open /mnt/rd: device not configured",
	} {
		if !isRecoverableRDWebDAVReadError(os.NewSyscallError("test", errString(errText))) {
			t.Fatalf("expected recoverable WebDAV read error for %q", errText)
		}
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}

func TestEpisodeSymlinkPathPrefersTVDBShowID(t *testing.T) {
	series := &models.Series{
		TMDBID:   262388,
		Title:    "M.I.A.",
		Year:     2026,
		Metadata: models.Metadata{"tvdb_id": 123456},
	}
	episode := &models.Episode{TMDBID: 6061110, Title: "Revenge"}
	candidate := rdWebDAVMediaCandidate{Season: 1, Episode: 1, Ext: ".mkv", QualityTags: []string{"720p", "WEB-DL"}}

	path := episodeSymlinkPath("/app/rd-library", series, episode, candidate)
	if filepath.Base(filepath.Dir(filepath.Dir(path))) != "M.I.A (2026) {tvdb-123456}" {
		t.Fatalf("unexpected series symlink folder: %s", filepath.Base(filepath.Dir(filepath.Dir(path))))
	}
	if filepath.Base(path) != "M.I.A - S01E01 - Revenge [720p WEB-DL].mkv" {
		t.Fatalf("unexpected episode symlink filename: %s", filepath.Base(path))
	}
}

func TestMovieSymlinkPathIncludesQualityTags(t *testing.T) {
	movie := &models.Movie{TMDBID: 122, Title: "The Return of the King", Year: 2003}
	candidate := rdWebDAVMediaCandidate{
		Year:        2003,
		Ext:         ".mkv",
		QualityTags: []string{"2160p", "Remux", "HEVC", "DV", "Atmos"},
	}

	path := movieSymlinkPath("/app/rd-library", movie, candidate)
	expected := "The Return of the King (2003) {tmdb-122} [2160p Remux HEVC DV Atmos].mkv"
	if filepath.Base(path) != expected {
		t.Fatalf("unexpected movie symlink filename: %s", filepath.Base(path))
	}
}

func TestRemoveSupersededRDWebDAVSymlinkKeepsCurrentPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "source.mkv")
	oldLink := filepath.Join(dir, "old.mkv")
	currentLink := filepath.Join(dir, "current.mkv")
	if err := os.WriteFile(target, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, oldLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, currentLink); err != nil {
		t.Fatal(err)
	}

	removeSupersededRDWebDAVSymlink(models.Metadata{"rd_webdav_symlink_path": oldLink}, currentLink)
	if _, err := os.Lstat(oldLink); !os.IsNotExist(err) {
		t.Fatalf("expected old symlink removed, err=%v", err)
	}
	if _, err := os.Lstat(currentLink); err != nil {
		t.Fatalf("expected current symlink kept: %v", err)
	}
}

func TestRemoveLegacyTMDBSeriesFolderRemovesGeneratedSymlinkTree(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "source.mkv")
	if err := os.WriteFile(target, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyLink := filepath.Join(dir, "TV", "The Sopranos (1999) {tmdb-1398}", "Season 01", "The Sopranos - S01E01 - The Sopranos.mkv")
	if err := os.MkdirAll(filepath.Dir(legacyLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, legacyLink); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{
		TMDBID:   1398,
		Title:    "The Sopranos",
		Year:     1999,
		Metadata: models.Metadata{"tvdb_id": 75299},
	}

	removeLegacyTMDBSeriesFolder(dir, series, filepath.Join(dir, "TV", "The Sopranos (1999) {tvdb-75299}"))
	if _, err := os.Lstat(filepath.Join(dir, "TV", "The Sopranos (1999) {tmdb-1398}")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy folder removed, err=%v", err)
	}
}

func TestRemoveGeneratedSymlinkTreeKeepsRegularFiles(t *testing.T) {
	dir := t.TempDir()
	regularFile := filepath.Join(dir, "TV", "Show (2026) {tmdb-1}", "Season 01", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(regularFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regularFile, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	if removeGeneratedSymlinkTree(filepath.Join(dir, "TV", "Show (2026) {tmdb-1}")) {
		t.Fatal("expected regular-file tree to be kept")
	}
	if _, err := os.Lstat(regularFile); err != nil {
		t.Fatalf("expected regular file kept: %v", err)
	}
}
