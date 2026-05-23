package services

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultOpenSubtitlesAPIBaseURL = "https://api.opensubtitles.com/api/v1"
	defaultOpenSubtitlesUserAgent  = "VortexoServer v1"
	defaultOpenSubtitlesCacheDir   = "cache/subtitles/opensubtitles"
	maxSubtitleDownloadBytes       = 10 << 20
)

// OpenSubtitlesConfig contains credentials and runtime options for the
// OpenSubtitles.com REST API.
type OpenSubtitlesConfig struct {
	Enabled   bool
	APIKey    string
	Username  string
	Password  string
	Languages string
	UserAgent string
	BaseURL   string
	CacheDir  string
}

// Ready returns true when the provider has enough information to search and
// download subtitles.
func (c OpenSubtitlesConfig) Ready() bool {
	return c.Enabled &&
		strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.Username) != "" &&
		strings.TrimSpace(c.Password) != ""
}

// ClientKey identifies the reusable client/session configuration.
func (c OpenSubtitlesConfig) ClientKey() string {
	return strings.Join([]string{
		fmt.Sprintf("%t", c.Enabled),
		strings.TrimSpace(c.APIKey),
		strings.TrimSpace(c.Username),
		strings.TrimSpace(c.Password),
		strings.TrimSpace(c.UserAgent),
		strings.TrimSpace(c.BaseURL),
	}, "\x00")
}

type OpenSubtitlesSearchRequest struct {
	Type         string
	IMDBID       string
	TMDBID       int
	ParentIMDBID string
	ParentTMDBID int
	Query        string
	Year         int
	Season       int
	Episode      int
	Language     string
	Format       string
}

type OpenSubtitlesFetchedSubtitle struct {
	Content     []byte
	ContentType string
	FileName    string
	Language    string
	Format      string
	FromCache   bool
}

type OpenSubtitlesClient struct {
	cfg        OpenSubtitlesConfig
	httpClient *http.Client

	mu      sync.Mutex
	token   string
	baseURL string
}

type openSubtitlesLoginResponse struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
	Status  int    `json:"status"`
}

type openSubtitlesSearchResponse struct {
	Data []openSubtitlesSearchItem `json:"data"`
}

type openSubtitlesSearchItem struct {
	ID         string                     `json:"id"`
	Type       string                     `json:"type"`
	Attributes openSubtitlesSubtitleAttrs `json:"attributes"`
}

type openSubtitlesSubtitleAttrs struct {
	Language         string                      `json:"language"`
	DownloadCount    int                         `json:"download_count"`
	NewDownloadCount int                         `json:"new_download_count"`
	FromTrusted      bool                        `json:"from_trusted"`
	HearingImpaired  bool                        `json:"hearing_impaired"`
	ForeignOnly      bool                        `json:"foreign_parts_only"`
	Release          string                      `json:"release"`
	Files            []openSubtitlesSubtitleFile `json:"files"`
}

type openSubtitlesSubtitleFile struct {
	FileID   int    `json:"file_id"`
	FileName string `json:"file_name"`
	CDNumber int    `json:"cd_number"`
}

type openSubtitlesDownloadResponse struct {
	Link      string `json:"link"`
	FileName  string `json:"file_name"`
	Requests  int    `json:"requests"`
	Remaining int    `json:"remaining"`
	Message   string `json:"message"`
}

type openSubtitlesAPIError struct {
	Status  int
	Message string
}

func (e *openSubtitlesAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("opensubtitles returned HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("opensubtitles returned HTTP %d", e.Status)
}

func NewOpenSubtitlesClient(cfg OpenSubtitlesConfig) *OpenSubtitlesClient {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.CacheDir = strings.TrimSpace(cfg.CacheDir)
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultOpenSubtitlesUserAgent
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenSubtitlesAPIBaseURL
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = defaultOpenSubtitlesCacheDir
	}

	return &OpenSubtitlesClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: normalizeOpenSubtitlesAPIBaseURL(cfg.BaseURL),
	}
}

