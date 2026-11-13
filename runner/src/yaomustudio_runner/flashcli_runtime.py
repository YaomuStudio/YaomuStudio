"""受管 FlashCLI 入口（硬约束 #5：仅已确认白名单）。

分离：``flashcli run xym/melband-roformer:1.0.10`` + 固定 flags。
克隆：``flashcli serve tts/omnivoice_flashrt:1.0.3``（回环）+ ``/predictions``。
ASR：``flashcli run asr/qwen3_asr:1.0.1`` + 音频/JSON 输出/分块/语种 flags。
不添加未确认的离线/JSON 模式等 CLI 开关；二进制由宿主 ``FLASHCLI_BIN`` 注入。
"""

from __future__ import annotations

import logging
import os
import subprocess
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from typing import Any

from .capability_common import (
    CapabilityCanceled,
    CapabilityFailed,
    CapabilityInvalidParams,
    CapabilityUnavailable,
)
from .execution_log import FLASHCLI_OUTPUT_OMITTED
from .executor import isolated_process_kwargs, terminate_process_tree

LOGGER = logging.getLogger("yaomustudio_runner.flashcli_runtime")

ENV_FLASHCLI = "FLASHCLI_BIN"

SEPARATE_MODEL_REF = "xym/melband-roformer:1.0.10"
VOICE_CLONE_MODEL_REF = "tts/omnivoice_flashrt:1.0.3"
ASR_MODEL_REF = "asr/qwen3_asr:1.0.1"

# Bundle 的 KV/编码器缓冲按 chunk_seconds 预分配；默认 180s 在 12 GiB 卡
# 的 WSL 显存预算下会在加载/推理时 OOM（本机 RTX 3060 实测）。30s 分块
# 仍由 bundle 在低能量点切分，长音频只是多几次分块推理。
ASR_CHUNK_SECONDS = 30.0

# Qwen3-ASR bundle 把 ``--language`` 直接注入 ``language <NAME><asr_text>``
# 提示，并要求官方语言名（模型卡：Chinese/English/Japanese/...）。Core 与
# UI 传的是 ISO 码，因此在 argv 边界统一转换，不能把 ``en``/``ja`` 原样下发。
# 未列出的码（如 uk/is/no）不在该模型 30 种语言内，直接可读失败，避免静默
# 换语言或产出错误转写。
ASR_LANGUAGE_NAMES: Mapping[str, str] = {
    "zh": "Chinese",
    "zh-cn": "Chinese",
    "zh-hans": "Chinese",
    "zh-tw": "Chinese",
    "zh-hant": "Chinese",
    "zh-hk": "Chinese",
    "zh-mo": "Chinese",
    "zh-wu": "Chinese",
    "zh-minnan": "Chinese",
    "zh-sichuan": "Chinese",
    "zh-yue": "Cantonese",
    "yue": "Cantonese",
    "en": "English",
    "ja": "Japanese",
    "ko": "Korean",
    "de": "German",
    "fr": "French",
    "es": "Spanish",
    "es-mx": "Spanish",
    "pt": "Portuguese",
    "pt-br": "Portuguese",
    "ar": "Arabic",
    "it": "Italian",
    "ru": "Russian",
    "hi": "Hindi",
    "id": "Indonesian",
    "th": "Thai",
    "tr": "Turkish",
    "vi": "Vietnamese",
    "cs": "Czech",
    "da": "Danish",
    "fi": "Finnish",
    "ms": "Malay",
    "pl": "Polish",
    "sv": "Swedish",
    "tl": "Filipino",
    "fil": "Filipino",
    "nl": "Dutch",
    "fa": "Persian",
    "el": "Greek",
    "hu": "Hungarian",
    "ro": "Romanian",
    "mk": "Macedonian",
}
_ASR_AUTO_LANGUAGE_CODES = frozenset({"und", "auto"})
_ASR_CANONICAL_NAMES = frozenset(ASR_LANGUAGE_NAMES.values())


def asr_language_name(value: str | None) -> str | None:
    """把 Core/UI 的 ASR 语言码转换为 bundle 认可的官方语言名。

    空值、``und``/``auto`` 返回 ``None``（不传 ``--language``，由模型检测）；
    已是官方语言名的值按原规范大小写透传；其余不支持的语言码抛
    ``CAPABILITY_INVALID_PARAMS``，让任务可读失败而不是静默换语言。
    """

    if value is None:
        return None
    token = value.strip()
    if not token:
        return None
    key = token.lower().replace("_", "-")
    if key in _ASR_AUTO_LANGUAGE_CODES:
        return None
    mapped = ASR_LANGUAGE_NAMES.get(key)
    if mapped is not None:
        return mapped
    for name in _ASR_CANONICAL_NAMES:
        if token.lower() == name.lower():
            return name
    raise CapabilityInvalidParams(f"当前 ASR 模型不支持语言码: {token}")


