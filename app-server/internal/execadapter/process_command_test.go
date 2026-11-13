package execadapter

import (
	"os/exec"
	"testing"
)

func TestCommandCoversDetachedChildren(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"wsl.exe", false},
		{"wsl", false},
		{"WSL.EXE", false},
		{" wsl.exe ", false},
		{`C:\Windows\System32\wsl.exe`, false},
		{`C:/Windows/System32/wsl.exe`, false},
		{"/usr/bin/wsl", false},
		{"python.exe", true},
		{"python3", true},
		{`C:\Program Files\Python\python.exe`, true},
		{"wslconfig.exe", true},
		{"wsl-host.exe", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := commandCoversDetachedChildren(tc.command); got != tc.want {
				t.Fatalf("commandCoversDetachedChildren(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestProcessTreeLaunchCommandPrefersPath(t *testing.T) {
	cases := []struct {
		name string
		cmd  *exec.Cmd
		want string
	}{
		{"path", &exec.Cmd{Path: `C:\Windows\System32\wsl.exe`, Args: []string{"wsl.exe"}}, `C:\Windows\System32\wsl.exe`},
		{"args", &exec.Cmd{Args: []string{"wsl.exe"}}, "wsl.exe"},
		{"empty", &exec.Cmd{}, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processTreeLaunchCommand(tc.cmd); got != tc.want {
				t.Fatalf("processTreeLaunchCommand(%+v) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}
