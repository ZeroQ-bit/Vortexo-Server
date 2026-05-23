package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

const (
	tmdbBaseURL      = "https://api.themoviedb.org/3"
	tmdbImageBaseURL = "https://image.tmdb.org/t/p"
	fanartBaseURL    = "https://webservice.fanart.tv/v3"
)

type TMDBClient struct {
	apiKey       string
	fanartAPIKey string
	httpClient   *http.Client
}

func NewTMDBClient(apiKey string, fanartAPIKey ...string) *TMDBClient {
	var fanartKey string
	if len(fanartAPIKey) > 0 {
		fanartKey = strings.TrimSpace(fanartAPIKey[0])
	}

	return &TMDBClient{
		apiKey:       apiKey,
		fanartAPIKey: fanartKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Movie API responses
type tmdbMovie struct {
	ID               int      `json:"id"`
	Title            string   `json:"title"`
	OriginalTitle    string   `json:"original_title"`
	OriginalLanguage string   `json:"original_language"`
	Overview         string   `json:"overview"`
	PosterPath       string   `json:"poster_path"`
	BackdropPath     string   `json:"backdrop_path"`
	ReleaseDate      string   `json:"release_date"`
	Runtime          int      `json:"runtime"`
	Adult            bool     `json:"adult"`
	OriginCountry    []string `json:"origin_country"`
	GenreIDs         []int    `json:"genre_ids"`
	Genres           []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	ProductionCountries []struct {
		ISO3166_1 string `json:"iso_3166_1"`
		Name      string `json:"name"`
	} `json:"production_countries"`
	VoteAverage         float64                  `json:"vote_average"`
	VoteCount           int                      `json:"vote_count"`
	Status              string                   `json:"status"`
	IMDbID              string                   `json:"imdb_id"`
	BelongsToCollection *tmdbBelongsToCollection `json:"belongs_to_collection"`
	Keywords            tmdbMovieKeywords        `json:"keywords"`
}

type DiscoverMovieFilters struct {
	GenreID             *int
	MinYear             int
	MinRuntime          int
	IncludeAdultVOD     bool
	OnlyReleasedContent bool
}

// tmdbBelongsToCollection represents the collection a movie belongs to
type tmdbBelongsToCollection struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

// tmdbCollection represents a full collection response
type tmdbCollection struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
	Parts        []struct {
		ID           int     `json:"id"`
		Title        string  `json:"title"`
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		ReleaseDate  string  `json:"release_date"`
		VoteAverage  float64 `json:"vote_average"`
	} `json:"parts"`
}

type tmdbSeries struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	OriginalName     string   `json:"original_name"`
	OriginalLanguage string   `json:"original_language"`
	Overview         string   `json:"overview"`
	PosterPath       string   `json:"poster_path"`
	BackdropPath     string   `json:"backdrop_path"`
	FirstAirDate     string   `json:"first_air_date"`
	Status           string   `json:"status"`
	NumberOfSeasons  int      `json:"number_of_seasons"`
	OriginCountry    []string `json:"origin_country"`
	Genres           []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	VoteAverage float64            `json:"vote_average"`
	VoteCount   int                `json:"vote_count"`
	Keywords    tmdbSeriesKeywords `json:"keywords"`
}

type tmdbKeyword struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tmdbMovieKeywords struct {
	Keywords []tmdbKeyword `json:"keywords"`
}

type tmdbSeriesKeywords struct {
	Results []tmdbKeyword `json:"results"`
}

type tmdbSeason struct {
	ID           int           `json:"id"`
	SeasonNumber int           `json:"season_number"`
	Name         string        `json:"name"`
	Overview     string        `json:"overview"`
	PosterPath   string        `json:"poster_path"`
	AirDate      string        `json:"air_date"`
	EpisodeCount int           `json:"episode_count"`
	Episodes     []tmdbEpisode `json:"episodes"`
}

type tmdbEpisode struct {
	ID            int     `json:"id"`
	SeasonNumber  int     `json:"season_number"`
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	AirDate       string  `json:"air_date"`
	StillPath     string  `json:"still_path"`
	Runtime       int     `json:"runtime"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
}

type tmdbSearchResult struct {
	Page         int           `json:"page"`
	Results      []interface{} `json:"results"`
	TotalResults int           `json:"total_results"`
	TotalPages   int           `json:"total_pages"`
}

type tmdbImageFile struct {
	FilePath    string  `json:"file_path"`
	ISO6391     string  `json:"iso_639_1"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
}

type tmdbImagesResponse struct {
	Logos []tmdbImageFile `json:"logos"`
}

type FanartArtwork struct {
	LogoPaths      []string `json:"logo_paths"`
	BackdropPaths  []string `json:"backdrop_paths"`
	LandscapePaths []string `json:"landscape_paths"`
	PosterPaths    []string `json:"poster_paths"`
}

