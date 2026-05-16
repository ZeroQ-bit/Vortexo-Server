package services

import (
	"testing"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

func TestIsIndianMovieDetectsProductionCountryObjects(t *testing.T) {
	movie := &models.Movie{
		Metadata: models.Metadata{
			"production_countries": []interface{}{
				map[string]interface{}{"iso_3166_1": "IN", "name": "India"},
			},
		},
	}

	if !IsIndianMovie(movie) {
		t.Fatal("expected movie with India production country object to be blocked")
	}
}

func TestIsIndianMovieDetectsRegionalIndianLanguages(t *testing.T) {
	for _, lang := range []string{"hi", "ta", "te", "ml", "kn", "bn", "mr", "pa", "gu", "ur"} {
		movie := &models.Movie{
			Metadata: models.Metadata{"original_language": lang},
		}

		if !IsIndianMovie(movie) {
			t.Fatalf("expected original_language=%q to be blocked", lang)
		}
	}
}

func TestIsIndianSeriesDetectsOriginCountryString(t *testing.T) {
	series := &models.Series{
		Metadata: models.Metadata{"origin_country": "IN"},
	}

	if !IsIndianSeries(series) {
		t.Fatal("expected series with origin_country=IN to be blocked")
	}
}

func TestIsIndianSeriesDetectsIPTVCategory(t *testing.T) {
	series := &models.Series{
		Metadata: models.Metadata{"iptv_vod_category": "Tamil Movies"},
	}

	if !IsIndianSeries(series) {
		t.Fatal("expected Indian-language IPTV category to be blocked")
	}
}

func TestIsIndianMovieAllowsNonIndianMetadata(t *testing.T) {
	movie := &models.Movie{
		Metadata: models.Metadata{
			"original_language":    "en",
			"production_countries": []string{"US"},
		},
	}

	if IsIndianMovie(movie) {
		t.Fatal("expected non-Indian movie metadata to be allowed")
	}
}

func TestMovieAllowedByContentFiltersBlocksMinimumYear(t *testing.T) {
	movie := &models.Movie{Year: 1999, Metadata: models.Metadata{}}

	allowed, reason := MovieAllowedByContentFilters(movie, ContentFilterOptions{MinYear: 2000, IncludeAdultVOD: true})
	if allowed || reason != "before minimum year" {
		t.Fatalf("expected minimum year block, got allowed=%v reason=%q", allowed, reason)
	}
}

func TestMovieAllowedByContentFiltersBlocksMinimumRuntime(t *testing.T) {
	movie := &models.Movie{Runtime: 42, Metadata: models.Metadata{}}

	allowed, reason := MovieAllowedByContentFilters(movie, ContentFilterOptions{MinRuntime: 60, IncludeAdultVOD: true})
	if allowed || reason != "below minimum runtime" {
		t.Fatalf("expected minimum runtime block, got allowed=%v reason=%q", allowed, reason)
	}
}

func TestMovieAllowedByContentFiltersBlocksAdultByDefault(t *testing.T) {
	movie := &models.Movie{Metadata: models.Metadata{"adult": true}}

	allowed, reason := MovieAllowedByContentFilters(movie, ContentFilterOptions{})
	if allowed || reason != "adult media" {
		t.Fatalf("expected adult block, got allowed=%v reason=%q", allowed, reason)
	}
}

func TestMovieAllowedByContentFiltersAllowsAdultWhenEnabled(t *testing.T) {
	movie := &models.Movie{Metadata: models.Metadata{"adult": true}}

	allowed, reason := MovieAllowedByContentFilters(movie, ContentFilterOptions{IncludeAdultVOD: true})
	if !allowed || reason != "" {
		t.Fatalf("expected adult movie to be allowed when enabled, got allowed=%v reason=%q", allowed, reason)
	}
}

func TestMovieAllowedByContentFiltersBlocksUnreleased(t *testing.T) {
	releaseDate := time.Now().AddDate(0, 1, 0)
	movie := &models.Movie{ReleaseDate: &releaseDate, Metadata: models.Metadata{}}

	allowed, reason := MovieAllowedByContentFilters(movie, ContentFilterOptions{OnlyReleasedContent: true, IncludeAdultVOD: true})
	if allowed || reason != "unreleased media" {
		t.Fatalf("expected unreleased block, got allowed=%v reason=%q", allowed, reason)
	}
}
