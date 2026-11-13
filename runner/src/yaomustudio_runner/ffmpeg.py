"""FFmpeg / ffprobe 适配：固定路径注入与白名单 argv 构造。

原则：

- 所有参数来自结构化字段，argv 始终是列表并交给 ``shell=False`` 启动；
  不拼接命令字符串，也不解析用户提供的命令行。
- 路径只接受应用拥有的受控位置：绝对路径，且拒绝 shell 元字符与控制字符
  （Core 应以资产 ID / 专属输出目录生成路径，而不是直接使用用户文件名）。
- 字幕样式与编码参数走白名单校验；非法值在构造阶段直接拒绝。
- ``FFMPEG_BIN`` / ``FFPROBE_BIN`` 由宿主注入应用拥有的固定版本；生产解析
  （``resolve_media_tool_paths``）拒绝临时从 PATH 解析任意版本，PATH 回退只
  用于 ``describe_media_tools`` 的本地诊断并标注 ``source=path``。缺少工具时
  抛出带修复动作的清晰错误。
- 执行入口（``FfmpegAdapter.execute``）只接受本文件定义的白名单操作与字段；
  输出只能是 ``output_dir`` 内的相对路径，绝对路径或 ``..`` 一律拒绝。
"""

from __future__ import annotations

import json
import logging
import os
import re
import shutil
import subprocess
import threading
import time
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any

from .executor import isolated_process_kwargs, terminate_process_tree
from .protocol import ExecutionRequest
from .windows_job import attach_windows_process_job

MEDIA_TOOL_UNAVAILABLE = "MEDIA_TOOL_UNAVAILABLE"
MEDIA_TOOL_FAILED = "MEDIA_TOOL_FAILED"
MEDIA_TOOL_TIMEOUT = "MEDIA_TOOL_TIMEOUT"
MEDIA_TOOL_CANCELED = "MEDIA_TOOL_CANCELED"
MEDIA_TOOL_INVALID_PARAMS = "MEDIA_TOOL_INVALID_PARAMS"

ENV_FFMPEG = "FFMPEG_BIN"
ENV_FFPROBE = "FFPROBE_BIN"

DEFAULT_STDOUT_LIMIT = 64 * 1024
PROBE_STDOUT_LIMIT = 4 * 1024 * 1024

_SHELL_METACHARS = frozenset(";|&`$<>")
_CONTROL_CHARS = frozenset("\x00\n\r")

_FFMPEG_PRESETS = frozenset(
    {
        "ultrafast",
        "superfast",
        "veryfast",
        "faster",
        "fast",
        "medium",
        "slow",
        "slower",
        "veryslow",
    }
)
_MIX_CODECS = {"aac": "aac", "mp3": "libmp3lame", "pcm_s16le": "pcm_s16le"}
_AUDIO_CODECS = {"aac": "aac", "mp3": "libmp3lame", "copy": "copy"}
# 成品合成含滤镜链，音频必须重编码；不接受 copy。
_MERGE_AUDIO_CODECS = {"aac": "aac", "mp3": "libmp3lame"}
_SAMPLE_RATES = frozenset({8000, 11025, 16000, 22050, 24000, 32000, 44100, 48000, 96000})
_VIDEO_CODECS = {"h264": "libx264", "hevc": "libx265", "copy": "copy"}
_BURN_VIDEO_CODECS = {"h264": "libx264", "hevc": "libx265"}

_COLOUR_RE = re.compile(r"^(?:&H[0-9A-Fa-f]{6}|&H[0-9A-Fa-f]{8}|#[0-9A-Fa-f]{6})$")
_FONT_NAME_FORBIDDEN = frozenset(",:;'\"\\[]{}")


class MediaToolError(RuntimeError):
    code = MEDIA_TOOL_FAILED
    retryable = False

    def __init__(self, message: str):
        super().__init__(message)
        self.message = message


class MediaToolUnavailable(MediaToolError):
    """媒体工具缺失或未按固定版本注入。"""

    code = MEDIA_TOOL_UNAVAILABLE

    def __init__(self, message: str, *, action_ids: Sequence[str] = ("open_engine_setup",)):
        super().__init__(message)
        self.action_ids = tuple(action_ids)


class MediaToolFailed(MediaToolError):
    """媒体命令以非零退出码结束。"""

    def __init__(self, message: str, *, exit_code: int | None = None):
        super().__init__(message)
        self.exit_code = exit_code


class MediaToolTimeout(MediaToolError):
    """媒体命令超时；已尝试终止本应用进程树。"""

    code = MEDIA_TOOL_TIMEOUT
    retryable = True

    def __init__(self, message: str, *, kill_confirmed: bool = False):
        super().__init__(message)
        self.kill_confirmed = kill_confirmed


class MediaToolCanceled(MediaToolError):
    """当前媒体命令因取消被终止。"""

    code = MEDIA_TOOL_CANCELED
    retryable = False


class MediaToolStopUnconfirmed(MediaToolError):
    """The adapter is quarantined because an owned process tree may still write."""

    code = "MEDIA_TOOL_STOP_UNCONFIRMED"
    retryable = False


class MediaToolInvalidParams(MediaToolError):
    """execute 参数不符合适配器白名单口径。"""

    code = MEDIA_TOOL_INVALID_PARAMS
    retryable = False


@dataclass(frozen=True)
class MediaToolPaths:
    ffmpeg: str
    # ffprobe 用于探测与产物媒体属性；缺失时仍可执行转码/混音，但不报告媒体属性。
    ffprobe: str | None = None


@dataclass(frozen=True)
class MediaToolStatus:
    ffmpeg_available: bool
    ffprobe_available: bool
    ffmpeg_source: str | None
    ffprobe_source: str | None
    error: str | None

    def as_dict(self) -> dict[str, Any]:
        return {
            "ffmpeg_available": self.ffmpeg_available,
            "ffprobe_available": self.ffprobe_available,
            "ffmpeg_source": self.ffmpeg_source,
            "ffprobe_source": self.ffprobe_source,
            "error": self.error,
        }


@dataclass(frozen=True)
class MediaCommandResult:
    operation: str
    exit_code: int
    duration_ms: int
    stdout: bytes = b""
    stdout_truncated: bool = False


