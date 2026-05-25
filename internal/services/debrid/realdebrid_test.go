package debrid

import (
	"errors"
	"testing"
	"time"
)

func TestIsValidTorrentHash(t *testing.T) {
	valid := "abcdef1234567890abcdef1234567890abcdef12"
	if !isValidTorrentHash(valid) {
		t.Fatal("expected 40-character hex hash to be valid")
	}
	if !isValidTorrentHash("urn:btih:" + valid) {
		t.Fatal("expected btih-prefixed hash to be valid")
	}
	if isValidTorrentHash("not-a-torrent-hash") {
		t.Fatal("expected non-hex hash to be invalid")
	}
	if isValidTorrentHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("expected v2-sized hash to be rejected for Real-Debrid btih magnet creation")
	}
}

func TestTerminalTorrentStatusIncludesMagnetError(t *testing.T) {
	if !isTerminalTorrentStatus("magnet_error") {
		t.Fatal("expected magnet_error to be terminal")
	}
	if isTerminalTorrentStatus("waiting_files_selection") {
		t.Fatal("expected waiting_files_selection to be selectable")
	}
}

func TestRateLimitErrorCarriesRetryAfter(t *testing.T) {
	err := &RateLimitError{
		Operation:  "add magnet",
		RetryAfter: 7 * time.Minute,
		Body:       `{"error":"too_many_requests","error_code":34}`,
	}
	wrapped := errors.New("other")
	if IsRateLimitError(wrapped) {
		t.Fatal("unexpected rate-limit classification for unrelated error")
	}
	if !IsRateLimitError(err) {
		t.Fatal("expected structured rate-limit error to be recognized")
	}
	if got := RateLimitRetryAfter(err); got != 7*time.Minute {
		t.Fatalf("retry delay = %s, want 7m", got)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("120"); got != 2*time.Minute {
		t.Fatalf("retry-after = %s, want 2m", got)
	}
}
