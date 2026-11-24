package core

import (
	"path/filepath"
	"testing"
)

func TestDirHistory_AddWritesPrivateStore(t *testing.T) {
	dataDir := t.TempDir()
	NewDirHistory(dataDir).Add("demo", "/srv/demo")
	assertMode(t, filepath.Join(dataDir, dirHistoryFileName), 0o600)
}
