package execadapter

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/executionlog"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

const runnerStderrSuppressedNotice = "Runner stderr 输出已省略（内容不记录）"

// Transport 是帧传输抽象：生产实现是子进程 stdio，测试可用内存管道。
type Transport interface {
	Send(Frame) error
	// Receive 阻塞读取下一帧；返回 io.EOF 表示对端关闭。
	Receive() (Frame, error)
	// Kill 只终止本应用拥有的进程树，并等待其退出确认（尽力而为）。
	Kill() error
	Close() error
}

// ProcessOptions 是拉起 Runner 子进程的参数。
type ProcessOptions struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
	Logger  *logging.Logger
	// StderrLimit 是单行 stderr 保留上限（默认 2000 字节，脱敏后写日志）。
	StderrLimit int
	// DetachedChildrenUncovered 表示该启动器可能有宿主 Job 覆盖不到的分离
	// 后代（如 WSL2 Linux 进程）。模型侧 WSL 路由必须显式设置，不能只靠
	// 可执行文件名推断包装器后的实际运行环境。
	DetachedChildrenUncovered bool
}

// ProcessTransport 是以 stdio 传输协议帧的 Runner 子进程。
type ProcessTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu  sync.Mutex
	done     chan struct{}
	waitErr  error
	killOnce sync.Once
	killErr  error
	identity string
	tree     *processTree

	log         *logging.Logger
	stderrLimit int
	stderrOnce  sync.Once
}

// StartProcess 拉起 Runner 子进程。stderr 逐行按受控摘要过滤后写入本地诊断；
// 无法完整匹配的内容不落正文，只报一次固定省略提示。
func StartProcess(ctx context.Context, opts ProcessOptions) (*ProcessTransport, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("execadapter: command required")
	}
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("runner stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("runner stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("runner stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start runner: %w", err)
	}
	tree, err := attachProcessTree(cmd, opts.DetachedChildrenUncovered)
	if err != nil {
		// The controlled Runner has received no hello/execute yet, so no media
		// task can have started before attachment. Reject unsupported nesting.
		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		return nil, errors.Join(fmt.Errorf("attach runner process tree: %w", err), killErr, waitErr)
	}

	limit := opts.StderrLimit
	if limit <= 0 {
		limit = 2000
	}
	t := &ProcessTransport{
		cmd:         cmd,
		stdin:       stdin,
		stdout:      bufio.NewReaderSize(stdout, 64*1024),
		done:        make(chan struct{}),
		log:         opts.Logger,
		stderrLimit: limit,
		identity:    fmt.Sprintf("runner:%d:%s", cmd.Process.Pid, randomLaunchID()),
		tree:        tree,
	}
	go t.drainStderr(stderr)
	go func() {
		t.waitErr = cmd.Wait()
		close(t.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Kill()
		case <-t.done:
		}
	}()
	return t, nil
}

func (t *ProcessTransport) drainStderr(stderr io.Reader) {
	reader := bufio.NewReaderSize(stderr, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if len(line) > t.stderrLimit {
				line = line[:t.stderrLimit]
			}
			if cleaned, ok := executionlog.Sanitize("runner", line); ok {
				if t.log != nil {
					t.log.Info("runner", "runner.stderr", map[string]any{"line": cleaned})
				}
			} else {
				// Runner stderr 可能携带模型原文、字幕、凭据、环境或路径；
				// 无法完整匹配受控摘要的内容一律不落盘，只报一次固定提示。
				t.stderrOnce.Do(func() {
					if t.log != nil {
						t.log.Warn("runner", "runner.stderr_suppressed", map[string]any{
							"notice": runnerStderrSuppressedNotice,
						})
					}
				})
			}
		}
		if err != nil {
			return
		}
	}
}

// Send 写一帧并刷新。
func (t *ProcessTransport) Send(f Frame) error {
	data, err := encodeFrame(f)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// Receive 读取一帧；对端关闭返回 io.EOF。
func (t *ProcessTransport) Receive() (Frame, error) {
	line, err := t.stdout.ReadBytes('\n')
	if err != nil {
		if len(line) == 0 {
			if err == io.EOF {
				return Frame{}, io.EOF
			}
			return Frame{}, fmt.Errorf("read frame: %w", err)
		}
		// 末尾没有换行：仍尝试解析（对端退出前的最后一帧）。
	}
	if len(line) > MaxFrameBytes+2 {
		return Frame{}, fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
	}
	return decodeFrame(line)
}

// Kill 终止本应用拥有的进程树并等待退出确认。
func (t *ProcessTransport) Kill() error {
	t.killOnce.Do(func() {
		if t.cmd.Process == nil {
			return
		}
		t.killErr = t.tree.Kill(t.Exited())
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			groupExited, err := t.tree.Exited()
			if err != nil {
				t.killErr = errors.Join(t.killErr, err)
				return
			}
			if t.Exited() && groupExited {
				return
			}
			select {
			case <-deadline.C:
				t.killErr = errors.Join(t.killErr, ErrStopUnconfirmed)
				return
			case <-tick.C:
			}
		}
	})
	return t.killErr
}

// Close 关闭 stdin 并终止子进程。
func (t *ProcessTransport) Close() error {
	_ = t.stdin.Close()
	killErr := t.Kill()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	return errors.Join(killErr, t.tree.Close())
}

func (t *ProcessTransport) CoversDetachedChildren() bool { return t.tree.CoversDetachedChildren() }

// Exited 报告子进程是否已退出（不把心跳丢失当死亡；这里只报告本地观察）。
func (t *ProcessTransport) Exited() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// WaitErr 返回 cmd.Wait 的错误（仅在退出后有效）。
func (t *ProcessTransport) WaitErr() error { return t.waitErr }

func (t *ProcessTransport) Identity() string { return t.identity }

func randomLaunchID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("launch-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", data[:])
}