func (c *OpenSubtitlesClient) FetchSubtitle(ctx context.Context, req OpenSubtitlesSearchRequest) (*OpenSubtitlesFetchedSubtitle, error) {
	if !c.cfg.Ready() {
		return nil, fmt.Errorf("opensubtitles is not configured")
	}

	req.Language = normalizeOpenSubtitlesLanguage(req.Language)
	if req.Language == "" {
		req.Language = "en"
	}
	req.Format = normalizeSubtitleFormat(req.Format)
	if req.Format == "" {
		req.Format = "vtt"
	}

	cachePath := c.cachePath(req)
	if cached, err := os.ReadFile(cachePath); err == nil && len(cached) > 0 {
		return &OpenSubtitlesFetchedSubtitle{
			Content:     cached,
			ContentType: OpenSubtitlesContentType(req.Format),
			FileName:    filepath.Base(cachePath),
			Language:    req.Language,
			Format:      req.Format,
			FromCache:   true,
		}, nil
	}

	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}

	item, file, err := c.searchBestSubtitle(ctx, req)
	if err != nil {
		return nil, err
	}
	if file.FileID == 0 {
		return nil, fmt.Errorf("opensubtitles returned a subtitle without a downloadable file")
	}

	sourceFormat := req.Format
	if sourceFormat == "vtt" {
		sourceFormat = "srt"
	}

	link, fileName, err := c.downloadLink(ctx, file.FileID, sourceFormat)
	if apiErr, ok := err.(*openSubtitlesAPIError); ok && apiErr.Status == http.StatusUnauthorized {
		c.resetSession()
		if loginErr := c.ensureSession(ctx); loginErr == nil {
			link, fileName, err = c.downloadLink(ctx, file.FileID, sourceFormat)
		}
	}
	if err != nil {
		return nil, err
	}

	payload, err := c.fetchSubtitleFile(ctx, link)
	if err != nil {
		return nil, err
	}
	payload, err = decodeSubtitleArchive(payload)
	if err != nil {
		return nil, err
	}
	if req.Format == "vtt" {
		payload = SRTToWebVTT(payload)
	}

	if fileName == "" {
		fileName = file.FileName
	}
	if fileName == "" {
		fileName = item.Attributes.Release
	}
	if fileName == "" {
		fileName = filepath.Base(cachePath)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err == nil {
		_ = os.WriteFile(cachePath, payload, 0644)
	}

	return &OpenSubtitlesFetchedSubtitle{
		Content:     payload,
		ContentType: OpenSubtitlesContentType(req.Format),
		FileName:    fileName,
		Language:    req.Language,
		Format:      req.Format,
		FromCache:   false,
	}, nil
}

func (c *OpenSubtitlesClient) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	if c.token != "" {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	payload, err := json.Marshal(map[string]string{
		"username": c.cfg.Username,
		"password": c.cfg.Password,
	})
	if err != nil {
		return err
	}

	var decoded openSubtitlesLoginResponse
	if err := c.doJSON(ctx, http.MethodPost, c.currentBaseURL()+"/login", payload, false, &decoded); err != nil {
		return err
	}
	if decoded.Token == "" {
		return fmt.Errorf("opensubtitles login did not return a token")
	}

	c.mu.Lock()
	c.token = decoded.Token
	if decoded.BaseURL != "" {
		c.baseURL = normalizeOpenSubtitlesAPIBaseURL(decoded.BaseURL)
	}
	c.mu.Unlock()

	return nil
}

func (c *OpenSubtitlesClient) resetSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.baseURL = normalizeOpenSubtitlesAPIBaseURL(c.cfg.BaseURL)
}

func (c *OpenSubtitlesClient) searchBestSubtitle(ctx context.Context, req OpenSubtitlesSearchRequest) (openSubtitlesSearchItem, openSubtitlesSubtitleFile, error) {
	values := url.Values{}
	values.Set("languages", req.Language)
	values.Set("order_by", "download_count")
	values.Set("order_direction", "desc")

	query := strings.TrimSpace(req.Query)
	if len([]rune(query)) >= 3 {
		values.Set("query", query)
	}
	if req.Year > 0 {
		values.Set("year", strconv.Itoa(req.Year))
	}

	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case "episode", "series":
		values.Set("type", "episode")
		if req.Season > 0 {
			values.Set("season_number", strconv.Itoa(req.Season))
		}
		if req.Episode > 0 {
			values.Set("episode_number", strconv.Itoa(req.Episode))
		}
		if parent := normalizeOpenSubtitlesIMDBID(req.ParentIMDBID); parent != "" {
			values.Set("parent_imdb_id", parent)
		} else if imdbID := normalizeOpenSubtitlesIMDBID(req.IMDBID); imdbID != "" {
			values.Set("imdb_id", imdbID)
		}
		if req.ParentTMDBID > 0 {
			values.Set("parent_tmdb_id", strconv.Itoa(req.ParentTMDBID))
		} else if req.TMDBID > 0 {
			values.Set("tmdb_id", strconv.Itoa(req.TMDBID))
		}
	default:
		values.Set("type", "movie")
		if imdbID := normalizeOpenSubtitlesIMDBID(req.IMDBID); imdbID != "" {
			values.Set("imdb_id", imdbID)
		}
		if req.TMDBID > 0 {
			values.Set("tmdb_id", strconv.Itoa(req.TMDBID))
		}
	}

	if values.Get("imdb_id") == "" && values.Get("parent_imdb_id") == "" &&
		values.Get("tmdb_id") == "" && values.Get("parent_tmdb_id") == "" &&
		values.Get("query") == "" {
		return openSubtitlesSearchItem{}, openSubtitlesSubtitleFile{}, fmt.Errorf("opensubtitles search needs an IMDb/TMDB id or title")
	}

	var decoded openSubtitlesSearchResponse
	endpoint := c.currentBaseURL() + "/subtitles?" + values.Encode()
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, true, &decoded); err != nil {
		return openSubtitlesSearchItem{}, openSubtitlesSubtitleFile{}, err
	}

	sort.SliceStable(decoded.Data, func(i, j int) bool {
		left := decoded.Data[i].Attributes
		right := decoded.Data[j].Attributes
		if left.ForeignOnly != right.ForeignOnly {
			return !left.ForeignOnly
		}
		if left.FromTrusted != right.FromTrusted {
			return left.FromTrusted
		}
		return left.DownloadCount+left.NewDownloadCount > right.DownloadCount+right.NewDownloadCount
	})

	for _, item := range decoded.Data {
		if len(item.Attributes.Files) == 0 {
			continue
		}
		return item, item.Attributes.Files[0], nil
	}

	return openSubtitlesSearchItem{}, openSubtitlesSubtitleFile{}, fmt.Errorf("no %s subtitles found for %s", req.Language, strings.TrimSpace(req.Query))
}