DEFAULT_SEPARATE_BGM_NAME = "background.wav"
DEFAULT_SEPARATE_VOCALS_NAME = "vocals.wav"
DEFAULT_SEPARATE_VOCALS_SAMPLE = 16000

DEFAULT_SERVE_HEALTH_TIMEOUT_S = 120.0
DEFAULT_RUN_TIMEOUT_S = 3600.0


def resolve_flashcli_bin(environ: Mapping[str, str] | None = None) -> str:
    env = os.environ if environ is None else environ
    raw = env.get(ENV_FLASHCLI, "").strip()
    if not raw:
        raise CapabilityUnavailable(
            f"未注入 {ENV_FLASHCLI}：无法启动已准入 FlashCLI 入口"
        )
    if len(raw) > 4096:
        raise CapabilityUnavailable(f"{ENV_FLASHCLI} 路径过长")
    if "\x00" in raw or any(ord(ch) < 0x20 for ch in raw):
        raise CapabilityUnavailable(f"{ENV_FLASHCLI} 含非法字符")
    if not os.path.isabs(raw):
        raise CapabilityUnavailable(f"{ENV_FLASHCLI} 必须是绝对路径")
    real = os.path.realpath(raw)
    if not os.path.isfile(real):
        raise CapabilityUnavailable(f"{ENV_FLASHCLI} 不是可读文件")
    if not os.access(real, os.X_OK) and os.name != "nt":
        raise CapabilityUnavailable(f"{ENV_FLASHCLI} 不可执行")
    return real


def build_separate_argv(
    flashcli_bin: str,
    *,
    input_path: str,
    output_dir: str,
    bgm_name: str,
    vocals_name: str,
    vocals_sample: int = DEFAULT_SEPARATE_VOCALS_SAMPLE,
) -> list[str]:
    """仅组装用户确认的分离白名单 argv；不附加未确认的 device 类 flags。"""

    if vocals_sample < 8000 or vocals_sample > 96000:
        raise CapabilityFailed("vocals_sample 超出允许范围")
    return [
        flashcli_bin,
        "run",
        SEPARATE_MODEL_REF,
        "--input",
        input_path,
        "--output_dir",
        output_dir,
        "--bgm_name",
        bgm_name,
        "--vocals_name",
        vocals_name,
        "--vocals_sample",
        str(int(vocals_sample)),
    ]


def build_asr_argv(
    flashcli_bin: str,
    *,
    audio_path: str,
    output_path: str,
    language: str | None = None,
) -> list[str]:
    """ASR 白名单 argv：只使用 bundle README 已确认的输入/输出/语种参数。"""

    argv = [
        flashcli_bin,
        "run",
        ASR_MODEL_REF,
        "--audio",
        audio_path,
        "--output",
        output_path,
        # 只使用 bundle manifest 已声明的 load 选项；值由产品侧固定，
        # 不接受上游/用户传入，避免把未准入参数带进执行入口。
        "--chunk-seconds",
        f"{ASR_CHUNK_SECONDS:g}",
    ]
    if language:
        name = asr_language_name(language)
        if name:
            argv.extend(["--language", name])
    return argv


def build_serve_argv(
    flashcli_bin: str,
    *,
    model_ref: str = VOICE_CLONE_MODEL_REF,
    host: str = "127.0.0.1",
    port: int,
) -> list[str]:
    """回环 serve：host/port 属于进程托管，不是模型业务参数扩展。"""

    if not (1 <= port <= 65535):
        raise CapabilityFailed("serve port 非法")
    if host not in ("127.0.0.1", "localhost"):
        raise CapabilityFailed("serve 仅允许回环地址")
    return [
        flashcli_bin,
        "serve",
        model_ref,
        "--host",
        host,
        "--port",
        str(int(port)),
    ]


