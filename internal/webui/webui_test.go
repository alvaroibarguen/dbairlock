package webui

import (
	"io/fs"
	"testing"
)

func TestFSHasKeep(t *testing.T) {
	if _, err := fs.Stat(FS(), ".keep"); err != nil {
		t.Fatalf("placeholder missing from embedded dist: %v", err)
	}
}
