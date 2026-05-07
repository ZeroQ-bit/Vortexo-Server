package streams

import "testing"

func TestParseHDRTypeDetectsDolbyVisionHDRHybrid(t *testing.T) {
	quality := ParseQualityFromTorrentName("Movie.2026.2160p.DV.HDR10.TrueHD.Atmos")

	if quality.HDRType != "DV+HDR" {
		t.Fatalf("expected DV+HDR, got %q", quality.HDRType)
	}
}

func TestParseHDRTypeDoesNotTreatDVDRipAsDolbyVision(t *testing.T) {
	quality := ParseQualityFromTorrentName("Movie.2001.DVDRip.XviD")

	if quality.HDRType != "SDR" {
		t.Fatalf("expected DVDRip to be SDR, got %q", quality.HDRType)
	}
}

func TestQualityTypeExcludedTreatsHDRAsAnyHDRBearingStream(t *testing.T) {
	excluded, reason := QualityTypeExcluded("Movie.2160p.DV.HDR10", "2160p", "DV+HDR", "hdr")

	if !excluded || reason != "HDR" {
		t.Fatalf("expected HDR exclusion to remove DV+HDR stream, excluded=%v reason=%q", excluded, reason)
	}
}

func TestQualityTypeExcludedDoesNotTreatHDRipAsHDR(t *testing.T) {
	excluded, reason := QualityTypeExcluded("Movie.2010.HDRip.XviD", "720p", "SDR", "hdr")

	if excluded {
		t.Fatalf("expected HDRip source to remain when excluding HDR video, reason=%q", reason)
	}
}
