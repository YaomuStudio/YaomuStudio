"""ASR 适配器：白名单、JSON→SRT、音频转换、失败与取消。"""

from __future__ import annotations

import json
import os
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
    CAPABILITY_UNAVAILABLE,
)
from yaomustudio_runner.extract import AsrAdapter
from yaomustudio_runner.flashcli_runtime import ASR_MODEL_REF
from yaomustudio_runner.protocol import ComponentRef, ExecutionRequest

CHINESE_RESULT = {
    "language": "Chinese",
    "text": "你好，世界。再见！",
    "time_stamps": [
        {"text": "你", "start_time": 0.0, "end_time": 0.2},
        {"text": "好", "start_time": 0.2, "end_time": 0.4},
        {"text": "，", "start_time": 0.4, "end_time": 0.4},
        {"text": "世", "start_time": 0.5, "end_time": 0.7},
        {"text": "界", "start_time": 0.7, "end_time": 0.9},
        {"text": "。", "start_time": 0.9, "end_time": 0.9},
        {"text": "再", "start_time": 1.0, "end_time": 1.2},
        {"text": "见", "start_time": 1.2, "end_time": 1.4},
        {"text": "！", "start_time": 1.4, "end_time": 1.4},
    ],
}


def _request(output_dir: str, params: dict, **overrides) -> ExecutionRequest:
    payload = {
        "workspace": "ws-1",
        "server_epoch": "epoch-1",
        "task_id": "task-asr",
        "step_id": "04_extract",
        "attempt": 1,
        "model": ComponentRef("asr-model", "sha256:m"),
        "adapter": ComponentRef("asr", "asr-flashcli-v1"),
        "runtime": ComponentRef("rt", "sha256:r"),
        "params": params,
        "input_asset_ids": ("asset-1",),
        "output_dir": output_dir,
    }
    payload.update(overrides)
    return ExecutionRequest(**payload)


class AsrAdapterTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.input_path = self.root / "in.wav"
        self.input_path.write_bytes(b"RIFF")
        self.flashcli = self.root / "flashcli"
        self.flashcli.write_text("#!/bin/sh\n", encoding="utf-8")
        self.flashcli.chmod(0o755)
        self.adapter = AsrAdapter()

    def _execute(self, params=None, **kwargs):
        merged = {"input": str(self.input_path)}
        if params:
            merged.update(params)
        return self.adapter.execute(_request(str(self.output_dir), merged), **kwargs)

    def _patch_flashcli(self, payload):
        def _fake_run(argv, *, env, cancel_event, timeout_s=3600.0, logger=None, on_log=None):
            del env, cancel_event, timeout_s, logger, on_log
            output = Path(argv[argv.index("--output") + 1])
            output.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")

        return mock.patch(
            "yaomustudio_runner.extract.run_flashcli", side_effect=_fake_run
        )

    def test_missing_flashcli_bin_is_unavailable(self):
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": ""}, clear=False):
            with self.assertRaises(Exception) as ctx:
                self._execute()
        self.assertEqual(ctx.exception.code, CAPABILITY_UNAVAILABLE)

    def test_rejects_unknown_params(self):
        with self.assertRaises(Exception) as ctx:
            self._execute({"unknown": 1})
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_rejects_non_asr_mode(self):
        with self.assertRaises(Exception) as ctx:
            self._execute({"extract": {"mode": "ocr"}})
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_rejects_unsafe_language(self):
        with self.assertRaises(Exception) as ctx:
            self._execute({"language": "zh tw"})
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_rejects_unsupported_language(self):
        # uk/is/no 不在 Qwen3-ASR 的 30 种语言内：可读失败，不静默换语言。
        with self.assertRaises(Exception) as ctx:
            self._execute({"language": "uk"})
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_wav_input_generates_single_srt(self):
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with self._patch_flashcli(CHINESE_RESULT):
                result = self._execute(
                    {
                        "language": "zh",
                        "extract": {"mode": "asr"},
                        "device": {"cuda_visible_devices": "0"},
                    }
                )

        self.assertEqual(result["executor"], "flashcli")
        self.assertEqual(result["model_ref"], ASR_MODEL_REF)
        self.assertEqual(result["detected_language"], "Chinese")
        self.assertEqual(result["segments"], 3)
        self.assertEqual([item["rel_path"] for item in result["outputs"]], ["source.srt"])
        srt = (self.output_dir / "source.srt").read_text(encoding="utf-8")
        self.assertIn("00:00:00,000 --> 00:00:00,400", srt)
        self.assertIn("你好，", srt)
        self.assertIn("世界。", srt)
        self.assertIn("再见！", srt)
        self.assertNotIn("raw_asr.json", srt)
        self.assertFalse((self.output_dir / "raw_asr.json").exists())
        self.assertFalse((self.output_dir / "asr-input.wav").exists())

        argv = list(self.adapter.last_argv or ())
        self.assertEqual(argv[:3], [os.path.realpath(str(self.flashcli)), "run", ASR_MODEL_REF])
        self.assertEqual(argv[argv.index("--language") + 1], "Chinese")
        self.assertEqual(self.adapter.last_environ["CUDA_VISIBLE_DEVICES"], "0")

    def test_invalid_json_fails_without_srt(self):
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with mock.patch(
                "yaomustudio_runner.extract.run_flashcli",
                side_effect=lambda *args, **kwargs: Path(
                    args[0][args[0].index("--output") + 1]
                ).write_text("not-json", encoding="utf-8"),
            ):
                with self.assertRaises(Exception) as ctx:
                    self._execute()
        self.assertEqual(ctx.exception.code, CAPABILITY_FAILED)
        self.assertFalse((self.output_dir / "source.srt").exists())

    def test_empty_timestamps_fails_without_srt(self):
        payload = {"language": "English", "text": "hello", "time_stamps": []}
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with self._patch_flashcli(payload):
                with self.assertRaises(Exception) as ctx:
                    self._execute()
        self.assertEqual(ctx.exception.code, CAPABILITY_FAILED)
        self.assertFalse((self.output_dir / "source.srt").exists())

    def test_non_passthrough_audio_is_converted_and_cleaned(self):
        input_path = self.root / "in.mp4"
        input_path.write_bytes(b"MP4")
        converted: list[str] = []

        class _FakeFfmpeg:
            def extract_audio(self, source, output, *, cancel_event=None, timeout_s=600.0):
                del source, cancel_event, timeout_s
                Path(output).write_bytes(b"RIFF-WAV")
                converted.append(output)

        def _fake_run(argv, *, env, cancel_event, timeout_s=3600.0, logger=None, on_log=None):
            del env, cancel_event, timeout_s, logger, on_log
            audio = argv[argv.index("--audio") + 1]
            self.assertTrue(audio.endswith("asr-input.wav"))
            output = Path(argv[argv.index("--output") + 1])
            output.write_text(json.dumps(CHINESE_RESULT, ensure_ascii=False), encoding="utf-8")

        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with mock.patch(
                "yaomustudio_runner.extract.FfmpegAdapter", return_value=_FakeFfmpeg()
            ):
                with mock.patch(
                    "yaomustudio_runner.extract.run_flashcli", side_effect=_fake_run
                ):
                    result = self.adapter.execute(
                        _request(
                            str(self.output_dir),
                            {"input": str(input_path), "language": "zh"},
                        )
                    )

        self.assertEqual(result["segments"], 3)
        self.assertTrue(converted)
        self.assertFalse(Path(converted[0]).exists())
        self.assertTrue((self.output_dir / "source.srt").exists())

    def test_heartbeat_reports_progress_without_child_output(self):
        lines: list[tuple[str, str]] = []
        started = threading.Event()
        release = threading.Event()
        errors: list[BaseException] = []

        def _fake_run(argv, *, env, cancel_event, timeout_s=3600.0, logger=None, on_log=None):
            del env, cancel_event, timeout_s, logger, on_log
            started.set()
            release.wait(5.0)
            output = Path(argv[argv.index("--output") + 1])
            output.write_text(json.dumps(CHINESE_RESULT, ensure_ascii=False), encoding="utf-8")

        def _run():
            try:
                self._execute(log=lambda stream, line: lines.append((stream, line)))
            except BaseException as exc:  # pragma: no cover - 失败时给出线程内异常
                errors.append(exc)

        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with mock.patch("yaomustudio_runner.extract.HEARTBEAT_INTERVAL_S", 0.05):
                with mock.patch(
                    "yaomustudio_runner.extract.run_flashcli", side_effect=_fake_run
                ):
                    worker = threading.Thread(target=_run)
                    worker.start()
                    self.assertTrue(started.wait(2.0))
                    time.sleep(0.18)
                    release.set()
                    worker.join(5.0)

        self.assertFalse(worker.is_alive())
        self.assertEqual(errors, [])
        messages = [line for _, line in lines]
        self.assertIn("启动 FlashCLI ASR", messages)
        self.assertTrue(
            any(line.startswith("ASR 执行中，已用时 ") for line in messages), messages
        )
        self.assertIn("ASR 完成，生成 3 条字幕", messages)
        self.assertTrue(all(stream == "runner" for stream, _ in lines))

    def test_cancel_after_flashcli_returns_canceled(self):
        def _fake_run(argv, *, env, cancel_event, timeout_s=3600.0, logger=None, on_log=None):
            del argv, env, timeout_s, logger, on_log
            cancel_event.set()

        cancel = threading.Event()
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(self.flashcli)}, clear=False):
            with mock.patch(
                "yaomustudio_runner.extract.run_flashcli", side_effect=_fake_run
            ):
                with self.assertRaises(Exception) as ctx:
                    self._execute(cancel_event=cancel)
        self.assertEqual(ctx.exception.code, CAPABILITY_CANCELED)
        self.assertFalse((self.output_dir / "source.srt").exists())


if __name__ == "__main__":
    unittest.main()
