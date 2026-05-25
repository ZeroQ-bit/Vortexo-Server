package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	rdBaseURL = "https://api.real-debrid.com/rest/1.0"
)

var ErrRealDebridDisabledEndpoint = errors.New("real-debrid endpoint disabled")

type RealDebridClient struct {
	apiKey     string
	httpClient *http.Client
}

type RealDebridAPIError struct {
	StatusCode int
	ErrorName  string
	ErrorCode  int
	Body       string
}

func (e *RealDebridAPIError) Error() string {
	if e == nil {
		return ""
	}
	if e.ErrorName != "" || e.ErrorCode != 0 {
		return fmt.Sprintf("Real-Debrid API returned status %d: {error=%s error_code=%d}", e.StatusCode, e.ErrorName, e.ErrorCode)
	}
	return fmt.Sprintf("Real-Debrid API returned status %d: %s", e.StatusCode, e.Body)
}

func (e *RealDebridAPIError) Is(target error) bool {
	return target == ErrRealDebridDisabledEndpoint && (e.ErrorCode == 37 || e.ErrorName == "disabled_endpoint")
}

type rdTorrentInfo struct {
	ID       string          `json:"id"`
	Filename string          `json:"filename"`
	Hash     string          `json:"hash"`
	Bytes    int64           `json:"bytes"`
	Host     string          `json:"host"`
	Status   string          `json:"status"`
	Added    string          `json:"added"`
	Links    []string        `json:"links"`
	Files    []rdTorrentFile `json:"files"`
}

type rdTorrentFile struct {
	ID       int         `json:"id"`
	Path     string      `json:"path"`
	Bytes    int64       `json:"bytes"`
	Selected interface{} `json:"selected"`
}

func (f rdTorrentFile) isSelected() bool {
	switch value := f.Selected.(type) {
	case bool:
		return value
	case float64:
		return value != 0
	case int:
		return value != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

type rdTorrentListItem struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Hash     string `json:"hash"`
	Bytes    int64  `json:"bytes"`
	Status   string `json:"status"`
	Added    string `json:"added"`
}

type RealDebridTorrentInfo = rdTorrentInfo
type RealDebridTorrentFile = rdTorrentFile
type RealDebridTorrentListItem = rdTorrentListItem

type rdInstantAvailability struct {
	Hash string                 `json:"-"`
	Data map[string]interface{} `json:"-"`
}

type rdUnrestrictLink struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Filesize int64  `json:"filesize"`
	Link     string `json:"link"`
	Host     string `json:"host"`
	Download string `json:"download"`
}

func NewRealDebridClient(apiKey string) *RealDebridClient {
	return &RealDebridClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CheckInstantAvailability checks if torrents are instantly available
func (c *RealDebridClient) CheckInstantAvailability(ctx context.Context, infoHashes []string) (map[string]bool, error) {
	if len(infoHashes) == 0 {
		return make(map[string]bool), nil
	}

	// Real-Debrid allows checking up to 100 hashes at once
	const batchSize = 100
	availability := make(map[string]bool)

	for i := 0; i < len(infoHashes); i += batchSize {
		end := i + batchSize
		if end > len(infoHashes) {
			end = len(infoHashes)
		}
		batch := infoHashes[i:end]

		endpoint := fmt.Sprintf("%s/torrents/instantAvailability/%s", rdBaseURL, strings.Join(batch, "/"))

		data, err := c.makeRequest(ctx, "GET", endpoint, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to check availability: %w", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal availability: %w", err)
		}

		// Parse availability response
		for _, hash := range batch {
			hashData, exists := result[strings.ToLower(hash)]
			if exists {
				// If the hash exists in response and has data, it's available
				if hashMap, ok := hashData.(map[string]interface{}); ok && len(hashMap) > 0 {
					availability[hash] = true
				} else {
					availability[hash] = false
				}
			} else {
				availability[hash] = false
			}
		}
	}

	return availability, nil
}

