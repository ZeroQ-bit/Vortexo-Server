package services

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

const (
	defaultPlexArtworkLimit      = 2000
	defaultPlexArtworkDelay      = 2 * time.Second
	defaultPlexArtworkStaleAfter = 30 * 24 * time.Hour
)

type PlexArtworkSyncOptions struct {
	Limit      int
	Delay      time.Duration
	StaleAfter time.Duration
	Progress   func(processed, total int, message string)
}

type PlexArtworkService struct {
	store         *database.PlexArtworkCacheStore
	client        *http.Client
	mu            sync.Mutex
	lastRequestAt time.Time
}

func NewPlexArtworkService(store *database.PlexArtworkCacheStore) *PlexArtworkService {
	return &PlexArtworkService{
		store: store,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (s *PlexArtworkService) SyncLibrary(ctx context.Context, opts PlexArtworkSyncOptions) (*models.PlexArtworkSyncStats, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("plex artwork service is not initialized")
	}

	if opts.Limit <= 0 {
		opts.Limit = defaultPlexArtworkLimit
	}
	if opts.Delay <= 0 {
		opts.Delay = defaultPlexArtworkDelay
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultPlexArtworkStaleAfter
	}

	started := time.Now().UTC()
	stats := &models.PlexArtworkSyncStats{
		Limit:     opts.Limit,
		StartedAt: started,
	}

	staleBefore := time.Now().Add(-opts.StaleAfter)
	items, err := s.store.ListLibrarySeedItems(ctx, opts.Limit, staleBefore)
	if err != nil {
		stats.CompletedAt = time.Now().UTC()
		return stats, err
	}
	stats.Attempted = len(items)

	log.Printf("[PlexArtwork] Starting sync: selected=%d limit=%d delay=%s staleAfter=%s", len(items), opts.Limit, opts.Delay, opts.StaleAfter)
	if opts.Progress != nil {
		opts.Progress(0, len(items), "Starting Plex artwork sync")
	}

	for index, item := range items {
		if err := ctx.Err(); err != nil {
			stats.CompletedAt = time.Now().UTC()
			return stats, err
		}

		label := fmt.Sprintf("%s:%d %s", item.MediaType, item.TMDBID, item.Title)
		if item.MediaType == "" || item.TMDBID <= 0 || strings.TrimSpace(item.Title) == "" {
			stats.Skipped++
			log.Printf("[PlexArtwork] skip %d/%d invalid item", index+1, len(items))
			continue
		}

		if opts.Progress != nil {
			opts.Progress(index+1, len(items), fmt.Sprintf("Fetching %s", label))
		}

		entry, err := s.scrapeItem(ctx, item, opts.Delay)
		if err != nil {
			var stopErr *plexArtworkStopError
			if errors.As(err, &stopErr) {
				stats.Stopped = stopErr.Error()
				stats.CompletedAt = time.Now().UTC()
				log.Printf("[PlexArtwork] stop %d/%d %s: %v", index+1, len(items), label, err)
				return stats, err
			}

			stats.Failed++
			_ = s.store.Upsert(ctx, &models.PlexArtworkEntry{
				Version:   1,
				MediaType: item.MediaType,
				TMDBID:    item.TMDBID,
				Title:     item.Title,
				Year:      item.Year,
				UpdatedAt: time.Now().UTC(),
				Artwork:   models.PlexArtwork{},
			}, "error", err.Error())
			log.Printf("[PlexArtwork] error %d/%d %s: %v", index+1, len(items), label, err)
			continue
		}

		if entry == nil || entry.Artwork.IsEmpty() {
			stats.Miss++
			_ = s.store.Upsert(ctx, &models.PlexArtworkEntry{
				Version:   1,
				MediaType: item.MediaType,
				TMDBID:    item.TMDBID,
				Title:     item.Title,
				Year:      item.Year,
				UpdatedAt: time.Now().UTC(),
				Artwork:   models.PlexArtwork{},
			}, "miss", "no public Plex artwork found")
			log.Printf("[PlexArtwork] miss %d/%d %s", index+1, len(items), label)
			continue
		}

		if err := s.store.Upsert(ctx, entry, "ok", ""); err != nil {
			stats.Failed++
			log.Printf("[PlexArtwork] cache error %d/%d %s: %v", index+1, len(items), label, err)
			continue
		}

		stats.OK++
		log.Printf("[PlexArtwork] ok %d/%d %s source=%s artwork=%s", index+1, len(items), label, entry.SourcePage, artworkSummary(entry.Artwork))
	}

	stats.CompletedAt = time.Now().UTC()
	log.Printf("[PlexArtwork] summary ok=%d miss=%d skipped=%d failed=%d stopped=%q", stats.OK, stats.Miss, stats.Skipped, stats.Failed, stats.Stopped)
	return stats, nil
}

func (s *PlexArtworkService) RefreshOne(ctx context.Context, item models.PlexArtworkSeedItem, delay time.Duration) (*models.PlexArtworkEntry, error) {
	if delay <= 0 {
		delay = defaultPlexArtworkDelay
	}

	entry, err := s.scrapeItem(ctx, item, delay)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		entry = &models.PlexArtworkEntry{
			Version:   1,
			MediaType: item.MediaType,
			TMDBID:    item.TMDBID,
			Title:     item.Title,
			Year:      item.Year,
			UpdatedAt: time.Now().UTC(),
			Artwork:   models.PlexArtwork{},
		}
		if err := s.store.Upsert(ctx, entry, "miss", "no public Plex artwork found"); err != nil {
			return nil, err
		}
		return entry, nil
	}

	if err := s.store.Upsert(ctx, entry, "ok", ""); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *PlexArtworkService) scrapeItem(ctx context.Context, item models.PlexArtworkSeedItem, delay time.Duration) (*models.PlexArtworkEntry, error) {
	urls := candidatePlexArtworkURLs(item)
	for _, pageURL := range urls {
		body, err := s.fetchText(ctx, pageURL, delay)
		if err != nil {
			var stopErr *plexArtworkStopError
			if errors.As(err, &stopErr) {
				return nil, err
			}
			log.Printf("[PlexArtwork] fetch-miss %s:%d %s %v", item.MediaType, item.TMDBID, pageURL, err)
			continue
		}

		artwork := structuredPlexArtwork(body)
		if artwork.IsEmpty() {
			continue
		}

		return &models.PlexArtworkEntry{
			Version:    1,
			MediaType:  normalizePlexArtworkMediaType(item.MediaType),
			TMDBID:     item.TMDBID,
			Title:      item.Title,
			Year:       item.Year,
			SourcePage: pageURL,
			UpdatedAt:  time.Now().UTC(),
			Artwork:    artwork,
		}, nil
	}

	return nil, nil
}

