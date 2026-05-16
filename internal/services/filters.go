package services

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

var indianLanguageCodes = map[string]struct{}{
	"as": {}, // Assamese
	"bn": {}, // Bengali
	"gu": {}, // Gujarati
	"hi": {}, // Hindi
	"kn": {}, // Kannada
	"ks": {}, // Kashmiri
	"ml": {}, // Malayalam
	"mr": {}, // Marathi
	"or": {}, // Odia
	"pa": {}, // Punjabi
	"sa": {}, // Sanskrit
	"ta": {}, // Tamil
	"te": {}, // Telugu
	"ur": {}, // Urdu
}

var indianCategoryTerms = []string{
	"bollywood",
	"desi",
	"hindi",
	"india",
	"indian",
	"kollywood",
	"mollywood",
	"sandalwood",
	"tamil",
	"telugu",
	"tollywood",
	"malayalam",
	"kannada",
	"bengali",
	"marathi",
	"punjabi",
	"gujarati",
	"urdu",
}

type ContentFilterOptions struct {
	MinYear             int
	MinRuntime          int
	IncludeAdultVOD     bool
	OnlyReleasedContent bool
	BlockBollywood      bool
}

// IsIndianMovie returns true if the movie appears to be from India
// Uses TMDB metadata: Indian original languages or origin/production country includes India.
func IsIndianMovie(m *models.Movie) bool {
	if m == nil || m.Metadata == nil {
		return false
	}

	if metadataHasIndianLanguage(m.Metadata) {
		return true
	}

	if metadataHasIndianCountry(m.Metadata, "production_countries", "origin_country", "country") {
		return true
	}

	return metadataHasIndianCategory(m.Metadata)
}

// IsIndianSeries returns true if the series appears to be from India
// Uses TMDB metadata: Indian original languages or origin/production country includes India.
func IsIndianSeries(s *models.Series) bool {
	if s == nil || s.Metadata == nil {
		return false
	}

	if metadataHasIndianLanguage(s.Metadata) {
		return true
	}

	if metadataHasIndianCountry(s.Metadata, "origin_country", "production_countries", "country") {
		return true
	}

	return metadataHasIndianCategory(s.Metadata)
}

func MovieAllowedByContentFilters(m *models.Movie, opts ContentFilterOptions) (bool, string) {
	if m == nil {
		return false, "missing movie"
	}

	if opts.BlockBollywood && IsIndianMovie(m) {
		return false, "India-origin media"
	}

	if !opts.IncludeAdultVOD && IsAdultMovie(m) {
		return false, "adult media"
	}

	if opts.MinYear > 0 {
		if year := MovieReleaseYear(m); year > 0 && year < opts.MinYear {
			return false, "before minimum year"
		}
	}

	if opts.MinRuntime > 0 {
		if runtime := MovieRuntimeMinutes(m); runtime > 0 && runtime < opts.MinRuntime {
			return false, "below minimum runtime"
		}
	}

	if opts.OnlyReleasedContent && !MovieIsReleased(m, time.Now()) {
		return false, "unreleased media"
	}

	return true, ""
}

func SeriesAllowedByContentFilters(s *models.Series, opts ContentFilterOptions) (bool, string) {
	if s == nil {
		return false, "missing series"
	}

	if opts.BlockBollywood && IsIndianSeries(s) {
		return false, "India-origin media"
	}

	if !opts.IncludeAdultVOD && metadataIndicatesAdult(s.Metadata) {
		return false, "adult media"
	}

	if opts.MinYear > 0 {
		if year := SeriesReleaseYear(s); year > 0 && year < opts.MinYear {
			return false, "before minimum year"
		}
	}

	if opts.OnlyReleasedContent && !SeriesIsReleased(s, time.Now()) {
		return false, "unreleased media"
	}

	return true, ""
}

func IsAdultMovie(m *models.Movie) bool {
	if m == nil {
		return false
	}
	return metadataIndicatesAdult(m.Metadata)
}

func MovieReleaseYear(m *models.Movie) int {
	if m == nil {
		return 0
	}
	if m.ReleaseDate != nil {
		return m.ReleaseDate.Year()
	}
	if m.Year > 0 {
		return m.Year
	}
	if releaseDate := contentMetadataDate(m.Metadata, "release_date"); releaseDate != nil {
		return releaseDate.Year()
	}
	return contentMetadataInt(m.Metadata, "year")
}

func SeriesReleaseYear(s *models.Series) int {
	if s == nil {
		return 0
	}
	if s.FirstAirDate != nil {
		return s.FirstAirDate.Year()
	}
	if s.Year > 0 {
		return s.Year
	}
	if firstAirDate := contentMetadataDate(s.Metadata, "first_air_date"); firstAirDate != nil {
		return firstAirDate.Year()
	}
	if releaseDate := contentMetadataDate(s.Metadata, "release_date"); releaseDate != nil {
		return releaseDate.Year()
	}
	return contentMetadataInt(s.Metadata, "year")
}