class ManagedFlashCliProcess:
    """可取消的 FlashCLI 子进程；stderr 只进本地诊断，不进协议 stdout。"""

    def __init__(
        self,
        argv: Sequence[str],
        *,
        env: Mapping[str, str] | None = None,
        logger: logging.Logger | None = None,
        on_log: Callable[[str, str], None] | None = None,
    ) -> None:
        if isinstance(argv, (str, bytes)) or not argv:
            raise ValueError("argv 必须是非空参数列表")
        self._argv = tuple(str(part) for part in argv)
        self._env = dict(env) if env is not None else None
        self._logger = logger or LOGGER
        self._on_log = on_log
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._pgid: int | None = None
        self._stderr_thread: threading.Thread | None = None
        self._stderr_omitted = False

    @property
    def argv(self) -> tuple[str, ...]:
        return self._argv

    @property
    def pid(self) -> int | None:
        with self._lock:
            return None if self._proc is None else self._proc.pid

    def start(self) -> ManagedFlashCliProcess:
        with self._lock:
            if self._proc is not None:
                raise RuntimeError("进程已经启动")
            try:
                self._proc = subprocess.Popen(
                    list(self._argv),
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.PIPE,
                    shell=False,
                    close_fds=True,
                    env=self._env,
                    **isolated_process_kwargs(),
                )
            except (FileNotFoundError, PermissionError, OSError) as exc:
                raise CapabilityUnavailable(
                    "无法启动 FlashCLI：二进制不可用"
                ) from exc
            self._pgid = None if os.name == "nt" else self._proc.pid
            if self._proc.stderr is not None:
                self._stderr_thread = threading.Thread(
                    target=self._read_stderr,
                    name="yaomu-flashcli-stderr",
                    daemon=True,
                )
                self._stderr_thread.start()
        return self

    def poll(self) -> int | None:
        with self._lock:
            proc = self._proc
        if proc is None:
            return None
        return proc.poll()

    def wait(
        self,
        *,
        cancel_event: threading.Event,
        timeout_s: float,
        term_grace_s: float = 1.0,
        kill_grace_s: float = 5.0,
        poll_s: float = 0.05,
    ) -> int:
        if timeout_s <= 0:
            raise ValueError("timeout_s 必须为正数")
        deadline = time.monotonic() + timeout_s
        while True:
            if cancel_event.is_set():
                self.terminate(term_grace_s=term_grace_s, kill_grace_s=kill_grace_s)
                raise CapabilityCanceled("FlashCLI 执行已取消")
            code = self.poll()
            if code is not None:
                self._join_stderr()
                return code
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                confirmed = self.terminate(
                    term_grace_s=term_grace_s, kill_grace_s=kill_grace_s
                )
                raise CapabilityFailed(
                    f"FlashCLI 超时（{timeout_s:.0f}s）"
                    + ("" if confirmed else "；进程树未确认退出")
                )
            time.sleep(min(poll_s, remaining))

    def terminate(
        self,
        *,
        term_grace_s: float = 1.0,
        kill_grace_s: float = 5.0,
    ) -> bool:
        with self._lock:
            proc = self._proc
            pgid = self._pgid
        if proc is None:
            return True
        confirmed = terminate_process_tree(
            proc,
            pgid=pgid,
            term_grace_s=term_grace_s,
            kill_grace_s=kill_grace_s,
            logger=self._logger,
        )
        self._join_stderr()
        return confirmed

    def _emit(self, stream: str, text: str) -> None:
        del stream
        if not (text or "").strip():
            return
        # FlashCLI 输出可能含字幕、凭据、环境或绝对路径；默认不记录正文，
        # 也不尝试用 basename/前缀正则证明自由文本安全。只发一次固定提示。
        with self._lock:
            if self._stderr_omitted:
                return
            self._stderr_omitted = True
        self._logger.warning(FLASHCLI_OUTPUT_OMITTED)
        if self._on_log is not None:
            self._on_log("runner", FLASHCLI_OUTPUT_OMITTED)

    def _read_stderr(self) -> None:
        with self._lock:
            proc = self._proc
        if proc is None or proc.stderr is None:
            return
        try:
            for raw in proc.stderr:
                try:
                    text = raw.decode("utf-8", "replace")
                except Exception:
                    continue
                for part in text.splitlines():
                    self._emit("stderr", part)
        except (OSError, ValueError):
            return

    def _join_stderr(self) -> None:
        thread = self._stderr_thread
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout=1.0)


def run_flashcli(
    argv: Sequence[str],
    *,
    env: Mapping[str, str],
    cancel_event: threading.Event,
    timeout_s: float = DEFAULT_RUN_TIMEOUT_S,
    logger: logging.Logger | None = None,
    on_log: Callable[[str, str], None] | None = None,
) -> None:
    managed = ManagedFlashCliProcess(argv, env=env, logger=logger, on_log=on_log)
    managed.start()
    code = managed.wait(cancel_event=cancel_event, timeout_s=timeout_s)
    if code != 0:
        raise CapabilityFailed(f"FlashCLI 退出码 {code}")
