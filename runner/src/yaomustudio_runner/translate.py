"""字幕翻译 Adapter（节点 06，mlstools ``subtitle_translation``）。

契约以 ``mlstools 1.0.0.20261103`` 的 ``--tool subtitle_translation`` 为准：
只接受 ``MLSTOOLS_BIN`` 注入的绝对路径二进制，argv 固定为
``--tool subtitle_translation --input <json>``，不构造文档外参数。

边界：
- Core 已把源字幕 revision 物化为冻结 SRT，``subtitle_url`` 必须是该文件；
- 产物固定为 ``output_dir/translated.srt``，Runner 只回收相对路径与大小；
- mlstools 的 stdout/stderr 可能包含字幕正文，**不逐行写本地日志**；
- 实测 mlstools 可能“退出码 0 但无产物”，因此以产物存在/非空为准。
"""

from __future__ import annotations

import json
import logging
import os
import subprocess
import threading
import time
from collections.abc import Callable, Mapping
from typing import Any

from .capability_common import (
    CapabilityCanceled,
    CapabilityError,
    CapabilityFailed,
    CapabilityInvalidParams,
    artifact_entry,
    optional_bool,
    optional_int,
    optional_str,
    reject_unknown,
    require_existing_input_path,
    resolve_output_path,
)
from .executor import isolated_process_kwargs, terminate_process_tree
from .protocol import ExecutionRequest

LOGGER = logging.getLogger("yaomustudio_runner.translate")

ENV_MLSTOOLS_BIN = "MLSTOOLS_BIN"
DEFAULT_OUTPUT_NAME = "translated.srt"
DEFAULT_TIMEOUT_S = 4 * 3600
MAX_TIMEOUT_S = 24 * 3600
MAX_CAPTURED_BYTES = 64 * 1024
_CAPTURE_CHUNK = 8192
_RESULT_MARKER = b"TASK_RESULT_INFO:"
# 云端翻译可能数分钟无逐句输出；心跳只证明适配器仍在等待子进程，
# 不伪造成逐句进度。间隔内的行数由 LogEmitter 统一节流。
HEARTBEAT_INTERVAL_S = 10.0


def _start_heartbeat(
    log: Callable[[str, str], None] | None,
    stop_event: threading.Event,
) -> threading.Thread | None:
    """按固定间隔发送“仍在执行”的受控行；不读取或转发 mlstools 原文。"""

    if log is None:
        return None

    def pump() -> None:
        started = time.monotonic()
        while not stop_event.wait(HEARTBEAT_INTERVAL_S):
            seconds = max(1, int(time.monotonic() - started))
            log("runner", f"翻译执行中，已用时 {seconds} 秒")

    thread = threading.Thread(
        target=pump,
        name="yaomu-translate-heartbeat",
        daemon=True,
    )
    thread.start()
    return thread

# UI/Core 目标语言 code → mlstools target_language 枚举。
# zh-TW 不在 mlstools 枚举内，明确拒绝而不是静默降级为简体。
TARGET_LANGUAGE_MAP = {
    "en": "english",
    "zh": "chinese",
    "zh-CN": "chinese",
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
        "subtitle_url",
        "language",
        "bilingual",
        "model_name",
        "timeout_s",
    }
)

MODEL_RUNTIME_UNAVAILABLE = "MODEL_RUNTIME_UNAVAILABLE"


class TranslationRuntimeUnavailable(CapabilityError):
    """mlstools 未注入/不可执行；Core 映射为“打开模型安装”。"""

    code = MODEL_RUNTIME_UNAVAILABLE
    retryable = False

    def __init__(self, message: str):
        super().__init__(message, action_ids=("open_model_setup",))


def resolve_mlstools_bin(environ: Mapping[str, str] | None = None) -> str:
    env = os.environ if environ is None else environ
    raw = env.get(ENV_MLSTOOLS_BIN, "").strip()
    if not raw:
        raise TranslationRuntimeUnavailable(
            f"未注入 {ENV_MLSTOOLS_BIN}：无法启动 mlstools 字幕翻译"
        )
    if len(raw) > 4096:
        raise TranslationRuntimeUnavailable(f"{ENV_MLSTOOLS_BIN} 路径过长")
    if "\x00" in raw or any(ord(ch) < 0x20 for ch in raw):
        raise TranslationRuntimeUnavailable(f"{ENV_MLSTOOLS_BIN} 含非法字符")
    if not os.path.isabs(raw):
        raise TranslationRuntimeUnavailable(f"{ENV_MLSTOOLS_BIN} 必须是绝对路径")
    real = os.path.realpath(raw)
    if not os.path.isfile(real):
        raise TranslationRuntimeUnavailable(f"{ENV_MLSTOOLS_BIN} 不是可读文件")
    if os.name != "nt" and not os.access(real, os.X_OK):
        raise TranslationRuntimeUnavailable(f"{ENV_MLSTOOLS_BIN} 不可执行")
    return real


