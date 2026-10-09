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

func TestSchemaPersistsPreprocessingAnalysis(t *testing.T) {
	for _, statement := range []string{
		"camera_make TEXT NOT NULL DEFAULT ''",
		"camera_model TEXT NOT NULL DEFAULT ''",
		"orientation TEXT NOT NULL DEFAULT ''",
		"perceptual_hash TEXT NOT NULL DEFAULT ''",
		"media_exif_perceptual_hash_idx",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("preprocessing schema does not contain %q", statement)
		}
	}
}

func TestSchemaCreatesNearDuplicateGroups(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS media_duplicate_groups",
		"CREATE TABLE IF NOT EXISTS media_duplicate_group_members",
		"hamming_distance INTEGER NOT NULL",
		"media_duplicate_primary_idx",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("duplicate schema does not contain %q", statement)
		}
	}
}

func TestDifferenceHashDistance(t *testing.T) {
	distance, err := differenceHashDistance("0000000000000000", "0000000000000007")
	if err != nil {
		t.Fatal(err)
	}
	if distance != 3 {
		t.Fatalf("unexpected Hamming distance: %d", distance)
	}
}

func TestSchemaPersistsVersionedImageEmbeddings(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS media_embeddings",
		"embedding DOUBLE PRECISION[] NOT NULL",
		"PRIMARY KEY (upload_id, model, version)",
		"array_length(embedding, 1) = dimensions",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("embedding schema does not contain %q", statement)
		}
	}
}

func TestSchemaPersistsStructuredImageDescriptions(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS media_image_descriptions",
		"people TEXT[] NOT NULL",
		"activities TEXT[] NOT NULL",
		"location_type TEXT NOT NULL",
		"weather TEXT NOT NULL",
		"PRIMARY KEY (upload_id, model, version)",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("image description schema does not contain %q", statement)
		}
	}
}
