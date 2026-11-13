package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDirUsageDoesNotFollowSymlinks 确认用量统计不跟随符号链接到应用目录之外，
// 不把外部大文件计入应用占用（也不遍历外部目录）。
func TestDirUsageDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.bin")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	usage, err := dirUsage(root)
	if err != nil {
		t.Fatalf("dirUsage: %v", err)
	}
	if usage >= 1<<20 {
		t.Fatalf("dirUsage followed symlink outside root: %d bytes", usage)
	}
}

// TestStorageUsageCacheAndInvalidation 覆盖四类用量扫描、TTL 缓存与主动失效。
func TestStorageUsageCacheAndInvalidation(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	svc := New(Options{
		DataDir: t.TempDir(),
		Clock:   func() time.Time { return now },
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		t.Fatalf("ensure storage dirs: %v", err)
	}
	writeFile := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(svc.cacheDir(), name), make([]byte, size), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	writeFile("first.bin", 128)
	usage, err := svc.StorageUsage(context.Background())
	if err != nil {
		t.Fatalf("StorageUsage: %v", err)
	}
	if usage.CacheBytes != 128 {
		t.Fatalf("cache bytes = %d, want 128", usage.CacheBytes)
	}

	// TTL 内不重新扫描：新增文件在缓存过期前不可见。
	writeFile("second.bin", 256)
	now = now.Add(30 * time.Second)
	usage, err = svc.StorageUsage(context.Background())
	if err != nil {
		t.Fatalf("StorageUsage cached: %v", err)
	}
	if usage.CacheBytes != 128 {
		t.Fatalf("cached cache bytes = %d, want 128", usage.CacheBytes)
	}

	// 超过 TTL 后重新扫描。
	now = now.Add(31 * time.Second)
	usage, err = svc.StorageUsage(context.Background())
	if err != nil {
		t.Fatalf("StorageUsage expired: %v", err)
	}
	if usage.CacheBytes != 384 {
		t.Fatalf("rescanned cache bytes = %d, want 384", usage.CacheBytes)
	}

	// 主动失效（清理后调用）立即生效。
	writeFile("third.bin", 512)
	svc.InvalidateStorageUsage()
	usage, err = svc.StorageUsage(context.Background())
	if err != nil {
		t.Fatalf("StorageUsage invalidated: %v", err)
	}
	if usage.CacheBytes != 896 {
		t.Fatalf("invalidated cache bytes = %d, want 896", usage.CacheBytes)
	}
}
