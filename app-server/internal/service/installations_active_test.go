package service

// 更新准备需要“安装 worker 尾部”这一进程内拥有者事实：即使持久化记录已
// 终态，未退出的 worker 仍阻断封闸。这里用受控下载器覆盖边界。

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
)

type blockingDownloader struct {
	entered chan struct{}
	release chan struct{}
}

func (d *blockingDownloader) Fetch(ctx context.Context, _ string, _ downloader.File, _ func(int64, int64)) error {
	select {
	case d.entered <- struct{}{}:
	default:
	}
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *blockingDownloader) Verify(context.Context, string, downloader.File) error { return nil }
func (d *blockingDownloader) Commit(string, downloader.File) error                  { return nil }
func (d *blockingDownloader) Cleanup(string) error                                  { return nil }

// TestActiveInstallationWorkTracksWorkerTail 证明安装 worker 在退出前一直是
// 活跃拥有者，退出后归零。
func TestActiveInstallationWorkTracksWorkerTail(t *testing.T) {
	dl := &blockingDownloader{entered: make(chan struct{}, 1), release: make(chan struct{})}
	releases := StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: {
			Release: domain.ReleaseRef{ReleaseID: "rel-asr", Version: "1.0.0", Digest: "sha256-asr"},
			Files: []downloader.File{{
				RelPath: "model.bin", URL: "https://releases.invalid/model.bin",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
				Size:   4,
			}},
			DownloadBytes: 4,
		},
	}}
	e := newInstallTestEnv(t, releases, dl)
	if e.svc.ActiveInstallationWork() {
		t.Fatal("fresh service reported installation work")
	}

	inst := e.create(domain.CapabilitySlotASR)
	select {
	case <-dl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("installation worker did not start")
	}
	if !e.svc.ActiveInstallationWork() {
		t.Fatal("active installation worker not reported")
	}

	close(dl.release)
	e.waitTerminal(inst.ID)
	deadline := time.Now().Add(5 * time.Second)
	for e.svc.ActiveInstallationWork() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.svc.ActiveInstallationWork() {
		t.Fatal("installation worker tail retained after completion")
	}
}
