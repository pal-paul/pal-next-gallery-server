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
		{candidate: moments.Candidate{ID: "third", CapturedAt: capturedAt.Add(2 * time.Minute)}, histogram: firstHistogram, hash: 1, sharpness: 0.5},
		{candidate: moments.Candidate{ID: "different", CapturedAt: capturedAt.Add(3 * time.Minute)}, histogram: differentHistogram, hash: ^uint64(1), sharpness: 0.5},
	})

	if len(groups) != 2 || len(groups[0]) != 3 || len(groups[1]) != 1 {
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

func TestClusterDoesNotChainEventsThroughBridgeImage(t *testing.T) {
	capturedAt := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	groups := cluster([]imageFeatures{
		{candidate: moments.Candidate{ID: "first", CapturedAt: capturedAt, Embedding: []float64{1, 0}}, hash: 0},
		{candidate: moments.Candidate{ID: "bridge", CapturedAt: capturedAt, Embedding: []float64{1, 1}}, hash: (uint64(1) << 29) - 1},
		{candidate: moments.Candidate{ID: "second", CapturedAt: capturedAt, Embedding: []float64{0, 1}}, hash: ^uint64(0)},
	})

	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 1 {
		t.Fatalf("bridge image chained distinct events: %#v", groups)
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

func TestCosineSimilarityNormalizesToUnitInterval(t *testing.T) {
	if similarity, ok := cosineSimilarity([]float64{1, 0}, []float64{1, 0}); !ok || similarity != 1 {
		t.Fatalf("identical vectors: similarity=%f ok=%t", similarity, ok)
	}
	if similarity, ok := cosineSimilarity([]float64{1, 0}, []float64{0, 1}); !ok || similarity != 0 {
		t.Fatalf("orthogonal vectors: similarity=%f ok=%t", similarity, ok)
	}
	if similarity, ok := cosineSimilarity([]float64{1, 0}, []float64{-1, 0}); !ok || similarity != 0 {
		t.Fatalf("opposite vectors: similarity=%f ok=%t", similarity, ok)
	}
	if _, ok := cosineSimilarity([]float64{1}, []float64{1, 0}); ok {
		t.Fatal("mismatched vectors should not produce a similarity")
	}
}

func TestExposureQualityPenalizesClippedImages(t *testing.T) {
	balanced := exposureQuality([]byte{96, 112, 128, 144, 160})
	clipped := exposureQuality([]byte{0, 0, 0, 255, 255})
	if balanced <= clipped {
		t.Fatalf("balanced exposure %f should exceed clipped exposure %f", balanced, clipped)
	}
}

func TestDiverseRepresentativeSelectionIncludesDifferentImage(t *testing.T) {
	capturedAt := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	commonHistogram := [48]float64{0: 1}
	differentHistogram := [48]float64{47: 1}
	features := make([]imageFeatures, 10)
	indexes := make([]int, 10)
	group := make([]moments.Candidate, 10)
	for index := range features {
		id := string(rune('a' + index))
		histogram := commonHistogram
		if index == 9 {
			histogram = differentHistogram
		}
		hash := uint64(0)
		if index == 9 {
			hash = ^uint64(0)
		}
		features[index] = imageFeatures{candidate: moments.Candidate{ID: id, CapturedAt: capturedAt}, histogram: histogram, hash: hash}
		indexes[index] = index
		group[index] = moments.Candidate{ID: id, RepresentativeScore: 0.8}
	}

	selected := selectDiverseRepresentatives(features, indexes, group, 5)
	if _, ok := selected["j"]; !ok {
		t.Fatalf("visually different candidate was not selected: %#v", selected)
	}
}
