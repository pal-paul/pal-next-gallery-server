package store

import "testing"

func TestAutomaticAlbumPart(t *testing.T) {
	tests := []struct {
		part      int
		wantKey   string
		wantTitle string
	}{
		{part: 1, wantKey: "week:2026-10-05", wantTitle: "October 5 - 11"},
		{part: 2, wantKey: "week:2026-10-05:part:2", wantTitle: "October 5 - 11 (Part 2)"},
		{part: 4, wantKey: "week:2026-10-05:part:4", wantTitle: "October 5 - 11 (Part 4)"},
	}
	for _, test := range tests {
		key, title := automaticAlbumPart("week:2026-10-05", "October 5 - 11", test.part)
		if key != test.wantKey || title != test.wantTitle {
			t.Fatalf("part %d = %q, %q; want %q, %q", test.part, key, title, test.wantKey, test.wantTitle)
		}
	}
}

func TestPartitionMediaIDsCapsPartsAtOneHundred(t *testing.T) {
	mediaIDs := make([]string, 250)
	parts := partitionMediaIDs(mediaIDs, 100)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	if len(parts[0]) != 100 || len(parts[1]) != 100 || len(parts[2]) != 50 {
		t.Fatalf("part sizes = %d, %d, %d; want 100, 100, 50", len(parts[0]), len(parts[1]), len(parts[2]))
	}
}