func (c *OpenSubtitlesClient) downloadLink(ctx context.Context, fileID int, format string) (string, string, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"file_id":    fileID,
		"sub_format": normalizeSubtitleFormat(format),
	})
	if err != nil {
		return "", "", err
	}

	var decoded openSubtitlesDownloadResponse
	if err := c.doJSON(ctx, http.MethodPost, c.currentBaseURL()+"/download", payload, true, &decoded); err != nil {
		return "", "", err
	}
	if decoded.Link == "" {
		if decoded.Message != "" {
			return "", "", fmt.Errorf("opensubtitles did not return a download link: %s", decoded.Message)
		}
		return "", "", fmt.Errorf("opensubtitles did not return a download link")
	}
	return decoded.Link, decoded.FileName, nil
}

func (c *OpenSubtitlesClient) fetchSubtitleFile(ctx context.Context, link string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", c.cfg.UserAgent)
	request.Header.Set("Accept", "text/vtt,application/x-subrip,text/plain,*/*")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &openSubtitlesAPIError{Status: response.StatusCode, Message: "subtitle file download failed"}
	}

	return io.ReadAll(io.LimitReader(response.Body, maxSubtitleDownloadBytes))
}

func (c *OpenSubtitlesClient) doJSON(ctx context.Context, method, endpoint string, payload []byte, auth bool, out interface{}) error {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Api-Key", c.cfg.APIKey)
	request.Header.Set("User-Agent", c.cfg.UserAgent)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		token := c.currentToken()
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxSubtitleDownloadBytes))
	if readErr != nil {
		return readErr
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &openSubtitlesAPIError{Status: response.StatusCode, Message: openSubtitlesErrorMessage(responseBody)}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode opensubtitles response: %w", err)
	}
	return nil
}

func (c *OpenSubtitlesClient) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *OpenSubtitlesClient) currentBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baseURL
}

func (c *OpenSubtitlesClient) cachePath(req OpenSubtitlesSearchRequest) string {
	format := normalizeSubtitleFormat(req.Format)
	if format == "" {
		format = "vtt"
	}

	parts := []string{strings.ToLower(strings.TrimSpace(req.Type))}
	if parts[0] == "" {
		parts[0] = "movie"
	}
	if imdbID := normalizeOpenSubtitlesIMDBID(firstOpenSubtitlesNonEmpty(req.IMDBID, req.ParentIMDBID)); imdbID != "" {
		parts = append(parts, "imdb"+imdbID)
	}
	if req.TMDBID > 0 {
		parts = append(parts, fmt.Sprintf("tmdb%d", req.TMDBID))
	}
	if req.ParentTMDBID > 0 {
		parts = append(parts, fmt.Sprintf("ptmdb%d", req.ParentTMDBID))
	}
	if req.Season > 0 || req.Episode > 0 {
		parts = append(parts, fmt.Sprintf("s%02de%02d", req.Season, req.Episode))
	}
	parts = append(parts, normalizeOpenSubtitlesLanguage(req.Language))

	name := sanitizeSubtitleCacheName(strings.Join(parts, "_")) + "." + format
	return filepath.Join(c.cfg.CacheDir, name)
}