func tmdbKeywordNames(keywords []tmdbKeyword) []string {
	names := make([]string, 0, len(keywords))
	seen := make(map[string]bool, len(keywords))
	for _, keyword := range keywords {
		name := strings.TrimSpace(keyword.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	return names
}

type fanartImageCandidate struct {
	URL   string
	Lang  string
	Likes int
}

// GetMovie retrieves movie details from TMDB
func (c *TMDBClient) GetMovie(ctx context.Context, tmdbID int) (*models.Movie, error) {
	endpoint := fmt.Sprintf("%s/movie/%d", tmdbBaseURL, tmdbID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("append_to_response", "keywords")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var tmdbMovie tmdbMovie
	if err := json.Unmarshal(data, &tmdbMovie); err != nil {
		return nil, fmt.Errorf("failed to unmarshal movie: %w", err)
	}

	return c.convertMovie(&tmdbMovie), nil
}

// GetMovieWithCollection retrieves movie details along with collection info
func (c *TMDBClient) GetMovieWithCollection(ctx context.Context, tmdbID int) (*models.Movie, *models.Collection, error) {
	endpoint := fmt.Sprintf("%s/movie/%d", tmdbBaseURL, tmdbID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("append_to_response", "keywords")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, nil, err
	}

	var tmdbMovie tmdbMovie
	if err := json.Unmarshal(data, &tmdbMovie); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal movie: %w", err)
	}

	movie := c.convertMovie(&tmdbMovie)

	var collection *models.Collection
	if tmdbMovie.BelongsToCollection != nil {
		collection = &models.Collection{
			TMDBID:       tmdbMovie.BelongsToCollection.ID,
			Name:         tmdbMovie.BelongsToCollection.Name,
			PosterPath:   tmdbMovie.BelongsToCollection.PosterPath,
			BackdropPath: tmdbMovie.BelongsToCollection.BackdropPath,
		}
	}

	return movie, collection, nil
}

// CollectionMovie represents a movie in a collection with library status
type CollectionMovie struct {
	TMDBID       int     `json:"tmdb_id"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	ReleaseDate  string  `json:"release_date"`
	VoteAverage  float64 `json:"vote_average"`
	InLibrary    bool    `json:"in_library"`
}

// GetCollection retrieves full collection details from TMDB
func (c *TMDBClient) GetCollection(ctx context.Context, collectionID int) (*models.Collection, []int, error) {
	endpoint := fmt.Sprintf("%s/collection/%d", tmdbBaseURL, collectionID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, nil, err
	}

	var tc tmdbCollection
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal collection: %w", err)
	}

	collection := &models.Collection{
		TMDBID:       tc.ID,
		Name:         tc.Name,
		Overview:     tc.Overview,
		PosterPath:   tc.PosterPath,
		BackdropPath: tc.BackdropPath,
		TotalMovies:  len(tc.Parts),
	}

	// Extract movie TMDB IDs
	movieIDs := make([]int, 0, len(tc.Parts))
	for _, part := range tc.Parts {
		movieIDs = append(movieIDs, part.ID)
	}

	return collection, movieIDs, nil
}

// GetCollectionByID fetches a single collection by ID, returns nil if not found
func (c *TMDBClient) GetCollectionByID(ctx context.Context, collectionID int) (*models.Collection, error) {
	endpoint := fmt.Sprintf("%s/collection/%d", tmdbBaseURL, collectionID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err // Collection doesn't exist or error
	}

	var tc tmdbCollection
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil, err
	}

	return &models.Collection{
		TMDBID:       tc.ID,
		Name:         tc.Name,
		Overview:     tc.Overview,
		PosterPath:   tc.PosterPath,
		BackdropPath: tc.BackdropPath,
		TotalMovies:  len(tc.Parts),
	}, nil
}

// FetchCollectionsByIDRange fetches all valid collections in an ID range
// This is useful for browsing all TMDB collections (IDs range from 1 to ~1,000,000+)
func (c *TMDBClient) FetchCollectionsByIDRange(ctx context.Context, startID, endID int) ([]*models.Collection, error) {
	if endID <= startID {
		return nil, fmt.Errorf("endID must be greater than startID")
	}

	// Limit range to prevent too many requests
	maxRange := 500
	if endID-startID > maxRange {
		endID = startID + maxRange
	}

	type result struct {
		collection *models.Collection
		err        error
	}

	results := make(chan result, endID-startID)
	semaphore := make(chan struct{}, 30) // 30 concurrent requests

	// Fetch all collections in range in parallel
	for id := startID; id < endID; id++ {
		go func(collectionID int) {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			collection, err := c.GetCollectionByID(ctx, collectionID)
			results <- result{collection: collection, err: err}
		}(id)
	}

	// Collect valid collections
	collections := make([]*models.Collection, 0)
	for i := 0; i < endID-startID; i++ {
		res := <-results
		if res.err == nil && res.collection != nil {
			collections = append(collections, res.collection)
		}
	}

	return collections, nil
}

// GetCollectionWithMovies retrieves full collection details including all movie info from TMDB
func (c *TMDBClient) GetCollectionWithMovies(ctx context.Context, collectionID int) (*models.Collection, []CollectionMovie, error) {
	endpoint := fmt.Sprintf("%s/collection/%d", tmdbBaseURL, collectionID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, nil, err
	}

	var tc tmdbCollection
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal collection: %w", err)
	}

	collection := &models.Collection{
		TMDBID:       tc.ID,
		Name:         tc.Name,
		Overview:     tc.Overview,
		PosterPath:   tc.PosterPath,
		BackdropPath: tc.BackdropPath,
		TotalMovies:  len(tc.Parts),
	}

	// Convert parts to CollectionMovie slice
	movies := make([]CollectionMovie, 0, len(tc.Parts))
	for _, part := range tc.Parts {
		movies = append(movies, CollectionMovie{
			TMDBID:       part.ID,
			Title:        part.Title,
			Overview:     part.Overview,
			PosterPath:   part.PosterPath,
			BackdropPath: part.BackdropPath,
			ReleaseDate:  part.ReleaseDate,
			VoteAverage:  part.VoteAverage,
			InLibrary:    false, // Will be set by the handler
		})
	}

	return collection, movies, nil
}

// GetSeries retrieves series details from TMDB, including IMDB ID
func (c *TMDBClient) GetSeries(ctx context.Context, tmdbID int) (*models.Series, error) {
	endpoint := fmt.Sprintf("%s/tv/%d", tmdbBaseURL, tmdbID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("append_to_response", "keywords")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var tmdbSeries tmdbSeries
	if err := json.Unmarshal(data, &tmdbSeries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal series: %w", err)
	}

	series := c.convertSeries(&tmdbSeries)

	// Fetch external IDs to get IMDB ID
	externalIDs, err := c.GetSeriesExternalIDs(ctx, tmdbID)
	if err == nil && externalIDs.IMDBID != "" {
		series.IMDBID = externalIDs.IMDBID
		series.Metadata["imdb_id"] = externalIDs.IMDBID
	}
	if err == nil && externalIDs.TVDBID > 0 {
		series.Metadata["tvdb_id"] = externalIDs.TVDBID
	}

	return series, nil
}

// GetSeason retrieves season details including episodes
func (c *TMDBClient) GetSeason(ctx context.Context, seriesID, seasonNumber int) (*tmdbSeason, error) {
	endpoint := fmt.Sprintf("%s/tv/%d/season/%d", tmdbBaseURL, seriesID, seasonNumber)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var season tmdbSeason
	if err := json.Unmarshal(data, &season); err != nil {
		return nil, fmt.Errorf("failed to unmarshal season: %w", err)
	}

	return &season, nil
}

// ExternalIDs represents external IDs for a TV series
type ExternalIDs struct {
	ID         int    `json:"id"`
	IMDBID     string `json:"imdb_id"`
	FreebaseID string `json:"freebase_id"`
	TVDBID     int    `json:"tvdb_id"`
}

// Video represents a TMDB video (trailer, teaser, etc.)
type Video struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Site      string `json:"site"`
	Size      int    `json:"size"`
	Type      string `json:"type"`
	Official  bool   `json:"official"`
	Published string `json:"published_at"`
}

// GetVideos retrieves videos (trailers, teasers, etc.) for a movie or TV show
func (c *TMDBClient) GetVideos(ctx context.Context, mediaType string, tmdbID int) ([]Video, error) {
	var endpoint string
	if mediaType == "movie" {
		endpoint = fmt.Sprintf("%s/movie/%d/videos", tmdbBaseURL, tmdbID)
	} else {
		endpoint = fmt.Sprintf("%s/tv/%d/videos", tmdbBaseURL, tmdbID)
	}

	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("language", "en-US")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []Video `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal videos: %w", err)
	}

	return result.Results, nil
}

// GetLogoPath retrieves the preferred clear title logo path for a movie/series.
func (c *TMDBClient) GetLogoPath(ctx context.Context, mediaType string, tmdbID int) (string, error) {
	var endpoint string
	if mediaType == "movie" {
		endpoint = fmt.Sprintf("%s/movie/%d/images", tmdbBaseURL, tmdbID)
	} else {
		endpoint = fmt.Sprintf("%s/tv/%d/images", tmdbBaseURL, tmdbID)
	}

	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("include_image_language", "en,null")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return "", err
	}

	var result tmdbImagesResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal images: %w", err)
	}

	var best *tmdbImageFile
	for i := range result.Logos {
		logo := &result.Logos[i]
		if strings.TrimSpace(logo.FilePath) == "" {
			continue
		}
		if best == nil || betterTMDBLogo(logo, best) {
			best = logo
		}
	}

	if best == nil {
		return "", nil
	}
	return best.FilePath, nil
}

