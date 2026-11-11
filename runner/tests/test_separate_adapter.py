"""分离 Adapter：mock 成功/失败/逃逸/取消/设备环境；flashcli 白名单。"""

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
    CAPABILITY_FAILED,
    CAPABILITY_INVALID_PARAMS,
    CAPABILITY_UNAVAILABLE,
)
from yaomustudio_runner.main import RunnerService
from yaomustudio_runner.protocol import (
    ComponentRef,
    ExecutionRequest,
    decode_frame,
    encode_frame,
    make_frame,
)
from yaomustudio_runner.separate import SeparateAdapter

MEDIA_STATUS = {
    "ffmpeg_available": True,
    "ffprobe_available": True,
    "ffmpeg_source": "env",
    "ffprobe_source": "env",
    "error": None,
}


def _execution_request(output_dir: str, params: dict, **overrides) -> ExecutionRequest:
    payload = {
        "workspace": "ws-1",
        "server_epoch": "epoch-1",
        "task_id": "task-sep",
        "step_id": "03_separate",
        "attempt": 1,
        "model": ComponentRef("sep-model", "sha256:m"),
        "adapter": ComponentRef("separate", "sha256:a"),
        "runtime": ComponentRef("rt", "sha256:r"),
        "params": params,
        "input_asset_ids": ("asset-1",),
        "output_dir": output_dir,
    }
    payload.update(overrides)
    return ExecutionRequest(**payload)


def _encode(op, req_id, payload=None):
    return encode_frame(make_frame(op, req_id, payload or {}))


class SeparateAdapterUnitTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.input_path = self.root / "in.wav"
        self.input_path.write_bytes(b"SOURCE")
        self.adapter = SeparateAdapter()

    def test_mock_writes_vocals_and_bgm(self):
        result = self.adapter.execute(
            _execution_request(
                str(self.output_dir),
                {
                    "executor": "mock",
                    "input": str(self.input_path),
                    "device": {"cuda_visible_devices": "0"},
                },
            )
        )
        self.assertEqual(result["executor"], "mock")
        self.assertEqual(
            [item["rel_path"] for item in result["outputs"]],
            ["vocals.wav", "bgm.wav"],
        )
        self.assertEqual(
            [item["media"]["role"] for item in result["outputs"]],
            ["vocals", "background"],
        )
        self.assertGreater(result["outputs"][0]["size_bytes"], 0)
        self.assertEqual(
            (self.output_dir / "vocals.wav").read_bytes(), b"MOCK-SEPARATE-VOCALS\n"
        )
        self.assertEqual(self.adapter.last_environ["CUDA_VISIBLE_DEVICES"], "0")

    def test_role_is_declared_for_custom_output_names(self):
        result = self.adapter.execute(
            _execution_request(
                str(self.output_dir),
                {
                    "executor": "mock",
                    "input": str(self.input_path),
                    "vocals_name": "lead.wav",
                    "bgm_name": "bed.wav",
                },
            )
        )
        self.assertEqual(
            [(item["rel_path"], item["media"]["role"]) for item in result["outputs"]],
            [("lead.wav", "vocals"), ("bed.wav", "background")],
        )

    def test_path_escape_rejected(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "mock",
                        "input": str(self.input_path),
                        "vocals_name": "../x.wav",
                    },
                )
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_INVALID_PARAMS)

    def test_flashcli_missing_bin(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {"executor": "flashcli", "input": str(self.input_path)},
                )
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_UNAVAILABLE)

    def test_flashcli_run_writes_outputs(self):
        flashcli = self.root / "flashcli"
        flashcli.write_text("#!/bin/sh\n", encoding="utf-8")
        # FLASHCLI_BIN 解析在 POSIX 上要求可执行位；Windows 不检查该位。
        flashcli.chmod(0o755)

        def _fake_run(argv, *, env, cancel_event, timeout_s=3600.0, logger=None, on_log=None):
            del env, cancel_event, timeout_s, logger, on_log
            out_dir = Path(argv[argv.index("--output_dir") + 1])
            vocals = argv[argv.index("--vocals_name") + 1]
            bgm = argv[argv.index("--bgm_name") + 1]
            (out_dir / vocals).write_bytes(b"REAL-VOCALS")
            (out_dir / bgm).write_bytes(b"REAL-BGM")

        with mock.patch.dict(os.environ, {"FLASHCLI_BIN": str(flashcli)}, clear=False):
            with mock.patch(
                "yaomustudio_runner.separate.run_flashcli", side_effect=_fake_run
            ):
                result = self.adapter.execute(
                    _execution_request(
                        str(self.output_dir),
                        {"executor": "flashcli", "input": str(self.input_path)},
                    )
                )
        self.assertEqual(result["executor"], "flashcli")
        self.assertEqual(
            (self.output_dir / "vocals.wav").read_bytes(), b"REAL-VOCALS"
        )
        self.assertEqual(
            (self.output_dir / "background.wav").read_bytes(), b"REAL-BGM"
        )
        self.assertEqual(
            [item["media"]["role"] for item in result["outputs"]],
            ["vocals", "background"],
        )
        self.assertIn("xym/melband-roformer:1.0.10", self.adapter.last_argv)

    def test_mock_fail(self):
        with self.assertRaises(Exception) as ctx:
            self.adapter.execute(
                _execution_request(
                    str(self.output_dir),
                    {
                        "executor": "mock",
                        "input": str(self.input_path),
                        "mock_fail": True,
                    },
                )
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_FAILED)

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
                        "input": str(self.input_path),
                        "mock_delay_ms": 2000,
                    },
                ),
                cancel_event=cancel,
            )
        self.assertEqual(ctx.exception.code, CAPABILITY_CANCELED)


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


class SeparateMainLoopTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.root = Path(self._temp.name)
        self.output_dir = self.root / "attempt"
        self.output_dir.mkdir()
        self.input_path = self.root / "in.wav"
        self.input_path.write_bytes(b"SOURCE")

    def _payload(self, params, **overrides):
        payload = {
            "workspace": "ws-1",
            "server_epoch": "epoch-1",
            "task_id": "task-sep",
            "step_id": "03_separate",
            "attempt": 1,
            "model": {"id": "m", "digest": "sha256:m"},
            "adapter": {"id": "separate", "digest": "sha256:a"},
            "runtime": {"id": "r", "digest": "sha256:r"},
            "params": params,
            "input_asset_ids": ["asset-1"],
            "output_dir": str(self.output_dir),
        }
        payload.update(overrides)
        return payload

    def test_execute_via_service(self):
        service = RunnerService(out=io.BytesIO(), media_tools_status=MEDIA_STATUS)
        frames = _serve_live(
            service,
            _encode(
                "execute",
                "req-1",
                self._payload({"executor": "mock", "input": str(self.input_path)}),
            ),
            lambda items: any(item.op in ("result", "error") for item in items),
        )
        self.assertEqual(len(frames), 3)
        self.assertTrue(all(frame.op in ("log", "result") for frame in frames), frames)
        terminals = [frame for frame in frames if frame.op == "result"]
        self.assertEqual(len(terminals), 1)
        self.assertEqual(len(terminals[0].payload["data"]["outputs"]), 2)
        logs = [frame for frame in frames if frame.op == "log"]
        self.assertEqual([frame.payload["line"] for frame in logs][0], "使用 mock 分离执行器")

    def test_hello_lists_separate(self):
        out = io.BytesIO()
        service = RunnerService(out=out, media_tools_status=MEDIA_STATUS)
        service.serve(io.BytesIO(_encode("hello", "h1")))
        frame = decode_frame(out.getvalue().splitlines(keepends=True)[0], direction="event")
        ids = [item["id"] for item in frame.payload["data"]["adapters"]]
        self.assertIn("separate", ids)
        self.assertIn("voice_clone", ids)


if __name__ == "__main__":
    unittest.main()
