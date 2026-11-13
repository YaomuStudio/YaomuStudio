package system

// 媒体探测：只通过固定参数调用 ffprobe，把结果转成 domain.MediaMeta。
//
// 边界（docs/data-and-storage.md、FR-IMP-03）：
//   - 固定 JSON 参数，不经过 shell、不拼接任意用户输入；
//   - ffprobe 输出/错误不进入 API；调用方只拿安全错误与可选元数据；
//   - 无法解析或没有音视频流时返回 ErrProbeFailed，由 Core 标记 corrupt，
//     不伪造 container/时长/采样率。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const (
	// defaultMediaProbeTimeout 是单文件探测上限；超时的文件按探测失败处理。
	defaultMediaProbeTimeout = 15 * time.Second
	// mediaProbeOutputLimit 限制 stdout 大小；ffprobe JSON 正常远小于该值。
	mediaProbeOutputLimit = 1 << 20
)

// ErrProberUnavailable 表示本机没有可用的 ffprobe（未配置或不在 PATH）。
// 调用方应把它与“文件探测失败”区分：前者不伪造元数据，也不把文件标 corrupt。
var ErrProberUnavailable = errors.New("media prober unavailable")

// ErrProbeFailed 表示 ffprobe 执行失败或输出不可解析。
var ErrProbeFailed = errors.New("media probe failed")

// MediaProbeOptions 是 FFprobeProber 的构造参数。
type MediaProbeOptions struct {
	// Path 是显式 ffprobe 可执行文件（如打包资源）；为空时搜索 PATH。
	Path string
	// LookPath 便于测试替换 PATH 查找；nil 时使用 exec.LookPath。
	LookPath func(string) (string, error)
	// Runner 执行固定命令；nil 时使用不经过 shell 的 mediaCommandRunner。
	Runner CommandRunner
	// Timeout 是单文件探测上限；<=0 使用默认 15s。
	Timeout time.Duration
}

// FFprobeProber 用固定参数的 ffprobe 读取媒体元数据。
type FFprobeProber struct {
	opts MediaProbeOptions
}

// NewFFprobeProber 构造使用显式路径（可为空）的探测实现。
func NewFFprobeProber(path string) *FFprobeProber {
	return NewFFprobeProberWithOptions(MediaProbeOptions{Path: path})
}

// NewFFprobeProberWithOptions 构造可注入路径查找/命令执行的探测实现。
func NewFFprobeProberWithOptions(opts MediaProbeOptions) *FFprobeProber {
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Runner == nil {
		opts.Runner = mediaCommandRunner{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultMediaProbeTimeout
	}
	return &FFprobeProber{opts: opts}
}

// Probe 读取 path 的容器/编码/分辨率/时长/音轨数/采样率。
//
// 返回 ErrProberUnavailable 表示工具不可用；其他错误表示该文件探测失败。
func (p *FFprobeProber) Probe(ctx context.Context, path string) (*domain.MediaMeta, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty media path", ErrProbeFailed)
	}
	binary, err := p.resolveBinary()
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	out, err := p.opts.Runner.Run(probeCtx, binary,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	if err != nil {
		// 不把原始 stderr/路径带出；只给出可区分的安全原因。
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: timeout", ErrProbeFailed)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// 进程已启动但拒绝了该文件：属于文件探测失败。
			return nil, fmt.Errorf("%w: ffprobe exited unsuccessfully", ErrProbeFailed)
		}
		var pathErr *os.PathError
		var execErr *exec.Error
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &pathErr) || errors.As(err, &execErr) {
			// 二进制不存在或无法启动（打包缺文件、架构/权限问题）：工具不可用，
			// 不能把用户文件误标为 corrupt。
			return nil, ErrProberUnavailable
		}
		return nil, fmt.Errorf("%w: ffprobe command failed", ErrProbeFailed)
	}
	meta, err := parseFFprobeJSON(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}
	return meta, nil
}

