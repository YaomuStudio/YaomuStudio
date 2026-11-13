"""执行过程行正向允许与节流测试。

默认不转发任意模型/子进程文本：只有应用构造的固定阶段消息与严格整数模板
可以通过；字幕、凭据、环境与路径等自由文本一律丢弃。
"""

from __future__ import annotations

import unittest

import _helpers  # noqa: F401  (导入即设置 src 路径)

from yaomustudio_runner.execution_log import LogEmitter, safe_log_line


class SafeMessageTests(unittest.TestCase):
    def test_controlled_stage_messages_pass_unchanged(self):
        for message in (
            "使用 mock 分离执行器",
            "mock 分离完成",
            "启动 FlashCLI 分离",
            "FlashCLI 分离完成",
            "使用 mock 克隆执行器",
            "mock 克隆完成",
            "使用 mock 单句克隆执行器",
            "mock 单句克隆完成",
            "启动受管 FlashCLI 克隆",
            "单句克隆完成",
            "参考音频回环服务已启动",
            "受管 FlashCLI serve 已启动",
            "FlashCLI serve 健康检查通过",
            "参考音频切片已准备",
            "单句克隆音频已生成",
            "克隆音频合成完成",
            "启动 mlstools 字幕翻译",
            "mlstools 字幕翻译完成",
            "转换 ASR 输入为 16 kHz 单声道 WAV",
            "启动 FlashCLI ASR",
            "日志输出过快，已省略部分行",
            "FlashCLI 输出已省略（内容不记录）",
        ):
            self.assertEqual(safe_log_line(message), message)

    def test_dynamic_messages_are_rebuilt_from_integers(self):
        self.assertEqual(
            safe_log_line("启动单句克隆 120-4560ms"), "启动单句克隆 120-4560ms"
        )
        self.assertEqual(
            safe_log_line("启动单句克隆 000120-004560ms"), "启动单句克隆 120-4560ms"
        )
        self.assertEqual(safe_log_line("逐句预测 3/12"), "逐句预测 3/12")
        self.assertEqual(safe_log_line("逐句预测 0003/0012"), "逐句预测 3/12")
        self.assertEqual(safe_log_line("克隆完成，句段数 12"), "克隆完成，句段数 12")
        self.assertEqual(safe_log_line("克隆完成，句段数 0012"), "克隆完成，句段数 12")
        self.assertEqual(
            safe_log_line("翻译执行中，已用时 10 秒"), "翻译执行中，已用时 10 秒"
        )
        self.assertEqual(
            safe_log_line("翻译执行中，已用时 000010 秒"), "翻译执行中，已用时 10 秒"
        )
        self.assertEqual(safe_log_line("ASR 执行中，已用时 10 秒"), "ASR 执行中，已用时 10 秒")
        self.assertEqual(
            safe_log_line("ASR 执行中，已用时 000010 秒"), "ASR 执行中，已用时 10 秒"
        )
        self.assertEqual(safe_log_line("ASR 完成，生成 3 条字幕"), "ASR 完成，生成 3 条字幕")
        self.assertEqual(
            safe_log_line("ASR 完成，生成 0003 条字幕"), "ASR 完成，生成 3 条字幕"
        )

    def test_dynamic_messages_reject_invalid_numbers(self):
        for message in (
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
        ):
            self.assertIsNone(safe_log_line(message), message)

    def test_free_form_content_is_rejected(self):
        samples = (
            "虚构字幕：这是不该落盘的一句台词",
            "FLASHCLI_TOKEN=虚构密钥-sk-not-real",
            "CUDA_VISIBLE_DEVICES=0 PATH=/mnt/d/private",
            "/mnt/d/private/ref.wav 已加载",
            "progress 完成 80%",
            "前缀 使用 mock 分离执行器",
            "使用 mock 分离执行器 后缀",
            "逐句预测 1/2\n虚构字幕",
            "使用 mock 分离执行器",
        )
        for sample in samples[:-1]:
            self.assertIsNone(safe_log_line(sample), sample)
        self.assertEqual(safe_log_line(samples[-1]), samples[-1])

    def test_non_string_and_empty_input_is_rejected(self):
        self.assertIsNone(safe_log_line(None))  # type: ignore[arg-type]
        self.assertIsNone(safe_log_line(3))  # type: ignore[arg-type]
        self.assertIsNone(safe_log_line(""))
        self.assertIsNone(safe_log_line(" \t "))


class _Clock:
    def __init__(self) -> None:
        self.value = 0.0

    def __call__(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


class LogEmitterTests(unittest.TestCase):
    def test_throttles_and_reports_dropped_lines(self):
        clock = _Clock()
        sent: list[tuple[str, str, int]] = []
        emitter = LogEmitter(
            lambda stream, line, seq: sent.append((stream, line, seq)),
            max_per_second=2,
            clock=clock,
        )
        emitter.emit("runner", "使用 mock 分离执行器")
        emitter.emit("runner", "mock 分离完成")
        emitter.emit("runner", "启动 FlashCLI 分离")
        self.assertEqual(
            [item[1] for item in sent],
            ["使用 mock 分离执行器", "mock 分离完成"],
        )

        clock.advance(1.0)
        emitter.emit("runner", "FlashCLI 分离完成")
        self.assertEqual(
            [item[1] for item in sent],
            [
                "使用 mock 分离执行器",
                "mock 分离完成",
                "日志输出过快，已省略部分行",
                "FlashCLI 分离完成",
            ],
        )
        self.assertEqual([item[2] for item in sent], [1, 2, 3, 4])

    def test_free_form_text_is_dropped_before_sending(self):
        sent: list[tuple[str, str, int]] = []
        emitter = LogEmitter(
            lambda stream, line, seq: sent.append((stream, line, seq))
        )
        emitter.emit("stderr", "虚构字幕：这是不该落盘的一句台词")
        emitter.emit("stderr", "FLASHCLI_TOKEN=虚构密钥")
        emitter.emit("stderr", "/mnt/d/private/ref.wav 已读取")
        self.assertEqual(sent, [])

    def test_controlled_line_is_sent_with_seq(self):
        sent: list[tuple[str, str, int]] = []
        emitter = LogEmitter(
            lambda stream, line, seq: sent.append((stream, line, seq))
        )
        emitter.emit("runner", "参考音频切片已准备")
        self.assertEqual(sent, [("runner", "参考音频切片已准备", 1)])

    def test_unknown_stream_is_dropped(self):
        sent: list[tuple[str, str, int]] = []
        emitter = LogEmitter(
            lambda stream, line, seq: sent.append((stream, line, seq))
        )
        emitter.emit("model", "使用 mock 分离执行器")
        self.assertEqual(sent, [])

    def test_empty_lines_are_dropped(self):
        sent: list[tuple[str, str, int]] = []
        emitter = LogEmitter(lambda stream, line, seq: sent.append((stream, line, seq)))
        emitter.emit("stderr", "   ")
        self.assertEqual(sent, [])


if __name__ == "__main__":
    unittest.main()
