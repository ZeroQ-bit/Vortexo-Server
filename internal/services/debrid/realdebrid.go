package debrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	realDebridBaseURL = "https://api.real-debrid.com/rest/1.0"
)

// RateLimitError preserves Real-Debrid backoff hints so callers can pause
// background jobs instead of retrying into the same limit.
type RateLimitError struct {
	Operation  string
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "Real-Debrid rate limit"
	}
	message := "Real-Debrid rate limit"
	if e.Operation != "" {
		message += " during " + e.Operation
	}
	if e.RetryAfter > 0 {
		message += fmt.Sprintf("; retry after %s", e.RetryAfter.Round(time.Second))
	}
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

// TorBoxRateLimitError preserves TorBox backoff hints for background sync jobs.
type TorBoxRateLimitError struct {
	Operation  string
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *TorBoxRateLimitError) Error() string {
	if e == nil {
		return "TorBox rate limit"
	}
	message := "TorBox rate limit"
	if e.Operation != "" {
		message += " during " + e.Operation
	}
	if e.RetryAfter > 0 {
		message += fmt.Sprintf("; retry after %s", e.RetryAfter.Round(time.Second))
	}
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	var rateErr *RateLimitError
	if errors.As(err, &rateErr) {
		return true
	}
	var torBoxRateErr *TorBoxRateLimitError
	if errors.As(err, &torBoxRateErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "too_many_requests") ||
		strings.Contains(msg, "error_code\": 34") ||
		strings.Contains(msg, "status 429") ||
		strings.Contains(msg, "rate limited") ||
		strings.Contains(msg, "rate limit")
}

func RateLimitRetryAfter(err error) time.Duration {
	var rateErr *RateLimitError
	if errors.As(err, &rateErr) && rateErr.RetryAfter > 0 {
		return rateErr.RetryAfter
	}
	var torBoxRateErr *TorBoxRateLimitError
	if errors.As(err, &torBoxRateErr) && torBoxRateErr.RetryAfter > 0 {
		return torBoxRateErr.RetryAfter
	}
	return 0
}

