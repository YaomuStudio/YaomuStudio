"""执行过程行的正向允许与发送节流。

边界：只是界面过程反馈，不作为任务成功条件，也不进入业务状态。

默认不转发任意模型/子进程 stdout/stderr 文本。只有本仓适配器真实产生的
固定阶段消息，或数字解析后按模板重建的动态消息，才允许进入 ``log`` 帧。
不做“包含完成/进度”之类子串筛选，也不靠密钥前缀正则证明自由文本安全；
未知文本一律返回 ``None``，由调用方丢弃。
"""

from __future__ import annotations

import re
import threading
import time
from collections.abc import Callable

from .protocol import LOG_STREAMS, MAX_LOG_LINE_CHARS

DEFAULT_MAX_LINES_PER_SECOND = 20

# 子进程输出被省略时只发固定提示，不保留原行或长度。
FLASHCLI_OUTPUT_OMITTED = "FlashCLI 输出已省略（内容不记录）"
CHILD_STDERR_OMITTED = "子进程 stderr 输出已省略（内容不记录）"
RATE_LIMIT_NOTICE = "日志输出过快，已省略部分行"

# 本仓真实产生点（separate / voice_clone / omnivoice_serve / LogEmitter）。
SAFE_STATIC_LINES = frozenset(
    {
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
        FLASHCLI_OUTPUT_OMITTED,
        CHILD_STDERR_OMITTED,
        RATE_LIMIT_NOTICE,
    }
)

_SAFE_DYNAMIC_PATTERNS = (
    re.compile(r"启动单句克隆 ([0-9]{1,12})-([0-9]{1,12})ms"),
    re.compile(r"逐句预测 ([0-9]{1,12})/([0-9]{1,12})"),
    re.compile(r"克隆完成，句段数 ([0-9]{1,12})"),
    re.compile(r"翻译执行中，已用时 ([0-9]{1,6}) 秒"),
    re.compile(r"ASR 执行中，已用时 ([0-9]{1,6}) 秒"),
    re.compile(r"ASR 完成，生成 ([0-9]{1,12}) 条字幕"),
)

LogSend = Callable[[str, str, int], None]
LogCallback = Callable[[str, str], None]


def _validated_int(token: str) -> int | None:
    try:
        return int(token)
    except (TypeError, ValueError):
        return None


def safe_log_line(text: object) -> str | None:
    """返回受控消息的规范文本；未知、动态模板非法或超长时返回 ``None``。

    完整匹配后重建动态消息，避免把任意对象字符串或原始行透传到下游。
    """

    if not isinstance(text, str) or not text:
        return None
    if len(text) > MAX_LOG_LINE_CHARS:
        return None
    if text in SAFE_STATIC_LINES:
        return text

    match = _SAFE_DYNAMIC_PATTERNS[0].fullmatch(text)
    if match is not None:
        start = _validated_int(match.group(1))
        end = _validated_int(match.group(2))
        if start is None or end is None or start < 0 or end <= start:
            return None
        return f"启动单句克隆 {start}-{end}ms"

    match = _SAFE_DYNAMIC_PATTERNS[1].fullmatch(text)
    if match is not None:
        index = _validated_int(match.group(1))
        total = _validated_int(match.group(2))
        if index is None or total is None or total < 1 or index < 1 or index > total:
            return None
        return f"逐句预测 {index}/{total}"

    match = _SAFE_DYNAMIC_PATTERNS[2].fullmatch(text)
    if match is not None:
        count = _validated_int(match.group(1))
        if count is None or count < 1:
            return None
        return f"克隆完成，句段数 {count}"

    match = _SAFE_DYNAMIC_PATTERNS[3].fullmatch(text)
    if match is not None:
        seconds = _validated_int(match.group(1))
        if seconds is None or seconds < 1:
            return None
        return f"翻译执行中，已用时 {seconds} 秒"

    match = _SAFE_DYNAMIC_PATTERNS[4].fullmatch(text)
    if match is not None:
        seconds = _validated_int(match.group(1))
        if seconds is None or seconds < 1:
            return None
        return f"ASR 执行中，已用时 {seconds} 秒"

    match = _SAFE_DYNAMIC_PATTERNS[5].fullmatch(text)
    if match is not None:
        count = _validated_int(match.group(1))
        if count is None or count < 1:
            return None
        return f"ASR 完成，生成 {count} 条字幕"

    return None


class LogEmitter:
    """把适配器的 (stream, line) 转为受控且带单调 seq 的行，并限制每秒行数。

    ``send`` 由主循环提供，负责构造 ``log`` 帧；本类不感知协议帧结构。
    """

    def __init__(
        self,
        send: LogSend,
        *,
        max_per_second: int = DEFAULT_MAX_LINES_PER_SECOND,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        if not callable(send):
            raise TypeError("send 必须可调用")
        self._send = send
        self._max = max(1, int(max_per_second))
        self._clock = clock
        self._lock = threading.Lock()
        self._window_start = clock()
        self._window_count = 0
        self._dropped = 0
        self._seq = 0

    def emit(self, stream: str, line: str) -> None:
        if stream not in LOG_STREAMS:
            return
        cleaned = safe_log_line(line)
        if cleaned is None:
            return
        notice = ""
        with self._lock:
            now = self._clock()
            if now - self._window_start >= 1.0:
                self._window_start = now
                self._window_count = 0
                if self._dropped:
                    self._dropped = 0
                    self._seq += 1
                    notice = RATE_LIMIT_NOTICE
                    notice_seq = self._seq
            if self._window_count >= self._max:
                self._dropped += 1
                return
            self._window_count += 1
            self._seq += 1
            seq = self._seq
        if notice:
            self._send("runner", notice, notice_seq)
        self._send(stream, cleaned, seq)
