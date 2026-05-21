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

func TestGenericStremioProviderPreservesConfiguredTorrentioURL(t *testing.T) {
	provider := NewGenericStremioProvider(
		"Torrentio",
		"https://torrentio.strem.fun/sort=qualitysize|qualityfilter=threed,scr",
		"rd-token",
		nil,
	)

	got := provider.buildConfigURL("movie", "tt123", nil, nil)
	want := "https://torrentio.strem.fun/sort=qualitysize|qualityfilter=threed,scr|debridoptions=nodownloadlinks,nocatalog|realdebrid=rd-token/stream/movie/tt123.json"
	if got != want {
		t.Fatalf("expected configured Torrentio URL to be preserved, got %q", got)
	}
}

func TestGenericStremioProviderRewritesManifestURL(t *testing.T) {
	provider := NewGenericStremioProvider(
		"MediaFusion",
		"https://mediafusion.example/config/manifest.json",
		"rd-token",
		nil,
	)

	got := provider.buildConfigURL("series", "tt123", intPtr(2), intPtr(5))
	want := "https://mediafusion.example/config/stream/series/tt123:2:5.json"
	if got != want {
		t.Fatalf("expected manifest URL to become stream URL, got %q", got)
	}
}

func intPtr(value int) *int {
	return &value
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

func TestFilterSeriesStreamsByEpisodeKeepsExactEpisodeOnly(t *testing.T) {
	streams := []TorrentioStream{
		{Title: "Historys.Greatest.Mysteries.S07E03.1080p.WEB.h264-EDITH"},
		{Title: "Historys.Greatest.Mysteries.S07E13.1080p.WEB.h264-EDITH"},
		{Title: "Historys Greatest Mysteries 7x03 720p WEB"},
		{Title: "Historys Greatest Mysteries Season 7 Episode 3 1080p"},
		{Title: "Historys Greatest Mysteries Season 7 Episode 30 1080p"},
		{Title: "Historys Greatest Mysteries Season 7 Pack 1080p"},
	}

	got := filterSeriesStreamsByEpisode(streams, 7, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 exact episode streams, got %d: %#v", len(got), got)
	}
	for _, stream := range got {
		season, episode, ok := parseStreamEpisodeNumber(stream)
		if !ok || season != 7 || episode != 3 {
			t.Fatalf("expected exact S07E03 stream, got season=%d episode=%d ok=%v stream=%#v", season, episode, ok, stream)
		}
	}
}

func TestGetSeriesStreamsFiltersProviderSeasonResults(t *testing.T) {
	mp := &MultiProvider{
		Providers: []StreamProvider{fakeStreamProvider{seriesStreams: []TorrentioStream{
			{Title: "Show.S01E01.1080p.WEB-DL", Quality: "1080p"},
			{Title: "Show.S01E02.1080p.WEB-DL", Quality: "1080p"},
			{Title: "Show.S01E10.1080p.WEB-DL", Quality: "1080p"},
		}}},
		ProviderNames: []string{"fake"},
	}

	streams, err := mp.GetSeriesStreams("tt123", 1, 1)
	if err != nil {
		t.Fatalf("expected filtered series streams, got error: %v", err)
	}
	if len(streams) != 1 || streams[0].Title != "Show.S01E01.1080p.WEB-DL" {
		t.Fatalf("expected only S01E01 stream, got %#v", streams)
	}
}
