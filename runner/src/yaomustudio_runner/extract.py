"""ASR 提取 Adapter（节点 04，``extract.mode=asr``）。

受管入口（硬约束 #5，只使用 bundle README 已确认的参数）：由
``flashcli_runtime.build_asr_argv`` 组装 ``run asr/qwen3_asr:1.0.1`` 的
音频输入、JSON 输出与可选语种；本模块不直接拼命令行。

Bundle 只接受音频；WAV/FLAC/OGG 直通，其余输入先由受控 FFmpeg 转为
16 kHz 单声道 WAV。FlashCLI 输出词级时间戳 JSON，由 ``asr_srt`` 合成为
Core 要求的唯一 ``source.srt``。OCR/asr_ocr 不在此适配器内实现。
"""

from __future__ import annotations

import json
import logging
import os
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from typing import Any

from .asr_srt import (
    AsrResultError,
    SubtitleCue,
    build_cues,
    parse_asr_result,
    render_srt,
)
from .capability_common import (
    CapabilityCanceled,
    CapabilityError,
    CapabilityFailed,
    CapabilityInvalidParams,
    artifact_entry,
    device_environ,
    optional_int,
    reject_unknown,
    require_existing_input_path,
    resolve_output_path,
)
from .ffmpeg import FfmpegAdapter
from .flashcli_runtime import (
    ASR_MODEL_REF,
    asr_language_name,
    build_asr_argv,
    resolve_flashcli_bin,
    run_flashcli,
)
from .protocol import ExecutionRequest

LOGGER = logging.getLogger("yaomustudio_runner.extract")

DEFAULT_OUTPUT_NAME = "source.srt"
DEFAULT_TIMEOUT_S = 3600
MAX_TIMEOUT_S = 24 * 3600
_AUDIO_PASSTHROUGH_EXTENSIONS = frozenset({".wav", ".flac", ".ogg"})
_RAW_JSON_NAME = "raw_asr.json"
_CONVERTED_AUDIO_NAME = "asr-input.wav"

_PARAM_KEYS = frozenset(
    {
        "input",
        "language",
        "extract",
        "device",
        "timeout_s",
    }
)
_EXTRACT_KEYS = frozenset({"mode", "ocr_language", "ocr_quality"})

# ASR 可能长时间没有逐句输出；心跳只证明适配器仍在等待子进程，
# 不伪造成识别进度。间隔内的行数由 LogEmitter 统一节流。
HEARTBEAT_INTERVAL_S = 10.0


def _start_heartbeat(
    log: Callable[[str, str], None] | None,
    stop_event: threading.Event,
) -> threading.Thread | None:
    if log is None:
        return None

    def pump() -> None:
        started = time.monotonic()
        while not stop_event.wait(HEARTBEAT_INTERVAL_S):
            seconds = max(1, int(time.monotonic() - started))
            log("runner", f"ASR 执行中，已用时 {seconds} 秒")

    thread = threading.Thread(
        target=pump,
        name="yaomu-asr-heartbeat",
        daemon=True,
    )
    thread.start()
    return thread


def _language_token(value: object) -> str | None:
    if value is None:
        return None
    if not isinstance(value, str):
        raise CapabilityInvalidParams("language 必须是字符串")
    token = value.strip()
    if not token:
        return None
    if len(token) > 64:
        raise CapabilityInvalidParams("language 过长")
    for char in token:
        if char.isspace() or ord(char) < 0x20 or ord(char) == 0x7F:
            raise CapabilityInvalidParams("language 含空白或控制字符")
    return asr_language_name(token)


def _extract_mode(params: Mapping[str, Any]) -> str:
    raw = params.get("extract")
    if raw is None:
        return "asr"
    if not isinstance(raw, Mapping):
        raise CapabilityInvalidParams("extract 必须是对象")
    unknown = sorted(set(raw) - _EXTRACT_KEYS)
    if unknown:
        raise CapabilityInvalidParams(f"extract 含未知字段: {unknown}")
    mode = raw.get("mode", "asr")
    if mode is None:
        return "asr"
    if not isinstance(mode, str) or not mode:
        raise CapabilityInvalidParams("extract.mode 必须是非空字符串")
    if mode != "asr":
        raise CapabilityInvalidParams("当前 ASR 适配器只支持 extract.mode=asr")
    return mode


def _remove_quietly(path: str | None) -> None:
    if not path:
        return
    try:
        os.unlink(path)
    except FileNotFoundError:
        return
    except OSError:
        LOGGER.warning("清理 ASR 临时文件失败")