func MovieRuntimeMinutes(m *models.Movie) int {
	if m == nil {
		return 0
	}
	if m.Runtime > 0 {
		return m.Runtime
	}
	return contentMetadataInt(m.Metadata, "runtime")
}

func MovieIsReleased(m *models.Movie, now time.Time) bool {
	if m == nil {
		return false
	}
	if m.ReleaseDate != nil {
		return !m.ReleaseDate.After(now)
	}
	if releaseDate := contentMetadataDate(m.Metadata, "release_date"); releaseDate != nil {
		return !releaseDate.After(now)
	}
	if year := MovieReleaseYear(m); year > 0 {
		return year <= now.Year()
	}
	return true
}

func SeriesIsReleased(s *models.Series, now time.Time) bool {
	if s == nil {
		return false
	}
	if s.FirstAirDate != nil {
		return !s.FirstAirDate.After(now)
	}
	if firstAirDate := contentMetadataDate(s.Metadata, "first_air_date"); firstAirDate != nil {
		return !firstAirDate.After(now)
	}
	if releaseDate := contentMetadataDate(s.Metadata, "release_date"); releaseDate != nil {
		return !releaseDate.After(now)
	}
	if year := SeriesReleaseYear(s); year > 0 {
		return year <= now.Year()
	}
	return true
}

func metadataHasIndianLanguage(metadata models.Metadata) bool {
	if lang, ok := metadata["original_language"].(string); ok {
		_, isIndianLanguage := indianLanguageCodes[strings.ToLower(strings.TrimSpace(lang))]
		return isIndianLanguage
	}
	return false
}

func metadataIndicatesAdult(metadata models.Metadata) bool {
	if metadata == nil {
		return false
	}

	for _, key := range []string{"adult", "is_adult"} {
		if contentMetadataBool(metadata[key]) {
			return true
		}
	}

	if streamType, ok := metadata["stream_type"].(string); ok {
		if strings.EqualFold(strings.TrimSpace(streamType), "adult") {
			return true
		}
	}

	if genres, ok := metadata["genres"].([]interface{}); ok {
		for _, genre := range genres {
			if strings.EqualFold(strings.TrimSpace(toString(genre)), "adult") {
				return true
			}
		}
	}

	if genres, ok := metadata["genres"].([]string); ok {
		for _, genre := range genres {
			if strings.EqualFold(strings.TrimSpace(genre), "adult") {
				return true
			}
		}
	}

	return false
}

func contentMetadataBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "y":
			return true
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	case json.Number:
		n, _ := v.Int64()
		return n != 0
	}
	return false
}

func contentMetadataInt(metadata models.Metadata, key string) int {
	if metadata == nil {
		return 0
	}
	switch v := metadata[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(strings.Split(v, ".")[0]))
		return n
	}
	return 0
}

func contentMetadataDate(metadata models.Metadata, key string) *time.Time {
	if metadata == nil {
		return nil
	}

	switch v := metadata[key].(type) {
	case time.Time:
		return &v
	case string:
		if parsed := parseMetadataDate(v); parsed != nil {
			return parsed
		}
	}
	return nil
}

func parseMetadataDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func toString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]interface{}:
		if name, ok := v["name"].(string); ok {
			return name
		}
	case map[string]string:
		return v["name"]
	}
	return ""
}

func metadataHasIndianCountry(metadata models.Metadata, keys ...string) bool {
	for _, key := range keys {
		if hasIndianCountryValue(metadata[key]) {
			return true
		}
	}
	return false
}

func hasIndianCountryValue(value interface{}) bool {
	switch v := value.(type) {
	case string:
		return isIndiaCountryString(v)
	case []string:
		for _, item := range v {
			if isIndiaCountryString(item) {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if hasIndianCountryValue(item) {
				return true
			}
		}
	case []map[string]interface{}:
		for _, item := range v {
			if hasIndianCountryValue(item) {
				return true
			}
		}
	case map[string]interface{}:
		return hasIndianCountryValue(v["iso_3166_1"]) ||
			hasIndianCountryValue(v["country_code"]) ||
			hasIndianCountryValue(v["code"]) ||
			hasIndianCountryValue(v["name"])
	case map[string]string:
		return isIndiaCountryString(v["iso_3166_1"]) ||
			isIndiaCountryString(v["country_code"]) ||
			isIndiaCountryString(v["code"]) ||
			isIndiaCountryString(v["name"])
	}
	return false
}

func isIndiaCountryString(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "in" || normalized == "india"
}

func metadataHasIndianCategory(metadata models.Metadata) bool {
	for _, key := range []string{"iptv_vod_category", "category", "group", "group_title"} {
		value, ok := metadata[key].(string)
		if !ok {
			continue
		}

		normalized := strings.ToLower(value)
		for _, term := range indianCategoryTerms {
			if strings.Contains(normalized, term) {
				return true
			}
		}
	}
	return false
}
