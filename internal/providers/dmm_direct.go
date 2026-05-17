package providers

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/bits"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DMMDirectProvider queries DMM API for pre-scraped torrents
type DMMDirectProvider struct {
	DMMURL           string // DMM instance URL (e.g., http://dmm:8080)
	RealDebridAPIKey string
	Client           *http.Client
	Cache            map[string]*DMMCachedResponse
}

type DMMCachedResponse struct {
	Data      []TorrentioStream
	Timestamp time.Time
}

// DMM API response format
type DMMSearchResult struct {
	Title    string  `json:"title"`
	FileSize float64 `json:"fileSize"` // Size in MB
	Hash     string  `json:"hash"`
}

type DMMAPIResponse struct {
	Results      []DMMSearchResult `json:"results,omitempty"`
	ErrorMessage string            `json:"errorMessage,omitempty"`
}

// NewDMMDirectProvider creates a new DMM API provider
func NewDMMDirectProvider(rdAPIKey string) *DMMDirectProvider {
	return NewDMMDirectProviderWithURL(rdAPIKey, "")
}

// NewDMMDirectProviderWithURL creates a new DMM API provider with a custom base URL.
func NewDMMDirectProviderWithURL(rdAPIKey, dmmURL string) *DMMDirectProvider {
	if strings.TrimSpace(dmmURL) == "" {
		dmmURL = "https://debridmediamanager.com"
	}
	dmmURL = strings.TrimRight(strings.TrimSpace(dmmURL), "/")

	return &DMMDirectProvider{
		DMMURL:           dmmURL,
		RealDebridAPIKey: rdAPIKey,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
		Cache: make(map[string]*DMMCachedResponse),
	}
}

// GetMovieStreams queries DMM API for movie torrents
func (d *DMMDirectProvider) GetMovieStreams(imdbID string) ([]TorrentioStream, error) {
	cacheKey := fmt.Sprintf("movie_%s", imdbID)

	// Check cache first (10 minute cache)
	if cached, ok := d.Cache[cacheKey]; ok {
		if time.Since(cached.Timestamp) < 10*time.Minute {
			log.Printf("[DMM API] Cache hit for movie %s (%d streams)", imdbID, len(cached.Data))
			return cached.Data, nil
		}
	}

	log.Printf("[DMM API] Fetching streams for movie %s from %s", imdbID, d.DMMURL)

	// Generate DMM authentication token (timestamp + hash)
	tokenWithTimestamp, tokenHash := d.generateDMMToken()

	// Query DMM API
	url := fmt.Sprintf("%s/api/torrents/movie?imdbId=%s&dmmProblemKey=%s&solution=%s&onlyTrusted=false",
		d.DMMURL, imdbID, tokenWithTimestamp, tokenHash)

	results, err := d.queryDMMAPI(url, "movie")
	if err != nil {
		log.Printf("[DMM API] Error querying DMM for movie %s: %v", imdbID, err)
		return nil, err
	}

	log.Printf("[DMM API] Found %d streams for movie %s", len(results), imdbID)

	// Cache results
	d.Cache[cacheKey] = &DMMCachedResponse{
		Data:      results,
		Timestamp: time.Now(),
	}

	return results, nil
}

// GetSeriesStreams queries DMM API for series torrents
func (d *DMMDirectProvider) GetSeriesStreams(imdbID string, season, episode int) ([]TorrentioStream, error) {
	cacheKey := fmt.Sprintf("series_%s_s%de%d", imdbID, season, episode)

	// Check cache first (10 minute cache)
	if cached, ok := d.Cache[cacheKey]; ok {
		if time.Since(cached.Timestamp) < 10*time.Minute {
			log.Printf("[DMM API] Cache hit for series %s S%dE%d (%d streams)", imdbID, season, episode, len(cached.Data))
			return cached.Data, nil
		}
	}

	log.Printf("[DMM API] Fetching streams for series %s S%dE%d from %s", imdbID, season, episode, d.DMMURL)

	// Generate DMM authentication token
	tokenWithTimestamp, tokenHash := d.generateDMMToken()

	// Query DMM API for TV show season
	url := fmt.Sprintf("%s/api/torrents/tv?imdbId=%s&seasonNum=%d&dmmProblemKey=%s&solution=%s&onlyTrusted=false",
		d.DMMURL, imdbID, season, tokenWithTimestamp, tokenHash)

	results, err := d.queryDMMAPI(url, "series")
	if err != nil {
		log.Printf("[DMM API] Error querying DMM for series %s S%d: %v", imdbID, season, err)
		return nil, err
	}
	results = filterSeriesStreamsByEpisode(results, season, episode)

	log.Printf("[DMM API] Found %d streams for series %s S%dE%d", len(results), imdbID, season, episode)

	// Cache results
	d.Cache[cacheKey] = &DMMCachedResponse{
		Data:      results,
		Timestamp: time.Now(),
	}

	return results, nil
}

