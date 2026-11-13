"""Runner stdio 主循环：读帧 -> 分发 -> 写帧。

- stdout 只写经过 ``protocol.encode_frame`` 的协议帧；日志统一走 stderr。
- 适配器注册表：``flashcli`` 为门控桩（其余槽位）；``ffmpeg`` 受控媒体；
  ``separate`` / ``voice_clone`` 默认 ``executor=flashcli``（已确认白名单），
  ``executor=mock`` 可联调。方案 A：单步执行，不做业务排队。
- execute 在独立线程运行，主循环继续读帧，因此 cancel 能在执行中到达；
  cancel 只在确认本应用进程树退出后才返回 ``exited=true``。
"""

from __future__ import annotations

import json
import logging
import os
import sys
import threading
from collections.abc import Mapping, Sequence
from typing import Any, BinaryIO

from . import __version__
from .capability_common import CapabilityError
from .execution_log import LogEmitter
from .extract import AsrAdapter
from .ffmpeg import (
    FfmpegAdapter,
    MediaToolError,
    MediaToolUnavailable,
    describe_media_tools,
)
from .flashcli import CapabilityUnavailable as FlashCliUnavailable
from .flashcli import FlashCliAdapter, ProbeRequest
from .protocol import (
    ERR_FRAME_TOO_LARGE,
    ERR_INTERNAL,
    ERR_INVALID_REQUEST,
    ERR_UNSUPPORTED_ADAPTER,
    ERR_UNKNOWN_OP,
    MAX_FRAME_BYTES,
    PROTOCOL_VERSION,
    ExecutionRequest,
    Frame,
    ProtocolError,
    decode_frame,
    encode_frame,
    error_frame,
    log_frame,
    result_frame,
    validate_cancel_payload,
    validate_execute_payload,
)
from .separate import SeparateAdapter
from .translate import TranslateAdapter
from .voice_clone import VoiceCloneAdapter

LOGGER = logging.getLogger("yaomustudio_runner")
UNKNOWN_REQ_ID = "unknown"
_MAX_OVERSIZED_DRAIN_BYTES = 8 * 1024 * 1024
_LOG_LEVELS = frozenset({"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"})


def _log_level_from_env(environ: Mapping[str, str] | None = None) -> str:
    env = os.environ if environ is None else environ
    level = env.get("YAOMU_RUNNER_LOG_LEVEL", "INFO").upper()
    return level if level in _LOG_LEVELS else "INFO"


def _safe_token(value: object, *, max_len: int = 256) -> str | None:
    if not isinstance(value, str) or not value or len(value) > max_len:
        return None
    for ch in value:
        if ch.isspace() or ord(ch) < 0x20 or ord(ch) == 0x7F:
            return None
    return value