func realDebridRateLimitError(operation string, resp *http.Response, body []byte) *RateLimitError {
	var retryAfter time.Duration
	if resp != nil {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	return &RateLimitError{
		Operation:  operation,
		StatusCode: statusCode,
		RetryAfter: retryAfter,
		Body:       strings.TrimSpace(string(body)),
	}
}

func torBoxRateLimitError(operation string, resp *http.Response, body []byte) *TorBoxRateLimitError {
	var retryAfter time.Duration
	if resp != nil {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	return &TorBoxRateLimitError{
		Operation:  operation,
		StatusCode: statusCode,
		RetryAfter: retryAfter,
		Body:       strings.TrimSpace(string(body)),
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if delay := time.Until(retryAt); delay > 0 {
			return delay
		}
	}
	return 0
}

// RealDebrid implements DebridService for Real-Debrid
type RealDebrid struct {
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

type realDebridTorrentFile struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Selected int    `json:"selected"`
}

type realDebridTorrentInfo struct {
	ID     string                  `json:"id"`
	Status string                  `json:"status"`
	Files  []realDebridTorrentFile `json:"files"`
}

// NewRealDebrid creates a new Real-Debrid service instance
func NewRealDebrid(apiKey string, logger *slog.Logger) *RealDebrid {
	return &RealDebrid{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// CheckCache checks which hashes are cached on Real-Debrid
func (rd *RealDebrid) CheckCache(ctx context.Context, hashes []string) (map[string]bool, error) {
	if len(hashes) == 0 {
		return make(map[string]bool), nil
	}

	// Real-Debrid instant availability endpoint
	// POST /torrents/instantAvailability/{hash1}/{hash2}/...
	url := fmt.Sprintf("%s/torrents/instantAvailability/%s",
		realDebridBaseURL,
		strings.Join(hashes, "/"))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+rd.apiKey)

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, realDebridRateLimitError("check cache", resp, body)
		}
		return nil, fmt.Errorf("real-debrid API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	// Format: { "hash1": { "rd": [{ "files": [...] }] }, "hash2": {} }
	var availability map[string]map[string][]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&availability); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Build result map
	cached := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		hashLower := strings.ToLower(hash)
		if rdData, exists := availability[hashLower]; exists && len(rdData["rd"]) > 0 {
			cached[hash] = true
		} else {
			cached[hash] = false
		}
	}

	rd.logger.Info("Checked Real-Debrid cache",
		"total", len(hashes),
		"cached", countCached(cached))

	return cached, nil
}

// GetStreamURL returns the direct streaming URL for a cached hash
func (rd *RealDebrid) GetStreamURL(ctx context.Context, hash string, fileIndex int) (string, error) {
	// Step 1: Add magnet to Real-Debrid
	magnetURL := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	form := url.Values{}
	form.Set("magnet", magnetURL)

	addURL := fmt.Sprintf("%s/torrents/addMagnet", realDebridBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", addURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create add magnet request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+rd.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", realDebridRateLimitError("add magnet", resp, body)
		}
		return "", fmt.Errorf("add magnet failed (status %d): %s", resp.StatusCode, string(body))
	}

	var addResult struct {
		ID  string `json:"id"`
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&addResult); err != nil {
		return "", fmt.Errorf("decode add magnet response: %w", err)
	}

	// Step 2: Select files (all files)
	selectURL := fmt.Sprintf("%s/torrents/selectFiles/%s", realDebridBaseURL, addResult.ID)
	selectReq, err := http.NewRequestWithContext(ctx, "POST", selectURL, strings.NewReader("files=all"))
	if err != nil {
		return "", fmt.Errorf("create select files request: %w", err)
	}

	selectReq.Header.Set("Authorization", "Bearer "+rd.apiKey)
	selectReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	selectResp, err := rd.httpClient.Do(selectReq)
	if err != nil {
		return "", fmt.Errorf("select files: %w", err)
	}
	defer selectResp.Body.Close()

	// Step 3: Get torrent info to find download link
	infoURL := fmt.Sprintf("%s/torrents/info/%s", realDebridBaseURL, addResult.ID)
	infoReq, err := http.NewRequestWithContext(ctx, "GET", infoURL, nil)
	if err != nil {
		return "", fmt.Errorf("create info request: %w", err)
	}

	infoReq.Header.Set("Authorization", "Bearer "+rd.apiKey)

	infoResp, err := rd.httpClient.Do(infoReq)
	if err != nil {
		return "", fmt.Errorf("get torrent info: %w", err)
	}
	defer infoResp.Body.Close()

	var torrentInfo struct {
		Links []string `json:"links"`
	}
	if err := json.NewDecoder(infoResp.Body).Decode(&torrentInfo); err != nil {
		return "", fmt.Errorf("decode torrent info: %w", err)
	}

	if len(torrentInfo.Links) == 0 {
		return "", fmt.Errorf("no download links available")
	}

	// Step 4: Unrestrict the link to get direct download URL
	unrestrictURL := fmt.Sprintf("%s/unrestrict/link", realDebridBaseURL)
	unrestrictForm := url.Values{}
	unrestrictForm.Set("link", torrentInfo.Links[0])
	unrestrictReq, err := http.NewRequestWithContext(ctx, "POST", unrestrictURL,
		strings.NewReader(unrestrictForm.Encode()))
	if err != nil {
		return "", fmt.Errorf("create unrestrict request: %w", err)
	}

	unrestrictReq.Header.Set("Authorization", "Bearer "+rd.apiKey)
	unrestrictReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	unrestrictResp, err := rd.httpClient.Do(unrestrictReq)
	if err != nil {
		return "", fmt.Errorf("unrestrict link: %w", err)
	}
	defer unrestrictResp.Body.Close()

	var unrestrictResult struct {
		Download string `json:"download"`
	}
	if err := json.NewDecoder(unrestrictResp.Body).Decode(&unrestrictResult); err != nil {
		return "", fmt.Errorf("decode unrestrict response: %w", err)
	}

	return unrestrictResult.Download, nil
}

// GetAvailableFiles returns list of files in a cached torrent
func (rd *RealDebrid) GetAvailableFiles(ctx context.Context, hash string) ([]TorrentFile, error) {
	// For instant availability, we need to check the cache status first
	cached, err := rd.CheckCache(ctx, []string{hash})
	if err != nil {
		return nil, fmt.Errorf("check cache: %w", err)
	}

	if !cached[hash] {
		return nil, fmt.Errorf("torrent not cached")
	}

	// Note: Full implementation would parse the instant availability response
	// to get detailed file information. For now, return basic info.
	return []TorrentFile{}, nil
}

