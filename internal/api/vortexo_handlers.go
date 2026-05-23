package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/providers"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
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
	Season       int     `json:"season,omitempty"`
	Episode      int     `json:"episode,omitempty"`
	PlayURL      string  `json:"play_url"`
	DirectURL    string  `json:"direct_url,omitempty"`
	DownloadURL  string  `json:"download_url,omitempty"`
}

type vortexoPlayToken struct {
	Hash    string `json:"hash,omitempty"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	FileIdx int    `json:"fileIdx,omitempty"`
}

type vortexoBlockedSource struct {
	Reason    string
	ExpiresAt time.Time
}

var vortexoBlockedSources = struct {
	sync.RWMutex
	byHash map[string]vortexoBlockedSource
}{
	byHash: make(map[string]vortexoBlockedSource),
}

const vortexoBlockedSourceTTL = 24 * time.Hour

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
		"home":                          true,
		"live_tv_rows":                  true,
		"source_api":                    true,
		"playback":                      true,
		"subtitles":                     true,
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
			filtered := filterVortexoMovieStreams(providerStreams, req.Title, releaseYear)
			if len(filtered) == 0 && releaseYear > 0 {
				if fallbackStreams, fallbackErr := h.streamProvider.GetMovieStreams(imdbID); fallbackErr == nil {
					filtered = filterVortexoMovieStreams(fallbackStreams, req.Title, releaseYear)
				} else {
					log.Printf("[Vortexo] Fallback movie stream lookup failed for %s: %v", imdbID, fallbackErr)
				}
			}
			providerStreams = filtered
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

	providerStreams = h.refreshVortexoCacheAvailability(
		r.Context(),
		providerStreams,
		h.vortexoOnlyCachedSourcesEnabled(),
	)

	sources := h.buildVortexoSources(providerStreams, req)
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
		if wantsVortexoPlayJSON(r) {
			respondVortexoPlaybackURL(w, token.URL)
			return
		}
		http.Redirect(w, r, token.URL, http.StatusFound)
		return
	}

	token.Hash = normalizeTorrentHash(token.Hash)
	if isVortexoSourceBlocked(token.Hash) {
		respondError(w, http.StatusGone, "Real-Debrid rejected source: infringing_file")
		return
	}

	if h.rdClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Real-Debrid is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	streamURL, err := h.rdClient.GetStreamURLForFile(ctx, token.Hash, token.FileIdx, token.Title)
	if err != nil {
		log.Printf("[Vortexo] Failed to resolve source %q: %v", token.Title, err)
		if isRealDebridBlockedPlaybackError(err) {
			markVortexoSourceBlocked(token.Hash, "infringing_file")
			respondError(w, http.StatusGone, safeVortexoPlaybackError(err))
			return
		}
		respondError(w, http.StatusBadGateway, safeVortexoPlaybackError(err))
		return
	}

	if wantsVortexoPlayJSON(r) {
		respondVortexoPlaybackURL(w, streamURL)
		return
	}
	http.Redirect(w, r, streamURL, http.StatusFound)
}

// VortexoSubtitle fetches an OpenSubtitles track for a private-client media
// token. The token is metadata-only, so source-addon playback can still use
// server-side subtitles even when the item is not in the Xtream library.
func (h *Handler) VortexoSubtitle(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	req, err := decodeVortexoSubtitleToken(vars["token"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid subtitle token")
		return
	}

	language := strings.TrimSpace(vars["lang"])
	format := strings.ToLower(strings.TrimSpace(vars["format"]))
	if format == "" {
		format = "vtt"
	}
	if format != "vtt" && format != "srt" {
		respondError(w, http.StatusBadRequest, "unsupported subtitle format")
		return
	}

	cfg := h.currentVortexoOpenSubtitlesConfig()
	if !cfg.Ready() {
		respondError(w, http.StatusServiceUnavailable, "OpenSubtitles is not configured")
		return
	}

	search, err := h.vortexoOpenSubtitlesSearchRequest(r.Context(), req, language, format)
	if err != nil {
		log.Printf("[Vortexo] Subtitle metadata lookup failed: %v", err)
		respondError(w, http.StatusNotFound, "subtitle metadata not found")
		return
	}

	subtitle, err := services.NewOpenSubtitlesClient(cfg).FetchSubtitle(r.Context(), search)
	if err != nil {
		log.Printf("[Vortexo] OpenSubtitles lookup failed type=%s title=%q lang=%s: %v", req.Type, req.Title, language, err)
		respondError(w, http.StatusNotFound, "subtitle not found")
		return
	}

	writeVortexoSubtitleResponse(w, subtitle)
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
	rawTargetLang := req.TargetLang
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
		text := req.Text
		if shouldTransliterateSerbianLatin(rawTargetLang) {
			text = transliterateSerbianCyrillicToLatin(text)
		}
		respondJSON(w, http.StatusOK, vortexoSubtitleTranslateResponse{
			TranslatedText: text,
			SourceLang:     req.SourceLang,
			TargetLang:     req.TargetLang,
			Translated:     text != req.Text,
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

	needsLatinSerbian := shouldTransliterateSerbianLatin(targetLang)
	translated, provider, err := h.translateSubtitleCueWithLibreTranslate(ctx, baseURL, apiKey, text, sourceLang, targetLang)
	if err == nil && needsLatinSerbian {
		translated = transliterateSerbianCyrillicToLatin(translated)
	}
	return translated, provider, err
}

func (h *Handler) translateSubtitleCueWithLibreTranslate(ctx context.Context, baseURL, apiKey, text, sourceLang, targetLang string) (string, string, error) {

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

func (h *Handler) currentVortexoOpenSubtitlesConfig() services.OpenSubtitlesConfig {
	cfg := services.OpenSubtitlesConfig{
		Enabled:   strings.EqualFold(os.Getenv("OPENSUBTITLES_ENABLED"), "true"),
		APIKey:    os.Getenv("OPENSUBTITLES_API_KEY"),
		Username:  os.Getenv("OPENSUBTITLES_USERNAME"),
		Password:  os.Getenv("OPENSUBTITLES_PASSWORD"),
		Languages: os.Getenv("OPENSUBTITLES_LANGUAGES"),
		UserAgent: os.Getenv("OPENSUBTITLES_USER_AGENT"),
		BaseURL:   os.Getenv("OPENSUBTITLES_BASE_URL"),
	}

	if h != nil && h.settingsManager != nil {
		settings := h.settingsManager.Get()
		cfg.Enabled = settings.OpenSubtitlesEnabled
		cfg.APIKey = settings.OpenSubtitlesAPIKey
		cfg.Username = settings.OpenSubtitlesUsername
		cfg.Password = settings.OpenSubtitlesPassword
		cfg.Languages = settings.OpenSubtitlesLanguages
	}

	if strings.TrimSpace(cfg.Languages) == "" {
		cfg.Languages = "en"
	}
	return cfg
}

func (h *Handler) vortexoOpenSubtitlesSearchRequest(
	ctx context.Context,
	req vortexoSourcesRequest,
	language string,
	format string,
) (services.OpenSubtitlesSearchRequest, error) {
	req.Type = normalizeVortexoType(req.Type)
	if req.Type == "" {
		return services.OpenSubtitlesSearchRequest{}, fmt.Errorf("missing media type")
	}

	resolvedIMDBID, resolvedYear, err := h.resolveVortexoIMDBID(ctx, req)
	if err != nil {
		return services.OpenSubtitlesSearchRequest{}, err
	}

	switch req.Type {
	case "movie":
		return services.OpenSubtitlesSearchRequest{
			Type:     "movie",
			IMDBID:   resolvedIMDBID,
			TMDBID:   req.TMDBID,
			Query:    req.Title,
			Year:     firstNonZero(req.Year, resolvedYear),
			Language: language,
			Format:   format,
		}, nil
	case "episode":
		if req.Season <= 0 || req.Episode <= 0 {
			return services.OpenSubtitlesSearchRequest{}, fmt.Errorf("missing season/episode")
		}
		return services.OpenSubtitlesSearchRequest{
			Type:         "episode",
			ParentIMDBID: resolvedIMDBID,
			ParentTMDBID: req.TMDBID,
			Query:        firstNonEmpty(req.ParentTitle, req.Title),
			Season:       req.Season,
			Episode:      req.Episode,
			Language:     language,
			Format:       format,
		}, nil
	default:
		return services.OpenSubtitlesSearchRequest{}, fmt.Errorf("unsupported media type")
	}
}

func writeVortexoSubtitleResponse(w http.ResponseWriter, subtitle *services.OpenSubtitlesFetchedSubtitle) {
	w.Header().Set("Content-Type", subtitle.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if subtitle.FileName != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", subtitle.FileName))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(subtitle.Content)
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

func shouldTransliterateSerbianLatin(targetLang string) bool {
	tokens := subtitleLanguageTokens(targetLang)
	if len(tokens) == 0 {
		return false
	}
	for _, token := range []string{"sr", "srp", "serbian", "hr", "hrv", "croatian", "bs", "bos", "bosnian"} {
		if tokens[token] {
			return true
		}
	}
	return false
}

func transliterateSerbianCyrillicToLatin(text string) string {
	replacer := strings.NewReplacer(
		"Љ", "Lj", "Њ", "Nj", "Џ", "Dž",
		"љ", "lj", "њ", "nj", "џ", "dž",
		"А", "A", "Б", "B", "В", "V", "Г", "G", "Д", "D",
		"Ђ", "Đ", "Е", "E", "Ж", "Ž", "З", "Z", "И", "I",
		"Ј", "J", "К", "K", "Л", "L", "М", "M", "Н", "N",
		"О", "O", "П", "P", "Р", "R", "С", "S", "Т", "T",
		"Ћ", "Ć", "У", "U", "Ф", "F", "Х", "H", "Ц", "C",
		"Ч", "Č", "Ш", "Š",
		"а", "a", "б", "b", "в", "v", "г", "g", "д", "d",
		"ђ", "đ", "е", "e", "ж", "ž", "з", "z", "и", "i",
		"ј", "j", "к", "k", "л", "l", "м", "m", "н", "n",
		"о", "o", "п", "p", "р", "r", "с", "s", "т", "t",
		"ћ", "ć", "у", "u", "ф", "f", "х", "h", "ц", "c",
		"ч", "č", "ш", "š",
	)
	return replacer.Replace(text)
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
		{"sr", "srp", "serbian", "hr", "hrv", "croatian", "bs", "bos", "bosnian"},
	}
}

func (h *Handler) resolveVortexoIMDBID(ctx context.Context, req vortexoSourcesRequest) (string, int, error) {
	if req.Type == "episode" {
		return h.resolveVortexoSeriesIMDBID(ctx, req)
	}

	if strings.HasPrefix(req.IMDBID, "tt") {
		return req.IMDBID, req.Year, nil
	}

	if req.TMDBID <= 0 {
		return "", 0, fmt.Errorf("missing tmdb_id or imdb_id")
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

func (h *Handler) resolveVortexoSeriesIMDBID(ctx context.Context, req vortexoSourcesRequest) (string, int, error) {
	if req.Season <= 0 || req.Episode <= 0 {
		return "", 0, fmt.Errorf("missing season/episode")
	}

	if req.TMDBID > 0 {
		if imdbID, year, ok := h.resolveVortexoSeriesByTMDBID(ctx, req.TMDBID, req.Year); ok {
			return imdbID, year, nil
		}
	}

	if imdbID := strings.TrimSpace(req.IMDBID); strings.HasPrefix(imdbID, "tt") {
		if h.tmdbClient != nil {
			if tmdbID, err := h.tmdbClient.IMDBToTMDB(imdbID, "tv"); err == nil && tmdbID > 0 {
				if resolvedIMDBID, year, ok := h.resolveVortexoSeriesByTMDBID(ctx, tmdbID, req.Year); ok {
					return resolvedIMDBID, year, nil
				}
			}
		}
	}

	searchTitle := firstNonEmpty(req.ParentTitle, req.Title)
	if imdbID, year, ok := h.resolveVortexoSeriesBySearch(ctx, searchTitle, req.Year); ok {
		return imdbID, year, nil
	}

	if imdbID := strings.TrimSpace(req.IMDBID); strings.HasPrefix(imdbID, "tt") {
		return imdbID, req.Year, nil
	}

	return "", 0, fmt.Errorf("series imdb id not found")
}

func (h *Handler) resolveVortexoSeriesByTMDBID(ctx context.Context, tmdbID int, fallbackYear int) (string, int, bool) {
	if tmdbID <= 0 {
		return "", 0, false
	}

	if h.seriesStore != nil {
		if series, err := h.seriesStore.GetByTMDBID(ctx, tmdbID); err == nil && series != nil {
			year := firstNonZero(services.SeriesReleaseYear(series), fallbackYear)
			if strings.HasPrefix(series.IMDBID, "tt") {
				return series.IMDBID, year, true
			}
			if imdbID, ok := series.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
				return imdbID, year, true
			}
		}
	}

	if h.tmdbClient != nil {
		externalIDs, err := h.tmdbClient.GetSeriesExternalIDs(ctx, tmdbID)
		if err == nil && strings.HasPrefix(externalIDs.IMDBID, "tt") {
			return externalIDs.IMDBID, fallbackYear, true
		}
	}

	return "", 0, false
}

func (h *Handler) resolveVortexoSeriesBySearch(ctx context.Context, title string, year int) (string, int, bool) {
	if h.tmdbClient == nil {
		return "", 0, false
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return "", 0, false
	}

	results, err := h.tmdbClient.SearchSeries(ctx, title, 1)
	if err != nil || len(results) == 0 {
		return "", 0, false
	}

	if match := bestVortexoSeriesMatch(results, title, year); match != nil {
		matchYear := firstNonZero(services.SeriesReleaseYear(match), year)
		if imdbID, resolvedYear, ok := h.resolveVortexoSeriesByTMDBID(ctx, match.TMDBID, matchYear); ok {
			return imdbID, firstNonZero(resolvedYear, matchYear), true
		}
	}

	return "", 0, false
}

func bestVortexoSeriesMatch(results []*models.Series, title string, year int) *models.Series {
	normalizedTitle := normalizeVortexoLookupTitle(title)
	var best *models.Series
	bestScore := -1

	for _, candidate := range results {
		if candidate == nil {
			continue
		}

		candidateTitle := normalizeVortexoLookupTitle(candidate.Title)
		candidateOriginalTitle := normalizeVortexoLookupTitle(candidate.OriginalTitle)
		score := 0

		if candidateTitle == normalizedTitle || candidateOriginalTitle == normalizedTitle {
			score += 100
		} else if normalizedTitle != "" && (strings.Contains(candidateTitle, normalizedTitle) || strings.Contains(normalizedTitle, candidateTitle)) {
			score += 50
		}

		if year > 0 {
			candidateYear := services.SeriesReleaseYear(candidate)
			switch delta := absInt(candidateYear - year); {
			case candidateYear == 0:
			case delta == 0:
				score += 30
			case delta == 1:
				score += 10
			default:
				score -= 20
			}
		}

		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}

	return best
}

func normalizeVortexoLookupTitle(value string) string {
	replacer := strings.NewReplacer(
		"&", " and ",
		":", " ",
		"-", " ",
		"_", " ",
		"'", "",
		"\"", "",
		".", " ",
	)
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(strings.TrimSpace(value)))), " ")
}

func filterVortexoMovieStreams(streams []providers.TorrentioStream, title string, year int) []providers.TorrentioStream {
	if len(streams) == 0 {
		return streams
	}

	filtered := make([]providers.TorrentioStream, 0, len(streams))
	for _, stream := range streams {
		if vortexoStreamLooksAdult(stream) {
			log.Printf("[Vortexo] Filtered adult-looking source for %q: %s", title, firstNonEmpty(stream.Title, stream.Name))
			continue
		}
		if !vortexoMovieStreamMatchesTitle(stream, title, year) {
			log.Printf("[Vortexo] Filtered title mismatch for %q (%d): %s", title, year, firstNonEmpty(stream.Title, stream.Name))
			continue
		}
		filtered = append(filtered, stream)
	}

	if len(filtered) != len(streams) {
		log.Printf("[Vortexo] Movie source filter kept %d/%d streams for %q (%d)", len(filtered), len(streams), title, year)
	}
	return filtered
}

func vortexoMovieStreamMatchesTitle(stream providers.TorrentioStream, title string, year int) bool {
	requested := normalizeVortexoLookupTitle(title)
	if requested == "" {
		return true
	}

	candidate := normalizeVortexoLookupTitle(firstNonEmpty(stream.Title, stream.Name, stream.BehaviorHints.Filename))
	if candidate == "" {
		return false
	}
	if candidate == requested {
		return true
	}

	requestedTokens := strings.Fields(requested)
	if len(requestedTokens) == 1 {
		if year > 0 {
			for _, acceptedYear := range vortexoAcceptedMovieYears(year) {
				if strings.HasPrefix(candidate, fmt.Sprintf("%s %d ", requested, acceptedYear)) {
					return true
				}
			}
			return false
		}
		return strings.HasPrefix(candidate, requested+" ")
	}

	if strings.HasPrefix(candidate, requested+" ") {
		return true
	}
	if year > 0 {
		for _, acceptedYear := range vortexoAcceptedMovieYears(year) {
			if strings.HasPrefix(candidate, fmt.Sprintf("%s %d ", requested, acceptedYear)) {
				return true
			}
		}
	}

	return false
}

func vortexoAcceptedMovieYears(year int) []int {
	if year <= 0 {
		return nil
	}
	return []int{year, year - 1, year + 1}
}

func vortexoStreamLooksAdult(stream providers.TorrentioStream) bool {
	text := " " + normalizeVortexoLookupTitle(strings.Join([]string{
		stream.Title,
		stream.Name,
		stream.BehaviorHints.Filename,
		stream.Source,
	}, " ")) + " "
	if strings.TrimSpace(text) == "" {
		return false
	}

	adultTerms := []string{
		" xxx ",
		" porn ",
		" adult ",
		" onlyfans ",
		" deepthroat ",
		" blowjob ",
		" brazzers ",
		" bangbros ",
		" blacked ",
		" anal ",
		" hentai ",
		" jav ",
	}
	for _, term := range adultTerms {
		if strings.Contains(text, term) {
			return true
		}
	}

	return false
}

func (h *Handler) vortexoOnlyCachedSourcesEnabled() bool {
	if h == nil || h.settingsManager == nil {
		return false
	}
	cfg := h.settingsManager.Get()
	return cfg.OnlyCachedStreams || cfg.CometOnlyShowCached
}

func (h *Handler) refreshVortexoCacheAvailability(ctx context.Context, streams []providers.TorrentioStream, onlyCached bool) []providers.TorrentioStream {
	if len(streams) == 0 {
		return streams
	}

	hashes := make([]string, 0, len(streams))
	seenHashes := make(map[string]bool)
	for _, stream := range streams {
		if directVortexoPlaybackURL(stream.URL) != "" {
			continue
		}
		hash := vortexoStreamHash(stream)
		if hash == "" || seenHashes[hash] {
			continue
		}
		seenHashes[hash] = true
		hashes = append(hashes, hash)
	}

	availability := map[string]bool{}
	verified := false
	if len(hashes) > 0 && h != nil && h.rdClient != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := h.rdClient.CheckInstantAvailability(checkCtx, hashes)
		cancel()
		if err != nil {
			log.Printf("[Vortexo] Real-Debrid cached-only availability check failed: %v", err)
		} else {
			availability = result
			verified = true
		}
	} else if len(hashes) > 0 && onlyCached {
		log.Printf("[Vortexo] Cached-only filtering requested but Real-Debrid client is unavailable; using provider cache markers")
	}

	filtered := applyVortexoCacheAvailability(streams, availability, verified, onlyCached)
	if onlyCached && len(filtered) != len(streams) {
		log.Printf("[Vortexo] Cached-only filter kept %d/%d streams", len(filtered), len(streams))
	}
	return filtered
}

func applyVortexoCacheAvailability(
	streams []providers.TorrentioStream,
	availability map[string]bool,
	verified bool,
	onlyCached bool,
) []providers.TorrentioStream {
	refreshed := make([]providers.TorrentioStream, 0, len(streams))
	for _, stream := range streams {
		if directVortexoPlaybackURL(stream.URL) != "" {
			stream.Cached = true
		} else if hash := vortexoStreamHash(stream); hash != "" {
			if cached, ok := availability[hash]; ok {
				stream.Cached = cached
			} else if verified {
				stream.Cached = false
			}
		}

		if onlyCached && !stream.Cached {
			continue
		}
		refreshed = append(refreshed, stream)
	}
	return refreshed
}

func vortexoStreamHash(stream providers.TorrentioStream) string {
	hash := normalizeTorrentHash(stream.InfoHash)
	if !isValidHash(hash) && stream.URL != "" {
		hash = extractHashFromURL(stream.URL)
	}
	hash = normalizeTorrentHash(hash)
	if !isValidHash(hash) {
		return ""
	}
	return hash
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (h *Handler) buildVortexoSources(providerStreams []providers.TorrentioStream, req vortexoSourcesRequest) []vortexoSource {
	sources := make([]vortexoSource, 0, len(providerStreams))
	for _, stream := range providerStreams {
		directURL := directVortexoPlaybackURL(stream.URL)
		hash := stream.InfoHash
		if !isValidHash(hash) && stream.URL != "" {
			hash = extractHashFromURL(stream.URL)
		}
		hash = normalizeTorrentHash(hash)
		if directURL == "" && hash != "" && isVortexoSourceBlocked(hash) {
			continue
		}

		token := vortexoPlayToken{
			Hash:    hash,
			Title:   stream.Title,
			FileIdx: stream.FileIdx,
		}
		if directURL != "" {
			token.Hash = ""
			token.URL = directURL
		} else if !isValidHash(hash) && stream.URL != "" {
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

		source := vortexoSource{
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
		}
		if directURL != "" {
			source.DirectURL = directURL
			source.DownloadURL = directURL
		}
		if req.Type == "episode" {
			source.Season = req.Season
			source.Episode = req.Episode
		}

		sources = append(sources, source)
	}

	return sources
}

func directVortexoPlaybackURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	lowerURL := strings.ToLower(rawURL)
	if strings.HasPrefix(lowerURL, "http://") || strings.HasPrefix(lowerURL, "https://") {
		return rawURL
	}

	return ""
}

func wantsVortexoPlayJSON(r *http.Request) bool {
	if r == nil {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "json", "direct", "url":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("resolve"))) {
	case "1", "true", "yes", "json":
		return true
	}

	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/json")
}

func respondVortexoPlaybackURL(w http.ResponseWriter, streamURL string) {
	w.Header().Set("Cache-Control", "no-store")
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"url":          streamURL,
		"stream_url":   streamURL,
		"direct_url":   streamURL,
		"download_url": streamURL,
		"download":     streamURL,
	})
}

func markVortexoSourceBlocked(hash, reason string) {
	hash = normalizeTorrentHash(hash)
	if !isValidHash(hash) {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "blocked"
	}

	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash[hash] = vortexoBlockedSource{
		Reason:    reason,
		ExpiresAt: time.Now().Add(vortexoBlockedSourceTTL),
	}
	vortexoBlockedSources.Unlock()
}

func isVortexoSourceBlocked(hash string) bool {
	hash = normalizeTorrentHash(hash)
	if !isValidHash(hash) {
		return false
	}

	now := time.Now()
	vortexoBlockedSources.RLock()
	entry, ok := vortexoBlockedSources.byHash[hash]
	vortexoBlockedSources.RUnlock()
	if !ok {
		return false
	}
	if now.Before(entry.ExpiresAt) {
		return true
	}

	vortexoBlockedSources.Lock()
	if current, exists := vortexoBlockedSources.byHash[hash]; exists && !now.Before(current.ExpiresAt) {
		delete(vortexoBlockedSources.byHash, hash)
	}
	vortexoBlockedSources.Unlock()
	return false
}

func isRealDebridBlockedPlaybackError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *services.RealDebridAPIError
	if errors.As(err, &apiErr) {
		name := strings.ToLower(strings.TrimSpace(apiErr.ErrorName))
		if name == "infringing_file" || name == "infringing_torrent" {
			return true
		}
		if strings.Contains(strings.ToLower(apiErr.Body), "infringing") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "infringing_file")
}

func safeVortexoPlaybackError(err error) string {
	if err == nil {
		return "failed to resolve source"
	}

	var apiErr *services.RealDebridAPIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorName != "" {
			return fmt.Sprintf("Real-Debrid rejected source: %s", apiErr.ErrorName)
		}
		if apiErr.StatusCode > 0 {
			return fmt.Sprintf("Real-Debrid returned HTTP %d", apiErr.StatusCode)
		}
	}

	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "failed to resolve source"
	}
	if len(message) > 180 {
		message = message[:180] + "..."
	}
	return "failed to resolve source: " + message
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

func encodeVortexoSubtitleToken(req vortexoSourcesRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeVortexoSubtitleToken(value string) (vortexoSourcesRequest, error) {
	var req vortexoSourcesRequest
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, err
	}
	req.Type = normalizeVortexoType(req.Type)
	if req.Type == "" {
		return req, fmt.Errorf("empty subtitle token")
	}
	return req, nil
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
