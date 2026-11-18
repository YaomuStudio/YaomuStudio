"""音色克隆 Adapter：mock 多候选、逃逸、取消；flashcli 字段门禁。"""

from __future__ import annotations

import io
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
    CAPABILITY_INVALID_PARAMS,
)
from yaomustudio_runner.main import RunnerService
from yaomustudio_runner.protocol import (
    ComponentRef,
    ExecutionRequest,
    decode_frame,
    encode_frame,
    make_frame,
)
from yaomustudio_runner.omnivoice_serve import OmniVoiceCloneSession
from yaomustudio_runner.voice_clone import VoiceCloneAdapter

MEDIA_STATUS = {
    "ffmpeg_available": True,
    "ffprobe_available": True,
    "ffmpeg_source": "env",
    "ffprobe_source": "env",
    "error": None,
}


def _execution_request(output_dir: str, params: dict) -> ExecutionRequest:
    return ExecutionRequest(
        workspace="ws-1",
        server_epoch="epoch-1",
        task_id="task-clone",
        step_id="07_clone",
        attempt=1,
        model=ComponentRef("tts", "sha256:m"),
        adapter=ComponentRef("voice_clone", "sha256:a"),
        runtime=ComponentRef("rt", "sha256:r"),
        params=params,
        input_asset_ids=("a1",),
        output_dir=output_dir,
    )


def _encode(op, req_id, payload=None):
    return encode_frame(make_frame(op, req_id, payload or {}))


class _LiveInput:
    def __init__(self):
        self._lock = threading.Lock()
        self._buffer = b""
        self._closed = threading.Event()

    def feed(self, data: bytes) -> None:
        with self._lock:
            self._buffer += data

    def close(self) -> None:
        self._closed.set()

    def readline(self, _size: int = -1) -> bytes:
        while True:
            with self._lock:
                if self._buffer:
                    index = self._buffer.find(b"\n")
                    if index >= 0:
                        line = self._buffer[: index + 1]
                        self._buffer = self._buffer[index + 1 :]
                        return line
                elif self._closed.is_set():
                    return b""
            time.sleep(0.005)


def _serve_live(service, frame_bytes, done, timeout=5.0):
    out = service._out  # noqa: SLF001
    inp = _LiveInput()
    thread = threading.Thread(target=service.serve, args=(inp,), daemon=True)
    thread.start()
    inp.feed(frame_bytes)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with service._send_lock:  # noqa: SLF001
            raw = out.getvalue()
        frames = []
        for line in raw.splitlines(keepends=True):
            try:
                frames.append(decode_frame(line, direction="event"))
            except ValueError:
                continue
        if done(frames):
            break
        time.sleep(0.01)
    inp.close()
    thread.join(timeout=5)
    with service._send_lock:  # noqa: SLF001
        raw = out.getvalue()
    return [decode_frame(line, direction="event") for line in raw.splitlines(keepends=True)]


class VoiceCloneAdapterUnitTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.voice = self.root / "ref.wav"
        self.subtitle = self.root / "sub.srt"
        self.voice.write_bytes(b"VOICE")
        self.subtitle.write_text("1\n00:00:00,000 --> 00:00:01,000\nhi\n", encoding="utf-8")
        self.adapter = VoiceCloneAdapter()

    def test_mock_single_and_segments(self):
        result = self.adapter.execute(
            _execution_request(
                str(self.output_dir),
                {
                    "executor": "mock",
                    "voice_path": str(self.voice),
                    "subtitle_path": str(self.subtitle),
                    "language": "chinese",
                    "segments_out": "out.segments.json",
                    "device": {"cuda_visible_devices": 1},
                },
            )
        )
        paths = [item["rel_path"] for item in result["outputs"]]
        self.assertEqual(paths, ["out.wav", "out.segments.json"])
        self.assertTrue((self.output_dir / "out.wav").exists())
        self.assertIn("chinese", (self.output_dir / "out.segments.json").read_text(encoding="utf-8"))
        self.assertEqual(self.adapter.last_environ["CUDA_VISIBLE_DEVICES"], "1")

    def test_mock_multi_candidates(self):
        result = self.adapter.execute(
            _execution_request(
                str(self.output_dir),
                {
                    "executor": "mock",
                    "voice_path": str(self.voice),
                    "subtitle_path": str(self.subtitle),
                    "language": "english",
                    "candidates": 3,
                },
            )
        )
        paths = [item["rel_path"] for item in result["outputs"]]
        self.assertEqual(paths, ["out.c1.wav", "out.c2.wav", "out.c3.wav"])

    def test_mock_segment_mode(self):
        result = self.adapter.execute(
            _execution_request(
                str(self.output_dir),
                {
                    "executor": "mock",
                    "voice_path": str(self.voice),
                    "text": "你好",
                    "original_text": "Hello",
                    "start_ms": 100,
                    "end_ms": 400,
                    "language": "chinese",
                },
            )
        )
        paths = [item["rel_path"] for item in result["outputs"]]
        self.assertEqual(paths, ["out.wav"])
        self.assertTrue((self.output_dir / "out.wav").exists())
        self.assertEqual(result["segments"], 1)

    def test_segment_mode_requires_window(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "mock",
                        "voice_path": str(self.voice),
                        "text": "你好",
                        "language": "chinese",
                    },
                )
            )
        self.assertEqual(ctx.exception.code, "CAPABILITY_INVALID_PARAMS")

    def test_flashcli_segment_calls_session_with_reference_window(self):
        from unittest import mock

        captured: dict[str, object] = {}

        class FakeSession:
            def __init__(self, **kwargs):
                captured["kwargs"] = kwargs

            def run_segment(self, **kwargs):
                captured["run"] = kwargs
                Path(kwargs["output_path"]).write_bytes(b"RIFF-segment")
                return {"segments": 1, "model_ref": "tts/omnivoice_flashrt:1.0.3"}

            def close(self):
                return None

        flashcli = self.root / "flashcli"
        flashcli.write_text("#!/bin/sh\n", encoding="utf-8")
        flashcli.chmod(0o755)
        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(flashcli)}, clear=False):
            with mock.patch(
                "yaomustudio_runner.voice_clone.OmniVoiceCloneSession",
                return_value=FakeSession(),
            ):
                result = self.adapter.execute(
                    _execution_request(
                        str(self.output_dir),
                        {
                            "executor": "flashcli",
                            "voice_path": str(self.voice),
                            "text": "你好",
                            "original_text": "Hello",
                            "start_ms": 100,
                            "end_ms": 400,
                            "language": "chinese",
                        },
                    )
                )
        self.assertEqual(captured["run"]["start_ms"], 100)
        self.assertEqual(captured["run"]["end_ms"], 400)
        self.assertEqual(captured["run"]["text"], "你好")
        self.assertEqual(captured["run"]["original_text"], "Hello")
        self.assertEqual(result["segments"], 1)
        self.assertTrue((self.output_dir / "out.wav").exists())

    def test_flashcli_segments_log_never_stringifies_unknown_objects(self):
        class RawSegments:
            def __str__(self):
                return "虚构字幕 FLASHCLI_TOKEN=虚构密钥 /mnt/d/private"

        original = self.root / "orig.srt"
        original.write_text(
            "1\n00:00:00,000 --> 00:00:01,000\n原文\n", encoding="utf-8"
        )

        def _fake_run(self_session, **kwargs):
            del self_session
            Path(kwargs["output_path"]).write_bytes(b"RIFF-fake-wav")
            return {
                "segments": RawSegments(),
                "model_ref": "tts/omnivoice_flashrt:1.0.3",
            }

        with mock.patch.object(
            OmniVoiceCloneSession, "run", autospec=True, side_effect=_fake_run
        ):
            with self.assertLogs(
                "yaomustudio_runner.voice_clone", level="INFO"
            ) as captured:
                self.adapter.execute(
                    _execution_request(
                        str(self.output_dir),
                        {
                            "executor": "flashcli",
                            "voice_url": str(self.voice),
                            "subtitle_url": str(self.subtitle),
                            "original_subtitle_url": str(original),
                            "output_path": "dub.wav",
                            "language": "chinese",
                        },
                    )
                )
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "/mnt/d/private"):
            self.assertNotIn(marker, joined)
        self.assertIn("segments=unknown", joined)

    def test_escape_rejected(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "mock",
                        "voice_path": str(self.voice),
                        "subtitle_path": str(self.subtitle),
                        "language": "chinese",
                        "out": "../escape.mp3",
                    },
                )
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)
        self.assertFalse((self.root / "escape.mp3").exists())

    def test_flashcli_requires_original_subtitle(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "flashcli",
                        "voice_path": str(self.voice),
                        "subtitle_path": str(self.subtitle),
                        "language": "chinese",
                    },
                )
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_flashcli_readme_aliases(self):
        original = self.root / "orig.srt"
        original.write_text(
            "1\n00:00:00,000 --> 00:00:01,000\n原文\n", encoding="utf-8"
        )

        def _fake_run(self_session, **kwargs):
            del self_session
            Path(kwargs["output_path"]).write_bytes(b"RIFF-fake-wav")
            return {
                "segments": 1,
                "model_ref": "tts/omnivoice_flashrt:1.0.3",
                "sample_format": "wav",
            }

        with mock.patch.object(OmniVoiceCloneSession, "run", autospec=True, side_effect=_fake_run):
            result = self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "flashcli",
                        "voice_url": str(self.voice),
                        "subtitle_url": str(self.subtitle),
                        "original_subtitle_url": str(original),
                        "output_path": "dub.wav",
                        "language": "chinese",
                    },
                )
            )
        self.assertEqual(result["executor"], "flashcli")
        self.assertEqual(result["outputs"][0]["rel_path"], "dub.wav")
        self.assertTrue((self.output_dir / "dub.wav").exists())

    def test_cancel_during_delay(self):
        cancel = threading.Event()

        def _cancel_soon():
            time.sleep(0.05)
            cancel.set()
            self.adapter.cancel()

        threading.Thread(target=_cancel_soon, daemon=True).start()
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "mock",
                        "voice_path": str(self.voice),
                        "subtitle_path": str(self.subtitle),
                        "language": "chinese",
                        "mock_delay_ms": 2000,
                    },
                ),
                cancel_event=cancel,
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_CANCELED)


class VoiceCloneMainLoopTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.voice = self.root / "ref.wav"
        self.subtitle = self.root / "sub.srt"
        self.voice.write_bytes(b"VOICE")
        self.subtitle.write_text("1\n00:00:00,000 --> 00:00:01,000\nhi\n", encoding="utf-8")

    def test_execute_and_cancel_via_protocol(self):
        service = RunnerService(out=io.BytesIO(), media_tools_status=MEDIA_STATUS)
        payload = {
            "workspace": "ws-1",
            "server_epoch": "epoch-1",
            "task_id": "task-clone",
            "step_id": "07_clone",
            "attempt": 2,
            "model": {"id": "m", "digest": "sha256:m"},
            "adapter": {"id": "voice_clone", "digest": "sha256:a"},
            "runtime": {"id": "r", "digest": "sha256:r"},
            "params": {
                "executor": "mock",
                "voice_path": str(self.voice),
                "subtitle_path": str(self.subtitle),
                "language": "chinese",
                "mock_delay_ms": 3000,
            },
            "input_asset_ids": ["a1"],
            "output_dir": str(self.output_dir),
        }
        frames = _serve_live(
            service,
            _encode("execute", "req-e", payload)
            + _encode("cancel", "req-c", {"task_id": "task-clone", "attempt": 2}),
            lambda items: any(
                item.op == "result" and item.payload.get("request_op") == "cancel"
                for item in items
            ),
        )
        codes = {frame.op: frame.payload.get("code") for frame in frames if frame.op == "error"}
        self.assertIn(CAPABILITY_CANCELED, codes.values())
        cancel = next(
            frame
            for frame in frames
            if frame.op == "result" and frame.payload.get("request_op") == "cancel"
        )
        self.assertTrue(cancel.payload["data"]["exited"])