func openSubtitlesErrorMessage(body []byte) string {
	var decoded struct {
		Message string        `json:"message"`
		Error   string        `json:"error"`
		Errors  []interface{} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil {
		if decoded.Message != "" {
			return decoded.Message
		}
		if decoded.Error != "" {
			return decoded.Error
		}
		if len(decoded.Errors) > 0 {
			values := make([]string, 0, len(decoded.Errors))
			for _, item := range decoded.Errors {
				values = append(values, fmt.Sprint(item))
			}
			return strings.Join(values, "; ")
		}
	}
	return strings.TrimSpace(string(body))
}

func firstOpenSubtitlesNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeOpenSubtitlesAPIBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultOpenSubtitlesAPIBaseURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(defaultOpenSubtitlesAPIBaseURL, "/")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/api/v1") {
		path = strings.TrimRight(path, "/") + "/api/v1"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func normalizeOpenSubtitlesIMDBID(imdbID string) string {
	imdbID = strings.TrimSpace(strings.ToLower(imdbID))
	imdbID = strings.TrimPrefix(imdbID, "tt")
	imdbID = strings.TrimLeft(imdbID, "0")
	for _, r := range imdbID {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return imdbID
}

func normalizeSubtitleFormat(format string) string {
	format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	switch format {
	case "webvtt":
		return "vtt"
	case "srt", "vtt":
		return format
	default:
		return ""
	}
}

func OpenSubtitlesContentType(format string) string {
	switch normalizeSubtitleFormat(format) {
	case "vtt":
		return "text/vtt; charset=utf-8"
	case "srt":
		return "application/x-subrip; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func OpenSubtitlesLanguageList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	if len(fields) == 0 {
		fields = []string{"en"}
	}

	seen := make(map[string]bool)
	languages := make([]string, 0, len(fields))
	for _, field := range fields {
		lang := normalizeOpenSubtitlesLanguage(field)
		if lang == "" || seen[lang] {
			continue
		}
		seen[lang] = true
		languages = append(languages, lang)
	}
	if len(languages) == 0 {
		return []string{"en"}
	}
	return languages
}

func normalizeOpenSubtitlesLanguage(language string) string {
	language = strings.TrimSpace(strings.ToLower(language))
	language = strings.ReplaceAll(language, "_", "-")
	if language == "" {
		return ""
	}

	aliases := map[string]string{
		"eng": "en", "english": "en", "en-us": "en", "en-gb": "en",
		"spa": "es", "spanish": "es",
		"fre": "fr", "fra": "fr", "french": "fr",
		"ger": "de", "deu": "de", "german": "de",
		"ita": "it", "italian": "it",
		"por": "pt", "portuguese": "pt",
		"rus": "ru", "russian": "ru",
		"srp": "sr", "serbian": "sr",
		"hrv": "hr", "croatian": "hr",
		"bos": "bs", "bosnian": "bs",
	}
	if alias, ok := aliases[language]; ok {
		return alias
	}

	if len(language) > 2 && strings.Contains(language, "-") {
		switch language {
		case "pt-br", "pt-pt", "zh-cn", "zh-tw":
			return language
		default:
			return strings.SplitN(language, "-", 2)[0]
		}
	}
	return language
}

func sanitizeSubtitleCacheName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	value := strings.Trim(b.String(), "_")
	if value == "" {
		return "subtitle"
	}
	return value
}

func decodeSubtitleArchive(payload []byte) ([]byte, error) {
	if len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(io.LimitReader(reader, maxSubtitleDownloadBytes))
	}

	if len(payload) >= 4 && bytes.Equal(payload[:4], []byte{'P', 'K', 3, 4}) {
		reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			return nil, err
		}
		for _, file := range reader.File {
			if file.FileInfo().IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(file.Name))
			if ext != ".srt" && ext != ".vtt" && ext != ".ass" && ext != ".ssa" && ext != ".sub" {
				continue
			}
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxSubtitleDownloadBytes))
		}
		return nil, fmt.Errorf("subtitle archive did not contain a supported subtitle file")
	}

	return payload, nil
}

var srtTimestampPattern = regexp.MustCompile(`(\d{2}:\d{2}:\d{2}),(\d{3})\s+-->\s+(\d{2}:\d{2}:\d{2}),(\d{3})`)

func SRTToWebVTT(input []byte) []byte {
	text := strings.TrimPrefix(string(input), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return []byte(strings.TrimSpace(text) + "\n")
	}

	text = srtTimestampPattern.ReplaceAllString(text, "$1.$2 --> $3.$4")
	return []byte("WEBVTT\n\n" + strings.TrimSpace(text) + "\n")
}