def target_language(code: object) -> str:
    if not isinstance(code, str) or not code:
        raise CapabilityInvalidParams("language 必须是非空字符串")
    mapped = TARGET_LANGUAGE_MAP.get(code)
    if mapped is None:
        raise CapabilityInvalidParams(f"mlstools 不支持目标语言: {code}")
    return mapped


class TranslateAdapter:
    adapter_id = "translate"

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._cancel_event = threading.Event()
        self._last_argv: tuple[str, ...] | None = None

    @property
    def ready(self) -> bool:
        try:
            resolve_mlstools_bin()
        except CapabilityError:
            return False
        return True

    @property
    def last_argv(self) -> tuple[str, ...] | None:
        return self._last_argv

    def describe(self) -> Mapping[str, Any]:
        try:
            resolve_mlstools_bin()
            return {
                "id": self.adapter_id,
                "ready": True,
                "executors": ["mlstools"],
            }
        except CapabilityError as exc:
            return {
                "id": self.adapter_id,
                "ready": False,
                "reason": exc.code,
            }

    def probe(self, params: Mapping[str, Any] | None = None) -> Mapping[str, Any]:
        del params
        resolve_mlstools_bin()
        return {"ok": True, "executor": "mlstools"}

    def execute(
        self,
        request: ExecutionRequest,
        *,
        cancel_event: threading.Event | None = None,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        # mlstools 的 stdout/stderr 可能含字幕正文，本适配器不转发原文；
        # 只发送应用构造的阶段行与心跳行，让界面能区分“执行中”和“已卡住”。
        params = dict(request.params)
        reject_unknown(params, _PARAM_KEYS, label="translate params")

        subtitle_path = require_existing_input_path(
            params.get("subtitle_url"), field="subtitle_url"
        )
        target = target_language(params.get("language"))
        bilingual = optional_bool(params, "bilingual", False)
        model_name = optional_str(params, "model_name")
        timeout_s = optional_int(params, "timeout_s", DEFAULT_TIMEOUT_S)
        if timeout_s is None:
            timeout_s = DEFAULT_TIMEOUT_S
        if timeout_s < 1 or timeout_s > MAX_TIMEOUT_S:
            raise CapabilityInvalidParams("timeout_s 超出范围")

        mlstools_bin = resolve_mlstools_bin()
        os.makedirs(request.output_dir, exist_ok=True)
        output_abs = resolve_output_path(request.output_dir, DEFAULT_OUTPUT_NAME)

        payload: dict[str, Any] = {
            "subtitle_url": subtitle_path,
            "target_language": target,
            "output_path": output_abs,
            "bilingual": bilingual,
        }
        if model_name:
            payload["model_name"] = model_name
        argv = [
            mlstools_bin,
            "--tool",
            "subtitle_translation",
            "--input",
            json.dumps(payload, ensure_ascii=False),
        ]
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
            active_cancel = self._cancel_event
            self._last_argv = tuple(argv)

        LOGGER.info(
            "translate mlstools 启动 task=%s target=%s bilingual=%s",
            request.task_id,
            target,
            bilingual,
        )
        if log is not None:
            log("runner", "启动 mlstools 字幕翻译")
        heartbeat_stop = threading.Event()
        heartbeat = _start_heartbeat(log, heartbeat_stop)
        try:
            exit_code, stdout_tail = self._run(
                argv,
                cancel_event=active_cancel,
                timeout_s=float(timeout_s),
            )
        finally:
            heartbeat_stop.set()
            if heartbeat is not None:
                heartbeat.join(timeout=1.0)
        if exit_code != 0:
            code = _task_result_code(stdout_tail)
            detail = f"（{code}）" if code else ""
            raise CapabilityFailed(f"mlstools 退出码 {exit_code}{detail}")
        if not os.path.isfile(output_abs) or os.path.getsize(output_abs) <= 0:
            code = _task_result_code(stdout_tail)
            detail = f"（{code}）" if code else ""
            raise CapabilityFailed(f"mlstools 未生成有效译文{detail}")

        size = os.path.getsize(output_abs)
        if log is not None:
            log("runner", "mlstools 字幕翻译完成")
        LOGGER.info(
            "translate mlstools 完成 task=%s exit=%s output_bytes=%s",
            request.task_id,
            exit_code,
            size,
        )
        return {
            "outputs": [artifact_entry(DEFAULT_OUTPUT_NAME, output_abs)],
            "executor": "mlstools",
            "target_language": target,
        }

    def cancel(self, *, term_grace_s: float = 1.0, kill_grace_s: float = 5.0) -> bool:
        del term_grace_s, kill_grace_s
        with self._lock:
            self._cancel_event.set()
        return True

    def _run(
        self,
        argv: list[str],
        *,
        cancel_event: threading.Event,
        timeout_s: float,
    ) -> tuple[int, bytes]:
        try:
            proc = subprocess.Popen(
                argv,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                shell=False,
                close_fds=True,
                env=dict(os.environ),
                **isolated_process_kwargs(),
            )
        except (FileNotFoundError, PermissionError, OSError) as exc:
            raise TranslationRuntimeUnavailable("无法启动 mlstools 字幕翻译") from exc

        stdout_tail = _TailBuffer(MAX_CAPTURED_BYTES)
        stderr_tail = _TailBuffer(MAX_CAPTURED_BYTES)
        readers = [
            threading.Thread(
                target=_drain,
                args=(proc.stdout, stdout_tail),
                name="yaomu-mlstools-stdout",
                daemon=True,
            ),
            threading.Thread(
                target=_drain,
                args=(proc.stderr, stderr_tail),
                name="yaomu-mlstools-stderr",
                daemon=True,
            ),
        ]
        for reader in readers:
            reader.start()

        pgid = None if os.name == "nt" else proc.pid
        deadline = time.monotonic() + timeout_s
        try:
            while True:
                if cancel_event.is_set():
                    confirmed = terminate_process_tree(
                        proc, pgid=pgid, term_grace_s=1.0, kill_grace_s=5.0
                    )
                    if not confirmed:
                        raise CapabilityFailed("mlstools 取消后进程树未确认退出")
                    raise CapabilityCanceled("mlstools 翻译已取消")
                code = proc.poll()
                if code is not None:
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    confirmed = terminate_process_tree(
                        proc, pgid=pgid, term_grace_s=1.0, kill_grace_s=5.0
                    )
                    suffix = "" if confirmed else "；进程树未确认退出"
                    raise CapabilityFailed(f"mlstools 翻译超时（{timeout_s:.0f}s）{suffix}")
                time.sleep(min(0.05, remaining))
        finally:
            for reader in readers:
                reader.join(timeout=2.0)
            for stream in (proc.stdout, proc.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except (OSError, ValueError):
                        pass
        return code, stdout_tail.value()


class _TailBuffer:
    """只保留最后 N 字节；不把内容写入日志。"""

    def __init__(self, limit: int) -> None:
        self._limit = limit
        self._data = bytearray()
        self._lock = threading.Lock()

    def append(self, chunk: bytes) -> None:
        with self._lock:
            self._data.extend(chunk)
            if len(self._data) > self._limit:
                del self._data[: len(self._data) - self._limit]

    def value(self) -> bytes:
        with self._lock:
            return bytes(self._data)


def _drain(stream: Any, sink: _TailBuffer) -> None:
    if stream is None:
        return
    try:
        while True:
            chunk = stream.read(_CAPTURE_CHUNK)
            if not chunk:
                return
            sink.append(chunk)
    except (OSError, ValueError):
        return


def _task_result_code(data: bytes) -> str:
    """从 mlstools 输出尾部提取 TASK_RESULT_INFO 的错误码，不返回正文。"""

    index = data.rfind(_RESULT_MARKER)
    if index < 0:
        return ""
    line = data[index + len(_RESULT_MARKER):].split(b"\n", 1)[0].strip()
    try:
        payload = json.loads(line.decode("utf-8", "replace"))
    except (ValueError, UnicodeError):
        return ""
    code = payload.get("code") if isinstance(payload, dict) else None
    if isinstance(code, bool) or not isinstance(code, int):
        return ""
    return f"code={code}"