// AddMagnet adds a magnet link to Real-Debrid
func (c *RealDebridClient) AddMagnet(ctx context.Context, magnetLink string) (string, error) {
	endpoint := fmt.Sprintf("%s/torrents/addMagnet", rdBaseURL)

	params := url.Values{}
	params.Set("magnet", magnetLink)

	data, err := c.makeRequest(ctx, "POST", endpoint, params, nil)
	if err != nil {
		return "", fmt.Errorf("failed to add magnet: %w", err)
	}

	var result struct {
		ID  string `json:"id"`
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal add magnet response: %w", err)
	}

	return result.ID, nil
}

// SelectFiles selects all files from a torrent
func (c *RealDebridClient) SelectFiles(ctx context.Context, torrentID string, fileIDs []int) error {
	endpoint := fmt.Sprintf("%s/torrents/selectFiles/%s", rdBaseURL, torrentID)

	filesValue := "all"
	if len(fileIDs) > 0 {
		// Convert file IDs to comma-separated string
		fileIDStrs := make([]string, len(fileIDs))
		for i, id := range fileIDs {
			fileIDStrs[i] = fmt.Sprintf("%d", id)
		}
		filesValue = strings.Join(fileIDStrs, ",")
	}

	params := url.Values{}
	params.Set("files", filesValue)

	_, err := c.makeRequest(ctx, "POST", endpoint, params, nil)
	if err != nil {
		return fmt.Errorf("failed to select files: %w", err)
	}

	return nil
}

// GetTorrentInfo retrieves information about a torrent
func (c *RealDebridClient) GetTorrentInfo(ctx context.Context, torrentID string) (*rdTorrentInfo, error) {
	endpoint := fmt.Sprintf("%s/torrents/info/%s", rdBaseURL, torrentID)

	data, err := c.makeRequest(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get torrent info: %w", err)
	}

	var info rdTorrentInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal torrent info: %w", err)
	}

	return &info, nil
}

// ListTorrents retrieves a page of torrents currently present in the user's Real-Debrid account.
func (c *RealDebridClient) ListTorrents(ctx context.Context, page, limit int) ([]rdTorrentListItem, error) {
	return c.ListTorrentsFiltered(ctx, page, limit, "")
}

// ListTorrentsFiltered retrieves a filtered page of torrents currently present
// in the user's Real-Debrid account. Real-Debrid applies the filter against the
// torrent filename, which keeps large libraries searchable without walking every
// page for common playback lookups.
func (c *RealDebridClient) ListTorrentsFiltered(ctx context.Context, page, limit int, filter string) ([]rdTorrentListItem, error) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 5000 {
		limit = 5000
	}

	endpoint := fmt.Sprintf("%s/torrents", rdBaseURL)
	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("limit", strconv.Itoa(limit))
	if strings.TrimSpace(filter) != "" {
		params.Set("filter", strings.TrimSpace(filter))
	}

	data, err := c.makeRequest(ctx, "GET", endpoint, params, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list torrents: %w", err)
	}

	var torrents []rdTorrentListItem
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, fmt.Errorf("failed to unmarshal torrents list: %w", err)
	}

	return torrents, nil
}

func (c *RealDebridClient) ListAllTorrentIDs(ctx context.Context) (map[string]struct{}, error) {
	const pageSize = 5000

	ids := make(map[string]struct{})
	for page := 1; ; page++ {
		torrents, err := c.ListTorrents(ctx, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, torrent := range torrents {
			id := strings.TrimSpace(torrent.ID)
			if id != "" {
				ids[id] = struct{}{}
			}
		}
		if len(torrents) < pageSize {
			break
		}
	}

	return ids, nil
}

// UnrestrictLink converts a Real-Debrid link to a direct download link
func (c *RealDebridClient) UnrestrictLink(ctx context.Context, link string) (*rdUnrestrictLink, error) {
	endpoint := fmt.Sprintf("%s/unrestrict/link", rdBaseURL)

	params := url.Values{}
	params.Set("link", link)

	data, err := c.makeRequest(ctx, "POST", endpoint, params, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to unrestrict link: %w", err)
	}

	var result rdUnrestrictLink
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal unrestrict response: %w", err)
	}

	return &result, nil
}

