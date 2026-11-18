"""音色克隆 Adapter（节点 07）。

整段成品（``op=script``）一次调用外部 ``voice-clone.py``，脚本自己启动
``flashcli serve`` 并写出成品 mp3。不把脚本源码拷进仓库。
单句重抽仍走受管 ``flashcli serve tts/omnivoice_flashrt:1.0.3``。
``executor=mock`` 写假产物供联调。
"""

from __future__ import annotations

import json
import logging
import os
import subprocess
import sys
import threading
import time
from collections.abc import Callable, Mapping
from typing import Any

from .capability_common import (
    CapabilityFailed,
    CapabilityInvalidParams,
    artifact_entry,
    device_environ,
    executor_mode,
    interruptible_sleep,
    optional_bool,
    optional_int,
    optional_str,
    reject_unknown,
    require_existing_input_path,
    require_relative_output,
    require_str,
    resolve_output_path,
    write_bytes,
)
from .omnivoice_serve import OmniVoiceCloneSession
from .protocol import ExecutionRequest

LOGGER = logging.getLogger("yaomustudio_runner.voice_clone")

# 已准入的克隆权重。脚本默认模型不是这个 ref，调用前必须用环境变量覆盖。
_SCRIPT_MODEL = "tts/omnivoice_flashrt:1.0.3"
_SCRIPT_TIMEOUT_S = 3600.0
_DEFAULT_VOICE_CLONE_SCRIPT = "/mnt/d/demo/voice-clone/voice-clone.py"
_SCRIPT_LANGUAGE = {
    "en": "english",
    "zh": "chinese",
    "zh-CN": "chinese",
    "zh-TW": "chinese",
    "ja": "japanese",
    "ko": "korean",
    "ru": "russian",
    "fr": "french",
    "de": "german",
    "es": "spanish",
    "pt": "portuguese",
    "ar": "arabic",
    "it": "italian",
    "hi": "hindi",
    "id": "indonesian",
    "th": "thai",
    "tr": "turkish",
    "vi": "vietnamese",
}

_PARAM_KEYS = frozenset(
    {
        "executor",
        "op",
        "voice_path",
        "voice_url",
        "subtitle_path",
        "subtitle_url",
        "original_subtitle_path",
        "original_subtitle_url",
        "language",
        "text",
        "original_text",
        "start_ms",
        "end_ms",
        "out",
        "output_path",
        "segments_out",
        "candidates",
        "device",
        "mock_delay_ms",
        "mock_fail",
        "timeout_s",
    }
)


def _first_str(params: Mapping[str, Any], *keys: str) -> str | None:
    for key in keys:
        value = params.get(key)
        if value is None:
            continue
        if not isinstance(value, str) or not value:
            raise CapabilityInvalidParams(f"{key} 必须是非空字符串")
        return value
    return None


