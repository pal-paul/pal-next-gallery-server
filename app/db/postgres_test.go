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
