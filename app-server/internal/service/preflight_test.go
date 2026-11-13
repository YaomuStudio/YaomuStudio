package service

import (
	"errors"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

func fakeStatfs(space system.DiskSpace, err error) func(string) (system.DiskSpace, error) {
	return func(string) (system.DiskSpace, error) { return space, err }
}

// TestRequiredDiskBytes 覆盖空间估算（输入 ×3，未知输入不估算）。
func TestRequiredDiskBytes(t *testing.T) {
	tests := []struct {
		name  string
		input int64
		want  int64
	}{
		{"unknown input", 0, 0},
		{"negative input", -1, 0},
		{"small input", 1 << 20, 3 << 20},
	}
	for _, tt := range tests {
		if got := requiredDiskBytes(tt.input); got != tt.want {
			t.Errorf("%s: requiredDiskBytes(%d) = %d, want %d", tt.name, tt.input, got, tt.want)
		}
	}
}

// TestDiskIssues 表驱动覆盖健康线警告、永久性不足阻断与探测失败。
func TestDiskIssues(t *testing.T) {
	const gib = int64(1) << 30
	tests := []struct {
		name       string
		space      system.DiskSpace
		probeErr   error
		required   int64
		wantCount  int
		wantSev    domain.PreflightSeverity
		wantCode   string
		wantAction bool
	}{
		{
			name:      "healthy",
			space:     system.DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 40 * gib},
			required:  3 * gib,
			wantCount: 0,
		},
		{
			name:       "below health line warns",
			space:      system.DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 20 * gib},
			required:   3 * gib,
			wantCount:  1,
			wantSev:    domain.PreflightWarning,
			wantCode:   string(domain.ErrorCodeInsufficientDisk),
			wantAction: true,
		},
		{
			name:       "required exceeds available blocks",
			space:      system.DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 2 * gib},
			required:   6 * gib,
			wantCount:  1,
			wantSev:    domain.PreflightBlocker,
			wantCode:   string(domain.ErrorCodeInsufficientDisk),
			wantAction: true,
		},
		{
			name:      "probe failure warns only",
			probeErr:  errors.New("statfs unavailable"),
			required:  3 * gib,
			wantCount: 1,
			wantSev:   domain.PreflightWarning,
			wantCode:  "DISK_PROBE_FAILED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(Options{
				DataDir:         t.TempDir(),
				DiskHealthBytes: 30 * gib,
				Statfs:          fakeStatfs(tt.space, tt.probeErr),
			})
			issues := svc.diskIssues("data", t.TempDir(), tt.required)
			if len(issues) != tt.wantCount {
				t.Fatalf("issues = %+v, want %d", issues, tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			issue := issues[0]
			if issue.Severity != tt.wantSev || issue.Code != tt.wantCode {
				t.Fatalf("issue = %+v, want severity=%s code=%s", issue, tt.wantSev, tt.wantCode)
			}
			if tt.wantAction && len(issue.ActionIDs) == 0 {
				t.Fatalf("issue %+v missing action ids", issue)
			}
		})
	}
}

// TestCheckDiskSpace 覆盖写前准入：不足返回 INSUFFICIENT_DISK，探测失败不阻断。
func TestCheckDiskSpace(t *testing.T) {
	const gib = int64(1) << 30
	svc := New(Options{
		DataDir: t.TempDir(),
		Statfs:  fakeStatfs(system.DiskSpace{TotalBytes: 10 * gib, AvailableBytes: 1 * gib}, nil),
	})
	if err := svc.checkDiskSpace("export", t.TempDir(), 500<<20); err != nil {
		t.Fatalf("fits: %v", err)
	}
	err := svc.checkDiskSpace("export", t.TempDir(), 2*gib)
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeInsufficientDisk {
		t.Fatalf("err = %v, want INSUFFICIENT_DISK", err)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.ErrorCodeInsufficientDisk {
		t.Fatalf("err = %v, want domain INSUFFICIENT_DISK", err)
	}
	if len(domainErr.ActionIDs) == 0 {
		t.Fatal("insufficient disk error missing action ids")
	}

	broken := New(Options{
		DataDir: t.TempDir(),
		Statfs:  fakeStatfs(system.DiskSpace{}, errors.New("probe failed")),
	})
	if err := broken.checkDiskSpace("export", t.TempDir(), 1<<40); err != nil {
		t.Fatalf("probe failure must not block: %v", err)
	}
}

// TestHasBlockerCode 确认只有指定原因码的 blocker 命中。
func TestHasBlockerCode(t *testing.T) {
	result := domain.PreflightResult{Issues: []domain.PreflightIssue{
		{Severity: domain.PreflightWarning, Code: string(domain.ErrorCodeInsufficientDisk)},
	}}
	if hasBlockerCode(result, domain.ErrorCodeInsufficientDisk) {
		t.Fatal("warning must not count as blocker")
	}
	result.Issues = append(result.Issues, domain.PreflightIssue{
		Severity: domain.PreflightBlocker, Code: string(domain.ErrorCodeInsufficientDisk),
	})
	if !hasBlockerCode(result, domain.ErrorCodeInsufficientDisk) {
		t.Fatal("blocker not detected")
	}
	if hasBlockerCode(result, domain.ErrorCodeModelNotInstalled) {
		t.Fatal("unrelated code matched")
	}
}
