package service

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// SRT 解析上限：节点产物是受控适配器输出，但仍按不可信输入处理，
// 避免一个畸形字幕文件把 Core 内存打满。
const maxSubtitleBytes = 32 << 20

// parseSRT 把 SRT 文本解析为句段输入（时间、文本、换行）。
//
// 只支持标准 SRT：可选序号行、`HH:MM:SS,mmm --> HH:MM:SS,mmm`（毫秒分隔符
// 接受逗号或点）、后续文本行以空行结束。解析后复用 domain.ValidateSegmentInputs
// 校验时间窗与非重叠；不做跨媒体的时长越界判断（需要媒体元数据，由预检负责）。
func parseSRT(data []byte) ([]domain.SegmentInput, error) {
	if len(data) == 0 {
		return nil, invalidRequestf("subtitle is empty")
	}
	if len(data) > maxSubtitleBytes {
		return nil, invalidRequestf("subtitle exceeds %d bytes", maxSubtitleBytes)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	blocks := splitSRTBlocks(text)
	if len(blocks) == 0 {
		return nil, invalidRequestf("subtitle has no cues")
	}
	segments := make([]domain.SegmentInput, 0, len(blocks))
	for i, block := range blocks {
		segment, err := parseSRTBlock(block)
		if err != nil {
			return nil, fmt.Errorf("subtitle cue %d: %w", i+1, err)
		}
		segments = append(segments, segment)
	}
	if err := domain.ValidateSegmentInputs(segments); err != nil {
		return nil, err
	}
	return segments, nil
}

// splitSRTBlocks 按空行切分 cue；忽略文件首尾与块间的多余空白。
func splitSRTBlocks(text string) [][]string {
	lines := strings.Split(text, "\n")
	blocks := make([][]string, 0)
	current := make([]string, 0)
	flush := func() {
		for len(current) > 0 && strings.TrimSpace(current[0]) == "" {
			current = current[1:]
		}
		for len(current) > 0 && strings.TrimSpace(current[len(current)-1]) == "" {
			current = current[:len(current)-1]
		}
		if len(current) > 0 {
			blocks = append(blocks, current)
		}
		current = make([]string, 0)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return blocks
}

func parseSRTBlock(lines []string) (domain.SegmentInput, error) {
	if len(lines) == 0 {
		return domain.SegmentInput{}, invalidRequestf("empty cue")
	}
	timingIndex := 0
	if isSRTIndex(strings.TrimSpace(lines[0])) {
		timingIndex = 1
	}
	if timingIndex >= len(lines) {
		return domain.SegmentInput{}, invalidRequestf("missing timing line")
	}
	start, end, err := parseSRTTiming(lines[timingIndex])
	if err != nil {
		return domain.SegmentInput{}, err
	}
	body := lines[timingIndex+1:]
	for i := range body {
		body[i] = strings.TrimRight(body[i], " \t")
	}
	return domain.SegmentInput{
		StartMs: start,
		EndMs:   end,
		Text:    strings.TrimSpace(strings.Join(body, "\n")),
	}, nil
}

func isSRTIndex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSRTTiming 解析 `start --> end`，忽略时间戳后的定位/样式提示。
func parseSRTTiming(line string) (domain.Millis, domain.Millis, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, invalidRequestf("timing line missing -->")
	}
	start, err := parseSRTTime(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := parseSRTTime(parts[1])
	if err != nil {
		return 0, 0, err
	}
	if end <= start {
		return 0, 0, invalidRequestf("cue end %d <= start %d", end, start)
	}
	return start, end, nil
}

// parseSRTTime 解析 HH:MM:SS,mmm；允许点号毫秒分隔符与周边空白。
func parseSRTTime(raw string) (domain.Millis, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, invalidRequestf("empty timestamp")
	}
	// 时间戳后的 `position:...` 等提示不是时间的一部分。
	if fields := strings.Fields(value); len(fields) > 0 {
		value = fields[0]
	}
	value = strings.Replace(value, ",", ".", 1)
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, invalidRequestf("timestamp %q must be HH:MM:SS,mmm", raw)
	}
	hours, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || hours < 0 {
		return 0, invalidRequestf("invalid timestamp hours in %q", raw)
	}
	minutes, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, invalidRequestf("invalid timestamp minutes in %q", raw)
	}
	secondsPart := parts[2]
	seconds, millis, err := parseSRTSeconds(secondsPart)
	if err != nil {
		return 0, invalidRequestf("invalid timestamp seconds in %q", raw)
	}
	if seconds < 0 || seconds > 59 || millis < 0 || millis > 999 {
		return 0, invalidRequestf("invalid timestamp seconds in %q", raw)
	}
	if hours > math.MaxInt64/3600000 {
		return 0, invalidRequestf("timestamp overflow in %q", raw)
	}
	total := ((hours*60+minutes)*60 + seconds) * 1000
	return domain.Millis(total + millis), nil
}

func parseSRTSeconds(raw string) (int64, int64, error) {
	parts := strings.SplitN(raw, ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	var millis int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) == 0 || len(frac) > 3 {
			return 0, 0, fmt.Errorf("invalid millisecond fraction %q", frac)
		}
		for len(frac) < 3 {
			frac += "0"
		}
		millis, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	return seconds, millis, nil
}