// GetStreamURL gets a direct streaming URL for a torrent
func (c *RealDebridClient) GetStreamURL(ctx context.Context, infoHash string) (string, error) {
	return c.GetStreamURLForFile(ctx, infoHash, 0, "")
}

func (c *RealDebridClient) GetStreamURLForTorrentID(ctx context.Context, torrentID string, fileIndex int, preferredName string) (string, error) {
	torrentID = strings.TrimSpace(torrentID)
	if torrentID == "" {
		return "", fmt.Errorf("missing Real-Debrid torrent ID")
	}
	return c.resolveTorrentToStreamURL(ctx, torrentID, fileIndex, preferredName, nil)
}

// GetStreamURLForFile gets a direct streaming URL for the requested torrent
// file. Stremio-style sources can include a file index; DMM sources usually do
// not, so the preferred name and largest-video fallback keep selection stable.
func (c *RealDebridClient) GetStreamURLForFile(ctx context.Context, infoHash string, fileIndex int, preferredName string) (string, error) {
	infoHash = normalizeRealDebridHash(infoHash)
	if !isValidRealDebridHash(infoHash) {
		return "", fmt.Errorf("invalid torrent hash %q", truncateString(infoHash, 12))
	}

	lookupCtx, cancelLookup := context.WithTimeout(ctx, 20*time.Second)
	info, torrentID, err := c.findTorrentInfoByHash(lookupCtx, infoHash)
	cancelLookup()
	createdTorrent := false
	verifiedInstantlyAvailable := false
	if err != nil {
		fmt.Printf("[RD-DEBUG] Existing torrent lookup skipped/failed for hash %s: %v\n", truncateString(infoHash, 12), err)
	}
	if info != nil {
		fmt.Printf("[RD-DEBUG] Using existing torrent %s for hash %s\n", torrentID, truncateString(infoHash, 12))
	} else {
		availabilityCtx, cancelAvailability := context.WithTimeout(ctx, 10*time.Second)
		availability, availabilityErr := c.CheckInstantAvailability(availabilityCtx, []string{infoHash})
		cancelAvailability()
		if availabilityErr != nil {
			if isRealDebridDisabledEndpointError(availabilityErr) {
				fmt.Printf("[RD-DEBUG] Instant availability endpoint disabled; adding magnet without precheck for hash %s\n", truncateString(infoHash, 12))
			} else {
				return "", fmt.Errorf("failed to verify Real-Debrid cache before adding magnet: %w", availabilityErr)
			}
		} else {
			if !availability[infoHash] {
				return "", fmt.Errorf("torrent not cached on RD")
			}
			verifiedInstantlyAvailable = true
		}

		// Build magnet link
		magnetLink := fmt.Sprintf("magnet:?xt=urn:btih:%s", infoHash)

		// Add magnet to Real-Debrid
		torrentID, err = c.AddMagnet(ctx, magnetLink)
		if err != nil {
			return "", fmt.Errorf("failed to add magnet: %w", err)
		}
		createdTorrent = true
		fmt.Printf("[RD-DEBUG] Added magnet, torrent ID: %s\n", torrentID)
	}

	cleanupCreatedTorrent := func(info *rdTorrentInfo) {
		if !createdTorrent {
			return
		}
		if verifiedInstantlyAvailable && info != nil && !isTerminalRDTorrentStatus(info.Status) {
			fmt.Printf("[RD-DEBUG] Keeping newly added cached torrent %s in account with status %s\n", torrentID, info.Status)
			return
		}
		_ = c.DeleteTorrent(ctx, torrentID)
	}

	return c.resolveTorrentToStreamURL(ctx, torrentID, fileIndex, preferredName, cleanupCreatedTorrent)
}