class VoiceCloneScriptTests(unittest.TestCase):
    def test_script_invocation_writes_product_without_serve(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            voice = root / "ref.wav"
            original = root / "ref.srt"
            subtitle = root / "zh.srt"
            script = root / "voice-clone.py"
            voice.write_bytes(b"RIFFvoice")
            original.write_text("1\n00:00:00,000 --> 00:00:01,000\nHello\n", encoding="utf-8")
            subtitle.write_text("1\n00:00:00,000 --> 00:00:01,000\n你好\n", encoding="utf-8")
            script.write_text(
                "import json, sys\n"
                "from pathlib import Path\n"
                "payload = json.loads(sys.argv[1][len('--input='):])\n"
                "assert payload['language'] == 'chinese'\n"
                "assert payload['voice_url'].endswith('ref.wav')\n"
                "Path(payload['output_path']).write_bytes(b'ID3-finished')\n",
                encoding="utf-8",
            )
            output = root / "out"
            output.mkdir()
            previous = os.environ.get("YAOMU_VOICE_CLONE_SCRIPT")
            os.environ["YAOMU_VOICE_CLONE_SCRIPT"] = str(script)
            self.addCleanup(
                lambda: os.environ.pop("YAOMU_VOICE_CLONE_SCRIPT", None)
                if previous is None
                else os.environ.__setitem__("YAOMU_VOICE_CLONE_SCRIPT", previous)
            )
            with mock.patch.object(OmniVoiceCloneSession, "run", autospec=True) as run:
                result = VoiceCloneAdapter().execute(
                    _execution_request(
                        str(output),
                        {
                            "executor": "flashcli",
                            "op": "script",
                            "voice_path": str(voice),
                            "original_subtitle_path": str(original),
                            "subtitle_path": str(subtitle),
                            "language": "zh-CN",
                            "out": "out.mp3",
                        },
                    )
                )
            run.assert_not_called()
            self.assertEqual(result["outputs"][0]["rel_path"], "out.mp3")
            self.assertEqual((output / "out.mp3").read_bytes(), b"ID3-finished")

    def test_explicit_missing_script_does_not_fall_through(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            voice = root / "ref.wav"
            original = root / "ref.srt"
            subtitle = root / "zh.srt"
            for path, text in (
                (voice, "RIFFvoice"),
                (original, "1\n00:00:00,000 --> 00:00:01,000\nHello\n"),
                (subtitle, "1\n00:00:00,000 --> 00:00:01,000\n你好\n"),
            ):
                path.write_bytes(text.encode("utf-8"))
            previous = os.environ.get("YAOMU_VOICE_CLONE_SCRIPT")
            os.environ["YAOMU_VOICE_CLONE_SCRIPT"] = str(root / "missing.py")
            self.addCleanup(
                lambda: os.environ.pop("YAOMU_VOICE_CLONE_SCRIPT", None)
                if previous is None
                else os.environ.__setitem__("YAOMU_VOICE_CLONE_SCRIPT", previous)
            )
            with self.assertRaises(Exception) as caught:
                VoiceCloneAdapter().execute(
                    _execution_request(
                        str(root),
                        {
                            "executor": "flashcli",
                            "op": "script",
                            "voice_path": str(voice),
                            "original_subtitle_path": str(original),
                            "subtitle_path": str(subtitle),
                            "language": "zh-CN",
                            "out": "out.mp3",
                        },
                    )
                )
            self.assertIn("未找到 voice-clone.py", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
