"""能力 Adapter 共用：错误码、路径白名单、设备环境、可取消 mock 等待。

方案 A：只做单步执行机制（设备绑定 / 产物落盘 / 取消），不做业务排队。
分离/克隆：显式 ``executor=flashcli|mock``；未指定时若宿主注入了 ``FLASHCLI_BIN``
则默认 flashcli（已确认白名单），否则 mock。可用 ``YAOMU_CAPABILITY_EXECUTOR`` 强制覆盖。
"""

from __future__ import annotations

import os
import threading
import time
from collections.abc import Mapping, Sequence
from typing import Any

ENV_CAPABILITY_EXECUTOR = "YAOMU_CAPABILITY_EXECUTOR"
ENV_FLASHCLI_BIN = "FLASHCLI_BIN"

_SHELL_METACHARS = frozenset(";|&`$<>")
_CONTROL_CHARS = frozenset("\x00\n\r")

CAPABILITY_INVALID_PARAMS = "CAPABILITY_INVALID_PARAMS"
CAPABILITY_FAILED = "CAPABILITY_FAILED"
CAPABILITY_CANCELED = "CAPABILITY_CANCELED"
CAPABILITY_UNAVAILABLE = "CAPABILITY_UNAVAILABLE"

GATE_ACTION_IDS = ("open_model_setup",)


class CapabilityError(RuntimeError):
    code = CAPABILITY_FAILED
    retryable = False

    def __init__(
        self,
        message: str,
        *,
        action_ids: Sequence[str] | None = None,
        retryable: bool | None = None,
    ):
        super().__init__(message)
        self.message = message
        self.action_ids = tuple(action_ids) if action_ids is not None else ()
        if retryable is not None:
            self.retryable = retryable


class CapabilityInvalidParams(CapabilityError):
    code = CAPABILITY_INVALID_PARAMS


class CapabilityFailed(CapabilityError):
    code = CAPABILITY_FAILED


class CapabilityCanceled(CapabilityError):
    code = CAPABILITY_CANCELED
    retryable = False


class CapabilityUnavailable(CapabilityError):
    code = CAPABILITY_UNAVAILABLE
    retryable = False

    def __init__(self, message: str, *, action_ids: Sequence[str] = GATE_ACTION_IDS):
        super().__init__(message, action_ids=action_ids)


def reject_unknown(params: Mapping[str, Any], allowed: frozenset[str], *, label: str) -> None:
    unknown = sorted(set(params) - allowed)
    if unknown:
        raise CapabilityInvalidParams(f"{label} 含未知字段: {unknown}")


def require_str(params: Mapping[str, Any], key: str) -> str:
    value = params.get(key)
    if not isinstance(value, str) or not value:
        raise CapabilityInvalidParams(f"{key} 必须是非空字符串")
    return value


def optional_str(params: Mapping[str, Any], key: str) -> str | None:
    value = params.get(key)
    if value is None:
        return None
    if not isinstance(value, str) or not value:
        raise CapabilityInvalidParams(f"{key} 必须是非空字符串")
    return value


def optional_bool(params: Mapping[str, Any], key: str, default: bool = False) -> bool:
    value = params.get(key, default)
    if not isinstance(value, bool):
        raise CapabilityInvalidParams(f"{key} 必须是布尔值")
    return value


def optional_int(params: Mapping[str, Any], key: str, default: int | None = None) -> int | None:
    value = params.get(key, default)
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int):
        raise CapabilityInvalidParams(f"{key} 必须是整数")
    return value


def require_relative_output(value: object, *, field: str = "out") -> str:
    if not isinstance(value, str) or not value:
        raise CapabilityInvalidParams(f"{field} 必须是非空相对路径")
    if len(value) > 255:
        raise CapabilityInvalidParams(f"{field} 过长")
    for ch in value:
        if ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise CapabilityInvalidParams(f"{field} 含控制字符")
    if os.path.isabs(value):
        raise CapabilityInvalidParams(f"{field} 必须是相对路径")
    parts = value.replace("\\", "/").split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise CapabilityInvalidParams(f"{field} 不得包含空片段、. 或 ..")
    return value


