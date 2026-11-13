"""ASR 词级时间戳合成 SRT。

FlashCLI 的 Qwen3-ASR bundle 输出 JSON：

```json
{"language": "English", "text": "Hello world.",
 "time_stamps": [{"text": "Hello", "start_time": 0.0, "end_time": 0.4}, ...]}
```

Core 的 04 产物契约要求恰好一个 UTF-8 ``.srt``。本模块只做纯数据转换：
优先用带标点的全文与词时间戳对齐，按句末/逗号标点、词间停顿和显示宽度切块；
对齐不足时退回按词分组。时间戳修正负责保证非负、非零时长且单调不重叠。
"""

from __future__ import annotations

import math
import unicodedata
from collections.abc import Mapping, Sequence
from dataclasses import dataclass

DEFAULT_MAX_WORD_GAP_MS = 2000.0
DEFAULT_MAX_LINE_WIDTH = 80

# 中英文句末与逗号；ASR 文本的标点用于自然断句。
BREAK_PUNCTUATIONS = frozenset("。！？；.!?;，,")


class AsrResultError(ValueError):
    """ASR JSON 结构不满足合成字幕的最低要求。"""


@dataclass(frozen=True)
class AsrWord:
    text: str
    start: float
    end: float


@dataclass(frozen=True)
class AsrResult:
    language: str
    text: str
    words: tuple[AsrWord, ...]


@dataclass(frozen=True)
class SubtitleCue:
    start: float
    end: float
    text: str


def _finite_number(value: object) -> float | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    number = float(value)
    if not math.isfinite(number):
        return None
    return number


def normalize_words(raw: object) -> list[AsrWord]:
    """过滤非法词项；时间非负，结束时间不早于开始时间。"""

    if isinstance(raw, (str, bytes)) or not isinstance(raw, Sequence):
        return []
    words: list[AsrWord] = []
    for item in raw:
        if not isinstance(item, Mapping):
            continue
        text = item.get("text")
        if not isinstance(text, str) or not text:
            continue
        start = _finite_number(item.get("start_time"))
        end = _finite_number(item.get("end_time"))
        if start is None:
            continue
        if end is None:
            end = start
        start = max(0.0, start)
        end = max(start, end)
        words.append(AsrWord(text=text, start=start, end=end))
    return words


def parse_asr_result(payload: object) -> AsrResult:
    if not isinstance(payload, Mapping):
        raise AsrResultError("ASR 输出不是 JSON 对象")
    language = payload.get("language")
    if not isinstance(language, str):
        language = ""
    text = payload.get("text")
    if not isinstance(text, str):
        text = ""
    words = normalize_words(payload.get("time_stamps"))
    if not text.strip() and not words:
        raise AsrResultError("ASR 结果为空")
    if not words:
        raise AsrResultError("ASR 结果缺少词级时间戳")
    return AsrResult(language=language.strip(), text=text, words=tuple(words))


def _display_width(char: str) -> int:
    return 2 if unicodedata.east_asian_width(char) in ("W", "F") else 1


def _word_display_width(text: str) -> int:
    return sum(_display_width(char) for char in text if char.isalnum())


def _join_text(left: str, right: str) -> str:
    if not left:
        return right
    if not right:
        return left
    if left[-1].isascii() and left[-1].isalnum() and right[0].isascii() and right[0].isalnum():
        return left + " " + right
    return left + right


def _join_words(words: Sequence[AsrWord]) -> str:
    text = ""
    for word in words:
        text = _join_text(text, word.text)
    return text


def _char_to_word_mapping(full_text: str, words: Sequence[AsrWord]) -> tuple[dict[int, int], int]:
    """把全文的字母/数字字符按顺序映射到词索引。"""

    word_chars: list[int] = []
    for word_index, word in enumerate(words):
        for char in word.text:
            if char.isalnum():
                word_chars.append(word_index)
    mapping: dict[int, int] = {}
    cursor = 0
    total_alnum = 0
    for char_index, char in enumerate(full_text):
        if not char.isalnum():
            continue
        total_alnum += 1
        if cursor < len(word_chars):
            mapping[char_index] = word_chars[cursor]
            cursor += 1
    return mapping, total_alnum