func betterTMDBLogo(candidate, current *tmdbImageFile) bool {
	candidateLanguageScore := tmdbLogoLanguageScore(candidate.ISO6391)
	currentLanguageScore := tmdbLogoLanguageScore(current.ISO6391)
	if candidateLanguageScore != currentLanguageScore {
		return candidateLanguageScore > currentLanguageScore
	}
	if candidate.VoteCount != current.VoteCount {
		return candidate.VoteCount > current.VoteCount
	}
	return candidate.VoteAverage > current.VoteAverage
}

func tmdbLogoLanguageScore(language string) int {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en":
		return 2
	case "":
		return 1
	default:
		return 0
	}
}

// GetFanartArtwork fetches Fanart.tv artwork for a movie or TV show.
// Fanart.tv accepts TMDB/IMDb IDs for movies and TVDB IDs for shows.
func (c *TMDBClient) GetFanartArtwork(
	ctx context.Context,
	mediaType string,
	tmdbID int,
	imdbID string,
	tvdbID int,
) (FanartArtwork, error) {
	var artwork FanartArtwork
	if strings.TrimSpace(c.fanartAPIKey) == "" {
		return artwork, nil
	}

	var endpoint string
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		id := strings.TrimSpace(imdbID)
		if tmdbID > 0 {
			id = strconv.Itoa(tmdbID)
		}
		if id == "" {
			return artwork, nil
		}
		endpoint = fmt.Sprintf("%s/movies/%s", fanartBaseURL, id)
	default:
		if tvdbID <= 0 {
			return artwork, nil
		}
		endpoint = fmt.Sprintf("%s/tv/%d", fanartBaseURL, tvdbID)
	}

	data, err := c.makeFanartRequest(ctx, endpoint)
	if err != nil {
		return artwork, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return artwork, fmt.Errorf("failed to unmarshal Fanart.tv response: %w", err)
	}

	if strings.EqualFold(mediaType, "movie") {
		artwork.LogoPaths = fanartURLs(raw, []string{"hdmovielogo", "movielogo"})
		artwork.BackdropPaths = fanartURLs(raw, []string{"moviebackground"})
		artwork.LandscapePaths = fanartURLs(raw, []string{"moviethumb"})
		artwork.PosterPaths = fanartURLs(raw, []string{"movieposter"})
	} else {
		artwork.LogoPaths = fanartURLs(raw, []string{"hdtvlogo", "clearlogo"})
		artwork.BackdropPaths = fanartURLs(raw, []string{"showbackground"})
		artwork.LandscapePaths = fanartURLs(raw, []string{"tvthumb"})
		artwork.PosterPaths = fanartURLs(raw, []string{"tvposter"})
	}

	return artwork, nil
}

