package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectSymlinkPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "attempt", "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "out.srt")
	if err := os.WriteFile(file, []byte("1\n00:00:00,000 --> 00:00:01,000\nx\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := rejectSymlinkPath(root, file); err != nil {
		t.Fatalf("regular path rejected: %v", err)
	}
	if err := rejectSymlinkPath(root, filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("outside path accepted")
	}

	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Join(root, "attempt"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := rejectSymlinkPath(root, filepath.Join(link, "nested", "out.srt")); err == nil {
		t.Fatal("symlinked parent accepted")
	}
}
