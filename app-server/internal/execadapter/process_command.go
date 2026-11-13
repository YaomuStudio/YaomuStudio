package execadapter

import (
	"os/exec"
	"strings"
)

// commandCoversDetachedChildren reports whether killing the process tree rooted
// at command also stops descendants that detach into their own process group.
//
// A Windows Job Object contains Windows processes only. wsl.exe itself is a
// Windows process, but Linux processes started inside WSL2 are not Job members,
// so terminating wsl.exe cannot confirm that the execution tree stopped. Other
// commands (for example the host Python Runner) stay in the Job together with
// their Windows descendants.
func commandCoversDetachedChildren(command string) bool {
	name := strings.TrimSpace(command)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	switch strings.ToLower(name) {
	case "wsl", "wsl.exe":
		return false
	default:
		return true
	}
}

// processTreeLaunchCommand is the command identity used by attachProcessTree.
// exec.Command always sets Path; Args[0] is the fallback for a manually built Cmd.
func processTreeLaunchCommand(cmd *exec.Cmd) string {
	if cmd == nil {
		return ""
	}
	if cmd.Path != "" {
		return cmd.Path
	}
	if len(cmd.Args) > 0 {
		return cmd.Args[0]
	}
	return ""
}