func (c *TMDBClient) makeFanartRequest(ctx context.Context, endpoint string) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Fanart.tv endpoint %s: %w", endpoint, err)
	}

	q := u.Query()
	q.Set("api_key", c.fanartAPIKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Fanart.tv request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request to Fanart.tv: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Fanart.tv response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return []byte(`{}`), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Fanart.tv returned status %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}

func fanartURLs(raw map[string]json.RawMessage, fields []string) []string {
	var candidates []fanartImageCandidate
	for _, field := range fields {
		var entries []map[string]interface{}
		if err := json.Unmarshal(raw[field], &entries); err != nil {
			continue
		}
		for _, entry := range entries {
			urlValue, _ := entry["url"].(string)
			if strings.TrimSpace(urlValue) == "" {
				continue
			}

			lang, _ := entry["lang"].(string)
			candidates = append(candidates, fanartImageCandidate{
				URL:   strings.TrimSpace(urlValue),
				Lang:  lang,
				Likes: fanartLikes(entry["likes"]),
			})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		iLang := fanartLanguageScore(candidates[i].Lang)
		jLang := fanartLanguageScore(candidates[j].Lang)
		if iLang != jLang {
			return iLang > jLang
		}
		return candidates[i].Likes > candidates[j].Likes
	})

	seen := make(map[string]bool, len(candidates))
	urls := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if seen[candidate.URL] {
			continue
		}
		seen[candidate.URL] = true
		urls = append(urls, candidate.URL)
	}
	return urls
}

func fanartLanguageScore(language string) int {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en":
		return 3
	case "", "00":
		return 2
	default:
		return 1
	}
}

func fanartLikes(value interface{}) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

// GetSeriesExternalIDs retrieves external IDs (IMDB, TVDB, etc.) for a series
func (c *TMDBClient) GetSeriesExternalIDs(ctx context.Context, seriesID int) (*ExternalIDs, error) {
	endpoint := fmt.Sprintf("%s/tv/%d/external_ids", tmdbBaseURL, seriesID)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var ids ExternalIDs
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("failed to unmarshal external IDs: %w", err)
	}

	return &ids, nil
}

// GetEpisodes retrieves all episodes for a series
func (c *TMDBClient) GetEpisodes(ctx context.Context, seriesID int64, tmdbID int, seasons int) ([]*models.Episode, error) {
	var allEpisodes []*models.Episode

	for seasonNum := 1; seasonNum <= seasons; seasonNum++ {
		season, err := c.GetSeason(ctx, tmdbID, seasonNum)
		if err != nil {
			return nil, fmt.Errorf("failed to get season %d: %w", seasonNum, err)
		}

		for _, ep := range season.Episodes {
			episode := c.convertEpisode(seriesID, &ep)
			allEpisodes = append(allEpisodes, episode)
		}
	}

	return allEpisodes, nil
}

// SearchMovies searches for movies
func (c *TMDBClient) SearchMovies(ctx context.Context, query string, page int) ([]*models.Movie, error) {
	endpoint := fmt.Sprintf("%s/search/movie", tmdbBaseURL)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search results: %w", err)
	}

	movies := make([]*models.Movie, 0, len(result.Results))
	for _, tmdbMovie := range result.Results {
		movies = append(movies, c.convertMovie(&tmdbMovie))
	}

	return movies, nil
}