def resolve_output_path(output_dir: str, rel_path: str) -> str:
    output_root = os.path.realpath(output_dir)
    candidate = os.path.realpath(os.path.join(output_dir, rel_path))
    if candidate == output_root:
        raise CapabilityInvalidParams("产物路径不能指向输出目录本身")
    try:
        inside = os.path.commonpath([output_root, candidate]) == output_root
    except ValueError:
        inside = False
    if not inside:
        raise CapabilityInvalidParams("产物路径逃逸出输出目录")
    return candidate


def require_existing_input_path(value: object, *, field: str) -> str:
    if not isinstance(value, str) or not value:
        raise CapabilityInvalidParams(f"{field} 必须是非空路径")
    if len(value) > 4096:
        raise CapabilityInvalidParams(f"{field} 过长")
    for ch in value:
        if ch in _CONTROL_CHARS or ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise CapabilityInvalidParams(f"{field} 含控制字符")
        if ch in _SHELL_METACHARS:
            raise CapabilityInvalidParams(f"{field} 含非法 shell 元字符")
    if not os.path.isabs(value):
        raise CapabilityInvalidParams(f"{field} 必须是绝对路径")
    real = os.path.realpath(value)
    if not os.path.isfile(real):
        raise CapabilityInvalidParams(f"{field} 不是可读文件")
    return real


def default_executor_mode(environ: Mapping[str, str] | None = None) -> str:
    env = os.environ if environ is None else environ
    raw = env.get(ENV_CAPABILITY_EXECUTOR, "").strip().lower()
    if raw in ("mock", "flashcli"):
        return raw
    if env.get(ENV_FLASHCLI_BIN, "").strip():
        return "flashcli"
    return "mock"


def executor_mode(
    params: Mapping[str, Any],
    *,
    environ: Mapping[str, str] | None = None,
) -> str:
    value = params.get("executor", default_executor_mode(environ))
    if value not in ("mock", "flashcli"):
        raise CapabilityInvalidParams("executor 必须是 mock 或 flashcli")
    return str(value)


def device_environ(params: Mapping[str, Any], base: Mapping[str, str] | None = None) -> dict[str, str]:
    """把 Core 下发的设备约束落到子进程环境；不做排队。"""

    env = dict(os.environ if base is None else base)
    device = params.get("device")
    if device is None:
        return env
    if not isinstance(device, Mapping):
        raise CapabilityInvalidParams("device 必须是对象")
    unknown = sorted(set(device) - {"cuda_visible_devices"})
    if unknown:
        raise CapabilityInvalidParams(f"device 含未知字段: {unknown}")
    cvd = device.get("cuda_visible_devices")
    if cvd is None:
        return env
    if isinstance(cvd, bool) or not isinstance(cvd, (str, int)):
        raise CapabilityInvalidParams("device.cuda_visible_devices 必须是字符串或整数")
    token = str(cvd).strip()
    if not token or any(ch.isspace() or ch in _SHELL_METACHARS for ch in token):
        raise CapabilityInvalidParams("device.cuda_visible_devices 非法")
    env["CUDA_VISIBLE_DEVICES"] = token
    return env


def interruptible_sleep(
    cancel_event: threading.Event,
    *,
    delay_ms: int,
    slice_ms: int = 20,
) -> None:
    if delay_ms <= 0:
        if cancel_event.is_set():
            raise CapabilityCanceled("执行已取消")
        return
    deadline = time.monotonic() + (delay_ms / 1000.0)
    while True:
        if cancel_event.is_set():
            raise CapabilityCanceled("执行已取消")
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return
        time.sleep(min(slice_ms / 1000.0, remaining))


def write_bytes(path: str, payload: bytes) -> int:
    parent = os.path.dirname(path)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(path, "wb") as handle:
        handle.write(payload)
    size = os.path.getsize(path)
    if size <= 0:
        raise CapabilityFailed("未生成有效产物")
    return size


def artifact_entry(rel_path: str, abs_path: str) -> dict[str, Any]:
    return {"rel_path": rel_path, "size_bytes": os.path.getsize(abs_path)}
