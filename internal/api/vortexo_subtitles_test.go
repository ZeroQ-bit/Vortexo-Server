package api

import "testing"

func TestSerbianLatinTransliteration(t *testing.T) {
	got := transliterateSerbianCyrillicToLatin("Љубав и ђак: њихов џеп је пун чаја.")
	want := "Ljubav i đak: njihov džep je pun čaja."
	if got != want {
		t.Fatalf("transliterateSerbianCyrillicToLatin() = %q, want %q", got, want)
	}
}

func TestCroatianBosnianTargetsUseSerbianLatin(t *testing.T) {
	for _, lang := range []string{"hr", "hr-HR", "Croatian", "bs", "bosnian", "sr", "Serbian"} {
		if !shouldTransliterateSerbianLatin(lang) {
			t.Fatalf("shouldTransliterateSerbianLatin(%q) = false, want true", lang)
		}
	}
}
