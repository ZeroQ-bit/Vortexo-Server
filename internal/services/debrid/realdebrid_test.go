package debrid

import "testing"

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
