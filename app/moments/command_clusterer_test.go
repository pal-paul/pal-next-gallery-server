package moments

import (
	"strings"
	"testing"
)

func TestRestoreClusterResponsePreservesTrustedCandidateData(t *testing.T) {
	originals := []Candidate{{ID: "media-1", SourcePath: "user/photo.jpg"}}
	groups, err := restoreClusterResponse(originals, [][]Candidate{{{
		ID: "media-1", SourcePath: "/untrusted/photo.jpg", Representative: true, RepresentativeScore: 0.9,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result := groups[0][0]
	if result.SourcePath != "user/photo.jpg" || !result.Representative || result.RepresentativeScore != 0.9 {
		t.Fatalf("unexpected restored candidate: %#v", result)
	}
}

func TestRestoreClusterResponseRejectsIncompleteResults(t *testing.T) {
	originals := []Candidate{{ID: "media-1"}, {ID: "media-2"}}
	_, err := restoreClusterResponse(originals, [][]Candidate{{{ID: "media-1"}}})
	if err == nil || !strings.Contains(err.Error(), "omitted 1") {
		t.Fatalf("expected omitted candidate error, got %v", err)
	}
}
