"""执行协议 v1：帧定义、编解码与校验。

权威语义见 ``docs/local-api-contract.md`` §6；帧类型摘要见
``contracts/openapi.yaml`` 的 ``x-execution-protocol``。本模块只做结构与绑定字段
校验，不解释 FlashCLI 参数，也不定义六槽位 bundle 内容。

帧为 UTF-8 单行 JSON::

    {"v": 1, "op": "<op>", "req_id": "<opaque id>", "payload": {...}}

Core -> Runner 请求：hello / probe / execute / cancel / shutdown
Runner -> Core 事件：heartbeat / progress / log / result / error

``log`` 是执行过程行（可丢弃，不作为成功条件）；其余事件语义见
``docs/local-api-contract.md`` §5/§6。
"""

from __future__ import annotations

import json
import os
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any

PROTOCOL_VERSION = 1
MAX_FRAME_BYTES = 1024 * 1024

REQUEST_OPS = frozenset({"hello", "probe", "execute", "cancel", "shutdown"})
EVENT_OPS = frozenset({"heartbeat", "progress", "log", "result", "error"})
KNOWN_OPS = REQUEST_OPS | EVENT_OPS

LOG_STREAMS = frozenset({"stderr", "stdout", "runner"})
MAX_LOG_LINE_CHARS = 512

_FRAME_KEYS = frozenset({"v", "op", "req_id", "payload"})

# Runner 协议内部错误码；Core 负责映射为对外的 API 错误码。
ERR_MALFORMED_FRAME = "MALFORMED_FRAME"
ERR_FRAME_TOO_LARGE = "FRAME_TOO_LARGE"
ERR_UNSUPPORTED_VERSION = "UNSUPPORTED_PROTOCOL_VERSION"
ERR_UNKNOWN_OP = "UNKNOWN_OP"
ERR_INVALID_REQUEST = "INVALID_REQUEST"
ERR_INTERNAL = "INTERNAL_ERROR"
ERR_UNSUPPORTED_ADAPTER = "UNSUPPORTED_ADAPTER"