def _best_effort_req_id(raw: bytes) -> str:
    try:
        obj = json.loads(raw.decode("utf-8", "replace"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return UNKNOWN_REQ_ID
    if isinstance(obj, dict):
        req_id = _safe_token(obj.get("req_id"), max_len=128)
        if req_id is not None:
            return req_id
    return UNKNOWN_REQ_ID


def _binding_from_frame(frame: Frame) -> dict[str, Any] | None:
    if frame.op != "execute":
        return None
    payload = frame.payload
    binding: dict[str, Any] = {}
    for key in ("workspace", "server_epoch", "task_id", "step_id"):
        token = _safe_token(payload.get(key))
        if token is not None:
            binding[key] = token
    attempt = payload.get("attempt")
    if isinstance(attempt, int) and not isinstance(attempt, bool):
        binding["attempt"] = attempt
    return binding or None


class _ActiveExecution:
    """一次运行中的 execute；cancel 通过适配器终止其媒体进程树。"""

    def __init__(self, request: ExecutionRequest, adapter: Any) -> None:
        self.request = request
        self.adapter = adapter
        self.cancel_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.stop_unconfirmed = False


class RunnerService:
    """stdin/stdout 协议服务；与传输绑定，便于测试注入内存流。"""

    def __init__(
        self,
        *,
        out: BinaryIO,
        flashcli_adapter: FlashCliAdapter | None = None,
        ffmpeg_adapter: FfmpegAdapter | None = None,
        separate_adapter: SeparateAdapter | None = None,
        voice_clone_adapter: VoiceCloneAdapter | None = None,
        translate_adapter: TranslateAdapter | None = None,
        asr_adapter: AsrAdapter | None = None,
        media_tools_status: Mapping[str, Any] | None = None,
    ) -> None:
        self._out = out
        self._send_lock = threading.Lock()
        self._flashcli = flashcli_adapter or FlashCliAdapter()
        self._separate = separate_adapter or SeparateAdapter()
        self._voice_clone = voice_clone_adapter or VoiceCloneAdapter()
        self._translate = translate_adapter or TranslateAdapter()
        self._asr = asr_adapter or AsrAdapter()
        self._ffmpeg_error: str | None = None
        if ffmpeg_adapter is None:
            try:
                ffmpeg_adapter = FfmpegAdapter()
            except MediaToolUnavailable as exc:
                # 未注入 FFMPEG_BIN 时仍提供 hello 门控信息；执行会返回可读错误。
                self._ffmpeg_error = exc.message
        self._ffmpeg = ffmpeg_adapter
        self._media_tools_status = media_tools_status
        self._running = True
        self._active_lock = threading.Lock()
        self._active: _ActiveExecution | None = None

    @property
    def running(self) -> bool:
        return self._running

    def serve(self, inp: BinaryIO) -> int:
        try:
            while self._running:
                line = inp.readline(MAX_FRAME_BYTES + 2)
                if not line:
                    break
                if len(line) > MAX_FRAME_BYTES + 1:
                    self._handle_oversized(inp, line)
                    continue
                self._handle_line(line)
        finally:
            # 输入关闭/退出时必须先终止并确认执行侧进程树退出。
            self._stop_active()
        return 0

    # -- 分发 -------------------------------------------------------------

    def _handle_line(self, line: bytes) -> None:
        try:
            frame = decode_frame(line, direction="request")
        except ProtocolError as exc:
            self._send(
                error_frame(
                    _best_effort_req_id(line),
                    code=exc.code,
                    message=exc.message,
                    retryable=False,
                )
            )
            return
        LOGGER.debug("收到帧 op=%s req_id=%s", frame.op, frame.req_id)
        try:
            response = self._dispatch(frame)
        except ProtocolError as exc:
            response = error_frame(
                frame.req_id,
                code=exc.code,
                message=exc.message,
                retryable=False,
                binding=_binding_from_frame(frame),
            )
        except FlashCliUnavailable as exc:
            response = error_frame(
                frame.req_id,
                code=exc.code,
                message=exc.message,
                retryable=exc.retryable,
                action_ids=exc.action_ids,
                binding=_binding_from_frame(frame),
            )
        except CapabilityError as exc:
            response = error_frame(
                frame.req_id,
                code=exc.code,
                message=exc.message,
                retryable=exc.retryable,
                action_ids=list(exc.action_ids) or None,
                binding=_binding_from_frame(frame),
            )
        except MediaToolError as exc:
            response = error_frame(
                frame.req_id,
                code=exc.code,
                message=exc.message,
                retryable=exc.retryable,
                action_ids=getattr(exc, "action_ids", None),
                binding=_binding_from_frame(frame),
            )
        except Exception:
            # 异常消息/栈可能携带模型原文、字幕、凭据或路径；只记受控阶段与
            # 已校验的 op/req_id，不输出 str(exc) 或 exc_info。
            LOGGER.error("处理帧失败 op=%s req_id=%s", frame.op, frame.req_id)
            response = error_frame(
                frame.req_id,
                code=ERR_INTERNAL,
                message="runner 内部错误",
                retryable=True,
                binding=_binding_from_frame(frame),
            )
        if response is not None:
            self._send(response)

    def _dispatch(self, frame: Frame) -> Frame | None:
        if frame.op == "hello":
            return self._on_hello(frame)
        if frame.op == "probe":
            return self._on_probe(frame)
        if frame.op == "execute":
            return self._on_execute(frame)
        if frame.op == "cancel":
            return self._on_cancel(frame)
        if frame.op == "shutdown":
            return self._on_shutdown(frame)
        raise ProtocolError(ERR_UNKNOWN_OP, f"未知操作: {frame.op}")

    def _on_hello(self, frame: Frame) -> Frame:
        status = self._media_tools_status
        if status is None:
            status = describe_media_tools().as_dict()
        adapters: list[Mapping[str, Any]] = [
            self._flashcli.describe(),
        ]
        if self._ffmpeg is not None:
            adapters.append(self._ffmpeg.describe())
        else:
            adapters.append(
                {
                    "id": "ffmpeg",
                    "ready": False,
                    "reason": self._ffmpeg_error or "MEDIA_TOOL_UNAVAILABLE",
                }
            )
        adapters.append(self._separate.describe())
        adapters.append(self._voice_clone.describe())
        adapters.append(self._translate.describe())
        adapters.append(self._asr.describe())
        return result_frame(
            frame.req_id,
            request_op="hello",
            data={
                "runner_version": __version__,
                "protocol_version": PROTOCOL_VERSION,
                "media_tools": dict(status),
                "adapters": adapters,
            },
        )

    def _on_probe(self, frame: Frame) -> Frame:
        adapter_id = _requested_adapter_id(frame.payload)
        if adapter_id == "ffmpeg":
            if self._ffmpeg is None:
                raise MediaToolUnavailable(
                    self._ffmpeg_error or "未注入 FFMPEG_BIN；无法执行媒体探测"
                )
            params = frame.payload.get("params")
            if not isinstance(params, Mapping) or not isinstance(params.get("input"), str):
                raise ProtocolError(ERR_INVALID_REQUEST, "ffmpeg probe 需要 params.input")
            data = self._ffmpeg.probe(params["input"])
            return result_frame(frame.req_id, request_op="probe", data={"probe": data})
        if adapter_id == self._separate.adapter_id:
            params = frame.payload.get("params")
            data = self._separate.probe(params if isinstance(params, Mapping) else None)
            return result_frame(frame.req_id, request_op="probe", data={"probe": dict(data)})
        if adapter_id == self._voice_clone.adapter_id:
            params = frame.payload.get("params")
            data = self._voice_clone.probe(params if isinstance(params, Mapping) else None)
            return result_frame(frame.req_id, request_op="probe", data={"probe": dict(data)})
        if adapter_id == self._translate.adapter_id:
            params = frame.payload.get("params")
            data = self._translate.probe(params if isinstance(params, Mapping) else None)
            return result_frame(frame.req_id, request_op="probe", data={"probe": dict(data)})
        if adapter_id == self._asr.adapter_id:
            params = frame.payload.get("params")
            data = self._asr.probe(params if isinstance(params, Mapping) else None)
            return result_frame(frame.req_id, request_op="probe", data={"probe": dict(data)})
        # 运行时探针必须绑定已准入 runtime；当前 bundle 契约未定，统一门控。
        self._flashcli.probe(ProbeRequest())
        raise RuntimeError("门控适配器不应返回探针结果")

    def _lookup_threaded_adapter(self, adapter_id: str) -> Any | None:
        if adapter_id == FfmpegAdapter.adapter_id:
            if self._ffmpeg is None:
                raise MediaToolUnavailable(
                    self._ffmpeg_error or "未注入 FFMPEG_BIN；无法执行媒体导出"
                )
            return self._ffmpeg
        if adapter_id == self._separate.adapter_id:
            return self._separate
        if adapter_id == self._voice_clone.adapter_id:
            return self._voice_clone
        if adapter_id == self._translate.adapter_id:
            return self._translate
        if adapter_id == self._asr.adapter_id:
            return self._asr
        return None

    def _on_execute(self, frame: Frame) -> Frame | None:
        request = validate_execute_payload(frame.payload)
        if request.adapter.id == self._flashcli.adapter_id:
            try:
                self._flashcli.execute(request)
            except FlashCliUnavailable as exc:
                return error_frame(
                    frame.req_id,
                    code=exc.code,
                    message=exc.message,
                    retryable=exc.retryable,
                    action_ids=exc.action_ids,
                    binding=request.binding(),
                )
            raise RuntimeError("门控适配器不应返回执行成功")

        adapter = self._lookup_threaded_adapter(request.adapter.id)
        if adapter is None:
            raise ProtocolError(
                ERR_UNSUPPORTED_ADAPTER,
                f"不支持的适配器: {request.adapter.id}",
                details={"adapter_id": request.adapter.id},
            )

        active = _ActiveExecution(request, adapter)
        with self._active_lock:
            if self._active is not None:
                raise ProtocolError(ERR_INVALID_REQUEST, "已有执行中的任务")
            self._active = active
        try:
            thread = threading.Thread(
                target=self._run_execute,
                args=(frame.req_id, active, adapter),
                name=f"yaomu-execute-{request.task_id}",
                daemon=True,
            )
            active.thread = thread
            thread.start()
        except BaseException:
            # 线程未创建/未启动也必须清空登记，否则后续 execute/cancel 都会被这次失败卡住。
            with self._active_lock:
                if self._active is active:
                    self._active = None
            raise
        # 结果在 worker 线程中发送；主循环继续处理 cancel/hello 等帧。
        return None

    def _run_execute(self, req_id: str, active: _ActiveExecution, adapter: Any) -> None:
        request = active.request
        try:
            execute_kwargs: dict[str, Any] = {"cancel_event": active.cancel_event}
            # 执行过程只对已接线的模型适配器开放；ffmpeg 导出仍只走 stdout 结果帧。
            if adapter is not self._ffmpeg:
                emitter = LogEmitter(
                    lambda stream, line, seq: self._send(
                        log_frame(
                            req_id,
                            server_epoch=request.server_epoch,
                            task_id=request.task_id,
                            step_id=request.step_id,
                            attempt=request.attempt,
                            stream=stream,
                            line=line,
                            seq=seq,
                        )
                    )
                )
                execute_kwargs["log"] = emitter.emit
            data = adapter.execute(request, **execute_kwargs)
        except MediaToolError as exc:
            active.stop_unconfirmed = exc.code == "MEDIA_TOOL_STOP_UNCONFIRMED"
            response = error_frame(
                req_id,
                code=exc.code,
                message=exc.message,
                retryable=exc.retryable,
                action_ids=getattr(exc, "action_ids", None),
                binding=request.binding(),
            )
        except CapabilityError as exc:
            response = error_frame(
                req_id,
                code=exc.code,
                message=exc.message,
                retryable=exc.retryable,
                action_ids=list(exc.action_ids) or None,
                binding=request.binding(),
            )
        except ProtocolError as exc:
            response = error_frame(
                req_id,
                code=exc.code,
                message=exc.message,
                retryable=False,
                binding=request.binding(),
            )
        except Exception:
            # 异常消息/栈可能携带模型原文；只记受控阶段与已校验绑定。
            LOGGER.error(
                "执行失败 task=%s attempt=%s", request.task_id, request.attempt
            )
            response = error_frame(
                req_id,
                code=ERR_INTERNAL,
                message="runner 内部错误",
                retryable=True,
                binding=request.binding(),
            )
        else:
            response = result_frame(
                req_id,
                request_op="execute",
                data=data,
                binding=request.binding(),
            )
        with self._active_lock:
            if self._active is active and not active.stop_unconfirmed:
                self._active = None
        self._send(response)

    def _on_cancel(self, frame: Frame) -> Frame:
        request = validate_cancel_payload(frame.payload)
        with self._active_lock:
            active = self._active
        data: dict[str, Any] = {
            "task_id": request.task_id,
            "attempt": request.attempt,
            "active": active is not None,
            "exited": True,
        }
        if request.step_id is not None:
            data["step_id"] = request.step_id
        if active is None:
            return result_frame(frame.req_id, request_op="cancel", data=data)
        if (
            active.request.task_id != request.task_id
            or active.request.attempt != request.attempt
            or (request.step_id is not None and active.request.step_id != request.step_id)
        ):
            # 取消必须绑定当前 attempt；不误杀同 Runner 上的其他执行。
            data["exited"] = False
            return result_frame(frame.req_id, request_op="cancel", data=data)

        confirmed = True
        # 先置位取消事件（覆盖尚未启动子进程的竞态），再终止当前进程树。
        active.cancel_event.set()
        cancel = getattr(active.adapter, "cancel", None)
        if callable(cancel):
            confirmed = bool(cancel())
        if active.thread is not None and active.thread is not threading.current_thread():
            active.thread.join(timeout=10.0)
        exited = confirmed and active.thread is not None and not active.thread.is_alive()
        data["exited"] = exited
        return result_frame(frame.req_id, request_op="cancel", data=data)

    def _stop_active(self) -> None:
        with self._active_lock:
            active = self._active
        if active is None:
            return
        active.cancel_event.set()
        cancel = getattr(active.adapter, "cancel", None)
        if callable(cancel):
            try:
                cancel()
            except Exception:
                LOGGER.error("终止执行进程树失败")
        if active.thread is not None and active.thread is not threading.current_thread():
            active.thread.join(timeout=10.0)

    def _on_shutdown(self, frame: Frame) -> Frame:
        self._running = False
        return result_frame(
            frame.req_id,
            request_op="shutdown",
            data={"status": "stopped"},
        )

    # -- 传输 -------------------------------------------------------------

    def _handle_oversized(self, inp: BinaryIO, line: bytes) -> None:
        self._send(
            error_frame(
                _best_effort_req_id(line),
                code=ERR_FRAME_TOO_LARGE,
                message=f"帧超过 {MAX_FRAME_BYTES} 字节上限",
                retryable=False,
            )
        )
        self._drain_to_newline(inp, line)

    def _drain_to_newline(self, inp: BinaryIO, first: bytes) -> None:
        if first.endswith(b"\n"):
            return
        drained = len(first)
        while drained < _MAX_OVERSIZED_DRAIN_BYTES:
            chunk = inp.readline(65536)
            if not chunk:
                return
            drained += len(chunk)
            if chunk.endswith(b"\n"):
                return
        LOGGER.error("超长帧在 %d 字节内仍未结束，停止读取协议流", _MAX_OVERSIZED_DRAIN_BYTES)
        self._running = False

    def _send(self, frame: Frame) -> None:
        try:
            data = encode_frame(frame)
        except ProtocolError as exc:
            LOGGER.error("拒绝发送非法帧: %s", exc.message)
            return
        with self._send_lock:
            self._out.write(data)
            self._out.flush()


def _requested_adapter_id(payload: Mapping[str, Any]) -> str | None:
    adapter = payload.get("adapter")
    if isinstance(adapter, Mapping):
        value = adapter.get("id")
        if isinstance(value, str):
            return value
    return None


def main(argv: Sequence[str] | None = None) -> int:
    logging.basicConfig(
        stream=sys.stderr,
        level=getattr(logging, _log_level_from_env()),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    args = list(sys.argv[1:] if argv is None else argv)
    if args:
        LOGGER.error("Runner 不接受命令行参数；执行请求只经 stdio 协议帧传入")
        return 2
    service = RunnerService(out=sys.stdout.buffer)
    try:
        return service.serve(sys.stdin.buffer)
    except BrokenPipeError:
        LOGGER.error("协议 stdout 已关闭")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