// SearchSeries searches for TV series
func (c *TMDBClient) SearchSeries(ctx context.Context, query string, page int) ([]*models.Series, error) {
	endpoint := fmt.Sprintf("%s/search/tv", tmdbBaseURL)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []tmdbSeries `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search results: %w", err)
	}

	series := make([]*models.Series, 0, len(result.Results))
	for _, tmdbSeries := range result.Results {
		series = append(series, c.convertSeries(&tmdbSeries))
	}

	return series, nil
}

// SearchCollections searches for collections
func (c *TMDBClient) SearchCollections(ctx context.Context, query string) ([]*models.Collection, error) {
	return c.SearchCollectionsPaged(ctx, query, 1)
}

// SearchCollectionsPaged searches for collections with pagination
func (c *TMDBClient) SearchCollectionsPaged(ctx context.Context, query string, page int) ([]*models.Collection, error) {
	endpoint := fmt.Sprintf("%s/search/collection", tmdbBaseURL)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			Overview     string `json:"overview"`
			PosterPath   string `json:"poster_path"`
			BackdropPath string `json:"backdrop_path"`
		} `json:"results"`
		TotalPages   int `json:"total_pages"`
		TotalResults int `json:"total_results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search results: %w", err)
	}

	collections := make([]*models.Collection, 0, len(result.Results))
	for _, c := range result.Results {
		collections = append(collections, &models.Collection{
			TMDBID:       c.ID,
			Name:         c.Name,
			Overview:     c.Overview,
			PosterPath:   c.PosterPath,
			BackdropPath: c.BackdropPath,
		})
	}

	return collections, nil
}

// DiscoverMovies discovers movies with filters
func (c *TMDBClient) DiscoverMovies(ctx context.Context, page int, year *int, genre *string) ([]*models.Movie, error) {
	filters := DiscoverMovieFilters{}
	if year != nil {
		filters.MinYear = *year
	}
	if genre != nil {
		if genreID, err := strconv.Atoi(*genre); err == nil {
			filters.GenreID = &genreID
		}
	}
	return c.DiscoverMoviesWithFilters(ctx, page, filters)
}

// DiscoverMoviesWithFilters discovers movies with application content filters.
func (c *TMDBClient) DiscoverMoviesWithFilters(ctx context.Context, page int, filters DiscoverMovieFilters) ([]*models.Movie, error) {
	endpoint := fmt.Sprintf("%s/discover/movie", tmdbBaseURL)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("sort_by", "popularity.desc")
	params.Set("include_adult", strconv.FormatBool(filters.IncludeAdultVOD))

	if filters.MinYear > 0 {
		params.Set("primary_release_date.gte", fmt.Sprintf("%04d-01-01", filters.MinYear))
	}
	if filters.MinRuntime > 0 {
		params.Set("with_runtime.gte", fmt.Sprintf("%d", filters.MinRuntime))
	}
	if filters.OnlyReleasedContent {
		params.Set("primary_release_date.lte", time.Now().Format("2006-01-02"))
	}
	if filters.GenreID != nil {
		params.Set("with_genres", fmt.Sprintf("%d", *filters.GenreID))
	}

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal discover results: %w", err)
	}

	movies := make([]*models.Movie, 0, len(result.Results))
	contentFilters := ContentFilterOptions{
		MinYear:             filters.MinYear,
		MinRuntime:          filters.MinRuntime,
		IncludeAdultVOD:     filters.IncludeAdultVOD,
		OnlyReleasedContent: filters.OnlyReleasedContent,
	}
	for _, tmdbMovie := range result.Results {
		movie := c.convertMovie(&tmdbMovie)
		if allowed, _ := MovieAllowedByContentFilters(movie, contentFilters); allowed {
			movies = append(movies, movie)
		}
	}

	return movies, nil
}

// makeRequest performs an HTTP GET request to TMDB API
func (c *TMDBClient) makeRequest(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	// Add API key to params
	params.Set("api_key", c.apiKey)

	// Build full URL without double-prepending the TMDB base
	baseURL := endpoint
	if !strings.HasPrefix(endpoint, "http") {
		baseURL = fmt.Sprintf("%s%s", tmdbBaseURL, endpoint)
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse TMDB endpoint %s: %w", baseURL, err)
	}

	q := u.Query()
	for k, vals := range params {
		for _, v := range vals {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request to %s: %w", u.String(), err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB API returned status %d for %s: %s", resp.StatusCode, u.String(), string(data))
	}

	return data, nil
}

// convertMovie converts TMDB movie to internal model
func (c *TMDBClient) convertMovie(tm *tmdbMovie) *models.Movie {
	genres := make([]string, len(tm.Genres))
	for i, g := range tm.Genres {
		genres[i] = g.Name
	}

	var releaseDate *time.Time
	if tm.ReleaseDate != "" {
		if parsed, err := time.Parse("2006-01-02", tm.ReleaseDate); err == nil {
			releaseDate = &parsed
		}
	}

	return &models.Movie{
		TMDBID:        tm.ID,
		Title:         tm.Title,
		OriginalTitle: tm.OriginalTitle,
		Overview:      tm.Overview,
		PosterPath:    tm.PosterPath,
		BackdropPath:  tm.BackdropPath,
		ReleaseDate:   releaseDate,
		Runtime:       tm.Runtime,
		Genres:        genres,
		Metadata: models.Metadata{
			"vote_average":      tm.VoteAverage,
			"vote_count":        tm.VoteCount,
			"status":            tm.Status,
			"imdb_id":           tm.IMDbID,
			"adult":             tm.Adult,
			"genre_ids":         tm.GenreIDs,
			"keywords":          tmdbKeywordNames(tm.Keywords.Keywords),
			"release_date":      tm.ReleaseDate,
			"runtime":           tm.Runtime,
			"original_language": tm.OriginalLanguage,
			"origin_country":    tm.OriginCountry,
			"production_countries": func() []string {
				codes := make([]string, 0, len(tm.ProductionCountries))
				for _, pc := range tm.ProductionCountries {
					if pc.ISO3166_1 != "" {
						codes = append(codes, pc.ISO3166_1)
					}
				}
				return codes
			}(),
		},
	}
}

