package execadapter

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var winDrivePath = regexp.MustCompile(`(?i)^([A-Za-z]):[\\/](.*)`)

// WindowsToWSL 把 Windows 绝对路径转为 WSL 挂载路径（/mnt/<drive>/...）。
// 非盘符路径原样把反斜杠换成斜杠；相对路径不转换。
// 盘符路径按 Windows 语义识别，不依赖当前宿主 OS：Linux 上 filepath.IsAbs(`D:\x`)
// 为 false，若先走 filepath 分支会把 Windows 路径原样透传。
func WindowsToWSL(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "/mnt/") || strings.HasPrefix(p, "/home/") || strings.HasPrefix(p, "/tmp/") {
		return filepath.ToSlash(p)
	}
	if m := winDrivePath.FindStringSubmatch(p); m != nil {
		return drivePathToWSL(m)
	}
	abs := p
	if filepath.IsAbs(p) {
		if cleaned, err := filepath.Abs(p); err == nil {
			abs = cleaned
		}
	} else {
		return filepath.ToSlash(p)
	}
	if m := winDrivePath.FindStringSubmatch(abs); m != nil {
		return drivePathToWSL(m)
	}
	return filepath.ToSlash(abs)
}

func drivePathToWSL(m []string) string {
	drive := strings.ToLower(m[1])
	rest := strings.ReplaceAll(m[2], `\`, `/`)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "/mnt/" + drive
	}
	return "/mnt/" + drive + "/" + rest
}

// WSLToWindows 把 /mnt/<drive>/... 转回 Windows 路径；其它路径原样返回。
func WSLToWindows(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	const prefix = "/mnt/"
	if !strings.HasPrefix(p, prefix) || len(p) < len(prefix)+2 {
		return p
	}
	rest := p[len(prefix):]
	drive := rest[0]
	if !unicode.IsLetter(rune(drive)) {
		return p
	}
	if len(rest) == 1 {
		return string(unicode.ToUpper(rune(drive))) + `:\`
	}
	if rest[1] != '/' {
		return p
	}
	tail := strings.ReplaceAll(rest[2:], `/`, `\`)
	return string(unicode.ToUpper(rune(drive))) + `:\` + tail
}

// translateRequestForWSL 转换下发给 WSL Runner 的绝对路径字段。
func translateRequestForWSL(req Request) Request {
	out := req
	out.OutputDir = WindowsToWSL(req.OutputDir)
	if len(req.Params) == 0 {
		return out
	}
	out.Params = translateAnyPaths(req.Params).(map[string]any)
	return out
}

// translateResultFromWSL 把产物侧路径（若为绝对 WSL 路径）转回宿主；RelPath 保持相对。
func translateResultFromWSL(result Result) Result {
	out := result
	if out.OutputDir != "" {
		out.OutputDir = WSLToWindows(out.OutputDir)
	}
	return out
}

func translateAnyPaths(v any) any {
	switch t := v.(type) {
	case string:
		if looksLikeWindowsAbs(t) {
			return WindowsToWSL(t)
		}
		return t
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = translateAnyPaths(val)
		}
		return m
	case []any:
		arr := make([]any, len(t))
		for i, val := range t {
			arr[i] = translateAnyPaths(val)
		}
		return arr
	default:
		return v
	}
}

func looksLikeWindowsAbs(p string) bool {
	if winDrivePath.MatchString(p) {
		return true
	}
	// UNC 暂不转换。
	return false
}