class AsrAdapter:
    adapter_id = "asr"

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._cancel_event = threading.Event()
        self._last_argv: tuple[str, ...] | None = None
        self._last_environ: Mapping[str, str] | None = None

    @property
    def last_argv(self) -> tuple[str, ...] | None:
        return self._last_argv

    @property
    def last_environ(self) -> Mapping[str, str] | None:
        return self._last_environ

    def describe(self) -> Mapping[str, Any]:
        try:
            resolve_flashcli_bin()
        except CapabilityError as exc:
            return {
                "id": self.adapter_id,
                "ready": False,
                "reason": exc.code,
                "model_ref": ASR_MODEL_REF,
            }
        return {
            "id": self.adapter_id,
            "ready": True,
            "executor": "flashcli",
            "model_ref": ASR_MODEL_REF,
        }

    def probe(self, params: Mapping[str, Any] | None = None) -> Mapping[str, Any]:
        del params
        resolve_flashcli_bin()
        return {
            "ok": True,
            "executor": "flashcli",
            "model_ref": ASR_MODEL_REF,
            # 独立 probe 需要真实权重与样本；MVP 由安装摘要门控。
            "probe": "skipped",
        }

    def execute(
        self,
        request: ExecutionRequest,
        *,
        cancel_event: threading.Event | None = None,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        params = dict(request.params)
        reject_unknown(params, _PARAM_KEYS, label="asr params")
        _extract_mode(params)

        input_path = require_existing_input_path(params.get("input"), field="input")
        language = _language_token(params.get("language"))
        timeout_s = optional_int(params, "timeout_s", DEFAULT_TIMEOUT_S)
        if timeout_s is None:
            timeout_s = DEFAULT_TIMEOUT_S
        if timeout_s < 1 or timeout_s > MAX_TIMEOUT_S:
            raise CapabilityInvalidParams("timeout_s 超出范围")

        flashcli_bin = resolve_flashcli_bin()
        env = device_environ(params)
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
            active_cancel = self._cancel_event
            self._last_environ = env

        os.makedirs(request.output_dir, exist_ok=True)
        source_abs = resolve_output_path(request.output_dir, DEFAULT_OUTPUT_NAME)
        raw_abs = resolve_output_path(request.output_dir, _RAW_JSON_NAME)
        converted_audio: str | None = None
        heartbeat_stop = threading.Event()
        heartbeat = _start_heartbeat(log, heartbeat_stop)
        try:
            audio_path, converted_audio = self._prepare_audio(
                input_path,
                request.output_dir,
                active_cancel,
                log=log,
            )
            argv = build_asr_argv(
                flashcli_bin,
                audio_path=audio_path,
                output_path=raw_abs,
                language=language,
            )
            with self._lock:
                self._last_argv = tuple(argv)

            LOGGER.info(
                "asr flashcli 启动 task=%s model=%s language=%s",
                request.task_id,
                ASR_MODEL_REF,
                language or "auto",
            )
            if log is not None:
                log("runner", "启动 FlashCLI ASR")
            run_flashcli(
                argv,
                env=env,
                cancel_event=active_cancel,
                timeout_s=float(timeout_s),
                on_log=log,
            )

            if active_cancel.is_set():
                raise CapabilityCanceled("ASR 执行已取消")
            payload = self._read_json(raw_abs)
            try:
                result = parse_asr_result(payload)
            except AsrResultError as exc:
                raise CapabilityFailed(str(exc)) from exc
            cues = build_cues(result.text, result.words)
            if not cues:
                raise CapabilityFailed("ASR 结果未生成有效字幕块")
            self._write_srt(source_abs, cues)
            if log is not None:
                log("runner", f"ASR 完成，生成 {len(cues)} 条字幕")

            return {
                "outputs": [artifact_entry(DEFAULT_OUTPUT_NAME, source_abs)],
                "executor": "flashcli",
                "model_ref": ASR_MODEL_REF,
                "detected_language": result.language,
                "segments": len(cues),
                "device": {
                    "cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES"),
                },
            }
        finally:
            heartbeat_stop.set()
            if heartbeat is not None:
                heartbeat.join(timeout=1.0)
            _remove_quietly(converted_audio)
            _remove_quietly(raw_abs)

    def cancel(self, *, term_grace_s: float = 1.0, kill_grace_s: float = 5.0) -> bool:
        del term_grace_s, kill_grace_s
        with self._lock:
            self._cancel_event.set()
        return True

    def _prepare_audio(
        self,
        input_path: str,
        output_dir: str,
        cancel_event: threading.Event,
        *,
        log: Callable[[str, str], None] | None,
    ) -> tuple[str, str | None]:
        suffix = os.path.splitext(input_path)[1].lower()
        if suffix in _AUDIO_PASSTHROUGH_EXTENSIONS:
            return input_path, None
        converted = resolve_output_path(output_dir, _CONVERTED_AUDIO_NAME)
        if log is not None:
            log("runner", "转换 ASR 输入为 16 kHz 单声道 WAV")
        # 独立的 FfmpegAdapter 实例：进程树隔离/取消语义与导出槽一致，
        # 但不会与主 Runner 的 ffmpeg 适配器共享执行状态。
        try:
            FfmpegAdapter().extract_audio(
                input_path,
                converted,
                cancel_event=cancel_event,
                timeout_s=600.0,
            )
            if not os.path.isfile(converted) or os.path.getsize(converted) <= 0:
                raise CapabilityFailed("ASR 输入音频转换未生成有效产物")
        except BaseException:
            _remove_quietly(converted)
            raise
        return converted, converted

    @staticmethod
    def _read_json(path: str) -> object:
        try:
            with open(path, "r", encoding="utf-8") as handle:
                return json.load(handle)
        except (OSError, json.JSONDecodeError) as exc:
            raise CapabilityFailed("FlashCLI 未生成可解析的 ASR JSON") from exc

    @staticmethod
    def _write_srt(path: str, cues: Sequence[SubtitleCue]) -> None:
        text = render_srt(cues)
        try:
            with open(path, "w", encoding="utf-8", newline="\n") as handle:
                handle.write(text)
        except OSError as exc:
            raise CapabilityFailed("写入 ASR 字幕失败") from exc
        if os.path.getsize(path) <= 0:
            raise CapabilityFailed("ASR 字幕产物为空")
