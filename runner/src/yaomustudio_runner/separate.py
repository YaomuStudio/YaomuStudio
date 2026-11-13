"""音频分离 Adapter（节点 03）。

默认 ``executor=flashcli``：受管 ``flashcli run xym/melband-roformer:1.0.10``
（白名单 flags，见 ``flashcli_runtime``）。``executor=mock`` 写假产物供联调。
"""

from __future__ import annotations

import logging
import os
import threading
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
    reject_unknown,
    require_existing_input_path,
    require_relative_output,
    resolve_output_path,
    write_bytes,
)
from .flashcli_runtime import (
    DEFAULT_SEPARATE_BGM_NAME,
    DEFAULT_SEPARATE_VOCALS_NAME,
    DEFAULT_SEPARATE_VOCALS_SAMPLE,
    build_separate_argv,
    resolve_flashcli_bin,
    run_flashcli,
)
from .protocol import ExecutionRequest

LOGGER = logging.getLogger("yaomustudio_runner.separate")

_PARAM_KEYS = frozenset(
    {
        "executor",
        "input",
        "vocals_name",
        "bgm_name",
        "vocals_sample",
        "device",
        "mock_delay_ms",
        "mock_fail",
        "timeout_s",
    }
)


class SeparateAdapter:
    adapter_id = "separate"

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._cancel_event = threading.Event()
        self._last_environ: Mapping[str, str] | None = None
        self._last_argv: tuple[str, ...] | None = None

    @property
    def ready(self) -> bool:
        return True

    @property
    def last_environ(self) -> Mapping[str, str] | None:
        return self._last_environ

    @property
    def last_argv(self) -> tuple[str, ...] | None:
        return self._last_argv

    def describe(self) -> Mapping[str, Any]:
        return {
            "id": self.adapter_id,
            "ready": True,
            "executors": ["flashcli", "mock"],
            "model_ref": "xym/melband-roformer:1.0.10",
        }

    def probe(self, params: Mapping[str, Any] | None = None) -> Mapping[str, Any]:
        del params
        # MVP：独立 probe 可免；安装哈希通过即可 READY。
        return {"ok": True, "executor": "flashcli", "probe": "skipped"}

    def execute(
        self,
        request: ExecutionRequest,
        *,
        cancel_event: threading.Event | None = None,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        params = dict(request.params)
        reject_unknown(params, _PARAM_KEYS, label="separate params")
        mode = executor_mode(params)
        with self._lock:
            self._cancel_event = cancel_event or threading.Event()
            active_cancel = self._cancel_event
            self._last_argv = None

        input_path = require_existing_input_path(params.get("input"), field="input")
        vocals_rel = require_relative_output(
            params.get("vocals_name", DEFAULT_SEPARATE_VOCALS_NAME),
            field="vocals_name",
        )
        bgm_rel = require_relative_output(
            params.get("bgm_name", DEFAULT_SEPARATE_BGM_NAME if mode == "flashcli" else "bgm.wav"),
            field="bgm_name",
        )
        if vocals_rel == bgm_rel:
            raise CapabilityInvalidParams("vocals_name 与 bgm_name 不能相同")

        vocals_sample = optional_int(
            params, "vocals_sample", DEFAULT_SEPARATE_VOCALS_SAMPLE
        )
        if vocals_sample is None:
            vocals_sample = DEFAULT_SEPARATE_VOCALS_SAMPLE

        env = device_environ(params)
        self._last_environ = env
        os.makedirs(request.output_dir, exist_ok=True)

        if mode == "mock":
            return self._execute_mock(
                request,
                params=params,
                input_path=input_path,
                vocals_rel=vocals_rel,
                bgm_rel=bgm_rel,
                env=env,
                cancel_event=active_cancel,
                log=log,
            )

        return self._execute_flashcli(
            request,
            params=params,
            input_path=input_path,
            vocals_rel=vocals_rel,
            bgm_rel=bgm_rel,
            vocals_sample=vocals_sample,
            env=env,
            cancel_event=active_cancel,
            log=log,
        )

    def _execute_mock(
        self,
        request: ExecutionRequest,
        *,
        params: Mapping[str, Any],
        input_path: str,
        vocals_rel: str,
        bgm_rel: str,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        delay_ms = optional_int(params, "mock_delay_ms", 0) or 0
        if delay_ms < 0 or delay_ms > 60_000:
            raise CapabilityInvalidParams("mock_delay_ms 超出范围")
        if log is not None:
            log("runner", "使用 mock 分离执行器")
        interruptible_sleep(cancel_event, delay_ms=delay_ms)

        if optional_bool(params, "mock_fail", False):
            raise CapabilityFailed("mock 强制失败")

        vocals_abs = resolve_output_path(request.output_dir, vocals_rel)
        bgm_abs = resolve_output_path(request.output_dir, bgm_rel)
        write_bytes(vocals_abs, b"MOCK-SEPARATE-VOCALS\n")
        write_bytes(bgm_abs, b"MOCK-SEPARATE-BGM\n")

        LOGGER.info(
            "separate mock 完成 task=%s cuda_visible_devices=%s input=%s",
            request.task_id,
            env.get("CUDA_VISIBLE_DEVICES"),
            os.path.basename(input_path),
        )
        if log is not None:
            log("runner", "mock 分离完成")
        return {
            "outputs": [
                artifact_entry(vocals_rel, vocals_abs),
                artifact_entry(bgm_rel, bgm_abs),
            ],
            "executor": "mock",
            "device": {
                "cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES"),
            },
        }

    def _execute_flashcli(
        self,
        request: ExecutionRequest,
        *,
        params: Mapping[str, Any],
        input_path: str,
        vocals_rel: str,
        bgm_rel: str,
        vocals_sample: int,
        env: Mapping[str, str],
        cancel_event: threading.Event,
        log: Callable[[str, str], None] | None = None,
    ) -> dict[str, Any]:
        flashcli_bin = resolve_flashcli_bin()
        argv = build_separate_argv(
            flashcli_bin,
            input_path=input_path,
            output_dir=request.output_dir,
            bgm_name=bgm_rel,
            vocals_name=vocals_rel,
            vocals_sample=vocals_sample,
        )
        with self._lock:
            self._last_argv = tuple(argv)

        timeout_s = optional_int(params, "timeout_s", 3600) or 3600
        if timeout_s < 1 or timeout_s > 24 * 3600:
            raise CapabilityInvalidParams("timeout_s 超出范围")

        LOGGER.info(
            "separate flashcli 启动 task=%s model=%s",
            request.task_id,
            "xym/melband-roformer:1.0.10",
        )
        if log is not None:
            log("runner", "启动 FlashCLI 分离")
        run_flashcli(
            argv,
            env=env,
            cancel_event=cancel_event,
            timeout_s=float(timeout_s),
            on_log=log,
        )

        vocals_abs = resolve_output_path(request.output_dir, vocals_rel)
        bgm_abs = resolve_output_path(request.output_dir, bgm_rel)
        if not os.path.isfile(vocals_abs) or os.path.getsize(vocals_abs) <= 0:
            raise CapabilityFailed("分离未生成有效人声产物")
        if not os.path.isfile(bgm_abs) or os.path.getsize(bgm_abs) <= 0:
            raise CapabilityFailed("分离未生成有效环境音产物")
        if log is not None:
            log("runner", "FlashCLI 分离完成")

        return {
            "outputs": [
                artifact_entry(vocals_rel, vocals_abs),
                artifact_entry(bgm_rel, bgm_abs),
            ],
            "executor": "flashcli",
            "model_ref": "xym/melband-roformer:1.0.10",
            "device": {
                "cuda_visible_devices": env.get("CUDA_VISIBLE_DEVICES"),
            },
        }

    def cancel(self, *, term_grace_s: float = 1.0, kill_grace_s: float = 5.0) -> bool:
        del term_grace_s, kill_grace_s
        with self._lock:
            self._cancel_event.set()
        return True
