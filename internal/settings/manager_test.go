package settings

import "testing"

func TestApplyLegacySettingAliasesMapsRealDebridToken(t *testing.T) {
	cfg := getDefaultSettings()

	err := applyLegacySettingAliases([]byte(`{"realdebrid_token":"legacy-rd-token"}`), cfg)
	if err != nil {
		t.Fatalf("expected legacy alias parsing to succeed, got %v", err)
	}

	if cfg.RealDebridAPIKey != "legacy-rd-token" {
		t.Fatalf("expected legacy Real-Debrid token to populate API key, got %q", cfg.RealDebridAPIKey)
	}
}

func TestParseBoolValueSupportsAdminPayloads(t *testing.T) {
	cases := []struct {
		name     string
		input    interface{}
		wantVal  bool
		wantOK   bool
	}{
		{name: "bool", input: true, wantVal: true, wantOK: true},
		{name: "string true", input: "true", wantVal: true, wantOK: true},
		{name: "string false", input: "false", wantVal: false, wantOK: true},
		{name: "number one", input: float64(1), wantVal: true, wantOK: true},
		{name: "number zero", input: float64(0), wantVal: false, wantOK: true},
		{name: "bad string", input: "not-bool", wantOK: false},
		{name: "untyped", input: struct{}{}, wantOK: false},
	}

	for _, tc := range cases {
		gotVal, ok := parseBoolValue(tc.input)
		if ok != tc.wantOK {
			t.Fatalf("%s: expected ok=%v got %v (value=%v)", tc.name, tc.wantOK, ok, gotVal)
		}
		if !ok {
			continue
		}
		if gotVal != tc.wantVal {
			t.Fatalf("%s: expected value=%v got %v", tc.name, tc.wantVal, gotVal)
		}
	}
}
