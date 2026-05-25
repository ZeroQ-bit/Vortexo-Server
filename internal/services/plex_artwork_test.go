package services

import (
	"strings"
	"testing"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

func TestStructuredPlexArtworkExtractsPublicImages(t *testing.T) {
	html := `
		<html>
			<head>
				<link as="image" imageSrcSet="https://images.plex.tv/photo?size=small 320w, https://images.plex.tv/photo?size=large 1280w">
				<meta property="og:image" content="https://metadata-static.plex.tv/thumb.jpg">
			</head>
			<body>
				<script>
				{"background":{"image":{"url":"https:\/\/images.plex.tv\/photo?url=https%3A%2F%2Fimage.tmdb.org%2Ft%2Fp%2Foriginal%2Fbackdrop.jpg"}}}
				{"clearLogo":{"url":"https:\/\/metadata-static.plex.tv\/logo.png"}}
				</script>
			</body>
		</html>`

	artwork := structuredPlexArtwork(html)
	if len(artwork.Background) != 2 {
		t.Fatalf("expected 2 background URLs, got %d: %#v", len(artwork.Background), artwork.Background)
	}
	if len(artwork.ClearLogo) != 1 {
		t.Fatalf("expected 1 clear logo URL, got %d: %#v", len(artwork.ClearLogo), artwork.ClearLogo)
	}
	if len(artwork.Thumbnail) != 1 {
		t.Fatalf("expected 1 thumbnail URL, got %d: %#v", len(artwork.Thumbnail), artwork.Thumbnail)
	}
}

func TestCandidatePlexArtworkURLsAvoidSearch(t *testing.T) {
	urls := candidatePlexArtworkURLs(models.PlexArtworkSeedItem{
		MediaType: "tv",
		TMDBID:    40075,
		Title:     "Gravity Falls",
		Year:      2012,
	})

	if len(urls) != 2 {
		t.Fatalf("expected 2 candidate URLs, got %d: %#v", len(urls), urls)
	}
	for _, url := range urls {
		if strings.Contains(url, "/search") {
			t.Fatalf("candidate URL should not use Plex search: %s", url)
		}
	}
}
