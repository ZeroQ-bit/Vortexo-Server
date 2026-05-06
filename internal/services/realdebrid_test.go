package services

import (
	"errors"
	"testing"
)

func TestRealDebridAPIErrorDisabledEndpoint(t *testing.T) {
	err := &RealDebridAPIError{
		StatusCode: 403,
		ErrorName:  "disabled_endpoint",
		ErrorCode:  37,
	}

	if !errors.Is(err, ErrRealDebridDisabledEndpoint) {
		t.Fatal("expected disabled endpoint sentinel to match")
	}
}
