"""FlashCLI 门控桩：全部操作返回 MODEL_RUNTIME_UNAVAILABLE，且不含任何启动参数。"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

import _helpers  # noqa: F401

from yaomustudio_runner.flashcli import (
    MODEL_RUNTIME_UNAVAILABLE,
    CancelRequest,
    CapabilityUnavailable,
    FlashCliAdapter,
    ProbeRequest,
)
from yaomustudio_runner.protocol import validate_execute_payload


def _execute_request():
    return validate_execute_payload(
        {
            "workspace": "ws-1",
            "server_epoch": "epoch-1",
            "task_id": "task-1",
            "step_id": "step-1",
            "attempt": 1,
            "model": {"id": "model-1", "digest": "sha256:aa"},
            "adapter": {"id": "flashcli", "digest": "sha256:bb"},
            "runtime": {"id": "runtime-1", "digest": "sha256:cc"},
            "params": {},
            "input_asset_ids": ["asset-1"],
            "output_dir": "/var/lib/yaomu/staging/task-1",
        }
    )


class FlashCliGatingTests(unittest.TestCase):
    def setUp(self):
        self.adapter = FlashCliAdapter()

    def test_adapter_is_never_ready(self):
        self.assertFalse(self.adapter.ready)
        description = dict(self.adapter.describe())
        self.assertEqual(description["id"], "flashcli")
        self.assertFalse(description["ready"])
        self.assertEqual(description["reason"], MODEL_RUNTIME_UNAVAILABLE)

    def test_execute_is_gated(self):
        with self.assertRaises(CapabilityUnavailable) as raised:
            self.adapter.execute(_execute_request())
        self.assertEqual(raised.exception.code, MODEL_RUNTIME_UNAVAILABLE)
        self.assertFalse(raised.exception.retryable)
        self.assertIn("open_model_setup", raised.exception.action_ids)

    def test_cancel_is_gated(self):
        with self.assertRaises(CapabilityUnavailable) as raised:
            self.adapter.cancel(CancelRequest(task_id="t-1", attempt=1))
        self.assertEqual(raised.exception.code, MODEL_RUNTIME_UNAVAILABLE)

    def test_probe_is_gated(self):
        with self.assertRaises(CapabilityUnavailable) as raised:
            self.adapter.probe(ProbeRequest())
        self.assertEqual(raised.exception.code, MODEL_RUNTIME_UNAVAILABLE)


class FlashCliSourceGuardTests(unittest.TestCase):
    """硬约束 #5：不得生成带猜测参数的 FlashCLI 真实执行入口。"""

    def setUp(self):
        self.source = (
            Path(_helpers.SRC) / "yaomustudio_runner" / "flashcli.py"
        ).read_text(encoding="utf-8")

    def test_no_process_launching_api(self):
        for needle in ("subprocess", "Popen", "os.system", "exec(", "argv"):
            self.assertNotIn(needle, self.source, f"flashcli.py 含进程启动痕迹: {needle}")

    def test_no_cli_flags(self):
        flags = re.findall(r"(?<!-)--[A-Za-z][\w-]*", self.source)
        self.assertEqual(flags, [])


# 两槽 MVP 已确认白名单（用户 2026-11-03）；ASR bundle README（2026-11-10）
# 追加 run 的音频/JSON/语种参数；仅允许出现在 flashcli_runtime.py。
_ADMITTED_FLASHCLI_FLAGS = frozenset(
    {
        "--input",
        "--output_dir",
        "--bgm_name",
        "--vocals_name",
        "--vocals_sample",
        "--host",
        "--port",
        "--audio",
        "--output",
        "--language",
        "--chunk-seconds",
    }
)


# mlstools 字幕翻译已确认入口（2026-11-05）；仅允许出现在 translate.py。
_ADMITTED_MLSTOOLS_FLAGS = frozenset({"--tool", "--input"})


class NoGuessedFlagsAcrossRunnerTests(unittest.TestCase):
    def test_src_contains_no_guessed_cli_flag_strings(self):
        pattern = re.compile(r"(?<!-)--[A-Za-z][\w-]*")
        package = Path(_helpers.SRC) / "yaomustudio_runner"
        offenders = {}
        for path in sorted(package.glob("*.py")):
            matches = pattern.findall(path.read_text(encoding="utf-8"))
            if not matches:
                continue
            if path.name == "flashcli_runtime.py":
                unknown = sorted(set(matches) - _ADMITTED_FLASHCLI_FLAGS)
                if unknown:
                    offenders[path.name] = unknown
                continue
            if path.name == "translate.py":
                unknown = sorted(set(matches) - _ADMITTED_MLSTOOLS_FLAGS)
                if unknown:
                    offenders[path.name] = unknown
                continue
            offenders[path.name] = matches
        self.assertEqual(offenders, {}, f"src 中出现未准入 CLI 参数: {offenders}")


if __name__ == "__main__":
    unittest.main()
