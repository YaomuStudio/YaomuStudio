// Package executionlog 定义执行过程行的正向允许策略。
//
// 边界（docs/logging-and-privacy.md、docs/local-api-contract.md §6）：
//   - 过程行只承载应用构造的固定阶段摘要，或解析整数后重建的严格模板；
//   - 模型/子进程 stdout/stderr 原文、字幕、凭据、环境与路径等自由文本
//     一律不得进入协议帧、cache、SSE 或回放；
//   - 不使用“包含完成/进度”的子串筛选，也不用密钥前缀正则推断安全。
//
// scheduler 写入、service 回放与 execadapter 的 Runner stderr 诊断共用本策略，
// 避免 service 依赖 scheduler。
package executionlog

import (
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf8"
)

const (
	// MaxLineChars 与 Runner / 协议侧一致：单行执行过程文本上限。
	MaxLineChars = 512

	// FileCapNotice 是 Core 在 cache 达到体积上限时发布的固定提示。
	FileCapNotice = "日志已达上限，后续输出已省略"
	// RateNotice 是 Runner LogEmitter 节流时发布的固定提示。
	RateNotice = "日志输出过快，已省略部分行"
	// FlashCLIOutputOmitted 是 Runner 省略 FlashCLI 子进程输出时的固定提示。
	FlashCLIOutputOmitted = "FlashCLI 输出已省略（内容不记录）"
	// ChildStderrOmitted 是 Runner 省略其他子进程 stderr 时的固定提示。
	ChildStderrOmitted = "子进程 stderr 输出已省略（内容不记录）"
)

// streams 是协议允许的过程行来源；未知来源一律丢弃。
var streams = map[string]struct{}{
	"stderr": {},
	"stdout": {},
	"runner": {},
}

// staticLines 是本仓真实产生点与两端固定提示；必须完整匹配。
var staticLines = map[string]struct{}{
	"使用 mock 分离执行器":             {},
	"mock 分离完成":                 {},
	"启动 FlashCLI 分离":            {},
	"FlashCLI 分离完成":             {},
	"使用 mock 克隆执行器":             {},
	"mock 克隆完成":                 {},
	"使用 mock 单句克隆执行器":           {},
	"mock 单句克隆完成":               {},
	"启动受管 FlashCLI 克隆":          {},
	"单句克隆完成":                    {},
	"参考音频回环服务已启动":               {},
	"受管 FlashCLI serve 已启动":     {},
	"FlashCLI serve 健康检查通过":     {},
	"参考音频切片已准备":                 {},
	"单句克隆音频已生成":                 {},
	"克隆音频合成完成":                  {},
	"启动 mlstools 字幕翻译":          {},
	"mlstools 字幕翻译完成":           {},
	"转换 ASR 输入为 16 kHz 单声道 WAV": {},
	"启动 FlashCLI ASR":           {},
	RateNotice:                  {},
	FileCapNotice:               {},
	FlashCLIOutputOmitted:       {},
	ChildStderrOmitted:          {},
}

type dynamicRule struct {
	pattern *regexp.Regexp
	render  func(match []string) (string, bool)
}

var dynamicRules = []dynamicRule{
	{
		pattern: regexp.MustCompile(`^启动单句克隆 ([0-9]{1,12})-([0-9]{1,12})ms$`),
		render: func(match []string) (string, bool) {
			start, ok := parseDecimal(match[1])
			if !ok {
				return "", false
			}
			end, ok := parseDecimal(match[2])
			if !ok || start < 0 || end <= start {
				return "", false
			}
			return fmt.Sprintf("启动单句克隆 %d-%dms", start, end), true
		},
	},
	{
		pattern: regexp.MustCompile(`^逐句预测 ([0-9]{1,12})/([0-9]{1,12})$`),
		render: func(match []string) (string, bool) {
			index, ok := parseDecimal(match[1])
			if !ok {
				return "", false
			}
			total, ok := parseDecimal(match[2])
			if !ok || total < 1 || index < 1 || index > total {
				return "", false
			}
			return fmt.Sprintf("逐句预测 %d/%d", index, total), true
		},
	},
	{
		pattern: regexp.MustCompile(`^克隆完成，句段数 ([0-9]{1,12})$`),
		render: func(match []string) (string, bool) {
			count, ok := parseDecimal(match[1])
			if !ok || count < 1 {
				return "", false
			}
			return fmt.Sprintf("克隆完成，句段数 %d", count), true
		},
	},
	{
		pattern: regexp.MustCompile(`^翻译执行中，已用时 ([0-9]{1,6}) 秒$`),
		render: func(match []string) (string, bool) {
			seconds, ok := parseDecimal(match[1])
			if !ok || seconds < 1 {
				return "", false
			}
			return fmt.Sprintf("翻译执行中，已用时 %d 秒", seconds), true
		},
	},
	{
		pattern: regexp.MustCompile(`^ASR 执行中，已用时 ([0-9]{1,6}) 秒$`),
		render: func(match []string) (string, bool) {
			seconds, ok := parseDecimal(match[1])
			if !ok || seconds < 1 {
				return "", false
			}
			return fmt.Sprintf("ASR 执行中，已用时 %d 秒", seconds), true
		},
	},
	{
		pattern: regexp.MustCompile(`^ASR 完成，生成 ([0-9]{1,12}) 条字幕$`),
		render: func(match []string) (string, bool) {
			count, ok := parseDecimal(match[1])
			if !ok || count < 1 {
				return "", false
			}
			return fmt.Sprintf("ASR 完成，生成 %d 条字幕", count), true
		},
	},
}

func parseDecimal(token string) (int64, bool) {
	value, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// Sanitize 返回规范化的受控过程行。第二个返回值为 false 时，调用方必须丢弃
// 该行；不得把原文写入 cache、SSE、本地诊断或 API 响应。
func Sanitize(stream, line string) (string, bool) {
	if _, ok := streams[stream]; !ok {
		return "", false
	}
	if line == "" || utf8.RuneCountInString(line) > MaxLineChars {
		return "", false
	}
	if _, ok := staticLines[line]; ok {
		return line, true
	}
	for _, rule := range dynamicRules {
		match := rule.pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		return rule.render(match)
	}
	return "", false
}