// convertSeries converts TMDB series to internal model
func (c *TMDBClient) convertSeries(ts *tmdbSeries) *models.Series {
	genres := make([]string, len(ts.Genres))
	for i, g := range ts.Genres {
		genres[i] = g.Name
	}

	var firstAirDate *time.Time
	if ts.FirstAirDate != "" {
		if parsed, err := time.Parse("2006-01-02", ts.FirstAirDate); err == nil {
			firstAirDate = &parsed
		}
	}

	return &models.Series{
		TMDBID:        ts.ID,
		Title:         ts.Name,
		OriginalTitle: ts.OriginalName,
		Overview:      ts.Overview,
		PosterPath:    ts.PosterPath,
		BackdropPath:  ts.BackdropPath,
		FirstAirDate:  firstAirDate,
		Status:        ts.Status,
		Seasons:       ts.NumberOfSeasons,
		Genres:        genres,
		Metadata: models.Metadata{
			"vote_average":      ts.VoteAverage,
			"vote_count":        ts.VoteCount,
			"keywords":          tmdbKeywordNames(ts.Keywords.Results),
			"original_language": ts.OriginalLanguage,
			"origin_country":    ts.OriginCountry,
		},
	}
}

// convertEpisode converts TMDB episode to internal model
func (c *TMDBClient) convertEpisode(seriesID int64, te *tmdbEpisode) *models.Episode {
	var airDate *time.Time
	if te.AirDate != "" {
		if parsed, err := time.Parse("2006-01-02", te.AirDate); err == nil {
			airDate = &parsed
		}
	}

	return &models.Episode{
		SeriesID:      seriesID,
		TMDBID:        te.ID,
		SeasonNumber:  te.SeasonNumber,
		EpisodeNumber: te.EpisodeNumber,
		Title:         te.Name,
		Overview:      te.Overview,
		AirDate:       airDate,
		StillPath:     te.StillPath,
		Runtime:       te.Runtime,
		Metadata: models.Metadata{
			"vote_average": te.VoteAverage,
			"vote_count":   te.VoteCount,
		},
	}
}

// GetPosterURL returns the full poster URL
func (c *TMDBClient) GetPosterURL(path string, size string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s%s", tmdbImageBaseURL, size, path)
}

// IMDBToTMDB converts an IMDB ID to TMDB ID
func (c *TMDBClient) IMDBToTMDB(imdbID string, mediaType string) (int, error) {
	ctx := context.Background()

	endpoint := fmt.Sprintf("/find/%s", imdbID)
	params := url.Values{}
	params.Set("external_source", "imdb_id")

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return 0, err
	}

	var result struct {
		MovieResults []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
		TVResults []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	if mediaType == "movie" && len(result.MovieResults) > 0 {
		return result.MovieResults[0].ID, nil
	}

	if mediaType == "tv" && len(result.TVResults) > 0 {
		return result.TVResults[0].ID, nil
	}

	return 0, fmt.Errorf("no TMDB ID found for IMDB ID %s", imdbID)
}

// TrendingItem represents a trending movie or TV show
type TrendingItem struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	MediaType    string  `json:"media_type"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	VoteAverage  float64 `json:"vote_average"`
}

// GetTrending returns trending movies and TV shows
// timeWindow can be "day" or "week"
func (c *TMDBClient) GetTrending(ctx context.Context, mediaType string, timeWindow string) ([]TrendingItem, error) {
	endpoint := fmt.Sprintf("%s/trending/%s/%s", tmdbBaseURL, mediaType, timeWindow)
	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			MediaType    string  `json:"media_type"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			Overview     string  `json:"overview"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal trending: %w", err)
	}

	items := make([]TrendingItem, 0, len(result.Results))
	for _, r := range result.Results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		releaseDate := r.ReleaseDate
		if releaseDate == "" {
			releaseDate = r.FirstAirDate
		}
		mediaTypeResult := r.MediaType
		if mediaType != "all" {
			mediaTypeResult = mediaType
		}
		items = append(items, TrendingItem{
			ID:           r.ID,
			Title:        title,
			MediaType:    mediaTypeResult,
			PosterPath:   r.PosterPath,
			BackdropPath: r.BackdropPath,
			Overview:     r.Overview,
			ReleaseDate:  releaseDate,
			VoteAverage:  r.VoteAverage,
		})
	}

	return items, nil
}

// GetPopular returns popular movies or TV shows
func (c *TMDBClient) GetPopular(ctx context.Context, mediaType string) ([]TrendingItem, error) {
	var endpoint string
	if mediaType == "movie" {
		endpoint = fmt.Sprintf("%s/movie/popular", tmdbBaseURL)
	} else {
		endpoint = fmt.Sprintf("%s/tv/popular", tmdbBaseURL)
	}

	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			Overview     string  `json:"overview"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal popular: %w", err)
	}

	items := make([]TrendingItem, 0, len(result.Results))
	for _, r := range result.Results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		releaseDate := r.ReleaseDate
		if releaseDate == "" {
			releaseDate = r.FirstAirDate
		}
		items = append(items, TrendingItem{
			ID:           r.ID,
			Title:        title,
			MediaType:    mediaType,
			PosterPath:   r.PosterPath,
			BackdropPath: r.BackdropPath,
			Overview:     r.Overview,
			ReleaseDate:  releaseDate,
			VoteAverage:  r.VoteAverage,
		})
	}

	return items, nil
}