// AddToLibrary adds a torrent to the user's Real-Debrid account without
// resolving a playback URL. When a file index is provided, we select just that
// file; otherwise we select all files so the torrent is usable later.
func (rd *RealDebrid) AddToLibrary(ctx context.Context, hash string, fileIndex int) (string, error) {
	hash = normalizeTorrentHash(hash)
	if !isValidTorrentHash(hash) {
		return "", fmt.Errorf("invalid torrent hash %q", truncateForLog(hash, 12))
	}

	magnetURL := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	form := url.Values{}
	form.Set("magnet", magnetURL)

	addURL := fmt.Sprintf("%s/torrents/addMagnet", realDebridBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", addURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create add magnet request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+rd.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", realDebridRateLimitError("add magnet", resp, body)
		}
		return "", fmt.Errorf("add magnet failed (status %d): %s", resp.StatusCode, string(body))
	}

	var addResult struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&addResult); err != nil {
		return "", fmt.Errorf("decode add magnet response: %w", err)
	}

	info, err := rd.waitForTorrentSelectionInfo(ctx, addResult.ID)
	if err != nil {
		if !IsRateLimitError(err) {
			_ = rd.deleteTorrent(ctx, addResult.ID)
		}
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(info.Status), "downloaded") {
		return addResult.ID, nil
	}
	if !strings.EqualFold(strings.TrimSpace(info.Status), "waiting_files_selection") && len(info.Files) == 0 {
		_ = rd.deleteTorrent(ctx, addResult.ID)
		return "", fmt.Errorf("torrent entered unselectable status %q", info.Status)
	}

	selectForm := url.Values{}
	selectForm.Set("files", selectFilesValue(fileIndex, info.Files))

	selectURL := fmt.Sprintf("%s/torrents/selectFiles/%s", realDebridBaseURL, addResult.ID)
	selectReq, err := http.NewRequestWithContext(ctx, "POST", selectURL, strings.NewReader(selectForm.Encode()))
	if err != nil {
		return "", fmt.Errorf("create select files request: %w", err)
	}

	selectReq.Header.Set("Authorization", "Bearer "+rd.apiKey)
	selectReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	selectResp, err := rd.httpClient.Do(selectReq)
	if err != nil {
		_ = rd.deleteTorrent(ctx, addResult.ID)
		return "", fmt.Errorf("select files: %w", err)
	}
	defer selectResp.Body.Close()

	if selectResp.StatusCode != http.StatusNoContent && selectResp.StatusCode != http.StatusCreated && selectResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(selectResp.Body)
		if selectResp.StatusCode == http.StatusTooManyRequests {
			return "", realDebridRateLimitError("select files", selectResp, body)
		}
		_ = rd.deleteTorrent(ctx, addResult.ID)
		return "", fmt.Errorf("select files failed (status %d): %s", selectResp.StatusCode, string(body))
	}

	return addResult.ID, nil
}

func (rd *RealDebrid) deleteTorrent(ctx context.Context, torrentID string) error {
	if strings.TrimSpace(torrentID) == "" {
		return nil
	}

	deleteURL := fmt.Sprintf("%s/torrents/delete/%s", realDebridBaseURL, torrentID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", deleteURL, nil)
	if err != nil {
		return fmt.Errorf("create delete torrent request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+rd.apiKey)

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete torrent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return realDebridRateLimitError("delete torrent", resp, body)
		}
		return fmt.Errorf("delete torrent failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (rd *RealDebrid) getTorrentInfo(ctx context.Context, torrentID string) (*realDebridTorrentInfo, error) {
	infoURL := fmt.Sprintf("%s/torrents/info/%s", realDebridBaseURL, torrentID)
	req, err := http.NewRequestWithContext(ctx, "GET", infoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create torrent info request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+rd.apiKey)

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get torrent info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, realDebridRateLimitError("get torrent info", resp, body)
		}
		return nil, fmt.Errorf("get torrent info failed (status %d): %s", resp.StatusCode, string(body))
	}

	var info realDebridTorrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode torrent info response: %w", err)
	}
	return &info, nil
}

func (rd *RealDebrid) waitForTorrentSelectionInfo(ctx context.Context, torrentID string) (*realDebridTorrentInfo, error) {
	var lastInfo *realDebridTorrentInfo
	var lastErr error

	for attempt := 0; attempt < 8; attempt++ {
		info, err := rd.getTorrentInfo(ctx, torrentID)
		if err == nil {
			lastInfo = info
			status := strings.ToLower(strings.TrimSpace(info.Status))
			if status == "downloaded" || len(info.Files) > 0 {
				return info, nil
			}
			if isTerminalTorrentStatus(status) {
				return nil, fmt.Errorf("torrent entered unselectable status %q", info.Status)
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}

	if lastInfo != nil {
		return lastInfo, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("torrent info unavailable for %s", torrentID)
}

func isTerminalTorrentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "virus", "dead", "magnet_error":
		return true
	default:
		return false
	}
}

func normalizeTorrentHash(hash string) string {
	hash = strings.TrimSpace(hash)
	hash = strings.TrimPrefix(strings.ToLower(hash), "urn:btih:")
	hash = strings.TrimPrefix(hash, "btih:")
	return hash
}

func isValidTorrentHash(hash string) bool {
	hash = normalizeTorrentHash(hash)
	if len(hash) != 40 {
		return false
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func truncateForLog(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func selectFilesValue(fileIndex int, files []realDebridTorrentFile) string {
	if fileIndex > 0 {
		for _, file := range files {
			if file.ID == fileIndex {
				return strconv.Itoa(fileIndex)
			}
		}
	}
	return "all"
}

// GetServiceName returns the service name
func (rd *RealDebrid) GetServiceName() string {
	return "Real-Debrid"
}

// IsAuthenticated checks if API key is valid
func (rd *RealDebrid) IsAuthenticated(ctx context.Context) bool {
	url := fmt.Sprintf("%s/user", realDebridBaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bearer "+rd.apiKey)

	resp, err := rd.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// Helper function to count cached hashes
func countCached(cached map[string]bool) int {
	count := 0
	for _, isCached := range cached {
		if isCached {
			count++
		}
	}
	return count
}
