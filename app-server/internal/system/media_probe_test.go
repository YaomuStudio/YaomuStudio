package system

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const videoProbeJSON = `{
  "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "12.345"},
  "streams": [
    {"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "duration": "12.300"},
    {"codec_type": "audio", "codec_name": "aac", "sample_rate": "48000", "duration": "12.345"},
    {"codec_type": "audio", "codec_name": "aac", "sample_rate": "44100", "duration": "12.345"}
  ]
}`

const audioProbeJSON = `{
  "format": {"format_name": "wav", "duration": "3.500000"},
  "streams": [
    {"codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "44100", "duration": "3.500000"}
  ]
}`

func TestParseFFprobeJSONVideo(t *testing.T) {
	meta, err := parseFFprobeJSON([]byte(videoProbeJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.Container != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Fatalf("container = %q", meta.Container)
	}
	if meta.Codec != "h264" || meta.Width != 1920 || meta.Height != 1080 {
		t.Fatalf("codec/size = %q %dx%d", meta.Codec, meta.Width, meta.Height)
	}
	if meta.DurationMs != 12345 {
		t.Fatalf("duration_ms = %d", meta.DurationMs)
	}
	if meta.AudioTracks != 2 {
		t.Fatalf("audio_tracks = %d", meta.AudioTracks)
	}
	if meta.SampleRate != 48000 {
		t.Fatalf("sample_rate = %d", meta.SampleRate)
	}
}

func TestParseFFprobeJSONAudio(t *testing.T) {
	meta, err := parseFFprobeJSON([]byte(audioProbeJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.Container != "wav" {
		t.Fatalf("container = %q", meta.Container)
	}
	if meta.Codec != "pcm_s16le" {
		t.Fatalf("codec = %q", meta.Codec)
	}
	if meta.Width != 0 || meta.Height != 0 {
		t.Fatalf("audio must not report resolution: %dx%d", meta.Width, meta.Height)
	}
	if meta.DurationMs != 3500 {
		t.Fatalf("duration_ms = %d", meta.DurationMs)
	}
	if meta.AudioTracks != 1 || meta.SampleRate != 44100 {
		t.Fatalf("audio_tracks/sample_rate = %d/%d", meta.AudioTracks, meta.SampleRate)
	}
}

func TestParseFFprobeJSONVideoWithAudioFirst(t *testing.T) {
	// 流顺序不保证 video 在前：编码/分辨率必须仍取自视频流。
	meta, err := parseFFprobeJSON([]byte(`{
      "format": {"format_name": "mp4", "duration": "2.0"},
      "streams": [
        {"codec_type": "audio", "codec_name": "aac", "sample_rate": "44100"},
        {"codec_type": "video", "codec_name": "h264", "width": 1280, "height": 720}
      ]
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.Codec != "h264" || meta.Width != 1280 || meta.Height != 720 {
		t.Fatalf("codec/size = %q %dx%d", meta.Codec, meta.Width, meta.Height)
	}
	if meta.AudioTracks != 1 || meta.SampleRate != 44100 {
		t.Fatalf("audio = %d/%d", meta.AudioTracks, meta.SampleRate)
	}
}

func TestParseFFprobeJSONNoAudio(t *testing.T) {
	meta, err := parseFFprobeJSON([]byte(`{
      "format": {"format_name": "matroska,webm", "duration": "5.0"},
      "streams": [{"codec_type": "video", "codec_name": "vp9", "width": 1280, "height": 720}]
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.AudioTracks != 0 || meta.SampleRate != 0 {
		t.Fatalf("audio = %d/%d, want 0/0", meta.AudioTracks, meta.SampleRate)
	}
	if meta.DurationMs != 5000 {
		t.Fatalf("duration_ms = %d", meta.DurationMs)
	}
}

func TestParseFFprobeJSONFallsBackToStreamDuration(t *testing.T) {
	meta, err := parseFFprobeJSON([]byte(`{
      "format": {"format_name": "wav", "duration": "N/A"},
      "streams": [{"codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "8000", "duration": "1.250000"}]
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.DurationMs != 1250 {
		t.Fatalf("duration_ms = %d", meta.DurationMs)
	}
}

func TestParseFFprobeJSONRejectsInvalidInput(t *testing.T) {
	cases := map[string]string{
		"bad json":       `{not-json`,
		"no streams":     `{"format": {"format_name": "wav"}, "streams": []}`,
		"missing format": `{"streams": [{"codec_type": "audio"}]}`,
		"no audio/video": `{"format": {"format_name": "srt"}, "streams": [{"codec_type": "subtitle"}]}`,
	}
	for name, payload := range cases {
		if _, err := parseFFprobeJSON([]byte(payload)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestFFprobeProberUnavailable(t *testing.T) {
	probe := NewFFprobeProberWithOptions(MediaProbeOptions{
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	})
	_, err := probe.Probe(context.Background(), "/tmp/whatever.wav")
	if !errors.Is(err, ErrProberUnavailable) {
		t.Fatalf("error = %v, want ErrProberUnavailable", err)
	}

	explicit := NewFFprobeProber(filepath.Join(t.TempDir(), "missing-ffprobe"))
	_, err = explicit.Probe(context.Background(), "/tmp/whatever.wav")
	if !errors.Is(err, ErrProberUnavailable) {
		t.Fatalf("explicit missing: error = %v, want ErrProberUnavailable", err)
	}
}

// fakeCommandRunner 返回固定结果，用于区分“工具无法启动”和“工具拒绝该文件”。
type fakeCommandRunner struct {
	out []byte
	err error
}

func (f fakeCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return f.out, f.err
}

// TestFFprobeProberClassifiesRunErrors 覆盖启动失败与文件探测失败的区分：
// 二进制缺失/无法启动是工具不可用（不标 corrupt），工具正常退出但拒绝文件才是探测失败。
func TestFFprobeProberClassifiesRunErrors(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	probe := func(runErr error) error {
		t.Helper()
		p := NewFFprobeProberWithOptions(MediaProbeOptions{
			Path:   exe,
			Runner: fakeCommandRunner{err: runErr},
		})
		_, err := p.Probe(context.Background(), "/tmp/whatever.wav")
		return err
	}

	cases := []struct {
		name        string
		runErr      error
		unavailable bool
	}{
		{"missing binary", exec.ErrNotFound, true},
		{"cannot start", &exec.Error{Name: "ffprobe", Err: errors.New("permission denied")}, true},
		{"invalid executable", &os.PathError{Op: "fork/exec", Path: "ffprobe", Err: errors.New("exec format error")}, true},
		{"tool rejects file", &exec.ExitError{}, false},
		{"unknown failure", errors.New("synthetic"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := probe(tc.runErr)
			if tc.unavailable {
				if !errors.Is(err, ErrProberUnavailable) {
					t.Fatalf("error = %v, want ErrProberUnavailable", err)
				}
				return
			}
			if !errors.Is(err, ErrProbeFailed) {
				t.Fatalf("error = %v, want ErrProbeFailed", err)
			}
		})
	}
}

// TestFFprobeProberRealWAV 用纯 Go 合成 WAV 并执行真实 ffprobe。
// ffprobe 缺失时显式跳过（输出 FFPROBE_UNAVAILABLE），不把跳过当通过。
func TestFFprobeProberRealWAV(t *testing.T) {
	binary := findProbeBinary()
	if binary == "" {
		t.Skip("FFPROBE_UNAVAILABLE: 未找到 ffprobe；真实媒体探测集成测试跳过（不得视为通过）")
	}
	t.Logf("ffprobe: %s", binary)

	dir := t.TempDir()
	wavPath := filepath.Join(dir, "tone.wav")
	if err := os.WriteFile(wavPath, synthWAV(8000, 500), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	probe := NewFFprobeProber(binary)
	meta, err := probe.Probe(context.Background(), wavPath)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if meta.Container == "" || meta.Codec == "" {
		t.Fatalf("empty container/codec: %+v", meta)
	}
	if meta.AudioTracks != 1 {
		t.Fatalf("audio_tracks = %d", meta.AudioTracks)
	}
	if meta.SampleRate != 8000 {
		t.Fatalf("sample_rate = %d, want 8000", meta.SampleRate)
	}
	if meta.DurationMs < 400 || meta.DurationMs > 700 {
		t.Fatalf("duration_ms = %d, want ~500", meta.DurationMs)
	}

	// 非媒体文件必须返回可区分失败，不伪造元数据。
	badPath := filepath.Join(dir, "not-media.wav")
	if err := os.WriteFile(badPath, []byte("not a wav"), 0o600); err != nil {
		t.Fatalf("write bad file: %v", err)
	}
	if _, err := probe.Probe(context.Background(), badPath); !errors.Is(err, ErrProbeFailed) {
		t.Fatalf("bad media error = %v, want ErrProbeFailed", err)
	}
}

// TestFFprobeProberRealMP4 用真实 ffmpeg 生成 MP4 后探测；工具缺失时显式跳过。
func TestFFprobeProberRealMP4(t *testing.T) {
	ffprobe := findProbeBinary()
	ffmpeg := findToolBinary("FFMPEG_BIN", "ffmpeg")
	if ffprobe == "" || ffmpeg == "" {
		t.Skip("FFMPEG_UNAVAILABLE: 未找到 ffmpeg/ffprobe；真实 MP4 探测集成测试跳过（不得视为通过）")
	}
	path := filepath.Join(t.TempDir(), "sample.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-shortest", "-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("FFMPEG_SYNTH_FAILED: 无法合成 MP4（%v）：%s", err, strings.TrimSpace(string(out)))
	}
	meta, err := NewFFprobeProber(ffprobe).Probe(ctx, path)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !strings.Contains(meta.Container, "mp4") {
		t.Fatalf("container = %q, want mp4 family", meta.Container)
	}
	if meta.Codec != "mpeg4" {
		t.Fatalf("codec = %q, want mpeg4", meta.Codec)
	}
	if meta.Width != 64 || meta.Height != 64 {
		t.Fatalf("size = %dx%d, want 64x64", meta.Width, meta.Height)
	}
	if meta.AudioTracks != 1 {
		t.Fatalf("audio_tracks = %d", meta.AudioTracks)
	}
}

// findProbeBinary 依次读取 FFPROBE_BIN 与 PATH，与生产解析顺序一致。
func findProbeBinary() string { return findToolBinary("FFPROBE_BIN", "ffprobe") }

func findToolBinary(envKey, name string) string {
	if envKey != "" {
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			if info, err := os.Stat(value); err == nil && !info.IsDir() {
				return value
			}
		}
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return found
}

// synthWAV 生成 16-bit 单声道 PCM WAV（静音），不依赖外部工具。
func synthWAV(sampleRate, durationMs int) []byte {
	samples := sampleRate * durationMs / 1000
	dataBytes := samples * 2
	buf := make([]byte, 44+dataBytes)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1) // mono
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))
	return buf
}
