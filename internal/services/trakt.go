package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const traktBaseURL = "https://api.trakt.tv"

type TraktClient struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

type TraktAPIError struct {
	StatusCode  int    `json:"status_code"`
	ErrorCode   string `json:"error"`
	Description string `json:"error_description"`
	Body        string `json:"-"`
}

func (e *TraktAPIError) Error() string {
	if strings.TrimSpace(e.ErrorCode) != "" {
		if strings.TrimSpace(e.Description) != "" {
			return fmt.Sprintf("trakt %s: %s", e.ErrorCode, e.Description)
		}
		return fmt.Sprintf("trakt %s", e.ErrorCode)
	}
	if strings.TrimSpace(e.Body) != "" {
		return fmt.Sprintf("trakt returned HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("trakt returned HTTP %d", e.StatusCode)
}

type TraktDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type TraktToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	CreatedAt    int64  `json:"created_at"`
}

type TraktIDs struct {
	Trakt int    `json:"trakt"`
	Slug  string `json:"slug"`
	IMDB  string `json:"imdb"`
	TMDB  int    `json:"tmdb"`
	TVDB  int    `json:"tvdb"`
}

type TraktMovie struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   TraktIDs `json:"ids"`
}

type TraktShow struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   TraktIDs `json:"ids"`
}

type TraktWatchedMovie struct {
	Plays         int        `json:"plays"`
	LastWatchedAt *time.Time `json:"last_watched_at"`
	LastUpdatedAt *time.Time `json:"last_updated_at"`
	Movie         TraktMovie `json:"movie"`
}

type TraktWatchedEpisode struct {
	Number        int        `json:"number"`
	Plays         int        `json:"plays"`
	LastWatchedAt *time.Time `json:"last_watched_at"`
}

type TraktWatchedSeason struct {
	Number   int                   `json:"number"`
	Episodes []TraktWatchedEpisode `json:"episodes"`
}

type TraktWatchedShow struct {
	Plays         int                  `json:"plays"`
	LastWatchedAt *time.Time           `json:"last_watched_at"`
	LastUpdatedAt *time.Time           `json:"last_updated_at"`
	Show          TraktShow            `json:"show"`
	Seasons       []TraktWatchedSeason `json:"seasons"`
}

type TraktWatchlistItem struct {
	ID       int        `json:"id"`
	Rank     int        `json:"rank"`
	Type     string     `json:"type"`
	ListedAt *time.Time `json:"listed_at"`
	Movie    TraktMovie `json:"movie"`
	Show     TraktShow  `json:"show"`
}

func NewTraktClient(clientID, clientSecret string) *TraktClient {
	return &TraktClient{
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *TraktClient) Configured() bool {
	return strings.TrimSpace(c.clientID) != "" && strings.TrimSpace(c.clientSecret) != ""
}

func (c *TraktClient) HasClientID() bool {
	return strings.TrimSpace(c.clientID) != ""
}

func (c *TraktClient) StartDeviceAuth(ctx context.Context) (*TraktDeviceCode, error) {
	var result TraktDeviceCode
	err := c.doJSON(ctx, http.MethodPost, "/oauth/device/code", "", map[string]string{
		"client_id": c.clientID,
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TraktClient) ExchangeDeviceCode(ctx context.Context, deviceCode string) (*TraktToken, error) {
	var result TraktToken
	err := c.doJSON(ctx, http.MethodPost, "/oauth/device/token", "", map[string]string{
		"code":          strings.TrimSpace(deviceCode),
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TraktClient) RefreshToken(ctx context.Context, refreshToken string) (*TraktToken, error) {
	var result TraktToken
	err := c.doJSON(ctx, http.MethodPost, "/oauth/token", "", map[string]string{
		"refresh_token": strings.TrimSpace(refreshToken),
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "refresh_token",
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *TraktClient) GetWatchedMovies(ctx context.Context, accessToken string) ([]TraktWatchedMovie, error) {
	var result []TraktWatchedMovie
	err := c.doJSON(ctx, http.MethodGet, "/sync/watched/movies", accessToken, nil, &result)
	return result, err
}

func (c *TraktClient) GetWatchedShows(ctx context.Context, accessToken string) ([]TraktWatchedShow, error) {
	var result []TraktWatchedShow
	err := c.doJSON(ctx, http.MethodGet, "/sync/watched/shows", accessToken, nil, &result)
	return result, err
}

func (c *TraktClient) GetWatchlistMovies(ctx context.Context, accessToken string) ([]TraktWatchlistItem, error) {
	var result []TraktWatchlistItem
	err := c.doJSON(ctx, http.MethodGet, "/sync/watchlist/movies/added", accessToken, nil, &result)
	return result, err
}

func (c *TraktClient) GetWatchlistShows(ctx context.Context, accessToken string) ([]TraktWatchlistItem, error) {
	var result []TraktWatchlistItem
	err := c.doJSON(ctx, http.MethodGet, "/sync/watchlist/shows/added", accessToken, nil, &result)
	return result, err
}

func (c *TraktClient) doJSON(ctx context.Context, method, path, accessToken string, payload interface{}, dest interface{}) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, traktBaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.clientID)
	if strings.TrimSpace(accessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &TraktAPIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
		_ = json.Unmarshal(data, apiErr)
		return apiErr
	}

	if dest == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}

func TraktTokenExpiresAt(token *TraktToken, fallback time.Time) *time.Time {
	if token == nil || token.ExpiresIn <= 0 {
		return nil
	}
	base := fallback
	if token.CreatedAt > 0 {
		base = time.Unix(token.CreatedAt, 0)
	}
	expiresAt := base.Add(time.Duration(token.ExpiresIn) * time.Second)
	return &expiresAt
}
