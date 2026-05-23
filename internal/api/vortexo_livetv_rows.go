package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/livetv"
)

type vortexoLiveTVRowsFeed struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	RefreshAfter time.Time          `json:"refresh_after"`
	Rows         []vortexoLiveTVRow `json:"rows"`
}

type vortexoLiveTVRow struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Reason       string                 `json:"reason,omitempty"`
	RefreshAfter time.Time              `json:"refresh_after"`
	Items        []vortexoLiveTVRowItem `json:"items"`
}

type vortexoLiveTVRowItem struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Logo           string                `json:"logo,omitempty"`
	StreamURL      string                `json:"stream_url,omitempty"`
	Category       string                `json:"category,omitempty"`
	Language       string                `json:"language,omitempty"`
	Country        string                `json:"country,omitempty"`
	Source         string                `json:"source,omitempty"`
	Active         bool                  `json:"active"`
	HasEPG         bool                  `json:"has_epg"`
	CurrentProgram *vortexoLiveTVProgram `json:"current_program,omitempty"`
	NextProgram    *vortexoLiveTVProgram `json:"next_program,omitempty"`
}

type vortexoLiveTVProgram struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Category    string    `json:"category,omitempty"`
	IsLive      bool      `json:"is_live"`
	Progress    float64   `json:"progress,omitempty"`
}

type vortexoLiveTVCandidate struct {
	channel *livetv.Channel
	item    vortexoLiveTVRowItem
	current *livetv.EPGProgram
	next    *livetv.EPGProgram
	hasEPG  bool
}

// VortexoLiveTVRows returns curated Live TV rows for private clients.
func (h *Handler) VortexoLiveTVRows(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	rowLimit := boundedHomeInt(r.URL.Query().Get("row_limit"), 8, 1, 12)
	itemLimit := boundedHomeInt(r.URL.Query().Get("item_limit"), 30, 6, 60)

	channels := h.vortexoFilteredLiveTVChannels()
	candidates := h.vortexoLiveTVCandidates(channels, now)
	favoriteIDs := orderedLiveTVIDList(r.URL.Query()["favorite_ids"], r.URL.Query().Get("favorite_ids"))
	recentIDs := orderedLiveTVIDList(r.URL.Query()["recent_ids"], r.URL.Query().Get("recent_ids"))

	rows := h.buildVortexoLiveTVRows(candidates, favoriteIDs, recentIDs, now, rowLimit, itemLimit)
	respondJSON(w, http.StatusOK, vortexoLiveTVRowsFeed{
		GeneratedAt:  now.UTC(),
		RefreshAfter: now.Add(15 * time.Minute).UTC(),
		Rows:         rows,
	})
}

func (h *Handler) vortexoFilteredLiveTVChannels() []*livetv.Channel {
	if h.channelManager == nil {
		return nil
	}

	channels := h.channelManager.GetAllChannels()
	if h.settingsManager == nil {
		return channels
	}

	settings := h.settingsManager.Get()
	if len(settings.LiveTVEnabledSources) > 0 {
		enabled := make(map[string]bool, len(settings.LiveTVEnabledSources))
		for _, source := range settings.LiveTVEnabledSources {
			enabled[source] = true
		}
		filtered := make([]*livetv.Channel, 0, len(channels))
		for _, channel := range channels {
			if enabled[channel.Source] {
				filtered = append(filtered, channel)
			}
		}
		channels = filtered
	}

	if len(settings.LiveTVEnabledCategories) > 0 {
		enabled := make(map[string]bool, len(settings.LiveTVEnabledCategories))
		for _, category := range settings.LiveTVEnabledCategories {
			enabled[category] = true
		}
		filtered := make([]*livetv.Channel, 0, len(channels))
		for _, channel := range channels {
			if enabled[channel.Category] {
				filtered = append(filtered, channel)
			}
		}
		channels = filtered
	}

	return channels
}

