package system

import (
	"math"
	"strconv"
	"strings"
)

// parseLinuxMeminfo 解析 /proc/meminfo；MemAvailable 缺失时用
// MemFree + Buffers + Cached 估算可用内存。
func parseLinuxMeminfo(data []byte) MemoryInfo {
	values := make(map[string]int64, 5)
	for _, line := range strings.Split(string(data), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "MemTotal" && key != "MemAvailable" && key != "MemFree" &&
			key != "Buffers" && key != "Cached" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || value < 0 {
			continue
		}
		values[key] = value
	}

	totalKiB, hasTotal := values["MemTotal"]
	if !hasTotal || totalKiB <= 0 || totalKiB > math.MaxInt64/1024 {
		return MemoryInfo{}
	}
	availableKiB, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		free, hasFree := values["MemFree"]
		cached, hasCached := values["Cached"]
		buffers, hasBuffers := values["Buffers"]
		if !hasFree || !hasCached || !hasBuffers {
			return MemoryInfo{TotalBytes: totalKiB * 1024, HasTotal: true}
		}
		availableKiB = free + cached + buffers
	}
	if availableKiB < 0 {
		availableKiB = 0
	}
	if availableKiB > totalKiB {
		availableKiB = totalKiB
	}
	return MemoryInfo{
		TotalBytes: totalKiB * 1024,
		UsedBytes:  (totalKiB - availableKiB) * 1024,
		HasTotal:   true,
		HasUsed:    true,
	}
}

// parseVMStat 从 macOS vm_stat 输出估算已用内存：
// (Pages active + Pages wired down + Pages occupied by compressor) * page size。
// 未匹配到任何页计数时返回 false，避免把 0 当成有效占用。
func parseVMStat(data []byte) (int64, bool) {
	pageSize := int64(4096)
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 {
		if idx := strings.Index(lines[0], "page size of "); idx >= 0 {
			rest := lines[0][idx+len("page size of "):]
			if end := strings.Index(rest, " bytes"); end > 0 {
				if parsed, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64); err == nil && parsed > 0 {
					pageSize = parsed
				}
			}
		}
	}

	var pages int64
	matched := false
	for _, line := range lines[1:] {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "Pages active" && key != "Pages wired down" &&
			key != "Pages occupied by compressor" {
			continue
		}
		value := strings.TrimSuffix(strings.TrimSpace(rest), ".")
		count, err := strconv.ParseInt(value, 10, 64)
		if err != nil || count < 0 {
			continue
		}
		if pages > math.MaxInt64-count {
			return 0, false
		}
		pages += count
		matched = true
	}
	if !matched || pages > math.MaxInt64/pageSize {
		return 0, false
	}
	return pages * pageSize, true
}

// parseProcCPUInfo 取 Linux /proc/cpuinfo 的首个型号字段。
func parseProcCPUInfo(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name", "Model", "Hardware", "CPU":
			if value := strings.TrimSpace(rest); value != "" {
				return value
			}
		}
	}
	return ""
}
