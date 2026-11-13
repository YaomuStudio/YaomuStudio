"""翻译 Adapter：mlstools argv、语言映射、产物校验、取消与日志边界。"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

import _helpers  # noqa: F401

from yaomustudio_runner.capability_common import (
    CAPABILITY_CANCELED,
    CAPABILITY_FAILED,
    CAPABILITY_INVALID_PARAMS,
)
from yaomustudio_runner.protocol import ComponentRef, ExecutionRequest
from yaomustudio_runner import translate as translate_module
from yaomustudio_runner.translate import (
    DEFAULT_OUTPUT_NAME,
    MODEL_RUNTIME_UNAVAILABLE,
    TARGET_LANGUAGE_MAP,
    TranslateAdapter,
)

SRT = "1\n00:00:00,000 --> 00:00:01,000\n你好\n\n"


def _execution_request(output_dir: str, params: dict) -> ExecutionRequest:
    return ExecutionRequest(
        workspace="ws-1",
        server_epoch="epoch-1",
        task_id="task-translate",
        step_id="06_translate",
        attempt=1,
        model=ComponentRef("mock-translate", "mock"),
        adapter=ComponentRef("translate", "mlstools-subtitle-v1"),
        runtime=ComponentRef("translate", "mlstools-subtitle-v1"),
        params=params,
        input_asset_ids=("rev-1",),
        output_dir=output_dir,
    )


class TranslateAdapterTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.input_path = self.root / "source.srt"
        self.input_path.write_text(SRT, encoding="utf-8")
        self.adapter = TranslateAdapter()

    def _write_fake_mlstools(self, body: str = "", *, exit_code: int = 0) -> Path:
        source = (
            "import json, pathlib, sys, time\n"
            "args = sys.argv\n"
            "payload = json.loads(args[args.index('--input') + 1])\n"
            + body
            + f"\nraise SystemExit({exit_code})\n"
        )
        if os.name == "nt":
            # Windows 不能直接执行带 shebang 的脚本；用 .cmd 转发到当前解释器，
            # 保持生产代码的 argv/MLSTOOLS_BIN 语义不变。
            script = self.root / "mlstools_fake.py"
            script.write_text(source, encoding="utf-8")
            shim = self.root / "mlstools.cmd"
            shim.write_text(
                "@echo off\r\n"
                f'"{sys.executable}" "%~dp0mlstools_fake.py" %*\r\n',
                encoding="utf-8",
                newline="",
            )
            return shim
        script = self.root / "mlstools"
        script.write_text("#!/usr/bin/env python3\n" + source, encoding="utf-8")
        os.chmod(script, 0o755)
        return script

    def _run(self, script: Path, params: dict, **kwargs):
        with mock.patch.dict(
            os.environ, {"MLSTOOLS_BIN": str(script)}, clear=False
        ):
            return self.adapter.execute(
                _execution_request(str(self.output_dir), params), **kwargs
            )

    def test_success_builds_whitelisted_argv_and_output(self):
        script = self._write_fake_mlstools(
            "pathlib.Path(payload['output_path']).write_text(" + repr(SRT) + ")"
        )
        result = self._run(script, {"subtitle_url": str(self.input_path), "language": "zh-CN"})

        self.assertEqual(result["executor"], "mlstools")
        self.assertEqual(result["target_language"], "chinese")
        self.assertEqual(result["outputs"][0]["rel_path"], DEFAULT_OUTPUT_NAME)
        argv = self.adapter.last_argv
        self.assertEqual(argv[0], str(script.resolve()))
        self.assertEqual(argv[1:5], ("--tool", "subtitle_translation", "--input", argv[4]))
        payload = json.loads(argv[4])
        self.assertEqual(payload["subtitle_url"], str(self.input_path.resolve()))
        self.assertEqual(payload["target_language"], "chinese")
        self.assertEqual(payload["output_path"], str((self.output_dir / DEFAULT_OUTPUT_NAME).resolve()))
        self.assertFalse(payload["bilingual"])
        self.assertNotIn("--offline", argv)

    def test_all_ui_codes_map_to_known_enum(self):
        self.assertEqual(TARGET_LANGUAGE_MAP["en"], "english")
        self.assertEqual(TARGET_LANGUAGE_MAP["zh-CN"], "chinese")
        self.assertEqual(TARGET_LANGUAGE_MAP["ja"], "japanese")
        self.assertNotIn("zh-TW", TARGET_LANGUAGE_MAP)

    def test_missing_bin_is_runtime_unavailable(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("MLSTOOLS_BIN", None)
            with self.assertRaises(Exception) as ctx:
                self.adapter.execute(
                    _execution_request(
                        str(self.output_dir),
                        {"subtitle_url": str(self.input_path), "language": "en"},
                    )
                )
        self.assertEqual(ctx.exception.code, MODEL_RUNTIME_UNAVAILABLE)

    def test_describe_and_probe_follow_binary_availability(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("MLSTOOLS_BIN", None)
            self.assertFalse(self.adapter.describe()["ready"])
        script = self._write_fake_mlstools()
        with mock.patch.dict(os.environ, {"MLSTOOLS_BIN": str(script)}, clear=False):
            described = self.adapter.describe()
            self.assertTrue(described["ready"])
            self.assertEqual(described["executors"], ["mlstools"])
            self.assertTrue(self.adapter.probe()["ok"])

    def test_unknown_param_rejected(self):
        script = self._write_fake_mlstools()
        with self.assertRaises(Exception) as ctx:
            self._run(
                script,
                {
                    "subtitle_url": str(self.input_path),
                    "language": "en",
                    "device_id": "cuda:0",
                },
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_traditional_chinese_rejected(self):
        script = self._write_fake_mlstools()
        with self.assertRaises(Exception) as ctx:
            self._run(script, {"subtitle_url": str(self.input_path), "language": "zh-TW"})
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_zero_timeout_rejected(self):
        script = self._write_fake_mlstools()
        with self.assertRaises(Exception) as ctx:
            self._run(
                script,
                {"subtitle_url": str(self.input_path), "language": "en", "timeout_s": 0},
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_missing_output_fails_even_with_zero_exit(self):
        script = self._write_fake_mlstools(
            "print('TASK_RESULT_INFO:{\"code\":2015,\"message\":\"翻译失败\"}')"
        )
        with self.assertRaises(Exception) as ctx:
            self._run(script, {"subtitle_url": str(self.input_path), "language": "en"})
        self.assertEqual(ctx.exception.code, CAPABILITY_FAILED)
        self.assertIn("code=2015", str(ctx.exception))

    def test_nonzero_exit_fails(self):
        script = self._write_fake_mlstools(exit_code=2)
        with self.assertRaises(Exception) as ctx:
            self._run(script, {"subtitle_url": str(self.input_path), "language": "en"})
        self.assertEqual(ctx.exception.code, CAPABILITY_FAILED)
        self.assertIn("退出码 2", str(ctx.exception))

    def test_cancel_during_execution(self):
        script = self._write_fake_mlstools("time.sleep(30)")
        cancel = threading.Event()

        def _cancel_soon():
            time.sleep(0.1)
            cancel.set()
            self.adapter.cancel()

        threading.Thread(target=_cancel_soon, daemon=True).start()
        with self.assertRaises(Exception) as ctx:
            self._run(
                script,
                {"subtitle_url": str(self.input_path), "language": "en"},
                cancel_event=cancel,
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_CANCELED)

    def test_subtitle_text_never_logged(self):
        script = self._write_fake_mlstools(
            "print('analyzer text: TOP-SECRET-SUBTITLE')\n"
            "import sys\n"
            "print('stderr TOP-SECRET-SUBTITLE', file=sys.stderr)\n"
            "pathlib.Path(payload['output_path']).write_text(" + repr(SRT) + ")"
        )
        with self.assertLogs("yaomustudio_runner.translate", level="INFO") as captured:
            self._run(script, {"subtitle_url": str(self.input_path), "language": "en"})
        joined = "\n".join(captured.output)
        self.assertNotIn("TOP-SECRET-SUBTITLE", joined)
        self.assertNotIn("你好", joined)

    def test_heartbeat_reports_progress_without_child_output(self):
        script = self._write_fake_mlstools(
            "print('TOP-SECRET-SUBTITLE')\n"
            "time.sleep(0.3)\n"
            "pathlib.Path(payload['output_path']).write_text(" + repr(SRT) + ")"
        )
        lines: list[tuple[str, str]] = []
        with mock.patch.object(translate_module, "HEARTBEAT_INTERVAL_S", 0.05):
            self._run(
                script,
                {"subtitle_url": str(self.input_path), "language": "en"},
                log=lambda stream, line: lines.append((stream, line)),
            )
        texts = [line for _, line in lines]
        self.assertEqual(texts[0], "启动 mlstools 字幕翻译")
        self.assertEqual(texts[-1], "mlstools 字幕翻译完成")
        self.assertTrue(
            any(text.startswith("翻译执行中，已用时 ") for text in texts[1:-1]),
            texts,
        )
        self.assertTrue(all(stream == "runner" for stream, _ in lines))
        self.assertNotIn("TOP-SECRET-SUBTITLE", "\n".join(texts))
        self.assertNotIn("你好", "\n".join(texts))


if __name__ == "__main__":
    unittest.main()
