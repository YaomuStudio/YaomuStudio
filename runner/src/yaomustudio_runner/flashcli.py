"""FlashCLI 适配器门控桩。

硬约束：六槽位 bundle 的真实入口、离线参数与准备流程尚未确认
（``docs/flashcli-integration.md``、``docs/open-questions.md``、项目硬约束 #5），
因此这里只保留类型化接口与方法语义，不构造任何命令行参数，也不启动进程。

所有方法都以 ``MODEL_RUNTIME_UNAVAILABLE`` 门控失败，由 Core 映射为
“打开模型安装”等修复动作（错误码见 ``docs/local-api-contract.md`` §4）。
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, NoReturn

from .protocol import CancelRequest, ExecutionRequest

MODEL_RUNTIME_UNAVAILABLE = "MODEL_RUNTIME_UNAVAILABLE"

GATE_ACTION_IDS = ("open_model_setup",)


class CapabilityUnavailable(RuntimeError):
    """能力未就绪；对应 Core API 错误码 ``MODEL_RUNTIME_UNAVAILABLE``。"""

    code = MODEL_RUNTIME_UNAVAILABLE
    retryable = False

    def __init__(self, message: str, *, action_ids: tuple[str, ...] = GATE_ACTION_IDS):
        super().__init__(message)
        self.message = message
        self.action_ids = action_ids


@dataclass(frozen=True)
class ProbeRequest:
    """运行时探针请求的最小类型；正式字段等待准入 bundle 契约。"""

    runtime_id: str | None = None
    runtime_digest: str | None = None


class FlashCliAdapter:
    """FlashCLI 的类型化门控接口；在契约确认前不会变为 ready。"""

    adapter_id = "flashcli"

    def __init__(self, *, admitted_bundle_id: str | None = None) -> None:
        # 只记录是否存在准入 bundle；当前发布契约未定，正常路径为 None。
        self._admitted_bundle_id = admitted_bundle_id

    @property
    def ready(self) -> bool:
        # 即使登记了 bundle id，也缺少已验证的启动/探针/取消接口，不能放行。
        return False

    def describe(self) -> Mapping[str, Any]:
        return {
            "id": self.adapter_id,
            "ready": self.ready,
            "reason": MODEL_RUNTIME_UNAVAILABLE,
        }

    def execute(self, request: ExecutionRequest) -> NoReturn:
        del request
        raise CapabilityUnavailable(
            "FlashCLI 执行入口尚未准入（bundle 契约与离线参数未确认）"
        )

    def cancel(self, request: CancelRequest) -> NoReturn:
        del request
        raise CapabilityUnavailable(
            "FlashCLI 运行时未就绪，不存在可取消的执行"
        )

    def probe(self, request: ProbeRequest) -> NoReturn:
        del request
        raise CapabilityUnavailable(
            "FlashCLI 运行时探针需要已准入的 bundle 与探针契约"
        )