func (h *Handler) vortexoLiveTVCandidates(channels []*livetv.Channel, now time.Time) []vortexoLiveTVCandidate {
	candidates := make([]vortexoLiveTVCandidate, 0, len(channels))
	for _, channel := range channels {
		if channel == nil || !channel.Active {
			continue
		}

		var current *livetv.EPGProgram
		var next *livetv.EPGProgram
		hasEPG := false
		if h.epgManager != nil {
			programs := h.epgManager.GetEPGWithFallback(channel.ID, channel.Name, now)
			hasEPG = len(programs) > 0 || h.epgManager.HasEPG(channel.ID)
			for i := range programs {
				program := programs[i]
				if !program.StartTime.After(now) && program.EndTime.After(now) {
					current = &program
					continue
				}
				if program.StartTime.After(now) && (next == nil || program.StartTime.Before(next.StartTime)) {
					next = &program
				}
			}
		}

		candidates = append(candidates, vortexoLiveTVCandidate{
			channel: channel,
			item: vortexoLiveTVRowItem{
				ID:             channel.ID,
				Name:           channel.Name,
				Logo:           channel.Logo,
				StreamURL:      channel.StreamURL,
				Category:       channel.Category,
				Language:       channel.Language,
				Country:        channel.Country,
				Source:         channel.Source,
				Active:         channel.Active,
				HasEPG:         hasEPG,
				CurrentProgram: vortexoLiveTVProgramFromEPG(current, now),
				NextProgram:    vortexoLiveTVProgramFromEPG(next, now),
			},
			current: current,
			next:    next,
			hasEPG:  hasEPG,
		})
	}
	return candidates
}

func (h *Handler) buildVortexoLiveTVRows(
	candidates []vortexoLiveTVCandidate,
	favoriteIDs []string,
	recentIDs []string,
	now time.Time,
	rowLimit int,
	itemLimit int,
) []vortexoLiveTVRow {
	refreshAfter := now.Add(15 * time.Minute).UTC()
	rows := make([]vortexoLiveTVRow, 0, rowLimit)
	byID := liveTVCandidateMap(candidates)

	addOrderedRow := func(id, title, reason string, ids []string) {
		items := liveTVItemsForOrderedIDs(byID, ids, itemLimit)
		if len(items) == 0 || len(rows) >= rowLimit {
			return
		}
		rows = append(rows, vortexoLiveTVRow{
			ID:           id,
			Title:        title,
			Reason:       reason,
			RefreshAfter: refreshAfter,
			Items:        items,
		})
	}

	addCandidateRow := func(id, title, reason string, matches func(vortexoLiveTVCandidate) bool, sortFn func([]vortexoLiveTVCandidate)) {
		if len(rows) >= rowLimit {
			return
		}
		matched := make([]vortexoLiveTVCandidate, 0)
		for _, candidate := range candidates {
			if matches(candidate) {
				matched = append(matched, candidate)
			}
		}
		if sortFn != nil {
			sortFn(matched)
		} else {
			sortLiveTVChannels(matched)
		}
		items := liveTVItemsFromCandidates(matched, itemLimit)
		if len(items) == 0 {
			return
		}
		rows = append(rows, vortexoLiveTVRow{
			ID:           id,
			Title:        title,
			Reason:       reason,
			RefreshAfter: refreshAfter,
			Items:        items,
		})
	}

	addOrderedRow("favorite-channels", "Favorite Channels", "Your saved Live TV channels", favoriteIDs)
	addOrderedRow("recently-watched-channels", "Recently Watched Channels", "Channels you opened recently", recentIDs)
	addCandidateRow("on-now", "On Now", "Live programs with guide data", func(candidate vortexoLiveTVCandidate) bool {
		return candidate.current != nil
	}, sortLiveTVByCurrentProgress)
	addCandidateRow("starting-soon", "Starting Soon", "Programs beginning soon", func(candidate vortexoLiveTVCandidate) bool {
		return candidate.next != nil && candidate.next.StartTime.Before(now.Add(2*time.Hour))
	}, sortLiveTVByNextStart)
	addCandidateRow("sports-on-now", "Sports On Now", "Live sport and match coverage", func(candidate vortexoLiveTVCandidate) bool {
		return candidate.current != nil && liveTVLooksLikeSports(candidate)
	}, sortLiveTVByCurrentProgress)
	addCandidateRow("movies-on-live-tv", "Movies On Live TV", "Film channels and movie programs", func(candidate vortexoLiveTVCandidate) bool {
		return liveTVLooksLikeMovie(candidate)
	}, sortLiveTVByCurrentProgress)
	addCandidateRow("channels-with-guide-data", "Channels With Guide Data", "Channels that have EPG available", func(candidate vortexoLiveTVCandidate) bool {
		return candidate.hasEPG
	}, sortLiveTVChannels)
	addCandidateRow("no-epg-channels", "No EPG Channels", "Troubleshoot channels missing guide data", func(candidate vortexoLiveTVCandidate) bool {
		return !candidate.hasEPG
	}, sortLiveTVChannels)

	return rows
}