def _split_with_text(
    full_text: str,
    words: Sequence[AsrWord],
    *,
    max_word_gap_ms: float,
    max_line_width: int,
) -> list[SubtitleCue]:
    mapping, total_alnum = _char_to_word_mapping(full_text, words)
    if total_alnum == 0 or len(mapping) / total_alnum < 0.8:
        return _split_by_words(
            words,
            max_word_gap_ms=max_word_gap_ms,
            max_line_width=max_line_width,
        )

    cues: list[SubtitleCue] = []
    segment_start = 0
    current_start: int | None = None
    current_end: int | None = None
    last_word: int | None = None
    cue_start: float | None = None
    cue_end = 0.0
    line_width = 0

    def emit(end_char: int) -> None:
        nonlocal segment_start, current_start, current_end, last_word
        nonlocal cue_start, cue_end, line_width
        if current_start is None or current_end is None or cue_start is None:
            return
        text = full_text[segment_start:end_char].strip()
        if text:
            cues.append(SubtitleCue(start=cue_start, end=cue_end, text=text))
        segment_start = end_char
        current_start = None
        current_end = None
        last_word = None
        cue_start = None
        cue_end = 0.0
        line_width = 0

    for char_index, char in enumerate(full_text):
        word_index = mapping.get(char_index) if char.isalnum() else None
        if word_index is not None:
            if word_index != last_word and current_start is not None:
                force_break = False
                if last_word is not None:
                    gap_ms = (words[word_index].start - words[last_word].end) * 1000.0
                    if gap_ms > max_word_gap_ms:
                        force_break = True
                if not force_break and line_width > 0:
                    next_width = _word_display_width(words[word_index].text)
                    if line_width + next_width > max_line_width:
                        force_break = True
                if force_break:
                    emit(char_index)
            if current_start is None:
                current_start = word_index
            if cue_start is None:
                cue_start = words[word_index].start
            else:
                cue_start = min(cue_start, words[word_index].start)
            cue_end = max(cue_end, words[word_index].end)
            current_end = word_index
            last_word = word_index
            line_width += _display_width(char)
        else:
            line_width += _display_width(char)
            if char in BREAK_PUNCTUATIONS and current_start is not None:
                emit(char_index + 1)

    emit(len(full_text))
    return cues


def _split_by_words(
    words: Sequence[AsrWord],
    *,
    max_word_gap_ms: float,
    max_line_width: int,
) -> list[SubtitleCue]:
    cues: list[SubtitleCue] = []
    group: list[AsrWord] = []
    width = 0

    def flush() -> None:
        nonlocal group, width
        if not group:
            return
        text = _join_words(group)
        if text.strip():
            cues.append(
                SubtitleCue(
                    start=min(word.start for word in group),
                    end=max(word.end for word in group),
                    text=text,
                )
            )
        group = []
        width = 0

    for word in words:
        word_width = _word_display_width(word.text)
        if group:
            gap_ms = (word.start - group[-1].end) * 1000.0
            if gap_ms > max_word_gap_ms or (width > 0 and width + word_width > max_line_width):
                flush()
        group.append(word)
        width += word_width
    flush()
    return cues


def _to_ms(seconds: float) -> int:
    if not math.isfinite(seconds):
        return 0
    return max(0, int(round(seconds * 1000.0)))


def _fix_cues(cues: Sequence[SubtitleCue]) -> list[SubtitleCue]:
    fixed: list[SubtitleCue] = []
    for cue in cues:
        text = " ".join(cue.text.split())
        if not text:
            continue
        start_ms = _to_ms(cue.start)
        end_ms = max(_to_ms(cue.end), start_ms + 1)
        if fixed:
            previous = fixed[-1]
            previous_start_ms = _to_ms(previous.start)
            previous_end_ms = _to_ms(previous.end)
            if start_ms < previous_end_ms:
                if start_ms > previous_start_ms:
                    previous_end_ms = max(start_ms, previous_start_ms + 1)
                    fixed[-1] = SubtitleCue(
                        start=previous_start_ms / 1000.0,
                        end=previous_end_ms / 1000.0,
                        text=previous.text,
                    )
                    if start_ms < previous_end_ms:
                        start_ms = previous_end_ms
                        end_ms = max(end_ms, start_ms + 1)
                else:
                    start_ms = previous_end_ms
                    end_ms = max(end_ms, start_ms + 1)
        fixed.append(
            SubtitleCue(
                start=start_ms / 1000.0,
                end=end_ms / 1000.0,
                text=text,
            )
        )
    return fixed


def build_cues(
    text: str,
    words: Sequence[AsrWord],
    *,
    max_word_gap_ms: float = DEFAULT_MAX_WORD_GAP_MS,
    max_line_width: int = DEFAULT_MAX_LINE_WIDTH,
) -> list[SubtitleCue]:
    if not words:
        return []
    full_text = " ".join(text.split())
    if full_text:
        cues = _split_with_text(
            full_text,
            words,
            max_word_gap_ms=max_word_gap_ms,
            max_line_width=max_line_width,
        )
    else:
        cues = _split_by_words(
            words,
            max_word_gap_ms=max_word_gap_ms,
            max_line_width=max_line_width,
        )
    return _fix_cues(cues)


def format_srt_time(seconds: float) -> str:
    if not math.isfinite(seconds):
        seconds = 0.0
    total_ms = int(round(max(0.0, seconds) * 1000.0))
    hours, remainder = divmod(total_ms, 3600 * 1000)
    minutes, remainder = divmod(remainder, 60 * 1000)
    secs, millis = divmod(remainder, 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def render_srt(cues: Sequence[SubtitleCue]) -> str:
    blocks = [
        f"{index}\n{format_srt_time(cue.start)} --> {format_srt_time(cue.end)}\n{cue.text}"
        for index, cue in enumerate(cues, 1)
    ]
    return "\n\n".join(blocks) + ("\n" if blocks else "")
