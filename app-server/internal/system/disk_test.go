package system

import "testing"

// TestStatfsProbe 验证容量探测返回自洽的数字（Darwin 本机、Linux 容器内实测）。
func TestStatfsProbe(t *testing.T) {
	space, err := Statfs(t.TempDir())
	if err != nil {
		t.Fatalf("Statfs: %v", err)
	}
	if space.TotalBytes <= 0 {
		t.Fatalf("total bytes = %d, want > 0", space.TotalBytes)
	}
	if space.AvailableBytes < 0 || space.FreeBytes < 0 {
		t.Fatalf("free/available negative: %+v", space)
	}
	if space.AvailableBytes > space.TotalBytes {
		t.Fatalf("available %d exceeds total %d", space.AvailableBytes, space.TotalBytes)
	}
	if _, err := Statfs(""); err == nil {
		t.Fatal("Statfs(\"\") should fail")
	}
}