// DiscoverCollectionsParams holds parameters for collection discovery
type DiscoverCollectionsParams struct {
	SortBy        string  // e.g., "popularity.desc", "vote_average.desc", "release_date.desc"
	Genre         *int    // Genre ID filter
	MinVoteCount  int     // Minimum vote count
	MinVoteAvg    float64 // Minimum vote average
	Country       string  // Origin country ISO code
	Company       string  // Production company ID(s)
	Year          *int    // Release year filter
	ReleasedAfter string  // Movies released after this date (YYYY-MM-DD)
}

// DiscoverCollections discovers collections by finding movies via /discover/movie
// and extracting their collection info (like tmdb-collections approach)
func (c *TMDBClient) DiscoverCollections(ctx context.Context, params DiscoverCollectionsParams, maxPages int) ([]*models.Collection, error) {
	if maxPages <= 0 {
		maxPages = 10
	}
	if params.MinVoteCount <= 0 {
		params.MinVoteCount = 100
	}
	if params.SortBy == "" {
		params.SortBy = "popularity.desc"
	}

	// First, collect all movie IDs from discover pages
	movieIDs := make(map[int]bool)

	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/discover/movie", tmdbBaseURL)
		urlParams := url.Values{}
		urlParams.Set("api_key", c.apiKey)
		urlParams.Set("page", fmt.Sprintf("%d", page))
		urlParams.Set("sort_by", params.SortBy)
		urlParams.Set("vote_count.gte", fmt.Sprintf("%d", params.MinVoteCount))

		if params.MinVoteAvg > 0 {
			urlParams.Set("vote_average.gte", fmt.Sprintf("%.1f", params.MinVoteAvg))
		}
		if params.Genre != nil {
			urlParams.Set("with_genres", fmt.Sprintf("%d", *params.Genre))
		}
		if params.Country != "" {
			urlParams.Set("with_origin_country", params.Country)
		}
		if params.Company != "" {
			urlParams.Set("with_companies", params.Company)
		}
		if params.Year != nil {
			urlParams.Set("year", fmt.Sprintf("%d", *params.Year))
		}
		if params.ReleasedAfter != "" {
			urlParams.Set("primary_release_date.gte", params.ReleasedAfter)
		}

		data, err := c.makeRequest(ctx, endpoint, urlParams)
		if err != nil {
			continue
		}

		var result struct {
			Results []struct {
				ID int `json:"id"`
			} `json:"results"`
			TotalPages int `json:"total_pages"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}

		for _, movie := range result.Results {
			movieIDs[movie.ID] = true
		}

		if page >= result.TotalPages {
			break
		}
	}

	// Now extract collections from these movies in parallel
	return c.getCollectionsFromMovieIDs(ctx, movieIDs)
}

// SearchPerson searches for a person (actor/director) and returns their movie collections
func (c *TMDBClient) SearchPerson(ctx context.Context, query string) ([]*models.Collection, error) {
	// First, search for the person
	endpoint := fmt.Sprintf("%s/search/person", tmdbBaseURL)
	params := url.Values{}
	params.Set("api_key", c.apiKey)
	params.Set("query", query)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var personResult struct {
		Results []struct {
			ID         int     `json:"id"`
			Name       string  `json:"name"`
			Popularity float64 `json:"popularity"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &personResult); err != nil {
		return nil, fmt.Errorf("failed to unmarshal person search: %w", err)
	}

	if len(personResult.Results) == 0 {
		return []*models.Collection{}, nil
	}

	// Get the most popular person match
	personID := personResult.Results[0].ID
	for _, p := range personResult.Results {
		if p.Popularity > personResult.Results[0].Popularity {
			personID = p.ID
		}
	}

	// Get their movie credits
	creditsEndpoint := fmt.Sprintf("%s/person/%d/movie_credits", tmdbBaseURL, personID)
	creditsParams := url.Values{}
	creditsParams.Set("api_key", c.apiKey)

	creditsData, err := c.makeRequest(ctx, creditsEndpoint, creditsParams)
	if err != nil {
		return nil, err
	}

	var credits struct {
		Cast []struct {
			ID int `json:"id"`
		} `json:"cast"`
		Crew []struct {
			ID         int    `json:"id"`
			Department string `json:"department"`
		} `json:"crew"`
	}
	if err := json.Unmarshal(creditsData, &credits); err != nil {
		return nil, fmt.Errorf("failed to unmarshal credits: %w", err)
	}

	// Collect unique movie IDs (cast + directing/writing crew)
	movieIDs := make(map[int]bool)
	for _, c := range credits.Cast {
		movieIDs[c.ID] = true
	}
	for _, c := range credits.Crew {
		if c.Department == "Directing" || c.Department == "Writing" {
			movieIDs[c.ID] = true
		}
	}

	// Extract collections from these movies
	return c.getCollectionsFromMovieIDs(ctx, movieIDs)
}

