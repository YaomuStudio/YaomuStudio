"""音色克隆 FlashCLI serve + OmniVoice ``/predictions``（清洁室 MVP）。

行为依据研究文档中的 HTTP 形状（``/health``、``POST /predictions``、base64 WAV），
不搬运 ``voice-clone.py`` 源码。业务入参仅白名单字段。
"""

from __future__ import annotations

import base64
import json
import logging
import os
import re
import socket
import threading
import time
import urllib.error
import urllib.request
import wave
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable

from .capability_common import (
    CapabilityCanceled,
    CapabilityFailed,
    CapabilityInvalidParams,
)
from .flashcli_runtime import (
    DEFAULT_SERVE_HEALTH_TIMEOUT_S,
    VOICE_CLONE_MODEL_REF,
    ManagedFlashCliProcess,
    build_serve_argv,
    resolve_flashcli_bin,
)

LOGGER = logging.getLogger("yaomustudio_runner.omnivoice_serve")

_BASE64_RE = re.compile(
    r"(?:data:[^;]+;base64,)?([A-Za-z0-9+/=\s]+)",
    re.DOTALL,
)
_MAX_PREDICTION_BYTES = 32 * 1024 * 1024
# 单句预测与 serve 健康检查使用同一档 WSL 执行等待。
_PREDICTION_TIMEOUT_S = 3600.0


@dataclass(frozen=True)
class SrtCue:
    index: int
    start_ms: int
    end_ms: int
    text: str


def _parse_ts(token: str) -> int:
    token = token.strip().replace(",", ".")
    parts = token.split(":")
    if len(parts) != 3:
        raise CapabilityInvalidParams("SRT 时间码非法")
    try:
        hours = int(parts[0])
        minutes = int(parts[1])
        seconds = float(parts[2])
    except ValueError as exc:
        raise CapabilityInvalidParams("SRT 时间码非法") from exc
    return int(round(((hours * 60 + minutes) * 60 + seconds) * 1000))


def parse_srt(text: str) -> list[SrtCue]:
    blocks = re.split(r"\n\s*\n", text.replace("\r\n", "\n").strip())
    cues: list[SrtCue] = []
    for block in blocks:
        lines = [line for line in block.split("\n") if line.strip() != ""]
        if len(lines) < 2:
            continue
        if "-->" in lines[0]:
            timing = lines[0]
            body = lines[1:]
            index = len(cues) + 1
        elif "-->" in lines[1]:
            try:
                index = int(lines[0].strip())
            except ValueError:
                index = len(cues) + 1
            timing = lines[1]
            body = lines[2:]
        else:
            continue
        left, _, right = timing.partition("-->")
        if not right:
            continue
        start_ms = _parse_ts(left)
        end_ms = _parse_ts(right.split()[0] if right.strip() else right)
        if end_ms <= start_ms:
            raise CapabilityInvalidParams("SRT 句段时间窗非法")
        content = " ".join(part.strip() for part in body).strip()
        if not content:
            continue
        cues.append(SrtCue(index=index, start_ms=start_ms, end_ms=end_ms, text=content))
    if not cues:
        raise CapabilityInvalidParams("字幕为空或无法解析")
    return cues


def instruct_for_language(language: str) -> str | None:
    """方言后缀作 instruct；粤语等不传（与产品研究口径一致，非整表拷贝）。"""

    if "-" not in language:
        return None
    dialect = language.split("-", 1)[1].strip()
    if not dialect or dialect in {"粤语", "广东话", "cantonese"}:
        return None
    return dialect


def pick_free_port(host: str = "127.0.0.1") -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind((host, 0))
        return int(sock.getsockname()[1])


class _StaticHandler(BaseHTTPRequestHandler):
    root: Path

    def do_GET(self) -> None:  # noqa: N802
        name = self.path.rsplit("/", 1)[-1]
        if not name or name != Path(name).name or ".." in name:
            self.send_error(404)
            return
        target = self.root / name
        if not target.is_file():
            self.send_error(404)
            return
        data = target.read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A003
        del format, args


def start_static_server(root: Path, *, host: str = "127.0.0.1") -> tuple[ThreadingHTTPServer, int, threading.Thread]:
    port = pick_free_port(host)

    class BoundHandler(_StaticHandler):
        pass

    BoundHandler.root = root
    server = ThreadingHTTPServer((host, port), BoundHandler)
    thread = threading.Thread(target=server.serve_forever, name="yaomu-ref-http", daemon=True)
    thread.start()
    return server, port, thread


