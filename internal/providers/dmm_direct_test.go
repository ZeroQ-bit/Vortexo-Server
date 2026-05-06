package providers

import "testing"

func TestDMMTokenHashMatchesDMMAlgorithm(t *testing.T) {
	token := "1a2b3c4d"
	tokenWithTimestamp := token + "-1770000000"
	tokenTimestampHash := generateDMMHash(tokenWithTimestamp)
	tokenSaltHash := generateDMMHash("debridmediamanager.com%%fe7#td00rA3vHz%VmI-" + token)

	if tokenTimestampHash != "212f409f" {
		t.Fatalf("expected timestamp hash to match DMM algorithm, got %q", tokenTimestampHash)
	}
	if tokenSaltHash != "5a13883" {
		t.Fatalf("expected salt hash to match DMM algorithm, got %q", tokenSaltHash)
	}
	if combined := combineDMMHashes(tokenTimestampHash, tokenSaltHash); combined != "251a21f3388f904" {
		t.Fatalf("expected combined hash to match DMM algorithm, got %q", combined)
	}
}