func (s *PlexArtworkService) fetchText(ctx context.Context, pageURL string, delay time.Duration) (string, error) {
	if err := s.waitForSlot(ctx, delay); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 VortexoArtworkCache")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retryAfter != "" {
			return "", &plexArtworkStopError{message: fmt.Sprintf("Plex returned HTTP %d retryAfter=%s url=%s", resp.StatusCode, retryAfter, pageURL)}
		}
		return "", &plexArtworkStopError{message: fmt.Sprintf("Plex returned HTTP %d url=%s", resp.StatusCode, pageURL)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, pageURL)
	}

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	body := string(bytes)
	if isPlexCloudflareChallenge(body) {
		return "", &plexArtworkStopError{message: fmt.Sprintf("Cloudflare challenge detected url=%s", pageURL)}
	}
	return body, nil
}

func (s *PlexArtworkService) waitForSlot(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	s.mu.Lock()
	wait := time.Duration(0)
	if !s.lastRequestAt.IsZero() {
		elapsed := time.Since(s.lastRequestAt)
		if elapsed < delay {
			wait = delay - elapsed
		}
	}
	s.mu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	s.mu.Lock()
	s.lastRequestAt = time.Now()
	s.mu.Unlock()
	return nil
}

func candidatePlexArtworkURLs(item models.PlexArtworkSeedItem) []string {
	kind := "movie"
	if normalizePlexArtworkMediaType(item.MediaType) == "tv" {
		kind = "show"
	}

	slug := slugifyPlexArtworkTitle(item.Title)
	var paths []string
	if slug != "" {
		if item.Year > 0 {
			paths = append(paths, fmt.Sprintf("/en-GB/%s/%s-%d", kind, slug, item.Year))
		}
		paths = append(paths, fmt.Sprintf("/en-GB/%s/%s", kind, slug))
	}

	seen := map[string]bool{}
	urls := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.ReplaceAll(path, `\u002F`, "/")
		path = strings.ReplaceAll(path, `\/`, "/")
		path = strings.ReplaceAll(path, "&amp;", "&")
		if !strings.HasPrefix(path, "/") || seen[path] {
			continue
		}
		seen[path] = true
		urls = append(urls, "https://watch.plex.tv"+path)
	}
	return urls
}

