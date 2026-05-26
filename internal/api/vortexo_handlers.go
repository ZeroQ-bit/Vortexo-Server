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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	Priority     int     `json:"priority,omitempty"`
	PlayURL      string  `json:"play_url"`
	DirectURL    string  `json:"direct_url,omitempty"`
	DownloadURL  string  `json:"download_url,omitempty"`
}

type vortexoPlayToken struct {
	Hash      string `json:"hash,omitempty"`
	URL       string `json:"url,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	Title     string `json:"title,omitempty"`
	FileIdx   int    `json:"fileIdx,omitempty"`
	TorrentID string `json:"torrentId,omitempty"`
}

const (
	vortexoRDWebDAVLibrarySource     = "RD WebDAV Library"
	vortexoRDWebDAVLibraryPriority   = 110
	vortexoRDWebDAVLocalURLPrefix    = "vortexo-rd-webdav://"
	vortexoRealDebridLibrarySource   = "Real-Debrid Library"
	vortexoRealDebridLibraryPriority = 100
	vortexoLibraryCacheSource        = "Vortexo Library Cache"
	vortexoLibraryCachePriority      = 90
)

type vortexoRealDebridLibrarySourceCacheEntry struct {
	Streams   []providers.TorrentioStream
	ExpiresAt time.Time
}

type vortexoBlockedSource struct {
	Reason    string
	ExpiresAt time.Time
}

var vortexoRealDebridLibrarySourceCache = struct {
	sync.RWMutex
	byKey map[string]vortexoRealDebridLibrarySourceCacheEntry
}{
	byKey: make(map[string]vortexoRealDebridLibrarySourceCacheEntry),
}

var vortexoBlockedSources = struct {
	sync.RWMutex
	byHash map[string]vortexoBlockedSource
}{
	byHash: make(map[string]vortexoBlockedSource),
}

const vortexoRealDebridLibrarySourceCacheTTL = 30 * time.Minute
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
		"trakt_import":                  true,
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

	releaseYear := req.Year
	libraryStreams := h.vortexoCachedLibraryStreams(r.Context(), req)
	if rdLibraryStreams := h.vortexoRealDebridLibraryStreams(r.Context(), req, releaseYear); len(rdLibraryStreams) > 0 {
		libraryStreams = prependVortexoPreferredStreams(rdLibraryStreams, libraryStreams)
	}
	if webDAVStreams := h.vortexoWebDAVLibraryStreams(r.Context(), req); len(webDAVStreams) > 0 {
		libraryStreams = prependVortexoPreferredStreams(webDAVStreams, libraryStreams)
	}

	sources := h.buildVortexoSources(libraryStreams, req)
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

	if strings.TrimSpace(token.LocalPath) != "" {
		fileURL := absoluteVortexoRequestURL(r, "/api/v1/vortexo/file/"+tokenValue)
		if wantsVortexoPlayJSON(r) {
			respondVortexoPlaybackURL(w, fileURL)
			return
		}
		http.Redirect(w, r, fileURL, http.StatusFound)
		return
	}

	if token.URL != "" && token.Hash == "" && token.TorrentID == "" {
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

	var streamURL string
	if strings.TrimSpace(token.TorrentID) != "" {
		streamURL, err = h.rdClient.GetStreamURLForTorrentID(ctx, token.TorrentID, token.FileIdx, token.Title)
	} else {
		streamURL, err = h.rdClient.GetStreamURLForFile(ctx, token.Hash, token.FileIdx, token.Title)
	}
	if err != nil {
		log.Printf("[Vortexo] Failed to resolve source %q: %v", token.Title, err)
		if isRealDebridBlockedPlaybackError(err) {
			if token.Hash != "" {
				markVortexoSourceBlocked(token.Hash, "infringing_file")
			}
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

// VortexoLocalFile serves a tokenized local RD WebDAV symlink. It is separate
// from VortexoPlay so clients that first resolve sources can receive a stable
// HTTP URL and then let the player stream the file with range requests.
func (h *Handler) VortexoLocalFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tokenValue := strings.TrimSpace(vars["token"])
	if tokenValue == "" {
		respondError(w, http.StatusBadRequest, "missing file token")
		return
	}

	token, err := decodeVortexoPlayToken(tokenValue)
	if err != nil || strings.TrimSpace(token.LocalPath) == "" {
		respondError(w, http.StatusBadRequest, "invalid file token")
		return
	}

	filePath, err := h.safeVortexoLocalFilePath(token.LocalPath)
	if err != nil {
		log.Printf("[Vortexo] Refused local RD WebDAV file %q: %v", token.LocalPath, err)
		respondError(w, http.StatusNotFound, "file not found")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeFile(w, r, filePath)
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

func filterVortexoEpisodeStreams(streams []providers.TorrentioStream, title string, season, episode, year int) []providers.TorrentioStream {
	if len(streams) == 0 {
		return streams
	}

	filtered := make([]providers.TorrentioStream, 0, len(streams))
	for _, stream := range streams {
		if vortexoStreamLooksAdult(stream) {
			log.Printf("[Vortexo] Filtered adult-looking episode source for %q S%02dE%02d: %s", title, season, episode, firstNonEmpty(stream.Title, stream.Name))
			continue
		}
		if !vortexoEpisodeStreamMatches(stream, title, season, episode, year) {
			log.Printf("[Vortexo] Filtered episode mismatch for %q S%02dE%02d: %s", title, season, episode, firstNonEmpty(stream.Title, stream.Name))
			continue
		}
		filtered = append(filtered, stream)
	}

	if len(filtered) != len(streams) {
		log.Printf("[Vortexo] Episode source filter kept %d/%d streams for %q S%02dE%02d", len(filtered), len(streams), title, season, episode)
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

func vortexoEpisodeStreamMatches(stream providers.TorrentioStream, title string, season, episode, year int) bool {
	requested := normalizeVortexoLookupTitle(title)
	candidate := normalizeVortexoLookupTitle(vortexoStreamTitleText(stream))
	if candidate == "" {
		return false
	}
	if requested != "" && !vortexoCandidateTitleMatches(candidate, requested, year) {
		return false
	}

	rawText := vortexoStreamTitleText(stream)
	if parsedSeason, parsedEpisode, ok := parseVortexoEpisodeReference(rawText); ok {
		return parsedSeason == season && parsedEpisode == episode
	}
	if parsedSeason, ok := parseVortexoSeasonReference(rawText); ok {
		return parsedSeason == season
	}

	return season <= 0 || episode <= 0
}

func vortexoStreamTitleText(stream providers.TorrentioStream) string {
	return strings.Join([]string{
		stream.Title,
		stream.Name,
		stream.BehaviorHints.Filename,
	}, " ")
}

func vortexoCandidateTitleMatches(candidate, requested string, year int) bool {
	if requested == "" {
		return true
	}
	if candidate == requested || strings.HasPrefix(candidate, requested+" ") {
		return true
	}
	if year > 0 {
		for _, acceptedYear := range vortexoAcceptedMovieYears(year) {
			if strings.HasPrefix(candidate, fmt.Sprintf("%s %d ", requested, acceptedYear)) {
				return true
			}
		}
	}

	requestedTokens := strings.Fields(requested)
	candidateTokens := strings.Fields(candidate)
	if len(requestedTokens) == 0 || len(candidateTokens) < len(requestedTokens) {
		return false
	}

	allRequestedTokensAreLetters := true
	var acronym strings.Builder
	for _, token := range requestedTokens {
		if len(token) != 1 {
			allRequestedTokensAreLetters = false
			break
		}
		acronym.WriteString(token)
	}
	if allRequestedTokensAreLetters && acronym.Len() > 1 {
		for index, token := range requestedTokens {
			if candidateTokens[index] != token {
				return false
			}
		}
		return true
	}

	if len(requestedTokens) == 1 && len(requestedTokens[0]) <= 4 {
		for index, char := range requestedTokens[0] {
			if index >= len(candidateTokens) || candidateTokens[index] != string(char) {
				return false
			}
		}
		return true
	}

	return false
}

func parseVortexoEpisodeReference(text string) (int, int, bool) {
	patterns := []string{
		`(?i)(?:^|[^A-Z0-9])S0*([0-9]{1,3})[\s._-]*E0*([0-9]{1,3})(?:[^0-9]|$)`,
		`(?i)(?:^|[^A-Z0-9])0*([0-9]{1,3})x0*([0-9]{1,3})(?:[^0-9]|$)`,
		`(?i)(?:^|[^A-Z0-9])season[\s._-]*0*([0-9]{1,3}).{0,40}episode[\s._-]*0*([0-9]{1,3})(?:[^0-9]|$)`,
	}
	for _, pattern := range patterns {
		matches := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if len(matches) < 3 {
			continue
		}
		season, seasonErr := strconv.Atoi(matches[1])
		episode, episodeErr := strconv.Atoi(matches[2])
		if seasonErr == nil && episodeErr == nil && season > 0 && episode > 0 {
			return season, episode, true
		}
	}
	return 0, 0, false
}

func parseVortexoSeasonReference(text string) (int, bool) {
	patterns := []string{
		`(?i)(?:^|[^A-Z0-9])S0*([0-9]{1,3})(?:[^0-9]|$)`,
		`(?i)(?:^|[^A-Z0-9])season[\s._-]*0*([0-9]{1,3})(?:[^0-9]|$)`,
	}
	for _, pattern := range patterns {
		matches := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if len(matches) < 2 {
			continue
		}
		season, err := strconv.Atoi(matches[1])
		if err == nil && season > 0 {
			return season, true
		}
	}
	return 0, false
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

func vortexoRealDebridLibrarySourceCacheKey(req vortexoSourcesRequest, releaseYear int) string {
	lookupTitle := normalizeVortexoLookupTitle(firstNonEmpty(req.ParentTitle, req.Title))
	if lookupTitle == "" && req.TMDBID <= 0 && strings.TrimSpace(req.IMDBID) == "" {
		return ""
	}

	parts := []string{
		normalizeVortexoType(req.Type),
		strconv.Itoa(req.TMDBID),
		strings.ToLower(strings.TrimSpace(req.IMDBID)),
		lookupTitle,
		strconv.Itoa(releaseYear),
		strconv.Itoa(req.Season),
		strconv.Itoa(req.Episode),
	}
	return strings.Join(parts, "|")
}

func cachedVortexoRealDebridLibrarySources(cacheKey string) ([]providers.TorrentioStream, bool) {
	if cacheKey == "" {
		return nil, false
	}

	now := time.Now()
	vortexoRealDebridLibrarySourceCache.RLock()
	entry, ok := vortexoRealDebridLibrarySourceCache.byKey[cacheKey]
	vortexoRealDebridLibrarySourceCache.RUnlock()
	if !ok {
		return nil, false
	}
	if now.After(entry.ExpiresAt) {
		vortexoRealDebridLibrarySourceCache.Lock()
		if current, exists := vortexoRealDebridLibrarySourceCache.byKey[cacheKey]; exists && now.After(current.ExpiresAt) {
			delete(vortexoRealDebridLibrarySourceCache.byKey, cacheKey)
		}
		vortexoRealDebridLibrarySourceCache.Unlock()
		return nil, false
	}

	return cloneVortexoTorrentioStreams(entry.Streams), len(entry.Streams) > 0
}

func cacheVortexoRealDebridLibrarySources(cacheKey string, streams []providers.TorrentioStream) {
	if cacheKey == "" || len(streams) == 0 {
		return
	}

	vortexoRealDebridLibrarySourceCache.Lock()
	vortexoRealDebridLibrarySourceCache.byKey[cacheKey] = vortexoRealDebridLibrarySourceCacheEntry{
		Streams:   cloneVortexoTorrentioStreams(streams),
		ExpiresAt: time.Now().Add(vortexoRealDebridLibrarySourceCacheTTL),
	}
	vortexoRealDebridLibrarySourceCache.Unlock()
}

func cloneVortexoTorrentioStreams(streams []providers.TorrentioStream) []providers.TorrentioStream {
	if len(streams) == 0 {
		return nil
	}
	cloned := make([]providers.TorrentioStream, len(streams))
	copy(cloned, streams)
	return cloned
}

func (h *Handler) vortexoRealDebridLibraryStreams(ctx context.Context, req vortexoSourcesRequest, releaseYear int) []providers.TorrentioStream {
	if h == nil || h.rdClient == nil {
		return nil
	}

	requestedTitle := firstNonEmpty(req.ParentTitle, req.Title)
	if strings.TrimSpace(requestedTitle) == "" {
		return nil
	}

	cacheKey := vortexoRealDebridLibrarySourceCacheKey(req, releaseYear)
	if cached, ok := cachedVortexoRealDebridLibrarySources(cacheKey); ok {
		log.Printf("[Vortexo] Reusing %d cached Real-Debrid library sources for %q", len(cached), requestedTitle)
		h.cacheVortexoRealDebridLibraryStreamOptions(ctx, req, cached)
		return cached
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	const pageSize = 5000
	const maxPages = 8
	const maxMatches = 12

	streams := make([]providers.TorrentioStream, 0, 2)
	seen := make(map[string]bool)
	for _, searchFilter := range vortexoRealDebridLibrarySearchFilters(requestedTitle) {
		if len(streams) >= maxMatches {
			break
		}
		for page := 1; page <= maxPages && len(streams) < maxMatches; page++ {
			torrents, err := h.rdClient.ListTorrentsFiltered(lookupCtx, page, pageSize, searchFilter)
			if err != nil {
				log.Printf("[Vortexo] Real-Debrid library source lookup failed on page %d filter=%q: %v", page, searchFilter, err)
				break
			}
			if len(torrents) == 0 {
				break
			}

			for _, torrent := range torrents {
				if len(streams) >= maxMatches {
					break
				}
				if !strings.EqualFold(strings.TrimSpace(torrent.Status), "downloaded") {
					continue
				}

				hash := normalizeTorrentHash(torrent.Hash)
				if !isValidHash(hash) || isVortexoSourceBlocked(hash) {
					continue
				}

				stream := vortexoRealDebridTorrentListStream(torrent)
				if !vortexoSourceRequestMatches(stream, req, releaseYear) {
					continue
				}

				if req.Type == "episode" {
					info, err := h.rdClient.GetTorrentInfo(lookupCtx, torrent.ID)
					if err != nil {
						log.Printf("[Vortexo] Real-Debrid library torrent info failed for %s: %v", torrent.ID, err)
						continue
					}
					rawTitle := vortexoStreamTitleText(stream)
					torrentSeason, hasTorrentSeason := parseVortexoSeasonReference(rawTitle)
					_, _, hasTorrentEpisode := parseVortexoEpisodeReference(rawTitle)
					allowOrdinalFallback := hasTorrentEpisode || (hasTorrentSeason && torrentSeason == req.Season)
					if file, ok := vortexoRealDebridEpisodeFile(info.Files, req.Season, req.Episode, allowOrdinalFallback); ok {
						stream.Title = vortexoRealDebridFileDisplayName(file.Path)
						stream.Name = stream.Title
						stream.BehaviorHints.Filename = stream.Title
						stream.FileIdx = file.ID
						if file.Bytes > 0 {
							stream.Size = file.Bytes
						}
					} else if _, _, ok := parseVortexoEpisodeReference(rawTitle); !ok {
						continue
					}
				}

				key := fmt.Sprintf("%s|%d", hash, stream.FileIdx)
				if seen[key] {
					continue
				}
				seen[key] = true
				streams = append(streams, stream)
			}
		}
	}

	if len(streams) > 0 {
		cacheVortexoRealDebridLibrarySources(cacheKey, streams)
		h.cacheVortexoRealDebridLibraryStreamOptions(ctx, req, streams)
		log.Printf("[Vortexo] Prioritizing %d downloaded Real-Debrid library sources for %q", len(streams), requestedTitle)
	}
	return streams
}

func (h *Handler) cacheVortexoRealDebridLibraryStreamOptions(ctx context.Context, req vortexoSourcesRequest, streams []providers.TorrentioStream) {
	if h == nil || h.streamCacheStore == nil || len(streams) == 0 {
		return
	}

	var movieID, seriesID int
	switch req.Type {
	case "movie":
		if h.movieStore == nil || req.TMDBID <= 0 {
			return
		}
		movie, err := h.movieStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || movie == nil {
			return
		}
		movieID = int(movie.ID)
	case "episode":
		if h.seriesStore == nil || req.TMDBID <= 0 || req.Season <= 0 || req.Episode <= 0 {
			return
		}
		series, err := h.seriesStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || series == nil {
			return
		}
		seriesID = int(series.ID)
	default:
		return
	}

	for _, stream := range streams {
		option := vortexoStreamOptionFromTorrentio(req, stream, movieID, seriesID)
		if option == nil {
			continue
		}
		if err := h.streamCacheStore.UpsertStreamOption(ctx, option); err != nil {
			log.Printf("[Vortexo] Failed to save Real-Debrid library stream option for %q: %v", option.StreamTitle, err)
		}
	}
}

func vortexoStreamOptionFromTorrentio(req vortexoSourcesRequest, stream providers.TorrentioStream, movieID, seriesID int) *models.CachedStream {
	hash := vortexoStreamHash(stream)
	if !isValidHash(hash) {
		return nil
	}

	title := firstNonEmpty(stream.Title, stream.Name, stream.BehaviorHints.Filename)
	if title == "" {
		title = "Real-Debrid library stream"
	}
	streamURL := strings.TrimSpace(stream.URL)
	if streamURL == "" {
		streamURL = "magnet:?xt=urn:btih:" + hash
	}

	sizeGB := 0.0
	if stream.Size > 0 {
		sizeGB = float64(stream.Size) / (1024 * 1024 * 1024)
	}

	quality := streammeta.ParseQualityFromTorrentName(title)
	if quality.Resolution == "" {
		quality.Resolution = stream.Quality
	}
	if quality.SizeGB <= 0 {
		quality.SizeGB = sizeGB
	}
	quality.Seeders = stream.Seeders
	score := streammeta.CalculateScore(quality).TotalScore

	mediaType := "movie"
	mediaID := movieID
	if req.Type == "episode" {
		mediaType = "series"
		mediaID = seriesID
	}

	return &models.CachedStream{
		MediaType:      mediaType,
		MediaID:        mediaID,
		MovieID:        movieID,
		SeriesID:       seriesID,
		Season:         req.Season,
		Episode:        req.Episode,
		StreamTitle:    title,
		StreamURL:      streamURL,
		StreamHash:     hash,
		QualityScore:   score,
		Resolution:     quality.Resolution,
		HDRType:        quality.HDRType,
		AudioFormat:    quality.AudioFormat,
		SourceType:     quality.Source,
		FileSizeGB:     sizeGB,
		Codec:          quality.Codec,
		Indexer:        vortexoRealDebridLibrarySource,
		IsAvailable:    true,
		RDLibraryAdded: true,
		RDTorrentID:    strings.TrimSpace(stream.TorrentID),
		RDFileID:       stream.FileIdx,
	}
}

func vortexoRealDebridLibrarySearchFilters(requestedTitle string) []string {
	seen := make(map[string]bool)
	filters := make([]string, 0, 8)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if seen[key] {
			return
		}
		seen[key] = true
		filters = append(filters, value)
	}

	normalized := normalizeVortexoLookupTitle(requestedTitle)
	normalizedFields := strings.Fields(normalized)
	add(requestedTitle)
	add(normalized)
	add(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(requestedTitle))
	add(strings.NewReplacer(".", "", "_", "", "-", "").Replace(requestedTitle))
	if len(normalizedFields) > 1 {
		add(strings.Join(normalizedFields, "."))
		add(strings.Join(normalizedFields, "_"))
		add(strings.Join(normalizedFields, ""))
	}
	return filters
}

func (h *Handler) vortexoWebDAVLibraryStreams(ctx context.Context, req vortexoSourcesRequest) []providers.TorrentioStream {
	if h == nil {
		return nil
	}

	switch req.Type {
	case "movie":
		if h.movieStore == nil || req.TMDBID <= 0 {
			return nil
		}
		movie, err := h.movieStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || movie == nil || !isRDWebDAVLibraryMetadata(movie.Metadata) {
			return nil
		}
		if stream, ok := h.vortexoWebDAVStreamFromMetadata(movie.Metadata, firstNonEmpty(movie.Title, req.Title)); ok {
			return []providers.TorrentioStream{stream}
		}
	case "episode":
		if h.seriesStore == nil || h.episodeStore == nil || req.TMDBID <= 0 || req.Season <= 0 || req.Episode <= 0 {
			return nil
		}
		series, err := h.seriesStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || series == nil {
			return nil
		}
		episode, err := h.episodeStore.GetBySeriesAndNumber(ctx, series.ID, req.Season, req.Episode)
		if err != nil || episode == nil || !isRDWebDAVLibraryMetadata(episode.Metadata) {
			return nil
		}
		title := firstNonEmpty(episode.Title, req.Title)
		if req.ParentTitle != "" {
			title = strings.TrimSpace(fmt.Sprintf("%s S%02dE%02d %s", req.ParentTitle, req.Season, req.Episode, title))
		}
		if stream, ok := h.vortexoWebDAVStreamFromMetadata(episode.Metadata, title); ok {
			stream.Seeders = 0
			return []providers.TorrentioStream{stream}
		}
	}

	return nil
}

func (h *Handler) vortexoWebDAVStreamFromMetadata(metadata models.Metadata, fallbackTitle string) (providers.TorrentioStream, bool) {
	filePath := firstNonEmpty(
		metadataString(metadata, "rd_webdav_symlink_path"),
		metadataString(metadata, "rd_webdav_source_path"),
	)
	if filePath == "" {
		return providers.TorrentioStream{}, false
	}

	safePath, err := h.safeVortexoLocalFilePath(filePath)
	if err != nil {
		log.Printf("[Vortexo] RD WebDAV library file unavailable: %s: %v", filePath, err)
		return providers.TorrentioStream{}, false
	}
	info, err := os.Stat(safePath)
	if err != nil || info.IsDir() {
		return providers.TorrentioStream{}, false
	}

	fileName := filepath.Base(safePath)
	title := firstNonEmpty(fileName, strings.TrimSpace(fallbackTitle), "RD WebDAV library file")
	parsed := streammeta.ParseQualityFromTorrentName(title)
	stream := providers.TorrentioStream{
		Name:    title,
		Title:   title,
		URL:     encodeVortexoLocalFileStreamURL(safePath),
		Quality: parsed.Resolution,
		Size:    info.Size(),
		Cached:  true,
		Source:  vortexoRDWebDAVLibrarySource,
	}
	stream.BehaviorHints.Filename = title
	stream.BehaviorHints.VideoSize = info.Size()
	return stream, true
}

func (h *Handler) vortexoCachedLibraryStreams(ctx context.Context, req vortexoSourcesRequest) []providers.TorrentioStream {
	if h == nil || h.streamCacheStore == nil {
		return nil
	}

	switch req.Type {
	case "movie":
		if h.movieStore == nil || req.TMDBID <= 0 {
			return nil
		}
		movie, err := h.movieStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || movie == nil {
			return nil
		}
		optionStreams := make([]providers.TorrentioStream, 0, 8)
		options, err := h.streamCacheStore.GetStreamOptionsForMovie(ctx, int(movie.ID), 20)
		if err != nil {
			log.Printf("[Vortexo] Cached library movie option lookup failed for tmdb=%d: %v", req.TMDBID, err)
		}
		for _, option := range options {
			if option != nil && option.IsAvailable {
				optionStreams = append(optionStreams, vortexoCachedStreamToTorrentio(*option, req))
			}
		}

		legacyStreams := make([]providers.TorrentioStream, 0, 1)
		cached, err := h.streamCacheStore.GetCachedStream(ctx, int(movie.ID))
		if err != nil {
			log.Printf("[Vortexo] Cached library movie source lookup failed for tmdb=%d: %v", req.TMDBID, err)
		} else if cached != nil && cached.IsAvailable {
			legacyStreams = append(legacyStreams, vortexoCachedStreamToTorrentio(*cached, req))
		}
		return prependVortexoPreferredStreams(optionStreams, legacyStreams)
	case "episode":
		if h.seriesStore == nil || req.TMDBID <= 0 || req.Season <= 0 || req.Episode <= 0 {
			return nil
		}
		series, err := h.seriesStore.GetByTMDBID(ctx, req.TMDBID)
		if err != nil || series == nil {
			return nil
		}
		optionStreams := make([]providers.TorrentioStream, 0, 8)
		options, err := h.streamCacheStore.GetStreamOptionsForSeriesEpisode(ctx, int(series.ID), req.Season, req.Episode, 20)
		if err != nil {
			log.Printf("[Vortexo] Cached library episode option lookup failed for tmdb=%d S%02dE%02d: %v", req.TMDBID, req.Season, req.Episode, err)
		}
		for _, option := range options {
			if option != nil && option.IsAvailable {
				optionStreams = append(optionStreams, vortexoCachedStreamToTorrentio(*option, req))
			}
		}

		legacyStreams := make([]providers.TorrentioStream, 0, 1)
		cached, err := h.streamCacheStore.GetCachedSeriesEpisode(ctx, int(series.ID), req.Season, req.Episode)
		if err != nil {
			log.Printf("[Vortexo] Cached library episode source lookup failed for tmdb=%d S%02dE%02d: %v", req.TMDBID, req.Season, req.Episode, err)
		} else if cached != nil && cached.IsAvailable {
			legacyStreams = append(legacyStreams, vortexoCachedStreamToTorrentio(*cached, req))
		}
		return prependVortexoPreferredStreams(optionStreams, legacyStreams)
	default:
		return nil
	}
}

func vortexoCachedStreamToTorrentio(cached models.CachedStream, req vortexoSourcesRequest) providers.TorrentioStream {
	hash := normalizeTorrentHash(cached.StreamHash)
	if !isValidHash(hash) {
		hash = extractHashFromURL(cached.StreamURL)
	}

	title := vortexoCachedStreamTitle(cached, req)
	source := vortexoLibraryCacheSource
	if cached.RDLibraryAdded || strings.TrimSpace(cached.RDTorrentID) != "" || cached.RDFileID > 0 || strings.EqualFold(strings.TrimSpace(cached.Indexer), vortexoRealDebridLibrarySource) {
		source = vortexoRealDebridLibrarySource
	}
	streamURL := strings.TrimSpace(cached.StreamURL)
	if streamURL == "" && isValidHash(hash) {
		streamURL = "magnet:?xt=urn:btih:" + hash
	}

	return providers.TorrentioStream{
		Name:      title,
		Title:     title,
		InfoHash:  hash,
		TorrentID: strings.TrimSpace(cached.RDTorrentID),
		FileIdx:   firstNonZero(cached.RDFileID, extractFileIndexFromStreamURL(cached.StreamURL)),
		URL:       streamURL,
		Quality:   cached.Resolution,
		Size:      int64(cached.FileSizeGB * 1024 * 1024 * 1024),
		Cached:    true,
		Source:    source,
	}
}

func vortexoCachedStreamTitle(cached models.CachedStream, req vortexoSourcesRequest) string {
	if strings.TrimSpace(cached.StreamTitle) != "" {
		return strings.TrimSpace(cached.StreamTitle)
	}
	title := firstNonEmpty(req.ParentTitle, req.Title)
	if req.Type == "episode" {
		title = fmt.Sprintf("%s S%02dE%02d", title, req.Season, req.Episode)
	}
	details := strings.Join(strings.Fields(strings.Join([]string{
		cached.Resolution,
		cached.HDRType,
		cached.Codec,
		cached.AudioFormat,
		cached.Indexer,
	}, " ")), " ")
	if details != "" {
		title = strings.TrimSpace(title + " " + details)
	}
	if title != "" {
		return title
	}
	return firstNonEmpty(cached.StreamURL, cached.StreamHash, "Vortexo cached stream")
}

func vortexoRealDebridTorrentListStream(torrent services.RealDebridTorrentListItem) providers.TorrentioStream {
	hash := normalizeTorrentHash(torrent.Hash)
	title := strings.TrimSpace(torrent.Filename)
	parsed := streammeta.ParseQualityFromTorrentName(title)
	return providers.TorrentioStream{
		Name:      title,
		Title:     title,
		InfoHash:  hash,
		TorrentID: strings.TrimSpace(torrent.ID),
		URL:       "magnet:?xt=urn:btih:" + hash,
		Quality:   parsed.Resolution,
		Size:      torrent.Bytes,
		Cached:    true,
		Source:    vortexoRealDebridLibrarySource,
	}
}

func vortexoSourceRequestMatches(stream providers.TorrentioStream, req vortexoSourcesRequest, releaseYear int) bool {
	if vortexoStreamLooksAdult(stream) {
		return false
	}
	switch req.Type {
	case "episode":
		return vortexoEpisodeStreamMatches(
			stream,
			firstNonEmpty(req.ParentTitle, req.Title),
			req.Season,
			req.Episode,
			releaseYear,
		)
	case "movie":
		return vortexoMovieStreamMatchesTitle(stream, req.Title, releaseYear)
	default:
		return false
	}
}

func vortexoRealDebridEpisodeFile(files []services.RealDebridTorrentFile, season, episode int, allowOrdinalFallback bool) (services.RealDebridTorrentFile, bool) {
	playableFiles := make([]services.RealDebridTorrentFile, 0, len(files))
	var best services.RealDebridTorrentFile
	var bestBytes int64
	for _, file := range files {
		if !vortexoRealDebridFileLooksPlayable(file.Path) {
			continue
		}
		playableFiles = append(playableFiles, file)
		fileSeason, fileEpisode, ok := parseVortexoEpisodeReference(file.Path)
		if !ok || fileSeason != season || fileEpisode != episode {
			continue
		}
		if file.Bytes >= bestBytes {
			best = file
			bestBytes = file.Bytes
		}
	}
	if best.ID > 0 {
		return best, true
	}
	if !allowOrdinalFallback || episode <= 0 {
		return services.RealDebridTorrentFile{}, false
	}

	if file, ok := vortexoRealDebridEpisodeFileByLooseNumber(playableFiles, episode); ok {
		return file, true
	}
	if episode <= len(playableFiles) {
		return playableFiles[episode-1], true
	}
	return services.RealDebridTorrentFile{}, false
}

func vortexoRealDebridEpisodeFileByLooseNumber(files []services.RealDebridTorrentFile, episode int) (services.RealDebridTorrentFile, bool) {
	var best services.RealDebridTorrentFile
	var bestBytes int64
	for _, file := range files {
		fileEpisode, ok := parseVortexoLooseEpisodeNumber(file.Path)
		if !ok || fileEpisode != episode {
			continue
		}
		if file.Bytes >= bestBytes {
			best = file
			bestBytes = file.Bytes
		}
	}
	return best, best.ID > 0
}

func parseVortexoLooseEpisodeNumber(path string) (int, bool) {
	name := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".mov", ".avi", ".ts", ".webm"} {
		name = strings.TrimSuffix(name, ext)
		name = strings.TrimSuffix(name, strings.ToUpper(ext))
	}

	patterns := []string{
		`(?i)^\s*0*([0-9]{1,3})(?:[\s._-]|$)`,
		`(?i)(?:^|[^A-Z0-9])E0*([0-9]{1,3})(?:[^0-9]|$)`,
		`(?i)(?:^|[^A-Z0-9])EP(?:ISODE)?[\s._-]*0*([0-9]{1,3})(?:[^0-9]|$)`,
	}
	for _, pattern := range patterns {
		matches := regexp.MustCompile(pattern).FindStringSubmatch(name)
		if len(matches) < 2 {
			continue
		}
		episode, err := strconv.Atoi(matches[1])
		if err == nil && episode > 0 {
			return episode, true
		}
	}
	return 0, false
}

func vortexoRealDebridFileLooksPlayable(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	name := strings.TrimLeft(strings.ReplaceAll(lower, "\\", "/"), "/")
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	for _, marker := range []string{"sample", "trailer", "extra", "extras"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".mov", ".avi", ".ts", ".webm"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func vortexoRealDebridFileDisplayName(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimLeft(path, "/")
	if path == "" {
		return "Real-Debrid library file"
	}
	return path
}

func prependVortexoPreferredStreams(preferred, existing []providers.TorrentioStream) []providers.TorrentioStream {
	combined := make([]providers.TorrentioStream, 0, len(preferred)+len(existing))
	seen := make(map[string]bool)
	replaceStream := func(index int, stream providers.TorrentioStream) {
		if key := vortexoStreamDedupKey(combined[index]); key != "" {
			delete(seen, key)
		}
		combined[index] = stream
		if key := vortexoStreamDedupKey(stream); key != "" {
			seen[key] = true
		}
	}
	appendStream := func(stream providers.TorrentioStream) {
		for index, existingStream := range combined {
			if !vortexoLibraryStreamsDescribeSameFile(existingStream, stream) {
				continue
			}
			replaceStream(index, mergeVortexoLibraryStreams(existingStream, stream))
			return
		}
		key := vortexoStreamDedupKey(stream)
		if key != "" {
			if seen[key] {
				return
			}
			seen[key] = true
		}
		combined = append(combined, stream)
	}
	for _, stream := range preferred {
		appendStream(stream)
	}
	for _, stream := range existing {
		appendStream(stream)
	}
	return combined
}

func vortexoLibraryStreamsDescribeSameFile(a, b providers.TorrentioStream) bool {
	aWebDAV := vortexoStreamIsRDWebDAVLibrary(a)
	bWebDAV := vortexoStreamIsRDWebDAVLibrary(b)
	if aWebDAV == bWebDAV {
		return false
	}
	if aWebDAV && !vortexoStreamIsSavedLibrarySource(b) {
		return false
	}
	if bWebDAV && !vortexoStreamIsSavedLibrarySource(a) {
		return false
	}
	return vortexoStreamSizesLikelyMatch(vortexoComparableStreamSize(a), vortexoComparableStreamSize(b))
}

func mergeVortexoLibraryStreams(a, b providers.TorrentioStream) providers.TorrentioStream {
	metadata := vortexoRicherLibraryMetadataStream(a, b)
	local := metadata
	if decodeVortexoLocalFileStreamURL(a.URL) != "" {
		local = a
	} else if decodeVortexoLocalFileStreamURL(b.URL) != "" {
		local = b
	}

	merged := local
	if metadata.Title != "" {
		merged.Title = metadata.Title
	}
	if metadata.Name != "" {
		merged.Name = metadata.Name
	}
	if metadata.Quality != "" && !strings.EqualFold(metadata.Quality, "SD") {
		merged.Quality = metadata.Quality
	}
	if merged.Quality == "" {
		merged.Quality = metadata.Quality
	}
	if metadata.BehaviorHints.Filename != "" {
		merged.BehaviorHints.Filename = metadata.BehaviorHints.Filename
	}
	if metadata.BehaviorHints.BingeGroup != "" {
		merged.BehaviorHints.BingeGroup = metadata.BehaviorHints.BingeGroup
	}
	merged.Size = maxInt64(vortexoComparableStreamSize(a), vortexoComparableStreamSize(b))
	merged.BehaviorHints.VideoSize = merged.Size
	merged.Cached = a.Cached || b.Cached
	merged.Seeders = maxInt(a.Seeders, b.Seeders)

	if decodeVortexoLocalFileStreamURL(merged.URL) != "" {
		merged.InfoHash = ""
		merged.TorrentID = ""
		merged.FileIdx = 0
	}
	return merged
}

func vortexoStreamIsRDWebDAVLibrary(stream providers.TorrentioStream) bool {
	return strings.EqualFold(strings.TrimSpace(stream.Source), vortexoRDWebDAVLibrarySource) ||
		decodeVortexoLocalFileStreamURL(stream.URL) != ""
}

func vortexoStreamIsSavedLibrarySource(stream providers.TorrentioStream) bool {
	source := strings.TrimSpace(stream.Source)
	return strings.EqualFold(source, vortexoRealDebridLibrarySource) ||
		strings.EqualFold(source, vortexoLibraryCacheSource)
}

func vortexoComparableStreamSize(stream providers.TorrentioStream) int64 {
	if stream.Size > 0 {
		return stream.Size
	}
	if stream.BehaviorHints.VideoSize > 0 {
		return stream.BehaviorHints.VideoSize
	}
	return 0
}

func vortexoStreamSizesLikelyMatch(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	tolerance := int64(1024 * 1024)
	if scaled := minInt64(a, b) / 1000; scaled > tolerance {
		tolerance = scaled
	}
	if maxTolerance := int64(64 * 1024 * 1024); tolerance > maxTolerance {
		tolerance = maxTolerance
	}
	return diff <= tolerance
}

func vortexoRicherLibraryMetadataStream(a, b providers.TorrentioStream) providers.TorrentioStream {
	if vortexoStreamIsRDWebDAVLibrary(a) && !vortexoStreamIsRDWebDAVLibrary(b) {
		return b
	}
	if vortexoStreamIsRDWebDAVLibrary(b) && !vortexoStreamIsRDWebDAVLibrary(a) {
		return a
	}
	if len(firstNonEmpty(b.Title, b.Name, b.BehaviorHints.Filename)) > len(firstNonEmpty(a.Title, a.Name, a.BehaviorHints.Filename)) {
		return b
	}
	return a
}

func vortexoStreamDedupKey(stream providers.TorrentioStream) string {
	if hash := vortexoStreamHash(stream); hash != "" {
		return fmt.Sprintf("hash:%s:%d", hash, stream.FileIdx)
	}
	if localPath := decodeVortexoLocalFileStreamURL(stream.URL); localPath != "" {
		return "rdwebdav:" + localPath
	}
	if direct := directVortexoPlaybackURL(stream.URL); direct != "" {
		return "url:" + direct
	}
	return ""
}

func vortexoStreamSourcePriority(stream providers.TorrentioStream) int {
	if strings.EqualFold(strings.TrimSpace(stream.Source), vortexoRDWebDAVLibrarySource) {
		return vortexoRDWebDAVLibraryPriority
	}
	if strings.EqualFold(strings.TrimSpace(stream.Source), vortexoRealDebridLibrarySource) {
		return vortexoRealDebridLibraryPriority
	}
	if strings.EqualFold(strings.TrimSpace(stream.Source), vortexoLibraryCacheSource) {
		return vortexoLibraryCachePriority
	}
	return 0
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (h *Handler) buildVortexoSources(providerStreams []providers.TorrentioStream, req vortexoSourcesRequest) []vortexoSource {
	sources := make([]vortexoSource, 0, len(providerStreams))
	for _, stream := range providerStreams {
		directURL := directVortexoPlaybackURL(stream.URL)
		localPath := decodeVortexoLocalFileStreamURL(stream.URL)
		hash := stream.InfoHash
		if !isValidHash(hash) && stream.URL != "" {
			hash = extractHashFromURL(stream.URL)
		}
		hash = normalizeTorrentHash(hash)
		if directURL == "" && hash != "" && isVortexoSourceBlocked(hash) {
			continue
		}

		token := vortexoPlayToken{
			Hash:      hash,
			Title:     stream.Title,
			FileIdx:   stream.FileIdx,
			TorrentID: strings.TrimSpace(stream.TorrentID),
		}
		if localPath != "" {
			token.Hash = ""
			token.LocalPath = localPath
		} else if directURL != "" {
			token.Hash = ""
			token.URL = directURL
		} else if !isValidHash(hash) && stream.URL != "" {
			token.Hash = ""
			token.URL = stream.URL
		}
		if token.Hash == "" && token.URL == "" && token.LocalPath == "" && token.TorrentID == "" {
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
			Priority:     vortexoStreamSourcePriority(stream),
			PlayURL:      "/api/v1/vortexo/play/" + id,
		}
		if directURL != "" {
			source.DirectURL = directURL
			source.DownloadURL = directURL
		} else if localPath != "" {
			localURL := "/api/v1/vortexo/file/" + id
			source.DirectURL = localURL
			source.DownloadURL = localURL
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
		if isVortexoResolverURL(rawURL) {
			return ""
		}
		return rawURL
	}

	return ""
}

func encodeVortexoLocalFileStreamURL(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	return vortexoRDWebDAVLocalURLPrefix + base64.RawURLEncoding.EncodeToString([]byte(filePath))
}

func decodeVortexoLocalFileStreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, vortexoRDWebDAVLocalURLPrefix) {
		return ""
	}
	encoded := strings.TrimPrefix(rawURL, vortexoRDWebDAVLocalURLPrefix)
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func absoluteVortexoRequestURL(r *http.Request, path string) string {
	if r == nil {
		return path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	host := strings.TrimSpace(r.Host)
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = strings.Split(forwardedHost, ",")[0]
	}
	if host == "" {
		return path
	}
	return fmt.Sprintf("%s://%s%s", strings.TrimSpace(scheme), strings.TrimSpace(host), path)
}

func (h *Handler) safeVortexoLocalFilePath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("empty local file path")
	}

	absPath, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil {
		return "", err
	}

	roots := h.vortexoLocalFileRoots()
	if !pathWithinAnyRoot(absPath, roots) {
		return "", fmt.Errorf("path is outside configured RD WebDAV roots")
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}

	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		resolvedAbs, absErr := filepath.Abs(filepath.Clean(resolved))
		if absErr != nil {
			return "", absErr
		}
		if !pathWithinAnyRoot(resolvedAbs, roots) {
			return "", fmt.Errorf("symlink target is outside configured RD WebDAV roots")
		}
	}

	return absPath, nil
}

func (h *Handler) vortexoLocalFileRoots() []string {
	libraryPath := "/app/rd-library"
	mountPath := "/mnt/rd"
	if h != nil && h.settingsManager != nil {
		cfg := h.settingsManager.Get()
		if strings.TrimSpace(cfg.RDWebDAVLibraryPath) != "" {
			libraryPath = cfg.RDWebDAVLibraryPath
		}
		if strings.TrimSpace(cfg.RDWebDAVMountPath) != "" {
			mountPath = cfg.RDWebDAVMountPath
		}
	}

	roots := make([]string, 0, 2)
	for _, root := range []string{libraryPath, mountPath} {
		if abs, err := filepath.Abs(filepath.Clean(root)); err == nil && abs != "" {
			roots = append(roots, abs)
		}
	}
	return roots
}

func pathWithinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithinRoot(path, root) {
			return true
		}
	}
	return false
}

func pathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func isVortexoResolverURL(rawURL string) bool {
	lowerURL := strings.ToLower(strings.TrimSpace(rawURL))
	if lowerURL == "" {
		return false
	}
	if strings.Contains(lowerURL, "/resolve/realdebrid/") {
		return true
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.ToLower(parsed.EscapedPath())
	if host == "real-debrid.com" && strings.HasPrefix(path, "/streaming-") {
		return true
	}

	return false
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
	if token.Hash == "" && token.URL == "" && token.LocalPath == "" && token.TorrentID == "" {
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
