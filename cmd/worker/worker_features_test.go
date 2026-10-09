package main

import "testing"

func TestColorHistogramHasUnitMass(t *testing.T) {
	histogram := colorHistogram([]byte{0, 127, 255, 16, 128, 240})
	total := 0.0
	for _, value := range histogram {
		total += value
	}
	if total != 1 {
		t.Fatalf("histogram sum = %f, want 1", total)
	}
}