def wait_healthy(
    base_url: str,
    *,
    cancel_event: threading.Event,
    timeout_s: float = DEFAULT_SERVE_HEALTH_TIMEOUT_S,
    opener: Callable[..., Any] | None = None,
) -> None:
    deadline = time.monotonic() + timeout_s
    url = f"{base_url.rstrip('/')}/health"
    open_fn = opener or urllib.request.urlopen
    while True:
        if cancel_event.is_set():
            raise CapabilityCanceled("等待 FlashCLI serve 时已取消")
        try:
            with open_fn(url, timeout=2.0) as resp:
                raw = resp.read(4096)
            payload = json.loads(raw.decode("utf-8"))
            if isinstance(payload, Mapping) and payload.get("status") == "healthy":
                return
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, ValueError, OSError):
            pass
        if time.monotonic() >= deadline:
            raise CapabilityFailed("FlashCLI serve /health 超时")
        time.sleep(0.5)


def _decode_audio_output(output: object) -> bytes:
    if not isinstance(output, str) or not output:
        raise CapabilityFailed("预测输出为空")
    match = _BASE64_RE.fullmatch(output.strip()) or _BASE64_RE.search(output)
    if match is None:
        raise CapabilityFailed("预测输出不是 base64 音频")
    try:
        data = base64.b64decode(match.group(1), validate=False)
    except (ValueError, TypeError) as exc:
        raise CapabilityFailed("预测输出 base64 解码失败") from exc
    if not data:
        raise CapabilityFailed("预测音频为空")
    if len(data) > _MAX_PREDICTION_BYTES:
        raise CapabilityFailed("预测音频过大")
    return data


