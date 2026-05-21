package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
)

func TestRealDebridInfringingFileBlocksVortexoSource(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"

	resetVortexoBlockedSourcesForTest()

	err := fmt.Errorf("failed to unrestrict link: %w", &services.RealDebridAPIError{
		StatusCode: 403,
		ErrorName:  "infringing_file",
		ErrorCode:  35,
	})
	if !isRealDebridBlockedPlaybackError(err) {
		t.Fatal("expected infringing_file Real-Debrid error to be classified as blocked")
	}

	markVortexoSourceBlocked(hash, "infringing_file")
	if !isVortexoSourceBlocked(hash) {
		t.Fatal("expected hash to be hidden after Real-Debrid rejected it")
	}
}

func TestExpiredVortexoBlockedSourceIsPruned(t *testing.T) {
	const hash = "fedcba9876543210fedcba9876543210fedcba98"

	resetVortexoBlockedSourcesForTest()
	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash[hash] = vortexoBlockedSource{
		Reason:    "infringing_file",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	vortexoBlockedSources.Unlock()

	if isVortexoSourceBlocked(hash) {
		t.Fatal("expected expired blocked source to be playable again")
	}

	vortexoBlockedSources.RLock()
	_, exists := vortexoBlockedSources.byHash[hash]
	vortexoBlockedSources.RUnlock()
	if exists {
		t.Fatal("expected expired blocked source to be pruned")
	}
}

func resetVortexoBlockedSourcesForTest() {
	vortexoBlockedSources.Lock()
	vortexoBlockedSources.byHash = make(map[string]vortexoBlockedSource)
	vortexoBlockedSources.Unlock()
}
