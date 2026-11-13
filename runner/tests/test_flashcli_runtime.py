"""FlashCLI 运行时：argv 白名单与受管进程。"""

from __future__ import annotations

import os
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

import _helpers  # noqa: F401

from yaomustudio_runner.capability_common import (
    CAPABILITY_INVALID_PARAMS,
    CAPABILITY_UNAVAILABLE,
)
from yaomustudio_runner.flashcli_runtime import (
    ASR_MODEL_REF,
    SEPARATE_MODEL_REF,
    VOICE_CLONE_MODEL_REF,
    ManagedFlashCliProcess,
    asr_language_name,
    build_asr_argv,
    build_separate_argv,
    build_serve_argv,
    resolve_flashcli_bin,
    run_flashcli,
)


class FlashCliRuntimeTests(unittest.TestCase):
    def test_resolve_requires_abs_executable(self):
        with self.assertRaises(Exception) as ctx:
            resolve_flashcli_bin({})
        self.assertEqual(ctx.exception.code, CAPABILITY_UNAVAILABLE)

        with tempfile.TemporaryDirectory() as tmp:
            bin_path = Path(tmp) / "flashcli"
            bin_path.write_text("#!/bin/sh\n", encoding="utf-8")
            os.chmod(bin_path, 0o755)
            resolved = resolve_flashcli_bin({"FLASHCLI_BIN": str(bin_path)})
            self.assertEqual(resolved, str(bin_path.resolve()))

    def test_separate_argv_whitelist(self):
        argv = build_separate_argv(
            "/usr/bin/flashcli",
            input_path="/in/a.wav",
            output_dir="/out",
            bgm_name="background.wav",
            vocals_name="vocals.wav",
            vocals_sample=16000,
        )
        self.assertEqual(
            argv,
            [
                "/usr/bin/flashcli",
                "run",
                SEPARATE_MODEL_REF,
                "--input",
                "/in/a.wav",
                "--output_dir",
                "/out",
                "--bgm_name",
                "background.wav",
                "--vocals_name",
                "vocals.wav",
                "--vocals_sample",
                "16000",
            ],
        )
        self.assertNotIn("--device", argv)
        self.assertNotIn("--offline", argv)

    def test_asr_argv_whitelist(self):
        argv = build_asr_argv(
            "/usr/bin/flashcli",
            audio_path="/in/a.wav",
            output_path="/out/asr.json",
            language="zh",
        )
        self.assertEqual(
            argv,
            [
                "/usr/bin/flashcli",
                "run",
                ASR_MODEL_REF,
                "--audio",
                "/in/a.wav",
                "--output",
                "/out/asr.json",
                "--chunk-seconds",
                "30",
                "--language",
                "Chinese",
            ],
        )
        self.assertNotIn("--timestamps", argv)
        self.assertNotIn("--offline", argv)

    def test_asr_language_codes_map_to_official_names(self):
        cases = {
            "zh": "Chinese",
            "zh-tw": "Chinese",
            "zh-CN": "Chinese",
            "zh-yue": "Cantonese",
            "en": "English",
            "ja": "Japanese",
            "ko": "Korean",
            "tl": "Filipino",
            "pt-br": "Portuguese",
            "es-mx": "Spanish",
            "English": "English",
        }
        for code, expected in cases.items():
            with self.subTest(code=code):
                self.assertEqual(asr_language_name(code), expected)
        self.assertIsNone(asr_language_name("und"))
        self.assertIsNone(asr_language_name("auto"))
        self.assertIsNone(asr_language_name(None))

    def test_asr_language_rejects_unsupported_code(self):
        for code in ("uk", "is", "no"):
            with self.subTest(code=code):
                with self.assertRaises(Exception) as ctx:
                    asr_language_name(code)
                self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_asr_argv_omits_auto_language(self):
        argv = build_asr_argv(
            "/usr/bin/flashcli",
            audio_path="/in/a.wav",
            output_path="/out/asr.json",
        )
        self.assertNotIn("--language", argv)
        argv = build_asr_argv(
            "/usr/bin/flashcli",
            audio_path="/in/a.wav",
            output_path="/out/asr.json",
            language="und",
        )
        self.assertNotIn("--language", argv)

    def test_serve_argv_loopback_only(self):
        argv = build_serve_argv("/usr/bin/flashcli", port=18080)
        self.assertEqual(
            argv,
            [
                "/usr/bin/flashcli",
                "serve",
                VOICE_CLONE_MODEL_REF,
                "--host",
                "127.0.0.1",
                "--port",
                "18080",
            ],
        )
        with self.assertRaises(Exception):
            build_serve_argv("/usr/bin/flashcli", host="0.0.0.0", port=1)

    def test_run_flashcli_success(self):
        cancel = threading.Event()
        managed = mock.Mock()
        managed.start.return_value = managed
        managed.wait.return_value = 0
        with mock.patch(
            "yaomustudio_runner.flashcli_runtime.ManagedFlashCliProcess",
            return_value=managed,
        ):
            run_flashcli(["flashcli", "run", "x"], env={}, cancel_event=cancel)
        managed.start.assert_called_once()
        managed.wait.assert_called_once()

    def test_run_flashcli_passes_log_callback(self):
        cancel = threading.Event()
        managed = mock.Mock()
        managed.start.return_value = managed
        managed.wait.return_value = 0
        on_log = mock.Mock()
        with mock.patch(
            "yaomustudio_runner.flashcli_runtime.ManagedFlashCliProcess",
            return_value=managed,
        ) as cls:
            run_flashcli(
                ["flashcli", "run", "x"],
                env={},
                cancel_event=cancel,
                on_log=on_log,
            )
        cls.assert_called_once_with(
            ["flashcli", "run", "x"], env={}, logger=None, on_log=on_log
        )

    def test_managed_process_never_forwards_raw_stderr(self):
        lines: list[tuple[str, str]] = []
        managed = ManagedFlashCliProcess(
            ["flashcli", "run", "x"],
            on_log=lambda stream, line: lines.append((stream, line)),
        )
        with self.assertLogs(
            "yaomustudio_runner.flashcli_runtime", level="WARNING"
        ) as captured:
            managed._emit("stderr", "虚构字幕：这是不该落盘的一句台词")
            managed._emit("stderr", "FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/ref.wav")
            managed._emit("stderr", "CUDA_VISIBLE_DEVICES=0")
        self.assertEqual(lines, [("runner", "FlashCLI 输出已省略（内容不记录）")])
        joined = "\n".join(captured.output)
        self.assertNotIn("虚构字幕", joined)
        self.assertNotIn("虚构密钥", joined)
        self.assertNotIn("CUDA_VISIBLE_DEVICES", joined)
        self.assertNotIn("/mnt/d/private", joined)


if __name__ == "__main__":
    unittest.main()
