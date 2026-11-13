package main

import (
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// TestNewMediaProberSelection 锁定构造顺序：显式宿主 FFPROBE_BIN 优先；
// 其次合法 WSL 配置；否则现有宿主 PATH 回退。非法 WSL 配置不得阻止 Core 启动。
func TestNewMediaProberSelection(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantWSL bool
	}{
		{
			name: "explicit host binary wins",
			env: map[string]string{
				"FFPROBE_BIN":           "/host/bin/ffprobe",
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "yaomu",
			},
			wantWSL: false,
		},
		{
			name: "wsl prober when host binary unset",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "yaomu",
			},
			wantWSL: true,
		},
		{
			name: "wsl exe override is accepted",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "Ubuntu-22.04",
				"YAOMU_WSL_EXE":         `C:\Windows\System32\wsl.exe`,
			},
			wantWSL: true,
		},
		{
			name: "blank host binary is ignored",
			env: map[string]string{
				"FFPROBE_BIN":           "   ",
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "yaomu",
			},
			wantWSL: true,
		},
		{
			name: "no wsl tool falls back to host path",
			env: map[string]string{
				"YAOMU_WSL_DISTRO": "yaomu",
			},
			wantWSL: false,
		},
		{
			name: "missing distro falls back",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
			},
			wantWSL: false,
		},
		{
			name: "unsafe distro falls back",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "-evil",
			},
			wantWSL: false,
		},
		{
			name: "relative tool path falls back",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "usr/local/bin/ffprobe",
				"YAOMU_WSL_DISTRO":      "yaomu",
			},
			wantWSL: false,
		},
		{
			name: "control character in tool path falls back",
			env: map[string]string{
				"YAOMU_WSL_FFPROBE_BIN": "/usr/local/bin/ff\nprobe",
				"YAOMU_WSL_DISTRO":      "yaomu",
			},
			wantWSL: false,
		},
		{
			name:    "empty environment uses host path fallback",
			env:     map[string]string{},
			wantWSL: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prober := newMediaProber(func(key string) string { return tc.env[key] })
			_, isWSL := prober.(*system.WSLFFprobeProber)
			if isWSL != tc.wantWSL {
				t.Fatalf("prober type WSL = %v, want %v", isWSL, tc.wantWSL)
			}
			if !isWSL {
				if _, ok := prober.(*system.FFprobeProber); !ok {
					t.Fatalf("fallback prober type = %T, want *system.FFprobeProber", prober)
				}
			}
		})
	}
}