func (c *RealDebridClient) resolveTorrentToStreamURL(
	ctx context.Context,
	torrentID string,
	fileIndex int,
	preferredName string,
	cleanup func(*rdTorrentInfo),
) (string, error) {
	if cleanup == nil {
		cleanup = func(*rdTorrentInfo) {}
	}

	info, err := c.waitForTorrentSelectionInfo(ctx, torrentID)
	if err != nil {
		cleanup(info)
		return "", fmt.Errorf("failed to get torrent info: %w", err)
	}
	fmt.Printf("[RD-DEBUG] Torrent status: %s, Links: %d, Bytes: %d\n", info.Status, len(info.Links), info.Bytes)
	targetFileID := chooseRDTorrentFileID(info, fileIndex, preferredName)

	// Check if torrent is ready - should be "downloaded" for cached torrents
	if info.Status != "downloaded" && info.Status != "waiting_files_selection" {
		cleanup(info)
		return "", fmt.Errorf("torrent not cached (status: %s)", info.Status)
	}

	// Select all files
	if info.Status == "waiting_files_selection" {
		fileIDs := []int(nil)
		if targetFileID > 0 {
			fileIDs = []int{targetFileID}
		}
		fmt.Printf("[RD-DEBUG] Selecting files for torrent %s: %v\n", torrentID, fileIDs)
		if err := c.SelectFiles(ctx, torrentID, fileIDs); err != nil {
			cleanup(info)
			return "", fmt.Errorf("failed to select files: %w", err)
		}

		info, err = c.waitForDownloadLinks(ctx, torrentID)
		if err != nil {
			cleanup(info)
			return "", fmt.Errorf("failed to wait for selected files: %w", err)
		}
		fmt.Printf("[RD-DEBUG] After file selection - Status: %s, Links: %d\n", info.Status, len(info.Links))
		if targetFileID <= 0 {
			targetFileID = chooseRDTorrentFileID(info, fileIndex, preferredName)
		}

		// If still not downloaded after selection, it's not cached - delete it
		if info.Status != "downloaded" {
			fmt.Printf("[RD-DEBUG] Torrent not instantly cached (status: %s)\n", info.Status)
			cleanup(info)
			return "", fmt.Errorf("torrent not cached on RD (status: %s)", info.Status)
		}
	}
	if info.Status == "downloaded" && len(info.Links) == 0 {
		info, err = c.waitForDownloadLinks(ctx, torrentID)
		if err != nil {
			cleanup(info)
			return "", fmt.Errorf("failed to wait for download links: %w", err)
		}
	}

	// Get the first link
	if len(info.Links) == 0 {
		cleanup(info)
		return "", fmt.Errorf("no download links available (status: %s)", info.Status)
	}

	link, err := chooseRDDownloadLink(info, targetFileID, fileIndex, preferredName)
	if err != nil {
		cleanup(info)
		return "", err
	}

	fmt.Printf("[RD-DEBUG] Unrestricting link: %s\n", link)
	// Unrestrict the link to get direct download URL
	unrestricted, err := c.UnrestrictLink(ctx, link)
	if err != nil {
		cleanup(info)
		return "", fmt.Errorf("failed to unrestrict link: %w", err)
	}

	// Return the direct download URL for streaming
	// Use the 'download' field which is the actual streaming URL
	if unrestricted.Download != "" {
		fmt.Printf("[RD-DEBUG] Got download URL: %s\n", unrestricted.Download)
		return unrestricted.Download, nil
	}

	fmt.Printf("[RD-DEBUG] Got link URL: %s\n", unrestricted.Link)
	return unrestricted.Link, nil
}

