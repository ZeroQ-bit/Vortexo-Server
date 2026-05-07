package providers

import "testing"

type fakeStreamProvider struct {
	movieStreams  []TorrentioStream
	seriesStreams []TorrentioStream
}

func (f fakeStreamProvider) GetMovieStreams(string) ([]TorrentioStream, error) {
	return f.movieStreams, nil
}

func (f fakeStreamProvider) GetSeriesStreams(string, int, int) ([]TorrentioStream, error) {
	return f.seriesStreams, nil
}

func TestBuildRuntimeAddonsKeepsConfiguredEnabledAddons(t *testing.T) {
	addons := []StremioAddon{
		{Name: "Custom", URL: "https://example.com/manifest.json/", Enabled: true},
		{Name: "Disabled", URL: "https://disabled.example.com/manifest.json", Enabled: false},
	}

	runtimeAddons := BuildRuntimeAddons(addons, true, "rd-token", true, "https://comet.example.com")

	if len(runtimeAddons) != 1 {
		t.Fatalf("expected 1 configured addon, got %d", len(runtimeAddons))
	}
	if runtimeAddons[0].Name != "Custom" {
		t.Fatalf("expected configured addon name to be preserved, got %q", runtimeAddons[0].Name)
	}
	if runtimeAddons[0].URL != "https://example.com/manifest.json" {
		t.Fatalf("expected addon URL to be normalized, got %q", runtimeAddons[0].URL)
	}
}

func TestBuildRuntimeAddonsBootstrapsDefaultsForRealDebrid(t *testing.T) {
	runtimeAddons := BuildRuntimeAddons(nil, true, "rd-token", true, "")

	if len(runtimeAddons) != 1 {
		t.Fatalf("expected 1 default addon, got %d", len(runtimeAddons))
	}

	if runtimeAddons[0].Name != "Torrentio" || runtimeAddons[0].URL != DefaultTorrentioAddonURL {
		t.Fatalf("expected Torrentio default addon, got %#v", runtimeAddons[0])
	}
}

func TestBuildRuntimeAddonsReturnsEmptyWithoutRealDebrid(t *testing.T) {
	runtimeAddons := BuildRuntimeAddons(nil, false, "", true, "")

	if len(runtimeAddons) != 0 {
		t.Fatalf("expected no runtime addons without Real-Debrid, got %#v", runtimeAddons)
	}
}

func TestGetBestStreamAppliesQualityExclusions(t *testing.T) {
	mp := &MultiProvider{
		Providers: []StreamProvider{fakeStreamProvider{movieStreams: []TorrentioStream{
			{Name: "Movie.2160p.HDR10.WEB-DL", Title: "Movie.2160p.HDR10.WEB-DL", Quality: "2160p", Cached: true, Size: 30 << 30},
			{Name: "Movie.1080p.SDR.WEB-DL", Title: "Movie.1080p.SDR.WEB-DL", Quality: "1080p", Cached: true, Size: 8 << 30},
		}}},
		ProviderNames: []string{"fake"},
	}
	mp.SetQualityFilterSettings(func() string { return "hdr" })

	stream, err := mp.GetBestStream("tt123", nil, nil, 2160)
	if err != nil {
		t.Fatalf("expected non-HDR fallback stream, got error: %v", err)
	}
	if stream == nil || stream.Quality != "1080p" {
		t.Fatalf("expected 1080p SDR stream after HDR exclusion, got %#v", stream)
	}
}

func TestGetBestStreamAppliesMaxResolution(t *testing.T) {
	mp := &MultiProvider{
		Providers: []StreamProvider{fakeStreamProvider{movieStreams: []TorrentioStream{
			{Name: "Movie.2160p.WEB-DL", Title: "Movie.2160p.WEB-DL", Quality: "2160p", Cached: true, Size: 30 << 30},
			{Name: "Movie.1080p.WEB-DL", Title: "Movie.1080p.WEB-DL", Quality: "1080p", Cached: true, Size: 8 << 30},
		}}},
		ProviderNames: []string{"fake"},
	}

	stream, err := mp.GetBestStream("tt123", nil, nil, 1080)
	if err != nil {
		t.Fatalf("expected 1080p stream, got error: %v", err)
	}
	if stream == nil || stream.Quality != "1080p" {
		t.Fatalf("expected max resolution filter to select 1080p, got %#v", stream)
	}
}
