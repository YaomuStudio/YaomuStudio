package system

// WSL 媒体探测：导入/重定位时使用 yaomu 镜像内的固定 ffprobe，不依赖
// Windows 宿主 PATH。
//
// 边界（docs/data-and-storage.md、FR-IMP-03）：
//   - 只接受 Windows 盘符绝对路径，映射为 /mnt/<drive>/...；UNC/设备/相对
//     路径、NUL/换行/控制字符一律拒绝，且错误信息不回显原路径；
//   - 固定 argv 经 wsl.exe 调用，不经过 shell；distro 与镜像内工具路径先校验；
//   - 复用 CommandRunner 与现有超时/输出上限语义；
//   - wsl.exe 缺失/无法启动、镜像命令/工具不存在（实测退出码 127）或发行版
//     不可用（实测 -1，Windows 0xFFFFFFFF）→ ErrProberUnavailable；其他非零/
//     超时/输出超限 → ErrProbeFailed，stderr 不进入 API。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const (
	// defaultWSLExe 是桌面壳未显式覆盖时的 WSL 启动器。
	defaultWSLExe = "wsl.exe"
	// maxWSLDistroLength 限制发行版名；过长的值不是有效发行版。
	maxWSLDistroLength = 64
	// maxWSLToolPathLength 与桌面壳工具解析上限一致（4096）。
	maxWSLToolPathLength = 4096
)

// errUnsupportedWSLMediaPath 表示宿主路径无法安全映射到镜像挂载路径。
var errUnsupportedWSLMediaPath = errors.New("unsupported WSL media path")

// WSLMediaProbeOptions 是 WSLFFprobeProber 的构造参数。
type WSLMediaProbeOptions struct {
	// Distro 是执行侧镜像发行版；必须非空、不以 '-' 开头且只含安全字符。
	Distro string
	// FFprobePath 是镜像内的绝对 POSIX 工具路径。
	FFprobePath string
	// WSLExe 是可选 WSL 启动器覆盖；空值使用 wsl.exe。
	WSLExe string
	// Runner 执行固定命令；nil 时使用不经过 shell 的 mediaCommandRunner。
	Runner CommandRunner
	// LookPath 便于测试替换 wsl.exe 查找；nil 时使用 exec.LookPath。
	LookPath func(string) (string, error)
	// Timeout 是单文件探测上限；<=0 使用默认 15s。
	Timeout time.Duration
}

// WSLFFprobeProber 通过 wsl.exe 调用镜像内 ffprobe 读取宿主媒体元数据。
type WSLFFprobeProber struct {
	opts WSLMediaProbeOptions
}