func (c *RealDebridClient) waitForTorrentSelectionInfo(ctx context.Context, torrentID string) (*rdTorrentInfo, error) {
	var lastInfo *rdTorrentInfo
	var lastErr error

	for attempt := 0; attempt < 12; attempt++ {
		info, err := c.GetTorrentInfo(ctx, torrentID)
		if err == nil {
			lastInfo = info
			status := normalizeRDTorrentStatus(info.Status)
			if status == "downloaded" || status == "waiting_files_selection" || len(info.Files) > 0 {
				return info, nil
			}
			if isTerminalRDTorrentStatus(status) {
				return nil, fmt.Errorf("torrent entered unselectable status %q", info.Status)
			}
		} else {
			lastErr = err
		}

		if err := sleepWithContext(ctx, time.Duration(attempt+1)*500*time.Millisecond); err != nil {
			return nil, err
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

func (c *RealDebridClient) waitForDownloadLinks(ctx context.Context, torrentID string) (*rdTorrentInfo, error) {
	var lastInfo *rdTorrentInfo
	var lastErr error

	for attempt := 0; attempt < 30; attempt++ {
		info, err := c.GetTorrentInfo(ctx, torrentID)
		if err == nil {
			lastInfo = info
			status := normalizeRDTorrentStatus(info.Status)
			if status == "downloaded" && len(info.Links) > 0 {
				return info, nil
			}
			if isTerminalRDTorrentStatus(status) {
				return nil, fmt.Errorf("torrent entered unselectable status %q", info.Status)
			}
		} else {
			lastErr = err
		}

		if err := sleepWithContext(ctx, time.Second); err != nil {
			return nil, err
		}
	}

	if lastInfo != nil {
		return lastInfo, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("download links unavailable for %s", torrentID)
}

func normalizeRDTorrentStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func isTerminalRDTorrentStatus(status string) bool {
	switch normalizeRDTorrentStatus(status) {
	case "error", "virus", "dead", "magnet_error":
		return true
	default:
		return false
	}
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *RealDebridClient) findTorrentInfoByHash(ctx context.Context, infoHash string) (*rdTorrentInfo, string, error) {
	infoHash = strings.ToLower(normalizeRealDebridHash(infoHash))
	if infoHash == "" {
		return nil, "", nil
	}

	const pageSize = 5000
	const maxPlaybackLookupPages = 8
	for page := 1; page <= maxPlaybackLookupPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		torrents, err := c.ListTorrents(ctx, page, pageSize)
		if err != nil {
			return nil, "", err
		}
		for _, torrent := range torrents {
			if strings.TrimSpace(torrent.ID) == "" {
				continue
			}
			if strings.EqualFold(normalizeRealDebridHash(torrent.Hash), infoHash) {
				info, err := c.GetTorrentInfo(ctx, torrent.ID)
				if err != nil {
					return nil, "", err
				}
				return info, torrent.ID, nil
			}
		}
		if len(torrents) < pageSize {
			break
		}
	}

	return nil, "", nil
}

func chooseRDTorrentFileID(info *rdTorrentInfo, fileIndex int, preferredName string) int {
	if info == nil || len(info.Files) == 0 {
		return 0
	}

	if preferred := normalizeRDFileMatch(preferredName); preferred != "" {
		bestID := 0
		var bestBytes int64
		for _, file := range info.Files {
			name := normalizeRDFileMatch(file.Path)
			if name == "" || !rdFileLooksPlayable(file.Path) {
				continue
			}
			if strings.Contains(name, preferred) || strings.Contains(preferred, name) {
				if file.Bytes >= bestBytes {
					bestID = file.ID
					bestBytes = file.Bytes
				}
			}
		}
		if bestID > 0 {
			return bestID
		}
	}

	if fileIndex > 0 {
		for _, file := range info.Files {
			if file.ID == fileIndex || file.ID == fileIndex+1 {
				return file.ID
			}
		}
		if fileIndex < len(info.Files) {
			return info.Files[fileIndex].ID
		}
		if fileIndex-1 >= 0 && fileIndex-1 < len(info.Files) {
			return info.Files[fileIndex-1].ID
		}
	}

	return largestRDVideoFileID(info.Files)
}

func chooseRDDownloadLink(info *rdTorrentInfo, targetFileID, fileIndex int, preferredName string) (string, error) {
	if info == nil || len(info.Links) == 0 {
		return "", fmt.Errorf("no download links available")
	}
	if len(info.Links) == 1 {
		return info.Links[0], nil
	}

	if targetFileID <= 0 {
		targetFileID = chooseRDTorrentFileID(info, fileIndex, preferredName)
	}

	selectedFiles := make([]rdTorrentFile, 0, len(info.Files))
	for _, file := range info.Files {
		if file.isSelected() {
			selectedFiles = append(selectedFiles, file)
		}
	}
	if len(selectedFiles) == 0 && len(info.Files) == len(info.Links) {
		selectedFiles = info.Files
	}

	if targetFileID > 0 && len(selectedFiles) == len(info.Links) {
		for index, file := range selectedFiles {
			if file.ID == targetFileID && index < len(info.Links) {
				return info.Links[index], nil
			}
		}
	}

	if fileIndex >= 0 && fileIndex < len(info.Links) {
		return info.Links[fileIndex], nil
	}
	if fileIndex > 0 && fileIndex-1 < len(info.Links) {
		return info.Links[fileIndex-1], nil
	}

	return info.Links[0], nil
}

func largestRDVideoFileID(files []rdTorrentFile) int {
	bestID := 0
	var bestBytes int64
	for _, file := range files {
		if !rdFileLooksPlayable(file.Path) {
			continue
		}
		if file.Bytes >= bestBytes {
			bestID = file.ID
			bestBytes = file.Bytes
		}
	}
	if bestID > 0 {
		return bestID
	}
	for _, file := range files {
		if file.Bytes >= bestBytes {
			bestID = file.ID
			bestBytes = file.Bytes
		}
	}
	return bestID
}

func rdFileLooksPlayable(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".mov", ".avi", ".ts", ".webm"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func normalizeRDFileMatch(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if slash := strings.LastIndex(value, "/"); slash >= 0 {
		value = value[slash+1:]
	}
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".mov", ".avi", ".ts", ".webm"} {
		value = strings.TrimSuffix(value, ext)
	}
	replacer := strings.NewReplacer(".", " ", "_", " ", "-", " ", "'", "", "\"", "", "[", " ", "]", " ", "(", " ", ")", " ")
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

// DeleteTorrent removes a torrent from Real-Debrid
func (c *RealDebridClient) DeleteTorrent(ctx context.Context, torrentID string) error {
	endpoint := fmt.Sprintf("%s/torrents/delete/%s", rdBaseURL, torrentID)

	_, err := c.makeRequest(ctx, "DELETE", endpoint, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to delete torrent: %w", err)
	}

	return nil
}

// makeRequest performs an HTTP request to Real-Debrid API with retry logic for rate limiting
func (c *RealDebridClient) makeRequest(ctx context.Context, method, endpoint string, params url.Values, body io.Reader) ([]byte, error) {
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Exponential backoff for retries: 2s, 4s, 8s
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			time.Sleep(backoff)
		}

		reqURL := endpoint
		if method == "GET" && params != nil {
			reqURL = fmt.Sprintf("%s?%s", endpoint, params.Encode())
		}

		var reqBody io.Reader
		if method == "POST" && params != nil {
			reqBody = strings.NewReader(params.Encode())
		} else if body != nil {
			reqBody = body
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
		if method == "POST" && params != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		// Retry on 429 (Too Many Requests)
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("rate limited by Real-Debrid")
			if attempt < maxRetries-1 {
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			apiErr := RealDebridAPIError{
				StatusCode: resp.StatusCode,
				Body:       strings.TrimSpace(string(data)),
			}
			var payload struct {
				Error     string `json:"error"`
				ErrorCode int    `json:"error_code"`
			}
			if err := json.Unmarshal(data, &payload); err == nil {
				apiErr.ErrorName = payload.Error
				apiErr.ErrorCode = payload.ErrorCode
			}
			return nil, &apiErr
		}

		return data, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("max retries exceeded")
}

func isRealDebridDisabledEndpointError(err error) bool {
	return errors.Is(err, ErrRealDebridDisabledEndpoint)
}

// TestConnection tests the Real-Debrid API connection
func (c *RealDebridClient) TestConnection(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/user", rdBaseURL)

	_, err := c.makeRequest(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to Real-Debrid: %w", err)
	}

	return nil
}

func normalizeRealDebridHash(hash string) string {
	hash = strings.TrimSpace(hash)
	hash = strings.TrimPrefix(strings.ToLower(hash), "urn:btih:")
	hash = strings.TrimPrefix(hash, "btih:")
	return hash
}

func isValidRealDebridHash(hash string) bool {
	hash = normalizeRealDebridHash(hash)
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

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
