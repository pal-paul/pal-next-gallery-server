//go:build opencv

package main

import (
	"testing"
	"time"

	"pal-next-gallery-server/app/moments"
)

func TestClusterGroupsSimilarImagesAndSelectsRepresentative(t *testing.T) {
	capturedAt := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	firstHistogram := [48]float64{0: 1}
	differentHistogram := [48]float64{47: 1}
	groups := cluster([]imageFeatures{
		{candidate: moments.Candidate{ID: "first", CapturedAt: capturedAt}, histogram: firstHistogram, hash: 1, sharpness: 0.2},
		{candidate: moments.Candidate{ID: "sharp", CapturedAt: capturedAt.Add(time.Minute)}, histogram: firstHistogram, hash: 1, sharpness: 0.9},
		{candidate: moments.Candidate{ID: "different", CapturedAt: capturedAt.Add(2 * time.Minute)}, histogram: differentHistogram, hash: ^uint64(1), sharpness: 0.5},
	})

	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 1 {
		t.Fatalf("unexpected clusters: %#v", groups)
	}
	representativeCount := 0
	representativeID := ""
	for _, candidate := range groups[0] {
		if candidate.Representative {
			representativeCount++
			representativeID = candidate.ID
		}
	}
	if representativeCount != 1 || representativeID != "sharp" {
		t.Fatalf("unexpected representative: count=%d id=%q", representativeCount, representativeID)
	}
}

func TestRepresentativeTargetScalesBetweenFiveAndFifteen(t *testing.T) {
	tests := []struct {
		groupSize int
		want      int
	}{
		{groupSize: 3, want: 1},
		{groupSize: 10, want: 5},
		{groupSize: 100, want: 10},
		{groupSize: 350, want: 15},
	}

	for _, test := range tests {
		if got := representativeTarget(test.groupSize); got != test.want {
			t.Errorf("representativeTarget(%d) = %d, want %d", test.groupSize, got, test.want)
		}
	}
}