def _locate(
    env_key: str,
    name: str,
    environ: Mapping[str, str],
    *,
    allow_path_fallback: bool,
) -> tuple[str, str]:
    injected = environ.get(env_key)
    if injected:
        path = os.path.expanduser(injected)
        if not os.path.isabs(path):
            raise MediaToolUnavailable(f"{env_key} 必须是绝对路径：{path}")
        if not (os.path.isfile(path) and os.access(path, os.X_OK)):
            raise MediaToolUnavailable(f"{env_key} 指向的 {name} 不存在或不可执行：{path}")
        return path, "env"
    if not allow_path_fallback:
        raise MediaToolUnavailable(
            f"未注入 {env_key}；应用只运行宿主指定的固定版本 {name}，"
            f"请通过 {env_key} 注入其绝对路径"
        )
    found = shutil.which(name, path=environ.get("PATH"))
    if not found:
        raise MediaToolUnavailable(
            f"未找到 {name}；请通过 {env_key} 注入应用拥有的固定版本"
        )
    return found, "path"


def resolve_media_tool_paths(environ: Mapping[str, str] | None = None) -> MediaToolPaths:
    """解析生产用 ffmpeg/ffprobe 路径；只接受 ``FFMPEG_BIN``/``FFPROBE_BIN`` 注入。

    缺失或不合法时抛出 MediaToolUnavailable，不回退到 PATH 上的任意版本。
    """

    env = os.environ if environ is None else environ
    ffmpeg, _ = _locate(ENV_FFMPEG, "ffmpeg", env, allow_path_fallback=False)
    ffprobe, _ = _locate(ENV_FFPROBE, "ffprobe", env, allow_path_fallback=False)
    return MediaToolPaths(ffmpeg=ffmpeg, ffprobe=ffprobe)


def resolve_execution_tools(environ: Mapping[str, str] | None = None) -> MediaToolPaths:
    """解析执行用工具：ffmpeg 必需，ffprobe 可缺失（仅影响探测与媒体属性）。

    与 ``resolve_media_tool_paths`` 相同，只接受显式注入，不回退 PATH。
    """

    env = os.environ if environ is None else environ
    ffmpeg, _ = _locate(ENV_FFMPEG, "ffmpeg", env, allow_path_fallback=False)
    try:
        ffprobe, _ = _locate(ENV_FFPROBE, "ffprobe", env, allow_path_fallback=False)
    except MediaToolUnavailable:
        ffprobe = None
    return MediaToolPaths(ffmpeg=ffmpeg, ffprobe=ffprobe)


def describe_media_tools(environ: Mapping[str, str] | None = None) -> MediaToolStatus:
    """不抛异常的工具状态，用于 hello 帧与诊断；PATH 回退仅用于诊断标注。"""

    env = os.environ if environ is None else environ
    paths: dict[str, str | None] = {}
    sources: dict[str, str | None] = {}
    errors: list[str] = []
    for env_key, name in ((ENV_FFMPEG, "ffmpeg"), (ENV_FFPROBE, "ffprobe")):
        try:
            path, source = _locate(
                env_key, name, env, allow_path_fallback=True
            )
        except MediaToolUnavailable as exc:
            paths[name] = None
            sources[name] = None
            errors.append(str(exc))
        else:
            paths[name] = path
            sources[name] = source
    return MediaToolStatus(
        ffmpeg_available=paths["ffmpeg"] is not None,
        ffprobe_available=paths["ffprobe"] is not None,
        ffmpeg_source=sources["ffmpeg"],
        ffprobe_source=sources["ffprobe"],
        error="；".join(errors) if errors else None,
    )


# -- 参数校验 -------------------------------------------------------------


def _require_path(value: object, what: str) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError(f"{what} 必须是非空路径字符串")
    if len(value) > 4096:
        raise ValueError(f"{what} 过长")
    for ch in value:
        if ch in _CONTROL_CHARS or ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise ValueError(f"{what} 含控制字符")
        if ch in _SHELL_METACHARS:
            raise ValueError(f"{what} 含被拒绝的 shell 元字符: {ch}")
    if not os.path.isabs(value):
        raise ValueError(f"{what} 必须是绝对路径")
    return value


def _require_bin(value: object, name: str) -> str:
    return _require_path(value, f"{name} 路径")


