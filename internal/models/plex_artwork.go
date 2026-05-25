package models

import "time"

// PlexArtwork stores public image URLs discovered from watch.plex.tv.
type PlexArtwork struct {
	CoverArt   []string `json:"coverArt"`
	Landscape  []string `json:"landscape"`
	Background []string `json:"background"`
	ClearLogo  []string `json:"clearLogo"`
	Thumbnail  []string `json:"thumbnail"`
}

type PlexArtworkEntry struct {
	Version    int         `json:"version"`
	MediaType  string      `json:"mediaType"`
	TMDBID     int         `json:"tmdbId"`
	Title      string      `json:"title,omitempty"`
	Year       int         `json:"year,omitempty"`
	SourcePage string      `json:"sourcePage,omitempty"`
	UpdatedAt  time.Time   `json:"updatedAt"`
	Artwork    PlexArtwork `json:"artwork"`
}

type PlexArtworkCacheRecord struct {
	PlexArtworkEntry
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

type PlexArtworkSeedItem struct {
	MediaType string
	TMDBID    int
	Title     string
	Year      int
}

type PlexArtworkSyncStats struct {
	Limit       int       `json:"limit"`
	Attempted   int       `json:"attempted"`
	OK          int       `json:"ok"`
	Miss        int       `json:"miss"`
	Skipped     int       `json:"skipped"`
	Failed      int       `json:"failed"`
	Stopped     string    `json:"stopped,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
}

func (a PlexArtwork) IsEmpty() bool {
	return len(a.CoverArt) == 0 &&
		len(a.Landscape) == 0 &&
		len(a.Background) == 0 &&
		len(a.ClearLogo) == 0 &&
		len(a.Thumbnail) == 0
}