func vortexoLiveTVProgramFromEPG(program *livetv.EPGProgram, now time.Time) *vortexoLiveTVProgram {
	if program == nil {
		return nil
	}
	isLive := !program.StartTime.After(now) && program.EndTime.After(now)
	progress := 0.0
	if isLive {
		duration := program.EndTime.Sub(program.StartTime).Seconds()
		if duration > 0 {
			progress = now.Sub(program.StartTime).Seconds() / duration
			if progress < 0 {
				progress = 0
			}
			if progress > 1 {
				progress = 1
			}
		}
	}
	return &vortexoLiveTVProgram{
		Title:       program.Title,
		Description: program.Description,
		StartTime:   program.StartTime.UTC(),
		EndTime:     program.EndTime.UTC(),
		Category:    program.Category,
		IsLive:      isLive,
		Progress:    progress,
	}
}

func orderedLiveTVIDList(values []string, combined string) []string {
	if combined != "" {
		values = append(values, combined)
	}
	seen := map[string]bool{}
	var ids []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			id := strings.TrimSpace(part)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func liveTVCandidateMap(candidates []vortexoLiveTVCandidate) map[string]vortexoLiveTVCandidate {
	byID := make(map[string]vortexoLiveTVCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.channel.ID] = candidate
		byID[normalizeHomeText(candidate.channel.ID)] = candidate
		byID[normalizeHomeText(candidate.channel.Name)] = candidate
	}
	return byID
}

func liveTVItemsForOrderedIDs(
	byID map[string]vortexoLiveTVCandidate,
	ids []string,
	limit int,
) []vortexoLiveTVRowItem {
	items := make([]vortexoLiveTVRowItem, 0, min(limit, len(ids)))
	seen := map[string]bool{}
	for _, id := range ids {
		candidate, ok := byID[id]
		if !ok {
			candidate, ok = byID[normalizeHomeText(id)]
		}
		if !ok || seen[candidate.channel.ID] {
			continue
		}
		seen[candidate.channel.ID] = true
		items = append(items, candidate.item)
		if len(items) >= limit {
			break
		}
	}
	return items
}

func liveTVItemsFromCandidates(candidates []vortexoLiveTVCandidate, limit int) []vortexoLiveTVRowItem {
	items := make([]vortexoLiveTVRowItem, 0, min(limit, len(candidates)))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.channel.ID] {
			continue
		}
		seen[candidate.channel.ID] = true
		items = append(items, candidate.item)
		if len(items) >= limit {
			break
		}
	}
	return items
}

func sortLiveTVChannels(candidates []vortexoLiveTVCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].channel
		right := candidates[j].channel
		if left.Category != right.Category {
			return strings.ToLower(left.Category) < strings.ToLower(right.Category)
		}
		return strings.ToLower(left.Name) < strings.ToLower(right.Name)
	})
}

func sortLiveTVByCurrentProgress(candidates []vortexoLiveTVCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.current != nil && right.current != nil {
			leftRemaining := left.current.EndTime.Sub(time.Now())
			rightRemaining := right.current.EndTime.Sub(time.Now())
			if leftRemaining != rightRemaining {
				return leftRemaining > rightRemaining
			}
		}
		return strings.ToLower(left.channel.Name) < strings.ToLower(right.channel.Name)
	})
}

func sortLiveTVByNextStart(candidates []vortexoLiveTVCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].next
		right := candidates[j].next
		if left != nil && right != nil && !left.StartTime.Equal(right.StartTime) {
			return left.StartTime.Before(right.StartTime)
		}
		return strings.ToLower(candidates[i].channel.Name) < strings.ToLower(candidates[j].channel.Name)
	})
}

func liveTVLooksLikeSports(candidate vortexoLiveTVCandidate) bool {
	return liveTVContainsAny(candidate, []string{
		"sport", "sports", "football", "soccer", "basketball", "cricket", "tennis",
		"rugby", "league", "afl", "nrl", "nba", "nfl", "mlb", "nhl", "f1",
		"formula", "motogp", "ufc", "boxing", "golf", "racing",
	})
}

func liveTVLooksLikeMovie(candidate vortexoLiveTVCandidate) bool {
	return liveTVContainsAny(candidate, []string{
		"movie", "movies", "film", "films", "cinema", "premiere", "feature",
	})
}

func liveTVContainsAny(candidate vortexoLiveTVCandidate, needles []string) bool {
	values := []string{
		candidate.channel.Name,
		candidate.channel.Category,
		candidate.channel.Source,
	}
	if candidate.current != nil {
		values = append(values, candidate.current.Title, candidate.current.Description, candidate.current.Category)
	}
	if candidate.next != nil {
		values = append(values, candidate.next.Title, candidate.next.Description, candidate.next.Category)
	}
	haystack := normalizeHomeText(strings.Join(values, " "))
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