def _require_int(value: object, what: str, *, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{what} 必须是整数")
    if not minimum <= value <= maximum:
        raise ValueError(f"{what} 必须在 {minimum}..{maximum} 之间")
    return value


def _require_choice(value: object, what: str, choices: Sequence[str] | Mapping[str, str]) -> str:
    allowed = tuple(choices)
    if not isinstance(value, str) or value not in allowed:
        raise ValueError(f"{what} 必须是 {sorted(allowed)} 之一")
    return value


def _require_ms(value: object, what: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{what} 必须是非负整数毫秒")
    return value


def _ms_to_seconds(value: int) -> str:
    return f"{value / 1000.0:.3f}"


def _require_distinct_output(input_paths: Sequence[str], output_path: str) -> None:
    output = os.path.normcase(os.path.abspath(output_path))
    for path in input_paths:
        if os.path.normcase(os.path.abspath(path)) == output:
            raise ValueError("输出路径不能与输入路径相同")


def _ffmpeg_prefix(ffmpeg_bin: str) -> list[str]:
    return [
        _require_bin(ffmpeg_bin, "ffmpeg"),
        "-hide_banner",
        "-nostdin",
        "-loglevel",
        "warning",
    ]


def _overwrite_arg(overwrite: bool) -> str:
    return "-y" if overwrite else "-n"


def _escape_filter_value(value: str) -> str:
    """转义 ffmpeg filtergraph 中会被解析为分隔符的字符。"""

    escaped: list[str] = []
    for ch in value:
        if ch in "\\':,[];":
            escaped.append("\\")
        escaped.append(ch)
    return "".join(escaped)


_WINDOWS_ABSOLUTE_PATH_RE = re.compile(r"^[A-Za-z]:[\\/]")


def _subtitle_filter_value(path: str) -> str:
    """把字幕路径转成 ``subtitles=filename=`` 的取值。

    Windows 盘符/UNC 路径改写成 POSIX 斜杠、转义盘符冒号并用单引号包裹，
    例如 ``'C\\:/work/subs.srt'``。单引号包裹后空格、逗号、方括号按字面
    处理；单引号本身在 FFmpeg 的 subtitles filename 中无法可靠转义，因此
    显式拒绝而不是生成必然失败的命令。POSIX 路径保持既有转义输出。
    """

    if _WINDOWS_ABSOLUTE_PATH_RE.match(path) or path.startswith("\\\\"):
        posix = path.replace("\\", "/")
        if "'" in posix:
            raise ValueError("字幕路径含单引号，无法安全写入 FFmpeg 滤镜")
        return "'" + posix.replace(":", "\\:") + "'"
    return _escape_filter_value(path)


def _normalise_colour(value: object) -> str:
    if not isinstance(value, str) or not _COLOUR_RE.fullmatch(value):
        raise ValueError("字幕颜色必须是 &HAABBGGRR、&HBBGGRR 或 #RRGGBB")
    if value.startswith("#"):
        red, green, blue = value[1:3], value[3:5], value[5:7]
        return f"&H00{blue}{green}{red}".upper()
    return value.upper()


def _font_name(value: object) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError("font_name 必须是非空字符串")
    if len(value) > 64:
        raise ValueError("font_name 过长")
    for ch in value:
        if ch in _FONT_NAME_FORBIDDEN or ch in _SHELL_METACHARS:
            raise ValueError("font_name 含被拒绝的字符")
        if ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise ValueError("font_name 含控制字符")
    return value


def _border_style(value: object) -> str:
    number = _require_int(value, "border_style", minimum=1, maximum=3)
    if number not in (1, 3):
        raise ValueError("border_style 只能是 1 或 3")
    return str(number)


def _require_number(value: object, what: str, minimum: float, maximum: float) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{what} 必须是数字")
    number = float(value)
    if not minimum <= number <= maximum:
        raise ValueError(f"{what} 必须在 {minimum}..{maximum} 之间")
    return number


_STYLE_FIELDS: tuple[tuple[str, str, Any], ...] = (
    ("font_name", "FontName", _font_name),
    ("font_size", "FontSize", lambda v: str(_require_int(v, "font_size", minimum=6, maximum=200))),
    ("primary_colour", "PrimaryColour", _normalise_colour),
    ("outline_colour", "OutlineColour", _normalise_colour),
    ("back_colour", "BackColour", _normalise_colour),
    ("border_style", "BorderStyle", _border_style),
    ("outline", "Outline", lambda v: f"{float(_require_number(v, 'outline', 0.0, 10.0)):.1f}"),
    ("shadow", "Shadow", lambda v: f"{float(_require_number(v, 'shadow', 0.0, 10.0)):.1f}"),
    ("alignment", "Alignment", lambda v: str(_require_int(v, "alignment", minimum=1, maximum=9))),
    ("margin_l", "MarginL", lambda v: str(_require_int(v, "margin_l", minimum=0, maximum=2000))),
    ("margin_r", "MarginR", lambda v: str(_require_int(v, "margin_r", minimum=0, maximum=2000))),
    ("margin_v", "MarginV", lambda v: str(_require_int(v, "margin_v", minimum=0, maximum=2000))),
)
_STYLE_FIELD_NAMES = frozenset(name for name, _, _ in _STYLE_FIELDS)


def _format_force_style(style: Mapping[str, Any] | None) -> str | None:
    if style is None:
        return None
    if not isinstance(style, Mapping):
        raise ValueError("subtitle style 必须是对象")
    unknown = set(style) - _STYLE_FIELD_NAMES
    if unknown:
        raise ValueError(f"未知字幕样式字段: {sorted(unknown)}")
    parts: list[str] = []
    for key, ass_key, validator in _STYLE_FIELDS:
        value = style.get(key)
        if value is not None:
            parts.append(f"{ass_key}={validator(value)}")
    if not parts:
        return None
    return ",".join(parts)


# -- argv 构造 ------------------------------------------------------------


def build_probe_argv(input_path: str, ffprobe_bin: str) -> list[str]:
    """ffprobe 探测 argv；输出 JSON 供适配层解析。"""

    return [
        _require_bin(ffprobe_bin, "ffprobe"),
        "-v",
        "error",
        "-print_format",
        "json",
        "-show_format",
        "-show_streams",
        _require_path(input_path, "输入路径"),
    ]


@dataclass(frozen=True)
class AudioMixInput:
    path: str
    volume: float = 1.0
    # 音频在输出时间轴上的起点（毫秒）；通过 adelay 补齐前导静音。
    start_ms: int = 0


def _validate_volume(value: object) -> float:
    return _require_number(value, "volume", 0.0, 2.0)


def build_mix_argv(
    inputs: Sequence[AudioMixInput],
    output_path: str,
    ffmpeg_bin: str,
    *,
    audio_codec: str = "aac",
    bitrate_kbps: int | None = None,
    sample_rate: int | None = None,
    overwrite: bool = False,
) -> list[str]:
    """多轨混音 argv；音量、编码器、采样率均为白名单参数。"""

    if isinstance(inputs, (str, bytes)) or not isinstance(inputs, Sequence):
        raise ValueError("inputs 必须是 AudioMixInput 数组")
    if not 1 <= len(inputs) <= 32:
        raise ValueError("inputs 数量必须在 1..32 之间")
    items: list[AudioMixInput] = []
    for item in inputs:
        if not isinstance(item, AudioMixInput):
            raise ValueError("inputs 每一项必须是 AudioMixInput")
        items.append(
            AudioMixInput(
                path=_require_path(item.path, "输入路径"),
                volume=_validate_volume(item.volume),
                start_ms=_require_ms(item.start_ms, "start_ms"),
            )
        )
    output = _require_path(output_path, "输出路径")
    _require_distinct_output([item.path for item in items], output)
    codec = _require_choice(audio_codec, "audio_codec", _MIX_CODECS)
    mapped_codec = _MIX_CODECS[codec]
    if bitrate_kbps is not None:
        bitrate = _require_int(bitrate_kbps, "bitrate_kbps", minimum=32, maximum=512)
    else:
        bitrate = None
    if sample_rate is not None:
        sample_rate = _require_int(sample_rate, "sample_rate", minimum=1, maximum=384000)
        if sample_rate not in _SAMPLE_RATES:
            raise ValueError(f"sample_rate 必须是 {sorted(_SAMPLE_RATES)} 之一")

    chains = [f"[{index}:a]volume={item.volume:.2f}" for index, item in enumerate(items)]
    # adelay 让句级配音落在各自的时间位置上；未指定起点时保持原时间轴。
    for index, item in enumerate(items):
        if item.start_ms > 0:
            chains[index] += f",adelay={item.start_ms}|{item.start_ms}"
    if len(items) == 1:
        graph = chains[0] + "[mix]"
    else:
        graph = "".join(
            chain + f"[a{index}];" for index, chain in enumerate(chains)
        )
        labels = "".join(f"[a{index}]" for index in range(len(items)))
        graph += f"{labels}amix=inputs={len(items)}:duration=longest:normalize=0[mix]"

    argv = _ffmpeg_prefix(ffmpeg_bin)
    for item in items:
        argv.extend(["-i", item.path])
    argv.extend(["-filter_complex", graph, "-map", "[mix]", "-c:a", mapped_codec])
    if bitrate is not None:
        argv.extend(["-b:a", f"{bitrate}k"])
    if sample_rate is not None:
        argv.extend(["-ar", str(sample_rate)])
    argv.extend([_overwrite_arg(overwrite), output])
    return argv


def build_burn_subtitles_argv(
    input_path: str,
    subtitle_path: str,
    output_path: str,
    ffmpeg_bin: str,
    *,
    style: Mapping[str, Any] | None = None,
    start_ms: int | None = None,
    end_ms: int | None = None,
    video_codec: str = "h264",
    crf: int | None = None,
    preset: str = "medium",
    audio_codec: str = "aac",
    audio_bitrate_kbps: int | None = None,
    overwrite: bool = False,
) -> list[str]:
    """字幕烧录 argv；时间窗为输出侧裁剪（视频/字幕时间轴保持一致）。"""

    source = _require_path(input_path, "输入路径")
    subtitles = _require_path(subtitle_path, "字幕路径")
    output = _require_path(output_path, "输出路径")
    _require_distinct_output([source], output)

    filter_value = f"subtitles=filename={_subtitle_filter_value(subtitles)}"
    force_style = _format_force_style(style)
    if force_style:
        filter_value += f":force_style='{force_style}'"

    if start_ms is not None:
        start_ms = _require_ms(start_ms, "start_ms")
    if end_ms is not None:
        end_ms = _require_ms(end_ms, "end_ms")
    if start_ms is not None and end_ms is not None and start_ms >= end_ms:
        raise ValueError("start_ms 必须小于 end_ms")

    video = _require_choice(video_codec, "video_codec", sorted(_BURN_VIDEO_CODECS))
    preset = _require_choice(preset, "preset", sorted(_FFMPEG_PRESETS))
    if crf is not None:
        crf = _require_int(crf, "crf", minimum=0, maximum=51)
    audio = _require_choice(audio_codec, "audio_codec", _AUDIO_CODECS)
    mapped_audio = _AUDIO_CODECS[audio]
    if audio == "copy":
        if audio_bitrate_kbps is not None:
            raise ValueError("audio_codec=copy 时不能指定 bitrate_kbps")
        bitrate = None
    elif audio_bitrate_kbps is not None:
        bitrate = _require_int(audio_bitrate_kbps, "audio_bitrate_kbps", minimum=32, maximum=512)
    else:
        bitrate = None

    argv = _ffmpeg_prefix(ffmpeg_bin)
    argv.extend(["-i", source])
    if start_ms is not None:
        argv.extend(["-ss", _ms_to_seconds(start_ms)])
    if end_ms is not None:
        argv.extend(["-to", _ms_to_seconds(end_ms)])
    argv.extend(["-vf", filter_value, "-c:v", _BURN_VIDEO_CODECS[video]])
    if crf is not None:
        argv.extend(["-crf", str(crf)])
    argv.extend(["-preset", preset, "-c:a", mapped_audio])
    if bitrate is not None:
        argv.extend(["-b:a", f"{bitrate}k"])
    argv.extend([_overwrite_arg(overwrite), output])
    return argv


def build_transcode_argv(
    input_path: str,
    output_path: str,
    ffmpeg_bin: str,
    *,
    video_codec: str | None = None,
    audio_codec: str | None = None,
    crf: int | None = None,
    preset: str | None = None,
    audio_bitrate_kbps: int | None = None,
    overwrite: bool = False,
) -> list[str]:
    """格式转换/重封装 argv；未指定编码器时按 ``-c copy`` 重封装。"""

    source = _require_path(input_path, "输入路径")
    output = _require_path(output_path, "输出路径")
    _require_distinct_output([source], output)

    if video_codec is None and audio_codec is None:
        video_codec = "copy"
        audio_codec = "copy"
    video = _require_choice(video_codec or "copy", "video_codec", _VIDEO_CODECS)
    audio = _require_choice(audio_codec or "copy", "audio_codec", _AUDIO_CODECS)
    mapped_video = _VIDEO_CODECS[video]
    mapped_audio = _AUDIO_CODECS[audio]

    if mapped_video == "copy":
        if crf is not None or preset is not None:
            raise ValueError("video_codec=copy 时不能指定 crf/preset")
    else:
        if crf is not None:
            crf = _require_int(crf, "crf", minimum=0, maximum=51)
        if preset is not None:
            preset = _require_choice(preset, "preset", sorted(_FFMPEG_PRESETS))
    if audio == "copy":
        if audio_bitrate_kbps is not None:
            raise ValueError("audio_codec=copy 时不能指定 audio_bitrate_kbps")
        bitrate = None
    elif audio_bitrate_kbps is not None:
        bitrate = _require_int(audio_bitrate_kbps, "audio_bitrate_kbps", minimum=32, maximum=512)
    else:
        bitrate = None

    argv = _ffmpeg_prefix(ffmpeg_bin)
    argv.extend(["-i", source, "-c:v", mapped_video])
    if crf is not None:
        argv.extend(["-crf", str(crf)])
    if preset is not None:
        argv.extend(["-preset", preset])
    argv.extend(["-c:a", mapped_audio])
    if bitrate is not None:
        argv.extend(["-b:a", f"{bitrate}k"])
    argv.extend([_overwrite_arg(overwrite), output])
    return argv


def build_audio_extract_argv(
    input_path: str,
    output_path: str,
    ffmpeg_bin: str,
) -> list[str]:
    """ASR 前置音频提取：固定 16 kHz 单声道 WAV，不接受额外用户参数。"""

    source = _require_path(input_path, "输入路径")
    output = _require_path(output_path, "输出路径")
    _require_distinct_output([source], output)
    argv = _ffmpeg_prefix(ffmpeg_bin)
    argv.extend(
        [
            "-i",
            source,
            "-vn",
            "-ac",
            "1",
            "-ar",
            "16000",
            "-f",
            "wav",
            _overwrite_arg(True),
            output,
        ]
    )
    return argv


def build_video_merge_argv(
    video_src: str,
    audio_inputs: Sequence[AudioMixInput],
    output_path: str,
    ffmpeg_bin: str,
    *,
    subtitle_path: str | None = None,
    style: Mapping[str, Any] | None = None,
    video_codec: str = "h264",
    crf: int | None = None,
    preset: str = "medium",
    audio_codec: str = "aac",
    audio_bitrate_kbps: int | None = None,
    sample_rate: int | None = None,
    overwrite: bool = False,
) -> list[str]:
    """成品合成 argv：源视频 + 按时间轴混入的音频轨 + 可选字幕烧录。

    单条 ffmpeg 命令完成音轨混合（adelay/amix）与字幕烧录，输出一条 H.264/AAC
    成片；视频时间轴跟随源视频（``-shortest`` 截断超出部分）。
    """

    source = _require_path(video_src, "视频源路径")
    output = _require_path(output_path, "输出路径")
    if isinstance(audio_inputs, (str, bytes)) or not isinstance(audio_inputs, Sequence):
        raise ValueError("audio_inputs 必须是 AudioMixInput 数组")
    if len(audio_inputs) > 32:
        raise ValueError("audio_inputs 数量超过上限")
    items: list[AudioMixInput] = []
    for item in audio_inputs:
        if not isinstance(item, AudioMixInput):
            raise ValueError("audio_inputs 每一项必须是 AudioMixInput")
        items.append(
            AudioMixInput(
                path=_require_path(item.path, "音频输入路径"),
                volume=_validate_volume(item.volume),
                start_ms=_require_ms(item.start_ms, "start_ms"),
            )
        )
    _require_distinct_output([source, *(item.path for item in items)], output)

    video = _require_choice(video_codec, "video_codec", sorted(_BURN_VIDEO_CODECS))
    preset = _require_choice(preset, "preset", sorted(_FFMPEG_PRESETS))
    if crf is not None:
        crf = _require_int(crf, "crf", minimum=0, maximum=51)
    audio = _require_choice(audio_codec, "audio_codec", _MERGE_AUDIO_CODECS)
    mapped_audio = _MERGE_AUDIO_CODECS[audio]
    if audio_bitrate_kbps is not None:
        bitrate = _require_int(audio_bitrate_kbps, "audio_bitrate_kbps", minimum=32, maximum=512)
    else:
        bitrate = None
    if sample_rate is not None:
        sample_rate = _require_int(sample_rate, "sample_rate", minimum=1, maximum=384000)
        if sample_rate not in _SAMPLE_RATES:
            raise ValueError(f"sample_rate 必须是 {sorted(_SAMPLE_RATES)} 之一")

    # 视频滤镜链：字幕烧录（可选）。
    video_filter = None
    if subtitle_path is not None:
        subtitles = _require_path(subtitle_path, "字幕路径")
        video_filter = f"subtitles=filename={_subtitle_filter_value(subtitles)}"
        force_style = _format_force_style(style)
        if force_style:
            video_filter += f":force_style='{force_style}'"
    elif style is not None:
        raise ValueError("未提供字幕路径时不能指定字幕样式")

    # 音频滤镜链：每个输入按 start_ms 延迟后混音。
    audio_filter = None
    if items:
        audio_chains = []
        for index, item in enumerate(items):
            chain = f"[{index + 1}:a]volume={item.volume:.2f}"
            if item.start_ms > 0:
                chain += f",adelay={item.start_ms}|{item.start_ms}"
            audio_chains.append(chain + f"[a{index}]")
        labels = "".join(f"[a{index}]" for index in range(len(items)))
        audio_filter = ";".join(audio_chains) + f";{labels}amix=inputs={len(items)}:duration=longest:normalize=0[mix]"

    argv = _ffmpeg_prefix(ffmpeg_bin)
    argv.extend(["-i", source])
    for item in items:
        argv.extend(["-i", item.path])
    filters: list[str] = []
    if video_filter:
        filters.append(f"[0:v]{video_filter}[vout]")
    if audio_filter:
        filters.append(audio_filter)
    if filters:
        argv.extend(["-filter_complex", ";".join(filters)])
    if video_filter:
        argv.extend(["-map", "[vout]"])
    else:
        argv.extend(["-map", "0:v:0"])
    if items:
        argv.extend(["-map", "[mix]"])
    else:
        argv.append("-an")
    argv.extend(["-c:v", _BURN_VIDEO_CODECS[video]])
    if crf is not None:
        argv.extend(["-crf", str(crf)])
    argv.extend(["-preset", preset, "-c:a", mapped_audio])
    if bitrate is not None:
        argv.extend(["-b:a", f"{bitrate}k"])
    if sample_rate is not None:
        argv.extend(["-ar", str(sample_rate)])
    if items:
        argv.append("-shortest")
    argv.extend([_overwrite_arg(overwrite), output])
    return argv


# -- 执行适配 -------------------------------------------------------------


class FfmpegAdapter:
    """只执行本文件定义的白名单操作（mix/burn_subtitles/transcode/video_merge）。"""

    adapter_id = "ffmpeg"

    def __init__(
        self,
        paths: MediaToolPaths | None = None,
        *,
        environ: Mapping[str, str] | None = None,
        logger: logging.Logger | None = None,
    ) -> None:
        self._paths = paths if paths is not None else resolve_execution_tools(environ)
        self._logger = logger or logging.getLogger("yaomustudio_runner.ffmpeg")
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._pgid: int | None = None
        self._windows_job = None
        self._quarantined = False
        self._cancel_event = threading.Event()

    @property
    def paths(self) -> MediaToolPaths:
        return self._paths

    def describe(self) -> Mapping[str, Any]:
        return {
            "id": self.adapter_id,
            "ready": True,
            "operations": ["mix", "burn_subtitles", "transcode", "video_merge"],
            "ffprobe_available": self._paths.ffprobe is not None,
        }

    def probe(self, input_path: str, *, timeout_s: float = 30.0) -> dict[str, Any]:
        if self._paths.ffprobe is None:
            raise MediaToolUnavailable(
                "未注入 FFPROBE_BIN；无法探测媒体属性（不影响已注入 ffmpeg 的转码执行）"
            )
        argv = build_probe_argv(input_path, self._paths.ffprobe)
        result = self._run("probe", argv, timeout_s=timeout_s, stdout_limit=PROBE_STDOUT_LIMIT)
        if result.stdout_truncated:
            raise MediaToolError("ffprobe 输出超过大小上限")
        try:
            payload = json.loads(result.stdout.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise MediaToolError("ffprobe 输出不是有效 JSON") from None
        if not isinstance(payload, dict):
            raise MediaToolError("ffprobe 输出不是 JSON 对象")
        return payload

    # -- execute 白名单入口 -------------------------------------------------

    def execute(
        self,
        request: ExecutionRequest,
        *,
        cancel_event: threading.Event | None = None,
    ) -> dict[str, Any]:
        """执行一次导出计划；只接受本文件定义的操作与字段。

        输出只能是 ``request.output_dir`` 内的相对路径；返回的清单也只用相对
        路径，Core 负责校验并复制到用户授权目录。``cancel_event`` 由主循环在
        cancel 帧到达时置位，媒体进程树会被立即终止。
        """

        params = dict(request.params)
        op = params.get("op")
        if not isinstance(op, str) or not op:
            raise MediaToolInvalidParams("params.op 必须是非空字符串")
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
        output_dir = request.output_dir
        os.makedirs(output_dir, exist_ok=True)

        if op == "mix":
            _reject_unknown(params, _MIX_PARAM_KEYS)
            inputs = _parse_audio_inputs(_require_param(params, "inputs"))
            out_rel = _require_relative_output(params.get("out"))
            output = _resolve_output_path(output_dir, out_rel)
            argv = build_mix_argv(
                inputs,
                output,
                self._paths.ffmpeg,
                audio_codec=_optional_choice(params, "audio_codec", _MIX_CODECS, "aac"),
                bitrate_kbps=_optional_int(params, "bitrate_kbps"),
                sample_rate=_optional_int(params, "sample_rate"),
                overwrite=_optional_bool(params, "overwrite"),
            )
            self._run("mix", argv, timeout_s=600.0)
        elif op == "video_merge":
            _reject_unknown(params, _VIDEO_MERGE_PARAM_KEYS)
            video_src = _require_param(params, "video_src")
            audio_inputs = _parse_audio_inputs(params.get("audio_inputs", []))
            out_rel = _require_relative_output(params.get("out"))
            output = _resolve_output_path(output_dir, out_rel)
            subtitle = _optional_str(params, "subtitle")
            style = _optional_mapping(params, "style")
            try:
                argv = build_video_merge_argv(
                    video_src,
                    audio_inputs,
                    output,
                    self._paths.ffmpeg,
                    subtitle_path=subtitle,
                    style=style,
                    video_codec=_optional_choice(params, "video_codec", _BURN_VIDEO_CODECS, "h264"),
                    crf=_optional_int(params, "crf"),
                    preset=_optional_choice(params, "preset", _FFMPEG_PRESETS, "medium"),
                    audio_codec=_optional_choice(params, "audio_codec", _MERGE_AUDIO_CODECS, "aac"),
                    audio_bitrate_kbps=_optional_int(params, "audio_bitrate_kbps"),
                    sample_rate=_optional_int(params, "sample_rate"),
                    overwrite=_optional_bool(params, "overwrite"),
                )
            except ValueError as exc:
                raise MediaToolInvalidParams(str(exc)) from exc
            self._run("video_merge", argv, timeout_s=3600.0)
        elif op == "burn_subtitles":
            _reject_unknown(params, _BURN_PARAM_KEYS)
            input_path = _require_param(params, "input")
            subtitle = _require_param(params, "subtitle")
            out_rel = _require_relative_output(params.get("out"))
            output = _resolve_output_path(output_dir, out_rel)
            try:
                argv = build_burn_subtitles_argv(
                    input_path,
                    subtitle,
                    output,
                    self._paths.ffmpeg,
                    style=_optional_mapping(params, "style"),
                    start_ms=_optional_int(params, "start_ms"),
                    end_ms=_optional_int(params, "end_ms"),
                    video_codec=_optional_choice(params, "video_codec", _BURN_VIDEO_CODECS, "h264"),
                    crf=_optional_int(params, "crf"),
                    preset=_optional_choice(params, "preset", _FFMPEG_PRESETS, "medium"),
                    audio_codec=_optional_choice(params, "audio_codec", _AUDIO_CODECS, "aac"),
                    audio_bitrate_kbps=_optional_int(params, "audio_bitrate_kbps"),
                    overwrite=_optional_bool(params, "overwrite"),
                )
            except ValueError as exc:
                raise MediaToolInvalidParams(str(exc)) from exc
            self._run("burn_subtitles", argv, timeout_s=3600.0)
        elif op == "transcode":
            _reject_unknown(params, _TRANSCODE_PARAM_KEYS)
            input_path = _require_param(params, "input")
            out_rel = _require_relative_output(params.get("out"))
            output = _resolve_output_path(output_dir, out_rel)
            argv = build_transcode_argv(
                input_path,
                output,
                self._paths.ffmpeg,
                video_codec=_optional_str(params, "video_codec"),
                audio_codec=_optional_str(params, "audio_codec"),
                crf=_optional_int(params, "crf"),
                preset=_optional_str(params, "preset"),
                audio_bitrate_kbps=_optional_int(params, "audio_bitrate_kbps"),
                overwrite=_optional_bool(params, "overwrite"),
            )
            self._run("transcode", argv, timeout_s=3600.0)
        else:
            raise MediaToolInvalidParams(f"不支持的操作: {op}")

        size = os.path.getsize(output)
        if size <= 0:
            raise MediaToolFailed(f"{op} 未生成有效产物")
        entry: dict[str, Any] = {"rel_path": out_rel, "size_bytes": size}
        media = self._media_metadata(output)
        if media:
            entry["media"] = media
        return {"outputs": [entry]}

    def _media_metadata(self, path: str) -> dict[str, Any] | None:
        """尽力探测产物媒体属性；ffprobe 缺失或失败时省略，不伪造。"""

        if self._paths.ffprobe is None:
            return None
        try:
            payload = self.probe(path)
        except MediaToolError as exc:
            # 不记录产物路径或异常正文；只保留受控错误码。
            self._logger.warning(
                "产物媒体探测失败（%s；内容不记录）", exc.code
            )
            return None
        meta: dict[str, Any] = {}
        format_info = payload.get("format")
        if isinstance(format_info, dict):
            for key in ("format_name", "duration", "size"):
                if key in format_info:
                    meta[key] = format_info[key]
        streams = payload.get("streams")
        if isinstance(streams, list):
            summary = []
            for stream in streams:
                if not isinstance(stream, dict):
                    continue
                summary.append(
                    {
                        key: stream[key]
                        for key in ("codec_type", "codec_name", "width", "height",
                                    "sample_rate", "channels", "duration")
                        if key in stream
                    }
                )
            meta["streams"] = summary
        return meta or None

    # -- 取消 --------------------------------------------------------------

    def cancel(self, *, term_grace_s: float = 1.0, kill_grace_s: float = 5.0) -> bool:
        """终止当前媒体命令树；返回是否确认进程树退出。"""

        with self._lock:
            self._cancel_event.set()
            proc = self._proc
            pgid = self._pgid
            windows_job = self._windows_job
        if proc is None:
            return not self._quarantined
        confirmed = terminate_process_tree(
            proc,
            pgid=pgid,
            windows_job=windows_job,
            term_grace_s=term_grace_s,
            kill_grace_s=kill_grace_s,
            logger=self._logger,
        )
        if confirmed:
            self._clear_proc(proc)
        return confirmed

    # -- 命令执行 ----------------------------------------------------------

    def mix(
        self,
        inputs: Sequence[AudioMixInput],
        output_path: str,
        *,
        timeout_s: float = 300.0,
        **options: Any,
    ) -> MediaCommandResult:
        argv = build_mix_argv(inputs, output_path, self._paths.ffmpeg, **options)
        return self._run("mix", argv, timeout_s=timeout_s)

    def burn_subtitles(
        self,
        input_path: str,
        subtitle_path: str,
        output_path: str,
        *,
        timeout_s: float = 600.0,
        **options: Any,
    ) -> MediaCommandResult:
        argv = build_burn_subtitles_argv(
            input_path,
            subtitle_path,
            output_path,
            self._paths.ffmpeg,
            **options,
        )
        return self._run("burn_subtitles", argv, timeout_s=timeout_s)

    def transcode(
        self,
        input_path: str,
        output_path: str,
        *,
        timeout_s: float = 300.0,
        **options: Any,
    ) -> MediaCommandResult:
        argv = build_transcode_argv(input_path, output_path, self._paths.ffmpeg, **options)
        return self._run("transcode", argv, timeout_s=timeout_s)

    def extract_audio(
        self,
        input_path: str,
        output_path: str,
        *,
        cancel_event: threading.Event | None = None,
        timeout_s: float = 600.0,
    ) -> MediaCommandResult:
        """ASR 前置：从任意媒体提取 16 kHz 单声道 WAV。"""

        argv = build_audio_extract_argv(input_path, output_path, self._paths.ffmpeg)
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
        return self._run("extract_audio", argv, timeout_s=timeout_s)

    def video_merge(
        self,
        video_src: str,
        audio_inputs: Sequence[AudioMixInput],
        output_path: str,
        *,
        timeout_s: float = 3600.0,
        **options: Any,
    ) -> MediaCommandResult:
        argv = build_video_merge_argv(
            video_src,
            audio_inputs,
            output_path,
            self._paths.ffmpeg,
            **options,
        )
        return self._run("video_merge", argv, timeout_s=timeout_s)

    def _run(
        self,
        operation: str,
        argv: Sequence[str],
        *,
        timeout_s: float,
        stdout_limit: int = DEFAULT_STDOUT_LIMIT,
    ) -> MediaCommandResult:
        if timeout_s <= 0:
            raise ValueError("timeout_s 必须为正数")
        with self._lock:
            if self._quarantined or self._proc is not None:
                raise MediaToolStopUnconfirmed("上次媒体进程树退出未确认；执行器已隔离")
        started = time.monotonic()
        self._logger.info("执行受控媒体命令: %s", operation)
        windows_job = None
        try:
            proc = subprocess.Popen(
                list(argv),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                shell=False,
                close_fds=True,
                **isolated_process_kwargs(suspend_windows=True),
            )
        except (FileNotFoundError, PermissionError) as exc:
            raise MediaToolUnavailable(
                f"无法启动 {operation}：媒体工具路径不可用"
            ) from exc
        if os.name == "nt":
            try:
                windows_job = attach_windows_process_job(proc)
            except Exception as exc:
                try:
                    proc.kill()
                    proc.wait(timeout=5.0)
                except BaseException as stop_exc:
                    raise MediaToolStopUnconfirmed(
                        "无法建立或清理 Windows 媒体进程树"
                    ) from stop_exc
                raise MediaToolFailed("无法建立 Windows 媒体进程树隔离") from exc

        with self._lock:
            self._proc = proc
            self._pgid = None if os.name == "nt" else proc.pid
            self._windows_job = windows_job
            pgid = self._pgid
            cancel_raced = self._cancel_event.is_set()
        if cancel_raced:
            if not terminate_process_tree(
                proc,
                pgid=pgid,
                windows_job=windows_job,
                term_grace_s=1.0,
                kill_grace_s=5.0,
                logger=self._logger,
            ):
                self._mark_quarantined()
                raise MediaToolStopUnconfirmed("取消后媒体进程树退出未确认")

        try:
            stdout, stderr = proc.communicate(timeout=timeout_s)
        except subprocess.TimeoutExpired:
            confirmed = terminate_process_tree(
                proc,
                pgid=pgid,
                windows_job=windows_job,
                term_grace_s=1.0,
                kill_grace_s=5.0,
                logger=self._logger,
            )
            try:
                proc.communicate(timeout=1.0)
            except subprocess.TimeoutExpired:
                pass
            if not confirmed:
                self._mark_quarantined()
                raise MediaToolStopUnconfirmed("超时后媒体进程树退出未确认") from None
            self._clear_proc(proc)
            raise MediaToolTimeout(
                f"{operation} 超时（{timeout_s:.1f}s）", kill_confirmed=confirmed
            ) from None
        except BaseException:
            if not terminate_process_tree(
                proc,
                pgid=pgid,
                windows_job=windows_job,
                term_grace_s=1.0,
                kill_grace_s=5.0,
                logger=self._logger,
            ):
                self._mark_quarantined()
                raise MediaToolStopUnconfirmed("媒体异常后进程树退出未确认") from None
            self._clear_proc(proc)
            raise

        # A natural leader exit is insufficient when it left descendants in its
        # owned session. Retain the captured group identity until it is empty.
        if not terminate_process_tree(
            proc,
            pgid=pgid,
            windows_job=windows_job,
            term_grace_s=1.0,
            kill_grace_s=5.0,
            logger=self._logger,
        ):
            self._mark_quarantined()
            raise MediaToolStopUnconfirmed("媒体命令返回后进程树退出未确认")
        self._clear_proc(proc)

        duration_ms = int((time.monotonic() - started) * 1000)
        truncated = len(stdout) > stdout_limit
        bounded_stdout = stdout[:stdout_limit]
        if stderr:
            # stderr 可能含字幕、密钥、环境、路径或媒体 metadata 正文；
            # 本地诊断只保留受控 operation 与退出码，不记录原文或长度。
            self._logger.warning(
                "媒体工具 stderr 输出已省略（%s，退出码 %s；内容不记录）",
                operation,
                proc.returncode,
            )
        with self._lock:
            canceled = self._cancel_event.is_set()
        if canceled and proc.returncode != 0:
            raise MediaToolCanceled(f"{operation} 已取消")
        if proc.returncode != 0:
            raise MediaToolFailed(
                f"{operation} 失败（退出码 {proc.returncode}）", exit_code=proc.returncode
            )
        return MediaCommandResult(
            operation=operation,
            exit_code=proc.returncode,
            duration_ms=duration_ms,
            stdout=bounded_stdout,
            stdout_truncated=truncated,
        )

    def _clear_proc(self, proc: subprocess.Popen[bytes]) -> None:
        windows_job = None
        with self._lock:
            if self._proc is proc:
                self._proc = None
                self._pgid = None
                windows_job = self._windows_job
                self._windows_job = None
        if windows_job is not None and not windows_job.close():
            self._logger.warning("关闭 Windows 媒体 Job Object 失败")

    def _mark_quarantined(self) -> None:
        with self._lock:
            self._quarantined = True


# -- execute 参数白名单 ----------------------------------------------------

_MIX_PARAM_KEYS = frozenset(
    {"op", "inputs", "out", "audio_codec", "bitrate_kbps", "sample_rate", "overwrite"}
)
_VIDEO_MERGE_PARAM_KEYS = frozenset(
    {
        "op",
        "video_src",
        "audio_inputs",
        "out",
        "subtitle",
        "style",
        "video_codec",
        "crf",
        "preset",
        "audio_codec",
        "audio_bitrate_kbps",
        "sample_rate",
        "overwrite",
    }
)
_BURN_PARAM_KEYS = frozenset(
    {
        "op",
        "input",
        "subtitle",
        "out",
        "style",
        "start_ms",
        "end_ms",
        "video_codec",
        "crf",
        "preset",
        "audio_codec",
        "audio_bitrate_kbps",
        "overwrite",
    }
)
_TRANSCODE_PARAM_KEYS = frozenset(
    {
        "op",
        "input",
        "out",
        "video_codec",
        "audio_codec",
        "crf",
        "preset",
        "audio_bitrate_kbps",
        "overwrite",
    }
)


def _reject_unknown(params: Mapping[str, Any], allowed: frozenset[str]) -> None:
    unknown = set(params) - allowed
    if unknown:
        raise MediaToolInvalidParams(f"execute 参数含未知字段: {sorted(unknown)}")


def _require_param(params: Mapping[str, Any], key: str) -> Any:
    if key not in params:
        raise MediaToolInvalidParams(f"execute 参数缺少 {key}")
    return params[key]


def _optional_str(params: Mapping[str, Any], key: str) -> str | None:
    value = params.get(key)
    if value is None:
        return None
    if not isinstance(value, str):
        raise MediaToolInvalidParams(f"{key} 必须是字符串")
    return value


def _optional_mapping(params: Mapping[str, Any], key: str) -> Mapping[str, Any] | None:
    value = params.get(key)
    if value is None:
        return None
    if not isinstance(value, Mapping):
        raise MediaToolInvalidParams(f"{key} 必须是对象")
    return value


def _optional_int(params: Mapping[str, Any], key: str) -> int | None:
    value = params.get(key)
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int):
        raise MediaToolInvalidParams(f"{key} 必须是整数")
    return value


def _optional_bool(params: Mapping[str, Any], key: str) -> bool:
    value = params.get(key, False)
    if not isinstance(value, bool):
        raise MediaToolInvalidParams(f"{key} 必须是布尔值")
    return value


def _optional_choice(
    params: Mapping[str, Any],
    key: str,
    choices: Sequence[str] | Mapping[str, str],
    default: str,
) -> str:
    value = params.get(key)
    if value is None:
        return default
    if not isinstance(value, str) or value not in choices:
        raise MediaToolInvalidParams(f"{key} 必须是 {sorted(choices)} 之一")
    return value


def _parse_audio_inputs(value: object) -> list[AudioMixInput]:
    if isinstance(value, (str, bytes)) or not isinstance(value, Sequence):
        raise MediaToolInvalidParams("inputs 必须是数组")
    items: list[AudioMixInput] = []
    for raw in value:
        if not isinstance(raw, Mapping):
            raise MediaToolInvalidParams("inputs 每一项必须是对象")
        unknown = set(raw) - frozenset({"path", "volume", "start_ms"})
        if unknown:
            raise MediaToolInvalidParams(f"inputs 含未知字段: {sorted(unknown)}")
        path = raw.get("path")
        if not isinstance(path, str):
            raise MediaToolInvalidParams("inputs[].path 必须是字符串")
        volume = raw.get("volume", 1.0)
        if isinstance(volume, bool) or not isinstance(volume, (int, float)):
            raise MediaToolInvalidParams("inputs[].volume 必须是数字")
        start_ms = raw.get("start_ms", 0)
        if isinstance(start_ms, bool) or not isinstance(start_ms, int):
            raise MediaToolInvalidParams("inputs[].start_ms 必须是整数")
        items.append(
            AudioMixInput(path=path, volume=float(volume), start_ms=int(start_ms))
        )
    return items


def _require_relative_output(value: object) -> str:
    """产物必须是 attempt_dir 内的简单相对路径；拒绝绝对路径与 .. 片段。"""

    if not isinstance(value, str) or not value:
        raise MediaToolInvalidParams("out 必须是非空相对路径")
    if len(value) > 255:
        raise MediaToolInvalidParams("out 过长")
    for ch in value:
        if ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise MediaToolInvalidParams("out 含控制字符")
    if os.path.isabs(value):
        raise MediaToolInvalidParams("out 必须是相对路径")
    parts = value.replace("\\", "/").split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise MediaToolInvalidParams("out 不得包含空片段、. 或 ..")
    return value


def _resolve_output_path(output_dir: str, rel_path: str) -> str:
    output_root = os.path.realpath(output_dir)
    candidate = os.path.realpath(os.path.join(output_dir, rel_path))
    if candidate == output_root:
        raise MediaToolInvalidParams("out 不能指向输出目录本身")
    try:
        inside = os.path.commonpath([output_root, candidate]) == output_root
    except ValueError:
        inside = False
    if not inside:
        raise MediaToolInvalidParams("out 逃逸出输出目录")
    return candidate
