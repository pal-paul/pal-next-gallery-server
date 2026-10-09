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

func TestAlbumListFallsBackToNewestAccessibleMediaForCover(t *testing.T) {
	for _, fragment := range []string{
		"ARRAY_AGG(m.upload_id ORDER BY COALESCE(exif.captured_at, m.created_at) DESC",
		"FILTER (WHERE m.deleted_at IS NULL",
		"LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id",
	} {
		if !strings.Contains(listAlbumsQuery, fragment) {
			t.Errorf("album list query does not contain %q", fragment)
		}
	}
}

func TestSchemaCreatesMomentsSeparatelyFromAlbums(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS moments",
		"CREATE TABLE IF NOT EXISTS moment_media",
		"REFERENCES moments(id) ON DELETE CASCADE",
		"REFERENCES media_uploads(upload_id) ON DELETE CASCADE",
		"user_edited BOOLEAN NOT NULL DEFAULT FALSE",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("moments schema does not contain %q", statement)
		}
	}
}
