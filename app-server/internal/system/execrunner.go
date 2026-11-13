package system

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// ExecRunner 使用 os/exec 执行固定命令；不经过 shell，并限制 stdout 大小。
type ExecRunner struct{}

// Run 实现 CommandRunner。
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > probeOutputLimit {
		return 0, errProbeOutputLimit
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}
