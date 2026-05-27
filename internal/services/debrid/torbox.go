package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const torBoxBaseURL = "https://api.torbox.app/v1/api"

var torBoxHashRE = regexp.MustCompile(`(?i)^[a-f0-9]{40}$`)

// TorBox implements DebridService for TorBox.
type TorBox struct {
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

type torBoxResponse struct {
	Success bool            `json:"success"`
	Error   any             `json:"error"`
	Detail  string          `json:"detail"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type TorBoxAPIError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *TorBoxAPIError) Error() string {
	if e == nil {
		return "TorBox API error"
	}
	message := "TorBox"
	if e.Operation != "" {
		message += " " + e.Operation
	}
	if e.StatusCode > 0 {
		message += fmt.Sprintf(" returned HTTP %d", e.StatusCode)
	} else {
		message += " failed"
	}
	if strings.TrimSpace(e.Body) != "" {
		message += ": " + strings.TrimSpace(e.Body)
	}
	return message
}

// NewTorBox creates a new TorBox service instance.
func NewTorBox(apiKey string, logger *slog.Logger) *TorBox {
	if logger == nil {
		logger = slog.Default()
	}
	return &TorBox{
		apiKey: strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
		logger: logger,
	}
}

func (tb *TorBox) CheckCache(ctx context.Context, hashes []string) (map[string]bool, error) {
	result := make(map[string]bool, len(hashes))
	unique := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		normalized := normalizeTorBoxHash(hash)
		if !isValidTorBoxHash(normalized) {
			continue
		}
		result[normalized] = false
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		unique = append(unique, normalized)
	}
	if len(unique) == 0 {
		return result, nil
	}

	payload, _ := json.Marshal(map[string]any{"hashes": unique})
	body, err := tb.do(ctx, http.MethodPost, "/torrents/checkcached?format=object&list_files=false", "check cache", bytes.NewReader(payload), "application/json")
	if err != nil {
		return result, err
	}

	availability := parseTorBoxAvailability(body)
	for hash, cached := range availability {
		result[normalizeTorBoxHash(hash)] = cached
	}
	return result, nil
}

func (tb *TorBox) GetStreamURL(ctx context.Context, hash string, fileIndex int) (string, error) {
	torrentID, err := tb.AddToLibrary(ctx, hash, fileIndex)
	if err != nil {
		return "", err
	}
	return tb.GetStreamURLForTorrentID(ctx, torrentID, fileIndex)
}

func (tb *TorBox) GetStreamURLForTorrentID(ctx context.Context, torrentID string, fileIndex int) (string, error) {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return "", fmt.Errorf("TorBox torrent ID is required")
	}
	if isValidTorBoxHash(torrentID) {
		resolvedID, err := tb.FindTorrentIDByHash(ctx, torrentID)
		if err != nil {
			return "", err
		}
		torrentID = resolvedID
	}
	values := url.Values{}
	values.Set("token", tb.apiKey)
	values.Set("torrent_id", torrentID)
	values.Set("zip_link", "false")
	values.Set("redirect", "false")
	values.Set("append_name", "true")
	if fileIndex > 0 {
		values.Set("file_id", strconv.Itoa(fileIndex))
	}
	body, err := tb.do(ctx, http.MethodGet, "/torrents/requestdl?"+values.Encode(), "request download", nil, "")
	if err != nil {
		return "", err
	}
	if streamURL := firstURLFromJSON(body); streamURL != "" {
		return streamURL, nil
	}
	return "", fmt.Errorf("TorBox did not return a stream URL")
}

func (tb *TorBox) GetAvailableFiles(ctx context.Context, hash string) ([]TorrentFile, error) {
	hash = normalizeTorBoxHash(hash)
	if !isValidTorBoxHash(hash) {
		return nil, fmt.Errorf("invalid TorBox hash %q", hash)
	}
	values := url.Values{}
	values.Set("hash", hash)
	values.Set("format", "object")
	values.Set("list_files", "true")
	body, err := tb.do(ctx, http.MethodGet, "/torrents/checkcached?"+values.Encode(), "list cached files", nil, "")
	if err != nil {
		return nil, err
	}

	files := parseTorBoxFiles(body)
	return files, nil
}

func (tb *TorBox) AddToLibrary(ctx context.Context, hash string, fileIndex int) (string, error) {
	hash = normalizeTorBoxHash(hash)
	if !isValidTorBoxHash(hash) {
		return "", fmt.Errorf("invalid TorBox hash %q", hash)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("magnet", "magnet:?xt=urn:btih:"+hash)
	_ = writer.WriteField("seed", "3")
	_ = writer.WriteField("allow_zip", "false")
	_ = writer.WriteField("name", hash)
	_ = writer.WriteField("as_queued", "false")
	_ = writer.WriteField("add_only_if_cached", "true")
	if fileIndex > 0 {
		_ = writer.WriteField("file_id", strconv.Itoa(fileIndex))
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	body, err := tb.do(ctx, http.MethodPost, "/torrents/createtorrent", "create torrent", &buf, writer.FormDataContentType())
	if err != nil {
		if existingID, findErr := tb.FindTorrentIDByHash(ctx, hash); findErr == nil && existingID != "" {
			return existingID, nil
		}
		return "", err
	}
	if id := firstIDFromJSON(body); id != "" {
		return id, nil
	}
	if existingID, err := tb.FindTorrentIDByHash(ctx, hash); err == nil && existingID != "" {
		return existingID, nil
	}
	return "", fmt.Errorf("TorBox create torrent did not return torrent ID")
}

func (tb *TorBox) FindTorrentIDByHash(ctx context.Context, hash string) (string, error) {
	hash = normalizeTorBoxHash(hash)
	if !isValidTorBoxHash(hash) {
		return "", fmt.Errorf("invalid TorBox hash %q", hash)
	}

	const limit = 1000
	for offset := 0; offset < 5000; offset += limit {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		values.Set("offset", strconv.Itoa(offset))
		values.Set("bypass_cache", "true")
		body, err := tb.do(ctx, http.MethodGet, "/torrents/mylist?"+values.Encode(), "find torrent", nil, "")
		if err != nil {
			return "", err
		}
		if id := torBoxTorrentIDForHash(body, hash); id != "" {
			return id, nil
		}
		if torBoxTopLevelListLength(body) < limit {
			break
		}
	}

	return "", fmt.Errorf("TorBox torrent %s is not in the account library", hash)
}

func (tb *TorBox) GetServiceName() string {
	return "TorBox"
}

func (tb *TorBox) IsAuthenticated(ctx context.Context) bool {
	_, err := tb.do(ctx, http.MethodGet, "/torrents/mylist?limit=1", "authenticate", nil, "")
	return err == nil
}

func (tb *TorBox) do(ctx context.Context, method, endpoint, operation string, body io.Reader, contentType string) ([]byte, error) {
	if strings.TrimSpace(tb.apiKey) == "" {
		return nil, fmt.Errorf("TorBox API key is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, torBoxBaseURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tb.apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := tb.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, torBoxRateLimitError(operation, resp, respBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &TorBoxAPIError{
			Operation:  operation,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	if torBoxResponseFailed(respBody) {
		return nil, &TorBoxAPIError{
			Operation:  operation,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	return respBody, nil
}

func torBoxResponseFailed(body []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	successRaw, ok := raw["success"]
	if !ok {
		return false
	}
	var success bool
	if err := json.Unmarshal(successRaw, &success); err != nil || success {
		return false
	}
	return len(raw["error"]) > 0 || len(raw["detail"]) > 0 || len(raw["message"]) > 0
}

func normalizeTorBoxHash(hash string) string {
	hash = strings.TrimSpace(hash)
	hash = strings.TrimPrefix(hash, "magnet:?xt=urn:btih:")
	if idx := strings.IndexAny(hash, "&?"); idx >= 0 {
		hash = hash[:idx]
	}
	return strings.ToLower(strings.TrimSpace(hash))
}

func isValidTorBoxHash(hash string) bool {
	return torBoxHashRE.MatchString(normalizeTorBoxHash(hash))
}

func parseTorBoxAvailability(body []byte) map[string]bool {
	result := make(map[string]bool)
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err != nil || len(response.Data) == 0 {
		return result
	}
	mark := func(hash string, cached bool) {
		hash = normalizeTorBoxHash(hash)
		if isValidTorBoxHash(hash) {
			result[hash] = cached
		}
	}
	var data any
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return result
	}
	parseTorBoxAvailabilityValue("", data, mark)
	return result
}

func parseTorBoxAvailabilityValue(hash string, value any, mark func(string, bool)) bool {
	switch v := value.(type) {
	case bool:
		mark(hash, v)
		return v
	case string:
		if isValidTorBoxHash(v) {
			mark(v, true)
			return true
		}
	case []any:
		cached := len(v) > 0
		if hash != "" {
			mark(hash, cached)
		}
		for _, item := range v {
			if parseTorBoxAvailabilityValue(hash, item, mark) {
				cached = true
			}
		}
		return cached
	case map[string]any:
		if h, _ := v["hash"].(string); h != "" {
			hash = h
		}
		cached := false
		for _, key := range []string{"cached", "is_cached", "instant", "available"} {
			if b, ok := v[key].(bool); ok {
				cached = cached || b
			}
		}
		if files, ok := v["files"].([]any); ok && len(files) > 0 {
			cached = true
		}
		for key, nested := range v {
			if isValidTorBoxHash(key) {
				if parseTorBoxAvailabilityValue(key, nested, mark) {
					cached = true
				}
			}
		}
		if hash != "" {
			mark(hash, cached)
		}
		return cached
	}
	return false
}

func parseTorBoxFiles(body []byte) []TorrentFile {
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err != nil || len(response.Data) == 0 {
		return nil
	}
	var data any
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return nil
	}
	files := make([]TorrentFile, 0)
	collectTorBoxFiles(data, &files)
	return files
}

func collectTorBoxFiles(value any, files *[]TorrentFile) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			collectTorBoxFiles(item, files)
		}
	case map[string]any:
		name := stringFromAny(v["name"])
		path := stringFromAny(v["path"])
		if path == "" {
			path = name
		}
		if path != "" {
			idx := intFromAny(v["file_id"])
			if idx == 0 {
				idx = intFromAny(v["id"])
			}
			if idx == 0 {
				idx = intFromAny(v["index"])
			}
			size := int64FromAny(v["size"])
			if size == 0 {
				size = int64FromAny(v["bytes"])
			}
			*files = append(*files, TorrentFile{
				Index:    idx,
				Path:     path,
				Size:     size,
				Selected: true,
			})
		}
		for _, item := range v {
			collectTorBoxFiles(item, files)
		}
	}
}

func firstIDFromJSON(body []byte) string {
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err == nil && len(response.Data) > 0 {
		if id := firstStringField(response.Data, "torrent_id", "id", "auth_id"); id != "" {
			return id
		}
		var s string
		if err := json.Unmarshal(response.Data, &s); err == nil {
			return strings.TrimSpace(s)
		}
	}
	return firstStringField(body, "torrent_id", "id", "auth_id")
}

func torBoxTorrentIDForHash(body []byte, hash string) string {
	hash = normalizeTorBoxHash(hash)
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err == nil && len(response.Data) > 0 {
		var data any
		if err := json.Unmarshal(response.Data, &data); err == nil {
			return torBoxTorrentIDForHashValue(data, hash)
		}
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	return torBoxTorrentIDForHashValue(data, hash)
}

func torBoxTorrentIDForHashValue(value any, hash string) string {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if id := torBoxTorrentIDForHashValue(item, hash); id != "" {
				return id
			}
		}
	case map[string]any:
		if torBoxValueContainsHash(v, hash) {
			if id := firstStringFieldFromAny(v, "torrent_id", "id", "auth_id"); id != "" {
				return id
			}
		}
		for _, item := range v {
			if id := torBoxTorrentIDForHashValue(item, hash); id != "" {
				return id
			}
		}
	}
	return ""
}

func torBoxValueContainsHash(value any, hash string) bool {
	hash = normalizeTorBoxHash(hash)
	switch v := value.(type) {
	case string:
		return normalizeTorBoxHash(v) == hash || strings.Contains(strings.ToLower(v), hash)
	case []any:
		for _, item := range v {
			if torBoxValueContainsHash(item, hash) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if torBoxValueContainsHash(item, hash) {
				return true
			}
		}
	}
	return false
}

func torBoxTopLevelListLength(body []byte) int {
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err == nil && len(response.Data) > 0 {
		var data any
		if err := json.Unmarshal(response.Data, &data); err == nil {
			return torBoxListLength(data)
		}
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return 0
	}
	return torBoxListLength(data)
}

func torBoxListLength(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case map[string]any:
		for _, key := range []string{"torrents", "items", "results", "data"} {
			if items, ok := v[key].([]any); ok {
				return len(items)
			}
		}
	}
	return 0
}

func firstURLFromJSON(body []byte) string {
	var response torBoxResponse
	if err := json.Unmarshal(body, &response); err == nil && len(response.Data) > 0 {
		if u := firstStringField(response.Data, "url", "download", "download_url", "link", "dl"); u != "" {
			return u
		}
		var s string
		if err := json.Unmarshal(response.Data, &s); err == nil && strings.HasPrefix(strings.TrimSpace(s), "http") {
			return strings.TrimSpace(s)
		}
	}
	return firstStringField(body, "url", "download", "download_url", "link", "dl")
}

func firstStringField(body []byte, keys ...string) string {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	return firstStringFieldFromAny(data, keys...)
}

func firstStringFieldFromAny(value any, keys ...string) string {
	return firstStringFieldFromAnyValue(value, keys, true)
}

func firstStringFieldFromAnyValue(value any, keys []string, allowBareString bool) string {
	switch v := value.(type) {
	case string:
		if allowBareString {
			return strings.TrimSpace(v)
		}
	case []any:
		for _, item := range v {
			if s := firstStringFieldFromAnyValue(item, keys, false); s != "" {
				return s
			}
		}
	case map[string]any:
		for _, key := range keys {
			if s := stringFromAny(v[key]); s != "" {
				return s
			}
		}
		for _, item := range v {
			if s := firstStringFieldFromAnyValue(item, keys, false); s != "" {
				return s
			}
		}
	}
	return ""
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(v))
		return i
	default:
		return 0
	}
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		i, _ := strconv.ParseInt(v.String(), 10, 64)
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return i
	default:
		return 0
	}
}
