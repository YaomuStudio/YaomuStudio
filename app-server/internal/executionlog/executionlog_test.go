package executionlog

import (
	"strings"
	"testing"
)

func TestSanitizeAcceptsControlledMessages(t *testing.T) {
	accepted := map[string]string{
		"使用 mock 分离执行器":             "使用 mock 分离执行器",
		"mock 分离完成":                 "mock 分离完成",
		"启动 FlashCLI 分离":            "启动 FlashCLI 分离",
		"FlashCLI 分离完成":             "FlashCLI 分离完成",
		"使用 mock 单句克隆执行器":           "使用 mock 单句克隆执行器",
		"单句克隆完成":                    "单句克隆完成",
		"参考音频回环服务已启动":               "参考音频回环服务已启动",
		"受管 FlashCLI serve 已启动":     "受管 FlashCLI serve 已启动",
		"FlashCLI serve 健康检查通过":     "FlashCLI serve 健康检查通过",
		"参考音频切片已准备":                 "参考音频切片已准备",
		"单句克隆音频已生成":                 "单句克隆音频已生成",
		"克隆音频合成完成":                  "克隆音频合成完成",
		"启动 mlstools 字幕翻译":          "启动 mlstools 字幕翻译",
		"mlstools 字幕翻译完成":           "mlstools 字幕翻译完成",
		"转换 ASR 输入为 16 kHz 单声道 WAV": "转换 ASR 输入为 16 kHz 单声道 WAV",
		"启动 FlashCLI ASR":           "启动 FlashCLI ASR",
		"日志输出过快，已省略部分行":             "日志输出过快，已省略部分行",
		"日志已达上限，后续输出已省略":            "日志已达上限，后续输出已省略",
		"FlashCLI 输出已省略（内容不记录）":     "FlashCLI 输出已省略（内容不记录）",
	}
	for input, want := range accepted {
		got, ok := Sanitize("runner", input)
		if !ok || got != want {
			t.Errorf("Sanitize(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
}

func TestSanitizeRebuildsIntegerTemplates(t *testing.T) {
	cases := map[string]string{
		"启动单句克隆 120-4560ms":      "启动单句克隆 120-4560ms",
		"启动单句克隆 000120-004560ms": "启动单句克隆 120-4560ms",
		"逐句预测 3/12":              "逐句预测 3/12",
		"逐句预测 0003/0012":         "逐句预测 3/12",
		"克隆完成，句段数 12":            "克隆完成，句段数 12",
		"克隆完成，句段数 0012":          "克隆完成，句段数 12",
		"翻译执行中，已用时 10 秒":         "翻译执行中，已用时 10 秒",
		"翻译执行中，已用时 000010 秒":     "翻译执行中，已用时 10 秒",
		"ASR 执行中，已用时 10 秒":       "ASR 执行中，已用时 10 秒",
		"ASR 执行中，已用时 000010 秒":   "ASR 执行中，已用时 10 秒",
		"ASR 完成，生成 3 条字幕":        "ASR 完成，生成 3 条字幕",
		"ASR 完成，生成 0003 条字幕":     "ASR 完成，生成 3 条字幕",
	}
	for input, want := range cases {
		got, ok := Sanitize("runner", input)
		if !ok || got != want {
			t.Errorf("Sanitize(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
}

func TestSanitizeRejectsInvalidTemplates(t *testing.T) {
	rejected := []string{
		"启动单句克隆 -1-20ms",
		"启动单句克隆 20-20ms",
		"启动单句克隆 30-20ms",
		"启动单句克隆 1-2",
		"逐句预测 0/3",
		"逐句预测 4/3",
		"逐句预测 1/0",
		"克隆完成，句段数 0",
		"克隆完成，句段数 abc",
		"翻译执行中，已用时 0 秒",
		"翻译执行中，已用时 abc 秒",
		"翻译执行中，已用时 10",
		"ASR 执行中，已用时 0 秒",
		"ASR 执行中，已用时 abc 秒",
		"ASR 执行中，已用时 10",
		"ASR 完成，生成 0 条字幕",
		"ASR 完成，生成 abc 条字幕",
	}
	for _, input := range rejected {
		if got, ok := Sanitize("runner", input); ok {
			t.Errorf("Sanitize(%q) accepted as %q", input, got)
		}
	}
}

func TestSanitizeRejectsFreeFormContent(t *testing.T) {
	rejected := []string{
		"虚构字幕：这是不该落盘的一句台词",
		"FLASHCLI_TOKEN=虚构密钥-sk-not-real",
		"CUDA_VISIBLE_DEVICES=0 PATH=/mnt/d/private",
		"/mnt/d/private/ref.wav 已加载",
		"progress 完成 80%",
		"前缀 使用 mock 分离执行器",
		"使用 mock 分离执行器 后缀",
		"逐句预测 1/2\n虚构字幕",
		strings.Repeat("x", MaxLineChars+1),
	}
	for _, input := range rejected {
		if got, ok := Sanitize("runner", input); ok {
			t.Errorf("Sanitize(%q) accepted as %q", input, got)
		}
	}
}

func TestSanitizeRejectsUnknownStreamOrEmptyLine(t *testing.T) {
	if _, ok := Sanitize("model", "使用 mock 分离执行器"); ok {
		t.Error("unknown stream accepted")
	}
	if _, ok := Sanitize("runner", ""); ok {
		t.Error("empty line accepted")
	}
}