class VoiceCloneAdapter:
    adapter_id = "voice_clone"

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._cancel_event = threading.Event()
        self._last_environ: Mapping[str, str] | None = None
        self._session: OmniVoiceCloneSession | None = None
        self._script_proc: subprocess.Popen[bytes] | None = None

    @property
    def ready(self) -> bool:
        return True

    @property
    def last_environ(self) -> Mapping[str, str] | None:
        return self._last_environ

    def describe(self) -> Mapping[str, Any]:
        return {
            "id": self.adapter_id,
            "ready": True,
            "executors": ["flashcli", "mock"],
            "model_ref": "tts/omnivoice_flashrt:1.0.3",
        }

    def probe(self, params: Mapping[str, Any] | None = None) -> Mapping[str, Any]:
        del params
        return {"ok": True, "executor": "flashcli", "probe": "skipped"}

    def execute(
        self,
        request: ExecutionRequest,
        *,
        cancel_event: threading.Event | None = None,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        params = dict(request.params)
        reject_unknown(params, _PARAM_KEYS, label="voice_clone params")
        mode = executor_mode(params)
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
            active_cancel = self._cancel_event
            self._session = None

        voice_raw = _first_str(params, "voice_path", "voice_url")
        if voice_raw is None:
            raise CapabilityInvalidParams("voice_path/voice_url 必填")
        voice_path = require_existing_input_path(voice_raw, field="voice_path")
        language = require_str(params, "language")

        text = optional_str(params, "text")
        original_text = optional_str(params, "original_text") or ""
        start_ms = optional_int(params, "start_ms")
        end_ms = optional_int(params, "end_ms")
        segment_mode = text is not None or start_ms is not None or end_ms is not None
        subtitle_path = None
        original_path = None
        if segment_mode:
            if text is None or start_ms is None or end_ms is None:
                raise CapabilityInvalidParams("segment clone requires text/start_ms/end_ms")
            if start_ms < 0 or end_ms <= start_ms:
                raise CapabilityInvalidParams("segment clone time window invalid")
        else:
            subtitle_raw = _first_str(params, "subtitle_path", "subtitle_url")
            original_raw = _first_str(
                params, "original_subtitle_path", "original_subtitle_url"
            )
            if subtitle_raw is None:
                raise CapabilityInvalidParams("subtitle_path/subtitle_url 必填")
            subtitle_path = require_existing_input_path(
                subtitle_raw, field="subtitle_path"
            )
            if original_raw is not None:
                original_path = require_existing_input_path(
                    original_raw, field="original_subtitle_path"
                )

        # 清洁室 MVP 直接写 WAV（不依赖 MP3 编码器）；默认扩展名必须与内容一致。
        out_token = _first_str(params, "out", "output_path") or "out.wav"
        # output_path 允许绝对路径（落在 attempt 目录内）或相对路径。
        if os.path.isabs(out_token):
            out_abs = os.path.realpath(out_token)
            output_root = os.path.realpath(request.output_dir)
            try:
                inside = os.path.commonpath([output_root, out_abs]) == output_root
            except ValueError:
                inside = False
            if not inside:
                raise CapabilityInvalidParams("output_path 逃逸出输出目录")
            out_rel = os.path.relpath(out_abs, output_root).replace("\\", "/")
            out_rel = require_relative_output(out_rel, field="out")
        else:
            out_rel = require_relative_output(out_token, field="out")

        segments_rel = optional_str(params, "segments_out")
        if segments_rel is not None:
            segments_rel = require_relative_output(segments_rel, field="segments_out")

        candidates = optional_int(params, "candidates", 1) or 1
        if candidates < 1 or candidates > 8:
            raise CapabilityInvalidParams("candidates 必须在 1..8")

        if segment_mode and candidates != 1:
            raise CapabilityInvalidParams("单句克隆仅支持 candidates=1")

        env = device_environ(params)
        self._last_environ = env
        os.makedirs(request.output_dir, exist_ok=True)

        if mode == "mock":
            if segment_mode:
                return self._execute_segment_mock(
                    request,
                    params=params,
                    out_rel=out_rel,
                    text=text,
                    env=env,
                    cancel_event=active_cancel,
                    log=log,
                )
            assert subtitle_path is not None
            return self._execute_mock(
                request,
                params=params,
                voice_path=voice_path,
                subtitle_path=subtitle_path,
                language=language,
                out_rel=out_rel,
                segments_rel=segments_rel,
                candidates=candidates,
                env=env,
                cancel_event=active_cancel,
                log=log,
            )

        if segment_mode:
            return self._execute_segment_flashcli(
                request,
                voice_path=voice_path,
                start_ms=start_ms,
                end_ms=end_ms,
                text=text,
                original_text=original_text,
                language=language,
                out_rel=out_rel,
                env=env,
                cancel_event=active_cancel,
                log=log,
            )

        if optional_str(params, "op") == "script":
            if original_path is None or subtitle_path is None:
                raise CapabilityInvalidParams("voice-clone.py 要求 original_subtitle_path")
            return self._execute_script(
                request,
                voice_path=voice_path,
                subtitle_path=subtitle_path,
                original_path=original_path,
                language=language,
                out_rel=out_rel,
                env=env,
                cancel_event=active_cancel,
                log=log,
            )

        if original_path is None:
            raise CapabilityInvalidParams(
                "flashcli 克隆要求 original_subtitle_path/original_subtitle_url"
            )
        assert subtitle_path is not None
        if candidates != 1:
            raise CapabilityInvalidParams("flashcli 克隆 MVP 仅支持 candidates=1")

        return self._execute_flashcli(
            request,
            voice_path=voice_path,
            subtitle_path=subtitle_path,
            original_path=original_path,
            language=language,
            out_rel=out_rel,
            segments_rel=segments_rel,
            env=env,
            cancel_event=active_cancel,
            log=log,
        )

    def _execute_mock(
        self,
        request: ExecutionRequest,
        *,
        params: Mapping[str, Any],
        voice_path: str,
        subtitle_path: str,
        language: str,
        out_rel: str,
        segments_rel: str | None,
        candidates: int,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        delay_ms = optional_int(params, "mock_delay_ms", 0) or 0
        if delay_ms < 0 or delay_ms > 60_000:
            raise CapabilityInvalidParams("mock_delay_ms 超出范围")
        if log is not None:
            log("runner", "使用 mock 克隆执行器")
        interruptible_sleep(cancel_event, delay_ms=delay_ms)

        if optional_bool(params, "mock_fail", False):
            raise CapabilityFailed("mock 强制失败")

        outputs: list[dict[str, Any]] = []
        for index in range(candidates):
            interruptible_sleep(cancel_event, delay_ms=0)
            if candidates == 1:
                rel = out_rel
            else:
                stem, ext = os.path.splitext(out_rel)
                rel = f"{stem}.c{index + 1}{ext or '.mp3'}"
                rel = require_relative_output(rel, field="out")
            abs_path = resolve_output_path(request.output_dir, rel)
            write_bytes(abs_path, f"MOCK-VOICE-CLONE-{index + 1}\n".encode("utf-8"))
            outputs.append(artifact_entry(rel, abs_path))

        if segments_rel is not None:
            seg_abs = resolve_output_path(request.output_dir, segments_rel)
            payload = {
                "language": language,
                "segments": [
                    {"index": 0, "start_ms": 0, "end_ms": 1000, "candidate": 1}
                ],
                "source": {
                    "voice": os.path.basename(voice_path),
                    "subtitle": os.path.basename(subtitle_path),
                },
            }
            write_bytes(
                seg_abs,
                (json.dumps(payload, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
            )
            outputs.append(artifact_entry(segments_rel, seg_abs))

        LOGGER.info(
            "voice_clone mock 完成 task=%s language=%s candidates=%s cuda=%s",
            request.task_id,
            language,
            candidates,
            env.get("CUDA_VISIBLE_DEVICES"),
        )
        if log is not None:
            log("runner", "mock 克隆完成")
        return {
            "outputs": outputs,
            "executor": "mock",
            "device": {
                "cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES"),
            },
        }

    def _execute_segment_mock(
        self,
        request: ExecutionRequest,
        *,
        params: Mapping[str, Any],
        out_rel: str,
        text: str,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        delay_ms = optional_int(params, "mock_delay_ms", 0) or 0
        if delay_ms < 0 or delay_ms > 60_000:
            raise CapabilityInvalidParams("mock_delay_ms 超出范围")
        if log is not None:
            log("runner", "使用 mock 单句克隆执行器")
        interruptible_sleep(cancel_event, delay_ms=delay_ms)
        if optional_bool(params, "mock_fail", False):
            raise CapabilityFailed("mock 强制失败")
        out_abs = resolve_output_path(request.output_dir, out_rel)
        write_bytes(out_abs, b"MOCK-CLONE-SEGMENT\n")
        LOGGER.info("voice_clone segment mock 完成 task=%s chars=%s", request.task_id, len(text))
        if log is not None:
            log("runner", "mock 单句克隆完成")
        return {
            "outputs": [artifact_entry(out_rel, out_abs)],
            "executor": "mock",
            "segments": 1,
            "device": {"cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES")},
        }

    def _execute_segment_flashcli(
        self,
        request: ExecutionRequest,
        *,
        voice_path: str,
        start_ms: int,
        end_ms: int,
        text: str,
        original_text: str,
        language: str,
        out_rel: str,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        out_abs = resolve_output_path(request.output_dir, out_rel)
        work_dir = os.path.join(request.output_dir, ".flashcli_work")
        if log is not None:
            log("runner", f"启动单句克隆 {start_ms}-{end_ms}ms")
        session = OmniVoiceCloneSession(
            env=env, cancel_event=cancel_event, on_log=log
        )
        with self._lock:
            self._session = session
        try:
            meta = session.run_segment(
                voice_path=voice_path,
                start_ms=start_ms,
                end_ms=end_ms,
                text=text,
                original_text=original_text,
                language=language,
                output_path=out_abs,
                work_dir=work_dir,
            )
        finally:
            with self._lock:
                if self._session is session:
                    self._session = None
        if not os.path.isfile(out_abs) or os.path.getsize(out_abs) <= 0:
            raise CapabilityFailed("未生成有效单句克隆音频")
        if log is not None:
            log("runner", "单句克隆完成")
        return {
            "outputs": [artifact_entry(out_rel, out_abs)],
            "executor": "flashcli",
            "model_ref": meta.get("model_ref"),
            "segments": 1,
            "device": {"cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES")},
        }

    def _execute_flashcli(
        self,
        request: ExecutionRequest,
        *,
        voice_path: str,
        subtitle_path: str,
        original_path: str,
        language: str,
        out_rel: str,
        segments_rel: str | None,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        out_abs = resolve_output_path(request.output_dir, out_rel)
        work_dir = os.path.join(request.output_dir, ".flashcli_work")
        if log is not None:
            log("runner", "启动受管 FlashCLI 克隆")
        session = OmniVoiceCloneSession(
            env=env, cancel_event=cancel_event, on_log=log
        )
        with self._lock:
            self._session = session
        try:
            meta = session.run(
                voice_path=voice_path,
                subtitle_path=subtitle_path,
                original_subtitle_path=original_path,
                language=language,
                output_path=out_abs,
                work_dir=work_dir,
            )
        finally:
            with self._lock:
                if self._session is session:
                    self._session = None

        outputs = [artifact_entry(out_rel, out_abs)]
        if segments_rel is not None:
            seg_abs = resolve_output_path(request.output_dir, segments_rel)
            payload = {
                "language": language,
                "segments": meta.get("segments"),
                "model_ref": meta.get("model_ref"),
                "sample_format": meta.get("sample_format"),
            }
            write_bytes(
                seg_abs,
                (json.dumps(payload, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
            )
            outputs.append(artifact_entry(segments_rel, seg_abs))

        # 先验证计数，再只记录安全整数或固定 unknown；任意对象不触发 __str__。
        raw_segments = meta.get("segments")
        if (
            isinstance(raw_segments, int)
            and not isinstance(raw_segments, bool)
            and raw_segments >= 1
        ):
            segments = raw_segments
        else:
            segments = None
        LOGGER.info(
            "voice_clone flashcli 完成 task=%s language=%s segments=%s",
            request.task_id,
            language,
            "unknown" if segments is None else segments,
        )
        if log is not None and segments is not None:
            # 只允许应用已知整数计数；None/任意对象字符串不进入过程行。
            log("runner", f"克隆完成，句段数 {segments}")
        return {
            "outputs": outputs,
            "executor": "flashcli",
            "model_ref": meta.get("model_ref"),
            "device": {
                "cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES"),
            },
        }

    def _execute_script(
        self,
        request: ExecutionRequest,
        *,
        voice_path: str,
        subtitle_path: str,
        original_path: str,
        language: str,
        out_rel: str,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        script = resolve_voice_clone_script()
        out_abs = resolve_output_path(request.output_dir, out_rel)
        payload = {
            "voice_url": voice_path,
            "original_subtitle_url": original_path,
            "subtitle_url": subtitle_path,
            "output_path": out_abs,
            "language": script_language(language),
        }
        child_env = os.environ.copy()
        child_env.update(env)
        child_env["FLASHCLI_MODEL"] = _SCRIPT_MODEL
        child_env.setdefault("TTS_TYPE", "omnivoice")
        flashcli = child_env.get("FLASHCLI_BIN", "")
        if flashcli:
            child_env["PATH"] = os.path.dirname(flashcli) + os.pathsep + child_env.get("PATH", "")
        argv = [
            sys.executable,
            script,
            "--input=" + json.dumps(payload, ensure_ascii=False),
        ]
        if log is not None:
            log("runner", "开始执行 voice-clone.py")
        proc = subprocess.Popen(
            argv,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env=child_env,
            cwd=os.path.dirname(script) or None,
        )
        with self._lock:
            self._script_proc = proc
        try:
            deadline = time.monotonic() + _SCRIPT_TIMEOUT_S
            while proc.poll() is None:
                if cancel_event.is_set():
                    proc.terminate()
                    raise CapabilityFailed("已取消")
                if time.monotonic() > deadline:
                    proc.kill()
                    raise CapabilityFailed("voice-clone.py 超时")
                time.sleep(0.2)
            if proc.returncode != 0:
                raise CapabilityFailed("voice-clone.py 失败")
        finally:
            with self._lock:
                if self._script_proc is proc:
                    self._script_proc = None
            if proc.poll() is None:
                proc.kill()
        if not os.path.isfile(out_abs) or os.path.getsize(out_abs) <= 0:
            raise CapabilityFailed("未生成成品音频")
        if log is not None:
            log("runner", "voice-clone.py 已完成")
        return {
            "outputs": [artifact_entry(out_rel, out_abs)],
            "executor": "flashcli",
            "model_ref": _SCRIPT_MODEL,
            "device": {"cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES")},
        }

    def cancel(self, *, term_grace_s: float = 1.0, kill_grace_s: float = 5.0) -> bool:
        del term_grace_s, kill_grace_s
        with self._lock:
            self._cancel_event.set()
            session = self._session
            proc = self._script_proc
        if session is not None:
            session.close()
        if proc is not None and proc.poll() is None:
            proc.terminate()
        return True


def script_language(language: str) -> str:
    return _SCRIPT_LANGUAGE.get(language, language)


def resolve_voice_clone_script() -> str:
    raw = os.environ.get("YAOMU_VOICE_CLONE_SCRIPT", "").strip()
    if raw:
        mapped = _windows_script_path(raw)
        for path in (raw, mapped):
            if path and os.path.isfile(path):
                return path
        raise CapabilityFailed("未找到 voice-clone.py")
    if os.path.isfile(_DEFAULT_VOICE_CLONE_SCRIPT):
        return _DEFAULT_VOICE_CLONE_SCRIPT
    raise CapabilityFailed("未找到 voice-clone.py")


def _windows_script_path(path: str) -> str:
    normalized = path.replace("\\", "/")
    if len(normalized) >= 3 and normalized[1] == ":":
        drive = normalized[0].lower()
        rest = normalized[2:].lstrip("/")
        return f"/mnt/{drive}/{rest}"
    return path
