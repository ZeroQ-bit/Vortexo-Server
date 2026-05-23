package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenSubtitlesLanguageListDefaultsAndAliases(t *testing.T) {
	got := OpenSubtitlesLanguageList("English, srp; pt-BR en-US")
	want := []string{"en", "sr", "pt-br"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("OpenSubtitlesLanguageList() = %#v, want %#v", got, want)
	}

	got = OpenSubtitlesLanguageList("")
	if len(got) != 1 || got[0] != "en" {
		t.Fatalf("OpenSubtitlesLanguageList(empty) = %#v, want [en]", got)
	}
}

func TestSRTToWebVTT(t *testing.T) {
	input := []byte("1\r\n00:00:01,250 --> 00:00:03,500\r\nHello\r\n")
	got := string(SRTToWebVTT(input))
	if !strings.HasPrefix(got, "WEBVTT\n\n") {
		t.Fatalf("SRTToWebVTT() missing WEBVTT header: %q", got)
	}
	if !strings.Contains(got, "00:00:01.250 --> 00:00:03.500") {
		t.Fatalf("SRTToWebVTT() did not convert timestamps: %q", got)
	}
}

func TestOpenSubtitlesFetchSubtitleWorkflow(t *testing.T) {
	var server *httptest.Server
	loginCount := 0
	searchCount := 0
	downloadCount := 0

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login":
			loginCount++
			if r.Header.Get("Api-Key") != "api-key" {
				t.Fatalf("login missing Api-Key header")
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"token":  "token",
				"status": 200,
			})
		case "/api/v1/subtitles":
			searchCount++
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("search missing Authorization header")
			}
			if got := r.URL.Query().Get("imdb_id"); got != "12345" {
				t.Fatalf("search imdb_id = %q, want 12345", got)
			}
			if got := r.URL.Query().Get("languages"); got != "en" {
				t.Fatalf("search languages = %q, want en", got)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id":   "1",
						"type": "subtitle",
						"attributes": map[string]interface{}{
							"language":       "en",
							"download_count": 10,
							"files": []map[string]interface{}{
								{"file_id": 42, "file_name": "movie.srt"},
							},
						},
					},
				},
			})
		case "/api/v1/download":
			downloadCount++
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("download missing Authorization header")
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"link":      server.URL + "/files/movie.srt",
				"file_name": "movie.srt",
				"remaining": 99,
			})
		case "/files/movie.srt":
			w.Header().Set("Content-Type", "application/x-subrip")
			w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewOpenSubtitlesClient(OpenSubtitlesConfig{
		Enabled:   true,
		APIKey:    "api-key",
		Username:  "user",
		Password:  "pass",
		BaseURL:   server.URL + "/api/v1",
		CacheDir:  t.TempDir(),
		Languages: "en",
	})

	got, err := client.FetchSubtitle(context.Background(), OpenSubtitlesSearchRequest{
		Type:     "movie",
		IMDBID:   "tt0012345",
		Query:    "A Movie",
		Language: "english",
		Format:   "vtt",
	})
	if err != nil {
		t.Fatalf("FetchSubtitle() error = %v", err)
	}
	if got.ContentType != "text/vtt; charset=utf-8" {
		t.Fatalf("ContentType = %q", got.ContentType)
	}
	if !strings.Contains(string(got.Content), "WEBVTT") {
		t.Fatalf("content was not converted to webvtt: %q", string(got.Content))
	}
	if loginCount != 1 || searchCount != 1 || downloadCount != 1 {
		t.Fatalf("unexpected API call counts login=%d search=%d download=%d", loginCount, searchCount, downloadCount)
	}

	cached, err := client.FetchSubtitle(context.Background(), OpenSubtitlesSearchRequest{
		Type:     "movie",
		IMDBID:   "tt0012345",
		Query:    "A Movie",
		Language: "en",
		Format:   "vtt",
	})
	if err != nil {
		t.Fatalf("cached FetchSubtitle() error = %v", err)
	}
	if !cached.FromCache {
		t.Fatalf("second FetchSubtitle() did not use cache")
	}
}