// SearchMoviesForCollections searches for movies by query and extracts their collections
func (c *TMDBClient) SearchMoviesForCollections(ctx context.Context, query string, maxPages int) ([]*models.Collection, error) {
	if maxPages <= 0 {
		maxPages = 3
	}

	movieIDs := make(map[int]bool)

	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/search/movie", tmdbBaseURL)
		params := url.Values{}
		params.Set("api_key", c.apiKey)
		params.Set("query", query)
		params.Set("page", fmt.Sprintf("%d", page))

		data, err := c.makeRequest(ctx, endpoint, params)
		if err != nil {
			continue
		}

		var result struct {
			Results    []tmdbMovie `json:"results"`
			TotalPages int         `json:"total_pages"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}

		for _, movie := range result.Results {
			movieIDs[movie.ID] = true
		}

		if page >= result.TotalPages || len(movieIDs) >= 50 {
			break
		}
	}

	return c.getCollectionsFromMovieIDs(ctx, movieIDs)
}

// EnhancedSearchCollections performs a multi-strategy search for collections
// (direct collection search + movie search + person search)
func (c *TMDBClient) EnhancedSearchCollections(ctx context.Context, query string) ([]*models.Collection, error) {
	collectionMap := make(map[int]*models.Collection)

	// Strategy 1: Direct collection search
	directCollections, err := c.SearchCollections(ctx, query)
	if err == nil {
		for _, col := range directCollections {
			collectionMap[col.TMDBID] = col
		}
	}

	// Strategy 2: Search by person (actor/director)
	personCollections, err := c.SearchPerson(ctx, query)
	if err == nil {
		for _, col := range personCollections {
			if _, exists := collectionMap[col.TMDBID]; !exists {
				collectionMap[col.TMDBID] = col
			}
		}
	}

	// Strategy 3: Search movies and extract collections (if we don't have enough results)
	if len(collectionMap) < 10 {
		movieCollections, err := c.SearchMoviesForCollections(ctx, query, 2)
		if err == nil {
			for _, col := range movieCollections {
				if _, exists := collectionMap[col.TMDBID]; !exists {
					collectionMap[col.TMDBID] = col
				}
			}
		}
	}

	// Convert to slice
	collections := make([]*models.Collection, 0, len(collectionMap))
	for _, c := range collectionMap {
		collections = append(collections, c)
	}

	return collections, nil
}

// getCollectionsFromMovieIDs extracts collection info from a set of movie IDs
func (c *TMDBClient) getCollectionsFromMovieIDs(ctx context.Context, movieIDs map[int]bool) ([]*models.Collection, error) {
	collectionMap := make(map[int]*models.Collection)

	// Process movies in parallel with limited concurrency
	type result struct {
		collection *models.Collection
	}

	results := make(chan result, len(movieIDs))
	semaphore := make(chan struct{}, 20) // Increased concurrent requests

	for movieID := range movieIDs {
		go func(id int) {
			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release

			_, collection, err := c.GetMovieWithCollection(ctx, id)
			if err == nil && collection != nil {
				results <- result{collection: collection}
			} else {
				results <- result{collection: nil}
			}
		}(movieID)
	}

	// Collect results
	for i := 0; i < len(movieIDs); i++ {
		res := <-results
		if res.collection != nil {
			if _, exists := collectionMap[res.collection.TMDBID]; !exists {
				collectionMap[res.collection.TMDBID] = res.collection
			}
		}
	}

	collections := make([]*models.Collection, 0, len(collectionMap))
	for _, c := range collectionMap {
		collections = append(collections, c)
	}

	return collections, nil
}

// GetNowPlaying returns movies currently in theaters or TV shows on air
func (c *TMDBClient) GetNowPlaying(ctx context.Context, mediaType string) ([]TrendingItem, error) {
	var endpoint string
	if mediaType == "movie" {
		endpoint = fmt.Sprintf("%s/movie/now_playing", tmdbBaseURL)
	} else {
		endpoint = fmt.Sprintf("%s/tv/on_the_air", tmdbBaseURL)
	}

	params := url.Values{}
	params.Set("api_key", c.apiKey)

	data, err := c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			Overview     string  `json:"overview"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal now playing: %w", err)
	}

	items := make([]TrendingItem, 0, len(result.Results))
	for _, r := range result.Results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		releaseDate := r.ReleaseDate
		if releaseDate == "" {
			releaseDate = r.FirstAirDate
		}
		items = append(items, TrendingItem{
			ID:           r.ID,
			Title:        title,
			MediaType:    mediaType,
			PosterPath:   r.PosterPath,
			BackdropPath: r.BackdropPath,
			Overview:     r.Overview,
			ReleaseDate:  releaseDate,
			VoteAverage:  r.VoteAverage,
		})
	}

	return items, nil
}
