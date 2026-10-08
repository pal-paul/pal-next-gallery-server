package store

import (
	"strings"
	"testing"
)

func TestSchemaUpgradesExistingAlbumsTable(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS automatic",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS auto_key",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS position",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS cover_media_id",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("schema does not contain %q", statement)
		}
	}
}

func TestSchemaUpgradesSharesWithReadPermission(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE user_media_shares ADD COLUMN IF NOT EXISTS permission TEXT NOT NULL DEFAULT 'read'",
		"CHECK (permission IN ('read', 'write'))",
		"trash_retention_days INTEGER NOT NULL DEFAULT 30",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("schema does not contain %q", statement)
		}
	}
}

func TestSharedTrashRequiresWritePermission(t *testing.T) {
	if !strings.Contains(trashMediaQuery, "share.permission = 'write'") {
		t.Fatal("shared trash query does not require write permission")
	}
}

func TestGalleryUsesCaptureTimeWithUploadFallback(t *testing.T) {
	effectiveDate := "COALESCE(exif.captured_at, m.created_at)"
	if !strings.Contains(mediaSelect, effectiveDate) {
		t.Fatal("gallery media date does not prefer capture time")
	}
}