// resolveBinary 解析 ffprobe；显式路径优先，否则搜索 PATH。
func (p *FFprobeProber) resolveBinary() (string, error) {
	if explicit := strings.TrimSpace(p.opts.Path); explicit != "" {
		info, err := os.Stat(explicit)
		if err != nil || info.IsDir() {
			return "", ErrProberUnavailable
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", ErrProberUnavailable
		}
		return explicit, nil
	}
	found, err := p.opts.LookPath("ffprobe")
	if err != nil || strings.TrimSpace(found) == "" {
		return "", ErrProberUnavailable
	}
	return found, nil
}

// ffprobeOutput 只取本任务需要的字段，忽略其余元数据。
type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Duration   string `json:"duration"`
	SampleRate string `json:"sample_rate"`
}

// parseFFprobeJSON 把 ffprobe JSON 转成 MediaMeta。
//
// container 保留 ffprobe 的 format_name（如 "wav" 或 "mov,mp4,m4a,..."），
// 不猜测更窄的容器别名；没有音视频流或缺少 container 视为探测失败。
func parseFFprobeJSON(data []byte) (*domain.MediaMeta, error) {
	var probe ffprobeOutput
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, errors.New("invalid ffprobe JSON")
	}
	container := strings.TrimSpace(probe.Format.FormatName)
	if container == "" {
		return nil, errors.New("missing container")
	}
	if len(probe.Streams) == 0 {
		return nil, errors.New("no media streams")
	}

	meta := &domain.MediaMeta{Container: container}
	audioTracks := 0
	var firstVideo *ffprobeStream
	var firstAudio *ffprobeStream
	var streamDuration float64

	for i := range probe.Streams {
		stream := &probe.Streams[i]
		switch stream.CodecType {
		case "video":
			if firstVideo == nil {
				firstVideo = stream
			}
		case "audio":
			audioTracks++
			if firstAudio == nil {
				firstAudio = stream
			}
		}
		if seconds, ok := parseSeconds(stream.Duration); ok && seconds > streamDuration {
			streamDuration = seconds
		}
	}
	if firstVideo == nil && firstAudio == nil {
		return nil, errors.New("no audio or video stream")
	}

	// 编码/分辨率优先取第一条视频流（不依赖 ffprobe 的流顺序）；
	// 纯音频文件才退回音频编码，避免音频流排在前面时丢掉视频属性。
	if firstVideo != nil {
		meta.Codec = firstVideo.CodecName
		meta.Width = firstVideo.Width
		meta.Height = firstVideo.Height
	} else {
		meta.Codec = firstAudio.CodecName
	}
	if firstAudio != nil {
		if rate, err := strconv.Atoi(strings.TrimSpace(firstAudio.SampleRate)); err == nil && rate > 0 {
			meta.SampleRate = rate
		}
	}

	if seconds, ok := parseSeconds(probe.Format.Duration); ok {
		meta.DurationMs = secondsToMillis(seconds)
	} else {
		meta.DurationMs = secondsToMillis(streamDuration)
	}
	meta.AudioTracks = audioTracks
	return meta, nil
}

// parseSeconds 解析 ffprobe 的秒数字符串（可能为 "N/A" 或空）。
func parseSeconds(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return seconds, true
}

func secondsToMillis(seconds float64) domain.Millis {
	if seconds <= 0 {
		return 0
	}
	return domain.Millis(seconds*1000 + 0.5)
}

// mediaCommandRunner 执行 ffprobe：不经过 shell，stderr 丢弃，stdout 有上限。
type mediaCommandRunner struct{}

func (mediaCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out limitedMediaBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type limitedMediaBuffer struct {
	buf bytes.Buffer
}

func (b *limitedMediaBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > mediaProbeOutputLimit {
		return 0, errors.New("ffprobe output exceeds limit")
	}
	return b.buf.Write(p)
}

func (b *limitedMediaBuffer) Bytes() []byte {
	return b.buf.Bytes()
}