func structuredPlexArtwork(rawHTML string) models.PlexArtwork {
	normalized := normalizePlexArtworkHTML(rawHTML)
	artwork := models.PlexArtwork{}

	if landscape := structuredPlexLandscapeTileURL(normalized); landscape != "" {
		artwork.CoverArt = append(artwork.CoverArt, landscape)
		artwork.Landscape = append(artwork.Landscape, landscape)
	}

	if background := structuredPlexImageURL(normalized, "background"); background != "" {
		artwork.Background = append(artwork.Background, background)
	}
	if landscape := structuredPlexImageURL(normalized, "backgroundLandscape"); landscape != "" {
		artwork.Background = append(artwork.Background, landscape)
	}
	if logo := structuredPlexClearLogoURL(normalized); logo != "" {
		artwork.ClearLogo = append(artwork.ClearLogo, logo)
	}
	if preload := preloadedPlexImageURL(normalized); preload != "" {
		artwork.Background = append(artwork.Background, preload)
	}
	if social := metaPlexImageURL(normalized, "property", "og:image"); social != "" {
		artwork.Thumbnail = append(artwork.Thumbnail, social)
	} else if social := metaPlexImageURL(normalized, "name", "twitter:image"); social != "" {
		artwork.Thumbnail = append(artwork.Thumbnail, social)
	}

	return dedupePlexArtwork(artwork)
}

func structuredPlexImageURL(rawHTML, field string) string {
	pattern := fmt.Sprintf(`"%s":\{"image":\{"url":"([^"]+)"`, regexp.QuoteMeta(field))
	matches := regexp.MustCompile(pattern).FindStringSubmatch(rawHTML)
	if len(matches) < 2 {
		return ""
	}
	decoded := decodePlexArtworkURL(matches[1])
	if isValidPlexArtworkURL(decoded) {
		return decoded
	}
	return ""
}

func structuredPlexLandscapeTileURL(rawHTML string) string {
	re := regexp.MustCompile(`"orientation":"landscape","size":"m","id":"[^"]*/extras/[^"]+","image":\{"url":"([^"]+)"`)
	for _, matches := range re.FindAllStringSubmatch(rawHTML, -1) {
		if len(matches) < 2 {
			continue
		}
		decoded := decodePlexArtworkURL(matches[1])
		if strings.Contains(decoded, "provider-static.plex.tv/discover/logos/p/") {
			continue
		}
		if isValidPlexArtworkURL(decoded) {
			return decoded
		}
	}
	return ""
}

func structuredPlexClearLogoURL(rawHTML string) string {
	re := regexp.MustCompile(`"clearLogo":\{"url":"([^"]+)"`)
	for _, matches := range re.FindAllStringSubmatch(rawHTML, -1) {
		if len(matches) < 2 {
			continue
		}
		decoded := decodePlexArtworkURL(matches[1])
		if strings.Contains(decoded, "provider-static.plex.tv/discover/logos/p/") {
			continue
		}
		if isValidPlexArtworkURL(decoded) {
			return decoded
		}
	}
	return ""
}