// NewWSLFFprobeProber 校验静态配置并构造镜像探测器。
//
// 配置非法（distro 缺失/不安全、工具路径不是镜像内绝对路径）时返回包装
// ErrProberUnavailable 的错误；调用方应安全回退到现有宿主 PATH 探测，
// 不阻塞 Core 启动。
func NewWSLFFprobeProber(opts WSLMediaProbeOptions) (*WSLFFprobeProber, error) {
	distro := strings.TrimSpace(opts.Distro)
	if !validWSLDistro(distro) {
		return nil, fmt.Errorf("%w: invalid WSL distro", ErrProberUnavailable)
	}
	ffprobePath := strings.TrimSpace(opts.FFprobePath)
	if !validWSLToolPath(ffprobePath) {
		return nil, fmt.Errorf("%w: invalid WSL ffprobe path", ErrProberUnavailable)
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Runner == nil {
		opts.Runner = mediaCommandRunner{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultMediaProbeTimeout
	}
	opts.Distro = distro
	opts.FFprobePath = ffprobePath
	opts.WSLExe = strings.TrimSpace(opts.WSLExe)
	return &WSLFFprobeProber{opts: opts}, nil
}

// Probe 读取宿主 path 的容器/编码/分辨率/时长/音轨数/采样率。
//
// 返回 ErrProberUnavailable 表示 wsl.exe、镜像工具或发行版不可用；
// 其他错误表示该文件探测失败。
func (p *WSLFFprobeProber) Probe(ctx context.Context, path string) (*domain.MediaMeta, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty media path", ErrProbeFailed)
	}
	// 工具不可用优先于单个文件路径问题：避免把“没有 wsl.exe”误报成文件损坏。
	wslExe, err := p.resolveWSLExe()
	if err != nil {
		return nil, err
	}
	mapped, err := windowsMediaPathToWSLMount(path)
	if err != nil {
		// 不把原始路径带进错误；UNC/相对/控制字符一律按探测失败处理。
		return nil, fmt.Errorf("%w: unsupported media path", ErrProbeFailed)
	}
	probeCtx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	out, err := p.opts.Runner.Run(probeCtx, wslExe,
		"-d", p.opts.Distro,
		"--", p.opts.FFprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		mapped,
	)
	if err != nil {
		// 不把原始 stderr/路径带出；只给出可区分的安全原因。
		// exec.CommandContext 超时会杀死进程并返回 *ExitError（非 context error），
		// 因此先按 probeCtx 判断是否由本次超时/取消终止，避免把 -1 误判成工具不可用。
		if probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: timeout", ErrProbeFailed)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// WSL 实测退出码（Windows 11/yaomu）：镜像内命令/工具不存在为 127，
			// 发行版不可用为 -1（宿主进程 0xFFFFFFFF；64 位 Go 可能表现为
			// 4294967295，统一按 uint32 归一）。这两种是工具不可用，不是文件
			// 损坏；其他非零（如损坏媒体退出 1）才是文件探测失败。不解析 stderr。
			code := exitErr.ExitCode()
			if code == 127 || uint32(code) == 0xFFFFFFFF {
				return nil, ErrProberUnavailable
			}
			return nil, fmt.Errorf("%w: ffprobe exited unsuccessfully", ErrProbeFailed)
		}
		var pathErr *os.PathError
		var execErr *exec.Error
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &pathErr) || errors.As(err, &execErr) {
			// wsl.exe 不存在或无法启动：工具不可用，不把用户文件误标为损坏。
			return nil, ErrProberUnavailable
		}
		return nil, fmt.Errorf("%w: ffprobe command failed", ErrProbeFailed)
	}
	// mediaCommandRunner 在写入阶段限流；这里再防御注入的 Runner 返回超限输出。
	if len(out) > mediaProbeOutputLimit {
		return nil, fmt.Errorf("%w: output exceeds limit", ErrProbeFailed)
	}
	meta, err := parseFFprobeJSON(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}
	return meta, nil
}

// resolveWSLExe 解析本次调用的 wsl.exe；显式覆盖仍经 LookPath 校验可执行性。
func (p *WSLFFprobeProber) resolveWSLExe() (string, error) {
	name := p.opts.WSLExe
	if name == "" {
		name = defaultWSLExe
	}
	found, err := p.opts.LookPath(name)
	if err != nil || strings.TrimSpace(found) == "" {
		return "", ErrProberUnavailable
	}
	return found, nil
}

// validWSLDistro 只接受非空、不以 '-' 开头、长度有界且仅含安全字符的发行版名。
// argv 不经过 shell，这里仍限制字符集，避免发行版名被 WSL 解释成额外选项或
// 携带不可见字符。
func validWSLDistro(value string) bool {
	if value == "" || len(value) > maxWSLDistroLength || value[0] == '-' {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validWSLToolPath 只接受镜像内绝对 POSIX 路径，拒绝 NUL/换行/控制字符。
func validWSLToolPath(value string) bool {
	if !strings.HasPrefix(value, "/") || len(value) > maxWSLToolPathLength {
		return false
	}
	return !hasControlRune(value)
}

// windowsMediaPathToWSLMount 把 Windows 盘符绝对路径映射为 /mnt/<drive>/...。
//
// 只做字符串转换：反斜杠转 '/'、盘符小写；不解析符号链接或权限。UNC、设备
// 路径、盘符相对路径、POSIX 路径与控制字符一律拒绝，避免映射到非预期文件。
func windowsMediaPathToWSLMount(path string) (string, error) {
	if path == "" || hasControlRune(path) {
		return "", errUnsupportedWSLMediaPath
	}
	// UNC（\\server\share、//server/share）与设备命名空间（\\.\）不映射。
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return "", errUnsupportedWSLMediaPath
	}
	if len(path) < 3 || !isASCIILetter(path[0]) || path[1] != ':' {
		return "", errUnsupportedWSLMediaPath
	}
	if path[2] != '\\' && path[2] != '/' {
		return "", errUnsupportedWSLMediaPath
	}
	rest := strings.ReplaceAll(path[3:], `\`, "/")
	return "/mnt/" + strings.ToLower(string(path[0])) + "/" + rest, nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func hasControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