def post_prediction(
    base_url: str,
    payload: Mapping[str, Any],
    *,
    cancel_event: threading.Event,
    timeout_s: float = _PREDICTION_TIMEOUT_S,
    opener: Callable[..., Any] | None = None,
) -> bytes:
    if cancel_event.is_set():
        raise CapabilityCanceled("预测前已取消")
    body = json.dumps({"input": dict(payload)}, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        f"{base_url.rstrip('/')}/predictions",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    open_fn = opener or urllib.request.urlopen
    try:
        with open_fn(req, timeout=timeout_s) as resp:
            raw = resp.read(_MAX_PREDICTION_BYTES + 1)
    except urllib.error.HTTPError as exc:
        raise CapabilityFailed(f"预测 HTTP {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        if cancel_event.is_set():
            raise CapabilityCanceled("预测时已取消") from exc
        raise CapabilityFailed("预测请求失败") from exc
    if len(raw) > _MAX_PREDICTION_BYTES:
        raise CapabilityFailed("预测响应过大")
    try:
        parsed = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CapabilityFailed("预测响应不是 JSON") from exc
    if isinstance(parsed, list):
        if not parsed:
            raise CapabilityFailed("预测响应为空数组")
        parsed = parsed[0]
    if not isinstance(parsed, Mapping):
        raise CapabilityFailed("预测响应形状非法")
    if parsed.get("status") != "succeeded":
        raise CapabilityFailed("预测未成功")
    return _decode_audio_output(parsed.get("output"))


def wav_pcm_params(data: bytes) -> tuple[int, int, int, bytes]:
    import io

    with wave.open(io.BytesIO(data), "rb") as handle:
        channels = handle.getnchannels()
        width = handle.getsampwidth()
        rate = handle.getframerate()
        frames = handle.readframes(handle.getnframes())
    if channels < 1 or width < 1 or rate < 1:
        raise CapabilityFailed("WAV 参数非法")
    return channels, width, rate, frames


def slice_reference_wav(source: Path, start_ms: int, end_ms: int, dest: Path) -> None:
    """按原文时间窗切出参考音频；当前只接受 WAV（清洁室 MVP）。"""

    if start_ms < 0 or end_ms <= start_ms:
        raise CapabilityInvalidParams("参考音频时间窗非法")
    try:
        with wave.open(str(source), "rb") as src:
            channels = src.getnchannels()
            width = src.getsampwidth()
            rate = src.getframerate()
            total = src.getnframes()
            start_frame = int(round(start_ms * rate / 1000.0))
            end_frame = int(round(end_ms * rate / 1000.0))
            start_frame = max(0, min(start_frame, total))
            end_frame = max(start_frame + 1, min(end_frame, total))
            src.setpos(start_frame)
            frames = src.readframes(end_frame - start_frame)
    except wave.Error as exc:
        raise CapabilityInvalidParams("单句克隆参考音频当前仅支持 WAV") from exc
    if not frames:
        raise CapabilityFailed("参考音频切片为空")
    dest.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(dest), "wb") as out:
        out.setnchannels(channels)
        out.setsampwidth(width)
        out.setframerate(rate)
        out.writeframes(frames)


def concat_wav_timeline(
    clips: Sequence[tuple[int, bytes]],
    *,
    total_ms: int,
) -> bytes:
    if not clips:
        raise CapabilityFailed("无可用音频片段")
    channels, width, rate, _ = wav_pcm_params(clips[0][1])
    frame_bytes = channels * width
    total_frames = max(1, int(round(total_ms * rate / 1000.0)))
    canvas = bytearray(total_frames * frame_bytes)
    for start_ms, blob in clips:
        c, w, r, pcm = wav_pcm_params(blob)
        if (c, w, r) != (channels, width, rate):
            raise CapabilityFailed("片段 WAV 格式不一致")
        start_frame = max(0, int(round(start_ms * rate / 1000.0)))
        offset = start_frame * frame_bytes
        end = min(len(canvas), offset + len(pcm))
        if end <= offset:
            continue
        canvas[offset:end] = pcm[: end - offset]
    import io

    buf = io.BytesIO()
    with wave.open(buf, "wb") as handle:
        handle.setnchannels(channels)
        handle.setsampwidth(width)
        handle.setframerate(rate)
        handle.writeframes(bytes(canvas))
    return buf.getvalue()


class OmniVoiceCloneSession:
    """一次 attempt：静态参考音频服务 + flashcli serve + 逐句预测。"""

    def __init__(
        self,
        *,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        flashcli_bin: str | None = None,
        serve_factory: Callable[..., ManagedFlashCliProcess] | None = None,
        health_waiter: Callable[..., None] | None = None,
        predictor: Callable[..., bytes] | None = None,
        static_starter: Callable[..., tuple[ThreadingHTTPServer, int, threading.Thread]] | None = None,
        pick_port: Callable[[], int] | None = None,
        on_log: Callable[[str, str], None] | None = None,
    ) -> None:
        self._env = dict(env)
        self._cancel = cancel_event
        self._flashcli_bin = flashcli_bin
        self._serve_factory = serve_factory
        self._health_waiter = health_waiter or wait_healthy
        self._predictor = predictor or post_prediction
        self._static_starter = static_starter or start_static_server
        self._pick_port = pick_port or (lambda: pick_free_port())
        self._on_log = on_log
        self._serve: ManagedFlashCliProcess | None = None
        self._static: ThreadingHTTPServer | None = None
        self._static_thread: threading.Thread | None = None
        self._work_dir: Path | None = None

    def _emit(self, line: str) -> None:
        if self._on_log is not None:
            self._on_log("runner", line)

    def close(self) -> None:
        if self._serve is not None:
            self._serve.terminate()
            self._serve = None
        if self._static is not None:
            try:
                self._static.shutdown()
            except Exception:
                # 异常栈可能携带 URL、路径或模型输出；只记固定关停阶段。
                LOGGER.error("关闭参考音频 HTTP 服务失败")
            self._static = None
        if self._static_thread is not None and self._static_thread.is_alive():
            self._static_thread.join(timeout=2.0)
            self._static_thread = None

    def run(
        self,
        *,
        voice_path: str,
        subtitle_path: str,
        original_subtitle_path: str,
        language: str,
        output_path: str,
        work_dir: str,
    ) -> dict[str, Any]:
        self._work_dir = Path(work_dir)
        self._work_dir.mkdir(parents=True, exist_ok=True)
        subtitle_text = Path(subtitle_path).read_text(encoding="utf-8")
        original_text = Path(original_subtitle_path).read_text(encoding="utf-8")
        targets = parse_srt(subtitle_text)
        originals = parse_srt(original_text)
        if len(targets) != len(originals):
            raise CapabilityInvalidParams("译文字幕与原文字幕句数不一致")

        ref_name = "reference" + Path(voice_path).suffix.lower()
        if ref_name == "reference":
            ref_name = "reference.wav"
        ref_copy = self._work_dir / ref_name
        ref_copy.write_bytes(Path(voice_path).read_bytes())

        self._static, static_port, self._static_thread = self._static_starter(self._work_dir)
        ref_url = f"http://127.0.0.1:{static_port}/{ref_name}"
        self._emit("参考音频回环服务已启动")

        flashcli_bin = self._flashcli_bin or resolve_flashcli_bin()
        serve_port = self._pick_port()
        argv = build_serve_argv(flashcli_bin, port=serve_port)
        if self._serve_factory is not None:
            try:
                self._serve = self._serve_factory(
                    argv, env=self._env
                )
            except TypeError:
                self._serve = self._serve_factory(argv, env=self._env)
        else:
            self._serve = ManagedFlashCliProcess(
                argv, env=self._env, on_log=self._on_log
            )
        self._serve.start()
        self._emit("受管 FlashCLI serve 已启动")

        base_url = f"http://127.0.0.1:{serve_port}"
        try:
            self._health_waiter(base_url, cancel_event=self._cancel)
            self._emit("FlashCLI serve 健康检查通过")
            instruct = instruct_for_language(language)
            clips: list[tuple[int, bytes]] = []
            for index, (target, original) in enumerate(
                zip(targets, originals, strict=True), start=1
            ):
                if self._cancel.is_set():
                    raise CapabilityCanceled("克隆执行已取消")
                self._emit(f"逐句预测 {index}/{len(targets)}")
                payload: dict[str, Any] = {
                    "text": target.text,
                    "reference_audio": ref_url,
                    "reference_text": original.text,
                }
                if instruct:
                    payload["instruct"] = instruct
                audio = self._predictor(
                    base_url,
                    payload,
                    cancel_event=self._cancel,
                )
                clips.append((original.start_ms, audio))
            total_ms = max(cue.end_ms for cue in originals) + 250
            wav_bytes = concat_wav_timeline(clips, total_ms=total_ms)
            out = Path(output_path)
            out.parent.mkdir(parents=True, exist_ok=True)
            # Windows 覆盖用 replace；产物先写临时再替换。
            tmp = out.with_suffix(out.suffix + ".tmp")
            tmp.write_bytes(wav_bytes)
            os.replace(tmp, out)
            if out.stat().st_size <= 0:
                raise CapabilityFailed("未生成有效克隆音频")
            self._emit("克隆音频合成完成")
            return {
                "segments": len(clips),
                "model_ref": VOICE_CLONE_MODEL_REF,
                "sample_format": "wav",
            }
        finally:
            self.close()

    def run_segment(
        self,
        *,
        voice_path: str,
        start_ms: int,
        end_ms: int,
        text: str,
        original_text: str,
        language: str,
        output_path: str,
        work_dir: str,
    ) -> dict[str, Any]:
        """逐句克隆：切参考音频 -> 受管 serve -> 单次 /predictions -> WAV。"""

        if not text.strip():
            raise CapabilityInvalidParams("单句克隆文本为空")
        out = Path(output_path)
        if out.suffix.lower() != ".wav":
            raise CapabilityInvalidParams("单句克隆输出必须是 .wav")
        self._work_dir = Path(work_dir)
        self._work_dir.mkdir(parents=True, exist_ok=True)
        ref_name = "ref-slice.wav"
        ref_copy = self._work_dir / ref_name
        slice_reference_wav(Path(voice_path), start_ms, end_ms, ref_copy)

        self._static, static_port, self._static_thread = self._static_starter(self._work_dir)
        ref_url = f"http://127.0.0.1:{static_port}/{ref_name}"
        self._emit("参考音频切片已准备")

        flashcli_bin = self._flashcli_bin or resolve_flashcli_bin()
        serve_port = self._pick_port()
        argv = build_serve_argv(flashcli_bin, port=serve_port)
        if self._serve_factory is not None:
            try:
                self._serve = self._serve_factory(argv, env=self._env)
            except TypeError:
                self._serve = self._serve_factory(argv, env=self._env)
        else:
            self._serve = ManagedFlashCliProcess(
                argv, env=self._env, on_log=self._on_log
            )
        self._serve.start()
        self._emit("受管 FlashCLI serve 已启动")

        base_url = f"http://127.0.0.1:{serve_port}"
        try:
            self._health_waiter(base_url, cancel_event=self._cancel)
            self._emit("FlashCLI serve 健康检查通过")
            payload: dict[str, Any] = {
                "text": text,
                "reference_audio": ref_url,
            }
            if original_text.strip():
                payload["reference_text"] = original_text
            instruct = instruct_for_language(language)
            if instruct:
                payload["instruct"] = instruct
            audio = self._predictor(
                base_url,
                payload,
                cancel_event=self._cancel,
            )
            out.parent.mkdir(parents=True, exist_ok=True)
            tmp = out.with_suffix(out.suffix + ".tmp")
            tmp.write_bytes(audio)
            os.replace(tmp, out)
            if out.stat().st_size <= 0:
                raise CapabilityFailed("未生成有效单句克隆音频")
            self._emit("单句克隆音频已生成")
            return {
                "segments": 1,
                "model_ref": VOICE_CLONE_MODEL_REF,
                "sample_format": "wav",
            }
        finally:
            self.close()