func preloadedPlexImageURL(rawHTML string) string {
	for _, tag := range htmlTags("link", rawHTML) {
		if !strings.EqualFold(htmlAttribute("as", tag), "image") {
			continue
		}
		if largest := largestPlexImageURL(htmlAttribute("imageSrcSet", tag)); largest != "" {
			return largest
		}
	}
	return ""
}

func largestPlexImageURL(srcset string) string {
	if strings.TrimSpace(srcset) == "" {
		return ""
	}

	var candidates []string
	for _, raw := range strings.Split(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) == 0 {
			continue
		}
		candidate := html.UnescapeString(fields[0])
		if candidate != "" {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

func metaPlexImageURL(rawHTML, keyAttribute, keyValue string) string {
	for _, tag := range htmlTags("meta", rawHTML) {
		if !strings.EqualFold(htmlAttribute(keyAttribute, tag), keyValue) {
			continue
		}
		if content := htmlAttribute("content", tag); content != "" {
			return html.UnescapeString(content)
		}
	}
	return ""
}

func htmlTags(name, rawHTML string) []string {
	re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(name) + `\b[^>]*>`)
	return re.FindAllString(rawHTML, -1)
}

func htmlAttribute(name, tag string) string {
	re := regexp.MustCompile(`(?i)\s` + regexp.QuoteMeta(name) + `="([^"]*)"`)
	matches := re.FindStringSubmatch(tag)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func normalizePlexArtworkHTML(value string) string {
	value = strings.ReplaceAll(value, `\u002F`, "/")
	value = strings.ReplaceAll(value, `\/`, "/")
	value = strings.ReplaceAll(value, "&amp;", "&")
	return value
}

func decodePlexArtworkURL(value string) string {
	decoded := html.UnescapeString(value)
	decoded = strings.ReplaceAll(decoded, `\u002F`, "/")
	decoded = strings.ReplaceAll(decoded, `\/`, "/")
	return decoded
}

func isValidPlexArtworkURL(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "metadata-static.plex.tv" ||
		strings.HasSuffix(host, ".metadata-static.plex.tv") ||
		host == "provider-static.plex.tv" ||
		strings.HasSuffix(host, ".provider-static.plex.tv") ||
		host == "images.plex.tv" ||
		strings.HasSuffix(host, ".images.plex.tv")
}

func dedupePlexArtwork(artwork models.PlexArtwork) models.PlexArtwork {
	artwork.CoverArt = dedupeStrings(artwork.CoverArt)
	artwork.Landscape = dedupeStrings(artwork.Landscape)
	artwork.Background = dedupeStrings(artwork.Background)
	artwork.ClearLogo = dedupeStrings(artwork.ClearLogo)
	artwork.Thumbnail = dedupeStrings(artwork.Thumbnail)
	return artwork
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func isPlexCloudflareChallenge(body string) bool {
	return strings.Contains(body, "cf_chl_") ||
		strings.Contains(body, "cf-browser-verification") ||
		strings.Contains(body, "challenge-platform") ||
		strings.Contains(body, "<title>Just a moment...</title>")
}

func slugifyPlexArtworkTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	var builder strings.Builder
	lastDash := false

	for _, r := range title {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '&' {
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteString("and")
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func normalizePlexArtworkMediaType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "show" || normalized == "series" {
		return "tv"
	}
	return normalized
}

func artworkSummary(artwork models.PlexArtwork) string {
	counts := map[string]int{
		"coverArt":   len(artwork.CoverArt),
		"landscape":  len(artwork.Landscape),
		"background": len(artwork.Background),
		"clearLogo":  len(artwork.ClearLogo),
		"thumbnail":  len(artwork.Thumbnail),
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

type plexArtworkStopError struct {
	message string
}

func (e *plexArtworkStopError) Error() string {
	return e.message
}
