package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/providers"
	streammeta "github.com/ZeroQ-bit/Vortexo-Server/internal/services/streams"
	"github.com/gorilla/mux"
)

type vortexoSourcesRequest struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Year          int    `json:"year,omitempty"`
	TMDBID        int    `json:"tmdb_id,omitempty"`
	IMDBID        string `json:"imdb_id,omitempty"`
	Season        int    `json:"season,omitempty"`
	Episode       int    `json:"episode,omitempty"`
	ParentTitle   string `json:"parent_title,omitempty"`
	PlexRatingKey string `json:"plex_rating_key,omitempty"`
}

type vortexoSource struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Title        string  `json:"title,omitempty"`
	Quality      string  `json:"quality,omitempty"`
	Cached       bool    `json:"cached"`
	HDR          bool    `json:"hdr"`
	DynamicRange string  `json:"dynamic_range,omitempty"`
	Codec        string  `json:"codec,omitempty"`
	Audio        string  `json:"audio,omitempty"`
	Source       string  `json:"source,omitempty"`
	Seeders      int     `json:"seeders,omitempty"`
	SizeGB       float64 `json:"size_gb,omitempty"`
	FileName     string  `json:"file_name,omitempty"`
	PlayURL      string  `json:"play_url"`
}

type vortexoPlayToken struct {
	Hash  string `json:"hash,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type vortexoSubtitleTranslateRequest struct {
	Text       string `json:"text"`
	SourceLang string `json:"source_lang,omitempty"`
	TargetLang string `json:"target_lang"`
}

type vortexoSubtitleTranslateResponse struct {
	TranslatedText string `json:"translated_text"`
	SourceLang     string `json:"source_lang,omitempty"`
	TargetLang     string `json:"target_lang"`
	Provider       string `json:"provider,omitempty"`
	Translated     bool   `json:"translated"`
}

type libreTranslateRequest struct {
	Text       string `json:"q"`
	SourceLang string `json:"source"`
	TargetLang string `json:"target"`
	Format     string `json:"format"`
	APIKey     string `json:"api_key,omitempty"`
}

type libreTranslateResponse struct {
	TranslatedText string `json:"translatedText"`
}

// VortexoCapabilities exposes a tiny discovery endpoint for private clients.
func (h *Handler) VortexoCapabilities(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"name":                          "Vortexo Server Sources",
		"source_api":                    true,
		"playback":                      true,
		"subtitle_translation":          true,
		"subtitle_translation_provider": h.configuredSubtitleTranslationProvider(),
		"types":                         []string{"movie", "episode"},
	})
}

// VortexoSources returns server-resolved source options for private clients.
func (h *Handler) VortexoSources(w http.ResponseWriter, r *http.Request) {
	if h.streamProvider == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   false,
			"available": false,
			"sources":   []vortexoSource{},
		})
		return
	}

	var req vortexoSourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Type = normalizeVortexoType(req.Type)
	if req.Type == "" {
		respondError(w, http.StatusBadRequest, "type must be movie or episode")
		return
	}

	imdbID, releaseYear, err := h.resolveVortexoIMDBID(r.Context(), req)
	if err != nil {
		log.Printf("[Vortexo] Could not resolve %s %q tmdb=%d: %v", req.Type, req.Title, req.TMDBID, err)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   false,
			"available": false,
			"sources":   []vortexoSource{},
		})
		return
	}

	var providerStreams []providers.TorrentioStream
	if req.Type == "episode" {
		providerStreams, err = h.streamProvider.GetSeriesStreams(imdbID, req.Season, req.Episode)
	} else {
		providerStreams, err = h.streamProvider.GetMovieStreamsWithYear(imdbID, releaseYear)
		if err == nil && req.Title != "" {
			providerStreams = validateMovieStreams(providerStreams, req.Title, releaseYear)
		}
	}
	if err != nil {
		log.Printf("[Vortexo] Stream lookup failed for %s %s: %v", req.Type, imdbID, err)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   true,
			"available": false,
			"sources":   []vortexoSource{},
		})
		return
	}

	sources := h.buildVortexoSources(providerStreams)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"matched":   true,
		"available": len(sources) > 0,
		"sources":   sources,
	})
}

// VortexoPlay resolves a private source token and redirects to a playable URL.
func (h *Handler) VortexoPlay(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tokenValue := strings.TrimSpace(vars["token"])
	if tokenValue == "" {
		respondError(w, http.StatusBadRequest, "missing source token")
		return
	}

	token, err := decodeVortexoPlayToken(tokenValue)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid source token")
		return
	}

	if token.URL != "" && token.Hash == "" {
		http.Redirect(w, r, token.URL, http.StatusFound)
		return
	}

	if h.rdClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Real-Debrid is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	streamURL, err := h.rdClient.GetStreamURL(ctx, token.Hash)
	if err != nil {
		log.Printf("[Vortexo] Failed to resolve source %q: %v", token.Title, err)
		respondError(w, http.StatusBadGateway, "failed to resolve source")
		return
	}

	http.Redirect(w, r, streamURL, http.StatusFound)
}

// VortexoTranslateSubtitle translates short embedded subtitle cues through the
// user's configured server-side translation provider. If no provider is
// configured, the original text is returned so clients can safely fall back.
func (h *Handler) VortexoTranslateSubtitle(w http.ResponseWriter, r *http.Request) {
	var req vortexoSubtitleTranslateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Text = strings.TrimSpace(req.Text)
	req.SourceLang = normalizeSubtitleLanguage(req.SourceLang, "auto")
	req.TargetLang = normalizeSubtitleLanguage(req.TargetLang, "")
	if req.Text == "" || req.TargetLang == "" {
		respondError(w, http.StatusBadRequest, "text and target_lang are required")
		return
	}

	if len([]rune(req.Text)) > 2000 {
		respondError(w, http.StatusBadRequest, "subtitle cue is too long")
		return
	}

	if subtitleLanguagesMatch(req.SourceLang, req.TargetLang) {
		respondJSON(w, http.StatusOK, vortexoSubtitleTranslateResponse{
			TranslatedText: req.Text,
			SourceLang:     req.SourceLang,
			TargetLang:     req.TargetLang,
			Translated:     false,
		})
		return
	}

	translated, provider, err := h.translateSubtitleCue(r.Context(), req.Text, req.SourceLang, req.TargetLang)
	if err != nil {
		log.Printf("[Vortexo] Subtitle translation unavailable target=%s provider=%s: %v", req.TargetLang, provider, err)
		respondJSON(w, http.StatusOK, vortexoSubtitleTranslateResponse{
			TranslatedText: req.Text,
			SourceLang:     req.SourceLang,
			TargetLang:     req.TargetLang,
			Provider:       provider,
			Translated:     false,
		})
		return
	}

	translated = strings.TrimSpace(translated)
	if translated == "" {
		translated = req.Text
	}

	respondJSON(w, http.StatusOK, vortexoSubtitleTranslateResponse{
		TranslatedText: translated,
		SourceLang:     req.SourceLang,
		TargetLang:     req.TargetLang,
		Provider:       provider,
		Translated:     translated != req.Text,
	})
}

func (h *Handler) configuredSubtitleTranslationProvider() string {
	baseURL, _ := h.subtitleTranslationConfig()
	if baseURL != "" {
		return "libretranslate"
	}
	return ""
}

func (h *Handler) translateSubtitleCue(ctx context.Context, text, sourceLang, targetLang string) (string, string, error) {
	baseURL, apiKey := h.subtitleTranslationConfig()
	if baseURL == "" {
		return "", "", fmt.Errorf("subtitle translation API is not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	payload := libreTranslateRequest{
		Text:       text,
		SourceLang: normalizeSubtitleLanguage(sourceLang, "auto"),
		TargetLang: normalizeSubtitleLanguage(targetLang, ""),
		Format:     "text",
		APIKey:     apiKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", "libretranslate", err
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/translate"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "libretranslate", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", "libretranslate", err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "libretranslate", fmt.Errorf("translation provider returned HTTP %d", response.StatusCode)
	}

	var decoded libreTranslateResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", "libretranslate", err
	}

	return decoded.TranslatedText, "libretranslate", nil
}

func (h *Handler) subtitleTranslationConfig() (string, string) {
	var settingsURL string
	var settingsKey string
	if h != nil && h.settingsManager != nil {
		cfg := h.settingsManager.Get()
		settingsURL = cfg.SubtitleTranslationAPIURL
		settingsKey = cfg.SubtitleTranslationAPIKey
	}

	apiKey := strings.TrimSpace(firstNonEmpty(settingsKey, os.Getenv("LIBRETRANSLATE_API_KEY")))
	baseURL := strings.TrimSpace(firstNonEmpty(settingsURL, os.Getenv("LIBRETRANSLATE_URL"), os.Getenv("VORTEXO_TRANSLATE_URL")))
	if baseURL == "" && apiKey != "" {
		baseURL = "https://libretranslate.com"
	}

	return baseURL, apiKey
}

func normalizeSubtitleLanguage(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "unknown" || value == "und" {
		return fallback
	}

	for _, group := range subtitleLanguageAliasGroups() {
		for _, alias := range group {
			if value == alias {
				return group[0]
			}
		}
	}

	return value
}

func subtitleLanguagesMatch(sourceLang, targetLang string) bool {
	sourceLang = normalizeSubtitleLanguage(sourceLang, "")
	targetLang = normalizeSubtitleLanguage(targetLang, "")
	if sourceLang == "" || sourceLang == "auto" || targetLang == "" {
		return false
	}

	sourceTokens := subtitleLanguageTokens(sourceLang)
	targetTokens := subtitleLanguageTokens(targetLang)
	for token := range sourceTokens {
		if targetTokens[token] {
			return true
		}
	}
	return false
}

func subtitleLanguageTokens(value string) map[string]bool {
	value = strings.TrimSpace(strings.ToLower(value))
	tokens := map[string]bool{}
	if value == "" {
		return tokens
	}

	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if token != "" {
			tokens[token] = true
		}
	}
	tokens[value] = true

	for _, group := range subtitleLanguageAliasGroups() {
		for _, alias := range group {
			if tokens[alias] {
				for _, expanded := range group {
					tokens[expanded] = true
				}
				break
			}
		}
	}

	return tokens
}

func subtitleLanguageAliasGroups() [][]string {
	return [][]string{
		{"en", "eng", "english", "en-us", "en-gb"},
		{"es", "spa", "spanish", "es-es", "es-419"},
		{"fr", "fra", "fre", "french"},
		{"de", "deu", "ger", "german"},
		{"it", "ita", "italian"},
		{"pt", "por", "portuguese", "pt-br", "pt-pt"},
		{"ru", "rus", "russian"},
		{"ja", "jpn", "japanese"},
		{"ko", "kor", "korean"},
		{"zh", "zho", "chi", "chinese", "cmn"},
		{"ar", "ara", "arabic"},
		{"hi", "hin", "hindi"},
		{"hr", "hrv", "croatian"},
		{"sr", "srp", "serbian"},
		{"bs", "bos", "bosnian"},
	}
}

func (h *Handler) resolveVortexoIMDBID(ctx context.Context, req vortexoSourcesRequest) (string, int, error) {
	if strings.HasPrefix(req.IMDBID, "tt") {
		return req.IMDBID, req.Year, nil
	}

	if req.TMDBID <= 0 {
		return "", 0, fmt.Errorf("missing tmdb_id or imdb_id")
	}

	if req.Type == "episode" {
		if req.Season <= 0 || req.Episode <= 0 {
			return "", 0, fmt.Errorf("missing season/episode")
		}
		if h.seriesStore != nil {
			if series, err := h.seriesStore.GetByTMDBID(ctx, req.TMDBID); err == nil && series != nil {
				if strings.HasPrefix(series.IMDBID, "tt") {
					return series.IMDBID, series.Year, nil
				}
				if imdbID, ok := series.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
					return imdbID, series.Year, nil
				}
			}
		}
		if h.tmdbClient != nil {
			externalIDs, err := h.tmdbClient.GetSeriesExternalIDs(ctx, req.TMDBID)
			if err == nil && strings.HasPrefix(externalIDs.IMDBID, "tt") {
				return externalIDs.IMDBID, req.Year, nil
			}
		}
		return "", 0, fmt.Errorf("series imdb id not found")
	}

	if h.movieStore != nil {
		if movie, err := h.movieStore.GetByTMDBID(ctx, req.TMDBID); err == nil && movie != nil {
			if imdbID, ok := movie.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
				year := movie.Year
				if movie.ReleaseDate != nil && !movie.ReleaseDate.IsZero() {
					year = movie.ReleaseDate.Year()
				}
				return imdbID, year, nil
			}
		}
	}
	if h.tmdbClient != nil {
		movie, err := h.tmdbClient.GetMovie(ctx, req.TMDBID)
		if err == nil && movie != nil {
			if imdbID, ok := movie.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
				year := movie.Year
				if movie.ReleaseDate != nil && !movie.ReleaseDate.IsZero() {
					year = movie.ReleaseDate.Year()
				}
				return imdbID, year, nil
			}
		}
	}

	return "", 0, fmt.Errorf("movie imdb id not found")
}

func (h *Handler) buildVortexoSources(providerStreams []providers.TorrentioStream) []vortexoSource {
	sources := make([]vortexoSource, 0, len(providerStreams))
	for _, stream := range providerStreams {
		hash := stream.InfoHash
		if !isValidHash(hash) && stream.URL != "" {
			hash = extractHashFromURL(stream.URL)
		}

		token := vortexoPlayToken{
			Hash:  hash,
			Title: stream.Title,
		}
		if !isValidHash(hash) && stream.URL != "" {
			token.Hash = ""
			token.URL = stream.URL
		}
		if token.Hash == "" && token.URL == "" {
			continue
		}

		id, err := encodeVortexoPlayToken(token)
		if err != nil {
			continue
		}

		parsed := streammeta.ParseQualityFromTorrentName(firstNonEmpty(stream.Title, stream.Name))
		quality := firstNonEmpty(parsed.Resolution, stream.Quality)
		dynamicRange := parsed.HDRType
		label := vortexoSourceLabel(quality, dynamicRange, parsed.Codec, parsed.AudioFormat, stream.Source)
		sizeGB := float64(stream.Size) / (1024 * 1024 * 1024)
		if sizeGB < 0 {
			sizeGB = 0
		}

		sources = append(sources, vortexoSource{
			ID:           id,
			Label:        label,
			Title:        stream.Name,
			Quality:      quality,
			Cached:       stream.Cached,
			HDR:          dynamicRange != "" && !strings.EqualFold(dynamicRange, "SDR"),
			DynamicRange: dynamicRange,
			Codec:        parsed.Codec,
			Audio:        parsed.AudioFormat,
			Source:       stream.Source,
			Seeders:      stream.Seeders,
			SizeGB:       sizeGB,
			FileName:     stream.Title,
			PlayURL:      "/api/v1/vortexo/play/" + id,
		})
	}

	return sources
}

func normalizeVortexoType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie":
		return "movie"
	case "episode", "series", "show", "tv":
		return "episode"
	default:
		return ""
	}
}

func encodeVortexoPlayToken(token vortexoPlayToken) (string, error) {
	data, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeVortexoPlayToken(value string) (vortexoPlayToken, error) {
	var token vortexoPlayToken
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return token, err
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return token, err
	}
	if token.Hash == "" && token.URL == "" {
		return token, fmt.Errorf("empty token")
	}
	return token, nil
}

func vortexoSourceLabel(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "Server Version"
	}
	return strings.Join(cleaned, " - ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