// queryDMMAPI queries DMM API and converts results to TorrentioStream format
func (d *DMMDirectProvider) queryDMMAPI(url, mediaType string) ([]TorrentioStream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// DMM returns 204 with status header when processing or requested
	if resp.StatusCode == 204 {
		status := resp.Header.Get("status")
		if status == "processing" {
			return nil, fmt.Errorf("DMM is still scraping this content, please try again in 1-2 minutes")
		}
		if status == "requested" {
			return nil, fmt.Errorf("content requested for scraping, please try again later")
		}
		return nil, fmt.Errorf("no results available (status: %s)", status)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DMM API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp DMMAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode DMM response: %v", err)
	}

	if apiResp.ErrorMessage != "" {
		return nil, fmt.Errorf("DMM error: %s", apiResp.ErrorMessage)
	}

	// Convert DMM results to TorrentioStream format
	streams := make([]TorrentioStream, 0, len(apiResp.Results))
	for _, result := range apiResp.Results {
		stream := TorrentioStream{
			Name:     result.Title,
			Title:    result.Title,
			InfoHash: result.Hash,
			URL:      fmt.Sprintf("magnet:?xt=urn:btih:%s", strings.TrimSpace(result.Hash)),
			Cached:   true, // DMM only returns cached torrents
			Source:   "DMM",
			Size:     int64(result.FileSize * 1024 * 1024), // Convert MB to bytes
		}

		// Extract quality from title
		stream.Quality = extractQualityFromTitle(result.Title)

		streams = append(streams, stream)
	}

	return streams, nil
}

// generateDMMToken generates authentication token for DMM API
func (d *DMMDirectProvider) generateDMMToken() (string, string) {
	token := generateRandomDMMToken()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	tokenWithTimestamp := fmt.Sprintf("%s-%s", token, timestamp)
	tokenTimestampHash := generateDMMHash(tokenWithTimestamp)
	tokenSaltHash := generateDMMHash("debridmediamanager.com%%fe7#td00rA3vHz%VmI-" + token)

	return tokenWithTimestamp, combineDMMHashes(tokenTimestampHash, tokenSaltHash)
}

func generateRandomDMMToken() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 16)
}

func generateDMMHash(str string) string {
	hash1 := uint32(0xdeadbeef) ^ uint32(len(str))
	hash2 := uint32(0x41c6ce57) ^ uint32(len(str))

	for _, char := range str {
		charCode := uint32(char)
		hash1 = bits.RotateLeft32((hash1^charCode)*2654435761, 5)
		hash2 = bits.RotateLeft32((hash2^charCode)*1597334677, 5)
	}

	hash1 = hash1 + hash2*1566083941
	hash2 = hash2 + hash1*2024237689

	return strconv.FormatUint(uint64(hash1^hash2), 16)
}

func combineDMMHashes(hash1, hash2 string) string {
	halfLength := len(hash1) / 2
	firstPart1 := hash1[:halfLength]
	secondPart1 := hash1[halfLength:]

	firstPart2 := hash2
	secondPart2 := ""
	if len(hash2) > halfLength {
		firstPart2 = hash2[:halfLength]
		secondPart2 = hash2[halfLength:]
	}

	var builder strings.Builder
	for i := 0; i < halfLength; i++ {
		builder.WriteByte(firstPart1[i])
		if i < len(firstPart2) {
			builder.WriteByte(firstPart2[i])
		}
	}
	builder.WriteString(reverseString(secondPart2))
	builder.WriteString(reverseString(secondPart1))

	return builder.String()
}

func reverseString(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// extractQualityFromTitle extracts quality info from title
func extractQualityFromTitle(title string) string {
	title = strings.ToUpper(title)

	qualities := []string{"2160P", "4K", "UHD", "1080P", "720P", "480P"}
	for _, q := range qualities {
		if strings.Contains(title, q) {
			return q
		}
	}

	return "Unknown"
}