class ProtocolError(ValueError):
    """帧级协议错误；code 用于 error 帧，message 只作本地诊断。"""

    def __init__(self, code: str, message: str, *, details: Mapping[str, Any] | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.details = dict(details or {})


@dataclass(frozen=True)
class Frame:
    """一帧协议消息。payload 只读，避免跨线程共享时被意外修改。"""

    v: int
    op: str
    req_id: str
    payload: Mapping[str, Any] = field(default_factory=dict)

    def __post_init__(self) -> None:
        object.__setattr__(self, "payload", MappingProxyType(dict(self.payload)))

    def as_dict(self) -> dict[str, Any]:
        return {
            "v": self.v,
            "op": self.op,
            "req_id": self.req_id,
            "payload": dict(self.payload),
        }


@dataclass(frozen=True)
class ComponentRef:
    """模型 / 适配器 / 运行时的精确版本引用（id + 不可变摘要）。"""

    id: str
    digest: str

    def as_dict(self) -> dict[str, str]:
        return {"id": self.id, "digest": self.digest}


@dataclass(frozen=True)
class ExecutionRequest:
    """通过校验的 execute 载荷。"""

    workspace: str
    server_epoch: str
    task_id: str
    step_id: str
    attempt: int
    model: ComponentRef
    adapter: ComponentRef
    runtime: ComponentRef
    params: Mapping[str, Any]
    input_asset_ids: tuple[str, ...]
    output_dir: str

    def binding(self) -> dict[str, Any]:
        """Core 用来核对迟到结果的绑定信息。"""
        return {
            "workspace": self.workspace,
            "server_epoch": self.server_epoch,
            "task_id": self.task_id,
            "step_id": self.step_id,
            "attempt": self.attempt,
        }


@dataclass(frozen=True)
class CancelRequest:
    task_id: str
    attempt: int
    step_id: str | None = None

    def binding(self) -> dict[str, Any]:
        binding: dict[str, Any] = {"task_id": self.task_id, "attempt": self.attempt}
        if self.step_id is not None:
            binding["step_id"] = self.step_id
        return binding


_COMPONENT_KEYS = frozenset({"id", "digest"})
_EXECUTE_KEYS = frozenset(
    {
        "workspace",
        "server_epoch",
        "task_id",
        "step_id",
        "attempt",
        "model",
        "adapter",
        "runtime",
        "params",
        "input_asset_ids",
        "output_dir",
    }
)
_CANCEL_KEYS = frozenset({"task_id", "attempt", "step_id"})


def _is_int(value: object) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def _require_int(value: object, *, what: str, minimum: int | None = None, maximum: int | None = None) -> int:
    if not _is_int(value):
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 必须是整数")
    number = int(value)  # type: ignore[arg-type]
    if minimum is not None and number < minimum:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 不能小于 {minimum}")
    if maximum is not None and number > maximum:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 不能大于 {maximum}")
    return number


def _require_token(value: object, *, what: str, max_len: int = 256) -> str:
    if not isinstance(value, str):
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 必须是字符串")
    if not value:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 不能为空")
    if len(value) > max_len:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 超过 {max_len} 字符上限")
    for ch in value:
        if ch.isspace() or ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 含空白或控制字符")
    return value


def _require_epoch(value: object) -> str:
    if _is_int(value):
        return str(value)
    if isinstance(value, str):
        return _require_token(value, what="server_epoch", max_len=128)
    raise ProtocolError(ERR_INVALID_REQUEST, "server_epoch 必须是字符串或整数")


def _require_mapping(value: object, *, what: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 必须是对象")
    for key in value:
        if not isinstance(key, str):
            raise ProtocolError(ERR_INVALID_REQUEST, f"{what} 的键必须是字符串")
    return value


def _component_ref(payload: Mapping[str, Any], key: str) -> ComponentRef:
    value = _require_mapping(payload.get(key), what=key)
    unknown = set(value) - _COMPONENT_KEYS
    if unknown:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{key} 含未知字段: {sorted(unknown)}")
    missing = _COMPONENT_KEYS - set(value)
    if missing:
        raise ProtocolError(ERR_INVALID_REQUEST, f"{key} 缺少字段: {sorted(missing)}")
    return ComponentRef(
        id=_require_token(value.get("id"), what=f"{key}.id", max_len=256),
        digest=_require_token(value.get("digest"), what=f"{key}.digest", max_len=256),
    )


def _validate_output_dir(value: object) -> str:
    if not isinstance(value, str) or not value:
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 必须是非空路径字符串")
    if len(value) > 4096:
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 过长")
    if "\x00" in value or "\n" in value or "\r" in value:
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 含控制字符")
    if not os.path.isabs(value):
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 必须是绝对路径")
    parts = value.replace("\\", "/").split("/")
    if ".." in parts:
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 不得包含上级目录片段")
    if os.path.normpath(value) == os.path.sep:
        raise ProtocolError(ERR_INVALID_REQUEST, "output_dir 不能是文件系统根目录")
    return os.path.normpath(value)


def validate_execute_payload(payload: Mapping[str, Any]) -> ExecutionRequest:
    """校验 execute 载荷并返回冻结的请求对象。

    必填绑定：workspace、server_epoch、task_id、step_id、attempt、
    model/adapter/runtime 的 id+digest、params、input_asset_ids、output_dir。
    """

    if not isinstance(payload, Mapping):
        raise ProtocolError(ERR_INVALID_REQUEST, "execute 载荷必须是对象")
    unknown = set(payload) - _EXECUTE_KEYS
    if unknown:
        raise ProtocolError(ERR_INVALID_REQUEST, f"execute 载荷含未知字段: {sorted(unknown)}")
    missing = _EXECUTE_KEYS - set(payload)
    if missing:
        raise ProtocolError(ERR_INVALID_REQUEST, f"execute 载荷缺少字段: {sorted(missing)}")

    workspace = _require_token(payload.get("workspace"), what="workspace", max_len=256)
    server_epoch = _require_epoch(payload.get("server_epoch"))
    task_id = _require_token(payload.get("task_id"), what="task_id", max_len=256)
    step_id = _require_token(payload.get("step_id"), what="step_id", max_len=256)
    attempt = _require_int(payload.get("attempt"), what="attempt", minimum=1)

    model = _component_ref(payload, "model")
    adapter = _component_ref(payload, "adapter")
    runtime = _component_ref(payload, "runtime")

    params = dict(_require_mapping(payload.get("params"), what="params"))

    raw_assets = payload.get("input_asset_ids")
    if isinstance(raw_assets, (str, bytes)) or not isinstance(raw_assets, Sequence):
        raise ProtocolError(ERR_INVALID_REQUEST, "input_asset_ids 必须是字符串数组")
    if len(raw_assets) > 256:
        raise ProtocolError(ERR_INVALID_REQUEST, "input_asset_ids 数量超过上限")
    asset_ids = tuple(
        _require_token(item, what="input_asset_ids[]", max_len=256) for item in raw_assets
    )
    if len(set(asset_ids)) != len(asset_ids):
        raise ProtocolError(ERR_INVALID_REQUEST, "input_asset_ids 不得重复")

    output_dir = _validate_output_dir(payload.get("output_dir"))

    return ExecutionRequest(
        workspace=workspace,
        server_epoch=server_epoch,
        task_id=task_id,
        step_id=step_id,
        attempt=attempt,
        model=model,
        adapter=adapter,
        runtime=runtime,
        params=params,
        input_asset_ids=asset_ids,
        output_dir=output_dir,
    )


def validate_cancel_payload(payload: Mapping[str, Any]) -> CancelRequest:
    if not isinstance(payload, Mapping):
        raise ProtocolError(ERR_INVALID_REQUEST, "cancel 载荷必须是对象")
    unknown = set(payload) - _CANCEL_KEYS
    if unknown:
        raise ProtocolError(ERR_INVALID_REQUEST, f"cancel 载荷含未知字段: {sorted(unknown)}")
    task_id = _require_token(payload.get("task_id"), what="task_id", max_len=256)
    attempt = _require_int(payload.get("attempt"), what="attempt", minimum=1)
    step_id = payload.get("step_id")
    if step_id is not None:
        step_id = _require_token(step_id, what="step_id", max_len=256)
    return CancelRequest(task_id=task_id, attempt=attempt, step_id=step_id)


def validate_frame(obj: object, *, direction: str | None = None) -> Frame:
    """校验帧结构；direction=request/event 时限制操作方向。"""

    if direction not in (None, "request", "event"):
        raise ValueError("direction 只能是 None、request 或 event")
    if not isinstance(obj, Mapping):
        raise ProtocolError(ERR_MALFORMED_FRAME, "帧必须是 JSON 对象")
    unknown = set(obj) - _FRAME_KEYS
    if unknown:
        raise ProtocolError(ERR_INVALID_REQUEST, f"帧含未知字段: {sorted(unknown)}")

    for key in ("v", "op", "req_id"):
        if key not in obj:
            raise ProtocolError(ERR_MALFORMED_FRAME, f"帧缺少字段: {key}")

    version = obj.get("v")
    if not _is_int(version):
        raise ProtocolError(ERR_MALFORMED_FRAME, "v 必须是整数")
    if version != PROTOCOL_VERSION:
        raise ProtocolError(
            ERR_UNSUPPORTED_VERSION,
            f"不支持的协议版本 {version}，当前版本为 {PROTOCOL_VERSION}",
        )

    op = obj.get("op")
    if not isinstance(op, str) or not op:
        raise ProtocolError(ERR_MALFORMED_FRAME, "op 必须是非空字符串")
    if op not in KNOWN_OPS:
        raise ProtocolError(ERR_UNKNOWN_OP, f"未知操作: {op}")
    if direction == "request" and op not in REQUEST_OPS:
        raise ProtocolError(ERR_UNKNOWN_OP, f"{op} 不是 Core -> Runner 请求")
    if direction == "event" and op not in EVENT_OPS:
        raise ProtocolError(ERR_UNKNOWN_OP, f"{op} 不是 Runner -> Core 事件")

    req_id = _require_token(obj.get("req_id"), what="req_id", max_len=128)
    payload = _require_mapping(obj.get("payload", {}), what="payload")

    return Frame(v=version, op=op, req_id=req_id, payload=dict(payload))


def encode_frame(frame: Frame) -> bytes:
    """编码为 UTF-8 单行 JSON（含行尾换行），并强制大小上限。"""

    if not isinstance(frame, Frame):
        raise TypeError("frame 必须是 Frame")
    obj = frame.as_dict()
    # 构造器也可能拼出非法帧，编码前再校验一次。
    validate_frame(obj)
    try:
        body = json.dumps(obj, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    except (TypeError, ValueError) as exc:
        raise ProtocolError(ERR_INVALID_REQUEST, f"payload 无法序列化为 JSON: {exc}") from None
    data = body.encode("utf-8")
    if len(data) > MAX_FRAME_BYTES:
        raise ProtocolError(
            ERR_FRAME_TOO_LARGE, f"帧超过 {MAX_FRAME_BYTES} 字节上限"
        )
    return data + b"\n"


def decode_frame(
    data: bytes | bytearray | memoryview | str, *, direction: str | None = None
) -> Frame:
    """解码单行帧；拒绝非 UTF-8、多行、超大或结构非法的输入。"""

    if isinstance(data, str):
        text = data
        if len(text.encode("utf-8", "replace")) > MAX_FRAME_BYTES + 2:
            raise ProtocolError(ERR_FRAME_TOO_LARGE, f"帧超过 {MAX_FRAME_BYTES} 字节上限")
    elif isinstance(data, (bytes, bytearray, memoryview)):
        raw = bytes(data)
        if len(raw) > MAX_FRAME_BYTES + 2:
            raise ProtocolError(ERR_FRAME_TOO_LARGE, f"帧超过 {MAX_FRAME_BYTES} 字节上限")
        try:
            text = raw.decode("utf-8")
        except UnicodeDecodeError:
            raise ProtocolError(ERR_MALFORMED_FRAME, "帧不是合法 UTF-8") from None
    else:
        raise TypeError("data 必须是 str 或 bytes")

    if text.endswith("\n"):
        text = text[:-1]
    if text.endswith("\r"):
        text = text[:-1]
    if not text or "\n" in text or "\r" in text:
        raise ProtocolError(ERR_MALFORMED_FRAME, "帧必须是单行非空 JSON")
    if len(text.encode("utf-8")) > MAX_FRAME_BYTES:
        raise ProtocolError(ERR_FRAME_TOO_LARGE, f"帧超过 {MAX_FRAME_BYTES} 字节上限")

    try:
        obj = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ProtocolError(ERR_MALFORMED_FRAME, f"帧不是合法 JSON: {exc.msg}") from None
    return validate_frame(obj, direction=direction)


def make_frame(op: str, req_id: str, payload: Mapping[str, Any] | None = None) -> Frame:
    """构造并校验一帧，供各 builder 与测试使用。"""

    frame = Frame(
        v=PROTOCOL_VERSION,
        op=op,
        req_id=req_id,
        payload=dict(payload or {}),
    )
    validate_frame(frame.as_dict())
    return frame


def result_frame(
    req_id: str,
    *,
    request_op: str,
    data: Mapping[str, Any] | None = None,
    binding: Mapping[str, Any] | None = None,
) -> Frame:
    if request_op not in REQUEST_OPS:
        raise ValueError(f"request_op 必须是请求操作: {request_op}")
    payload: dict[str, Any] = {"request_op": request_op, "status": "ok"}
    if data:
        payload["data"] = dict(data)
    if binding:
        payload["binding"] = dict(binding)
    return make_frame("result", req_id, payload)


def error_frame(
    req_id: str,
    *,
    code: str,
    message: str,
    retryable: bool = False,
    action_ids: Sequence[str] | None = None,
    binding: Mapping[str, Any] | None = None,
) -> Frame:
    payload: dict[str, Any] = {
        "code": _require_token(code, what="error.code", max_len=128),
        "message": message,
        "retryable": bool(retryable),
    }
    if action_ids:
        payload["action_ids"] = [
            _require_token(item, what="action_ids[]", max_len=128) for item in action_ids
        ]
    if binding:
        payload["binding"] = dict(binding)
    return make_frame("error", req_id, payload)


def heartbeat_frame(
    req_id: str,
    *,
    server_epoch: str,
    task_id: str,
    step_id: str | None,
    attempt: int,
    pid: int,
    uptime_ms: int,
) -> Frame:
    """心跳只上报观察结果，不判断进程死亡；终态由 Core 决定。"""

    payload: dict[str, Any] = {
        "server_epoch": _require_epoch(server_epoch),
        "task_id": _require_token(task_id, what="task_id"),
        "attempt": _require_int(attempt, what="attempt", minimum=1),
        "pid": _require_int(pid, what="pid", minimum=1),
        "uptime_ms": _require_int(uptime_ms, what="uptime_ms", minimum=0),
    }
    if step_id is not None:
        payload["step_id"] = _require_token(step_id, what="step_id")
    return make_frame("heartbeat", req_id, payload)


def progress_frame(
    req_id: str,
    *,
    server_epoch: str,
    task_id: str,
    step_id: str,
    attempt: int,
    stage: str,
    current: int | None = None,
    total: int | None = None,
) -> Frame:
    """高频、可丢弃的进度帧；只上报执行器真实知道的阶段与计数。"""

    payload: dict[str, Any] = {
        "server_epoch": _require_epoch(server_epoch),
        "task_id": _require_token(task_id, what="task_id"),
        "step_id": _require_token(step_id, what="step_id"),
        "attempt": _require_int(attempt, what="attempt", minimum=1),
        "stage": _require_token(stage, what="stage", max_len=64),
    }
    if current is not None:
        payload["current"] = _require_int(current, what="current", minimum=0)
    if total is not None:
        total_value = _require_int(total, what="total", minimum=0)
        if current is None:
            raise ProtocolError(ERR_INVALID_REQUEST, "total 不能脱离 current 单独出现")
        if current > total_value:
            raise ProtocolError(ERR_INVALID_REQUEST, "current 不能大于 total")
        payload["total"] = total_value
    return make_frame("progress", req_id, payload)


def log_frame(
    req_id: str,
    *,
    server_epoch: str,
    task_id: str,
    step_id: str,
    attempt: int,
    stream: str,
    line: str,
    seq: int | None = None,
) -> Frame:
    """执行过程行（可丢弃）；调用方负责先脱敏，单行不超过 512 字符。"""

    if stream not in LOG_STREAMS:
        raise ProtocolError(
            ERR_INVALID_REQUEST, f"stream 必须是 {sorted(LOG_STREAMS)} 之一"
        )
    if not isinstance(line, str) or not line:
        raise ProtocolError(ERR_INVALID_REQUEST, "line 必须是非空字符串")
    if len(line) > MAX_LOG_LINE_CHARS:
        raise ProtocolError(
            ERR_INVALID_REQUEST, f"line 超过 {MAX_LOG_LINE_CHARS} 字符上限"
        )
    for ch in line:
        if ch in ("\n", "\r") or (ord(ch) < 0x20 and ch != "\t") or ord(ch) == 0x7F:
            raise ProtocolError(ERR_INVALID_REQUEST, "line 含换行或控制字符")
    payload: dict[str, Any] = {
        "server_epoch": _require_epoch(server_epoch),
        "task_id": _require_token(task_id, what="task_id"),
        "step_id": _require_token(step_id, what="step_id"),
        "attempt": _require_int(attempt, what="attempt", minimum=1),
        "stream": stream,
        "line": line,
    }
    if seq is not None:
        payload["seq"] = _require_int(seq, what="seq", minimum=0)
    return make_frame("log", req_id, payload)
