"""stdio 主循环：握手、门控错误、非法帧、超长帧、关闭握手与 stdout 纯净。"""

from __future__ import annotations

import io
import json
import os
import stat
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

import _helpers  # noqa: F401

from yaomustudio_runner.ffmpeg import FfmpegAdapter, MediaToolPaths, MediaToolStopUnconfirmed
from yaomustudio_runner.main import RunnerService, _ActiveExecution
from yaomustudio_runner.protocol import (
    ERR_FRAME_TOO_LARGE,
    ERR_INVALID_REQUEST,
    ERR_MALFORMED_FRAME,
    ERR_UNKNOWN_OP,
    ERR_UNSUPPORTED_ADAPTER,
    MAX_FRAME_BYTES,
    PROTOCOL_VERSION,
    decode_frame,
    encode_frame,
    make_frame,
    validate_execute_payload,
)

MEDIA_STATUS = {
    "ffmpeg_available": True,
    "ffprobe_available": True,
    "ffmpeg_source": "env",
    "ffprobe_source": "env",
    "error": None,
}


def _execute_payload(**overrides):
    payload = {
        "workspace": "ws-1",
        "server_epoch": "epoch-9",
        "task_id": "task-9",
        "step_id": "step-9",
        "attempt": 4,
        "model": {"id": "model-1", "digest": "sha256:aa"},
        "adapter": {"id": "flashcli", "digest": "sha256:bb"},
        "runtime": {"id": "runtime-1", "digest": "sha256:cc"},
        "params": {},
        "input_asset_ids": ["asset-1"],
        "output_dir": "/var/lib/yaomu/staging/task-9",
    }
    payload.update(overrides)
    return payload


def _encode(op, req_id, payload=None):
    return encode_frame(make_frame(op, req_id, payload or {}))


def _serve(input_bytes):
    out = io.BytesIO()
    service = RunnerService(out=out, media_tools_status=MEDIA_STATUS)
    code = service.serve(io.BytesIO(input_bytes))
    raw = out.getvalue()
    frames = [
        decode_frame(line, direction="event")
        for line in raw.splitlines(keepends=True)
    ]
    return code, frames, raw


class HandshakeTests(unittest.TestCase):
    def test_hello_returns_runner_description(self):
        code, frames, _ = _serve(_encode("hello", "req-hello"))
        self.assertEqual(code, 0)
        self.assertEqual(len(frames), 1)
        frame = frames[0]
        self.assertEqual(frame.op, "result")
        self.assertEqual(frame.payload["request_op"], "hello")
        data = frame.payload["data"]
        self.assertEqual(data["protocol_version"], PROTOCOL_VERSION)
        self.assertTrue(data["media_tools"]["ffmpeg_available"])
        self.assertEqual(data["adapters"][0]["id"], "flashcli")
        self.assertFalse(data["adapters"][0]["ready"])

    def test_shutdown_stops_processing(self):
        payload = _encode("shutdown", "req-stop") + _encode("hello", "req-after")
        code, frames, _ = _serve(payload)
        self.assertEqual(code, 0)
        self.assertEqual(len(frames), 1)
        self.assertEqual(frames[0].payload["request_op"], "shutdown")

    def test_eof_exits_cleanly(self):
        code, frames, raw = _serve(b"")
        self.assertEqual(code, 0)
        self.assertEqual(frames, [])
        self.assertEqual(raw, b"")


class InternalErrorLogPrivacyTests(unittest.TestCase):
    RAW = "虚构字幕：这是不该落盘的一句台词 FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/out.mp4"

    def _service(self):
        out = io.BytesIO()
        return RunnerService(out=out, media_tools_status=MEDIA_STATUS), out

    def _assert_no_raw(self, captured):
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "/mnt/d/private"):
            self.assertNotIn(marker, joined)
        return joined

    def test_frame_handler_exception_log_omits_exception_text(self):
        service, out = self._service()
        with mock.patch.object(service, "_dispatch", side_effect=RuntimeError(self.RAW)):
            with self.assertLogs("yaomustudio_runner", level="ERROR") as captured:
                service._handle_line(_encode("hello", "req-err"))
        joined = self._assert_no_raw(captured)
        self.assertIn("处理帧失败", joined)
        self.assertIn("req-err", joined)
        frames = [
            decode_frame(line, direction="event")
            for line in out.getvalue().splitlines(keepends=True)
        ]
        self.assertEqual(frames[0].payload["code"], "INTERNAL_ERROR")

    def test_execute_exception_log_omits_exception_text(self):
        service, out = self._service()
        request = validate_execute_payload(_execute_payload())
        adapter = mock.Mock()
        adapter.execute.side_effect = RuntimeError(self.RAW)
        active = _ActiveExecution(request, adapter)
        with self.assertLogs("yaomustudio_runner", level="ERROR") as captured:
            service._run_execute("req-exec", active, adapter)
        joined = self._assert_no_raw(captured)
        self.assertIn("执行失败", joined)
        self.assertIn("task-9", joined)
        self.assertIn("attempt=4", joined)
        frames = [
            decode_frame(line, direction="event")
            for line in out.getvalue().splitlines(keepends=True)
        ]
        self.assertEqual(frames[0].payload["code"], "INTERNAL_ERROR")

    def test_stop_active_exception_log_omits_exception_text(self):
        service, _ = self._service()
        request = validate_execute_payload(_execute_payload())
        adapter = mock.Mock()
        adapter.cancel.side_effect = RuntimeError(self.RAW)
        active = _ActiveExecution(request, adapter)
        service._active = active
        with self.assertLogs("yaomustudio_runner", level="ERROR") as captured:
            service._stop_active()
        joined = self._assert_no_raw(captured)
        self.assertIn("终止执行进程树失败", joined)


class GatedExecutionTests(unittest.TestCase):
    def test_execute_returns_gated_error_with_binding(self):
        _, frames, _ = _serve(_encode("execute", "req-exec", _execute_payload()))
        self.assertEqual(len(frames), 1)
        error = frames[0]
        self.assertEqual(error.op, "error")
        self.assertEqual(error.payload["code"], "MODEL_RUNTIME_UNAVAILABLE")
        self.assertFalse(error.payload["retryable"])
        self.assertEqual(error.payload["action_ids"], ["open_model_setup"])
        self.assertEqual(
            error.payload["binding"],
            {
                "workspace": "ws-1",
                "server_epoch": "epoch-9",
                "task_id": "task-9",
                "step_id": "step-9",
                "attempt": 4,
            },
        )

    def test_execute_missing_epoch_is_rejected(self):
        payload = _execute_payload()
        del payload["server_epoch"]
        _, frames, _ = _serve(_encode("execute", "req-bad", payload))
        error = frames[0]
        self.assertEqual(error.payload["code"], ERR_INVALID_REQUEST)
        self.assertEqual(error.payload["binding"]["attempt"], 4)
        self.assertNotIn("server_epoch", error.payload["binding"])

    def test_unsupported_adapter_is_rejected(self):
        payload = _execute_payload(adapter={"id": "other", "digest": "sha256:dd"})
        _, frames, _ = _serve(_encode("execute", "req-other", payload))
        self.assertEqual(frames[0].payload["code"], ERR_UNSUPPORTED_ADAPTER)

    def test_probe_is_gated(self):
        _, frames, _ = _serve(_encode("probe", "req-probe"))
        self.assertEqual(frames[0].payload["code"], "MODEL_RUNTIME_UNAVAILABLE")

    def test_cancel_reports_no_active_execution(self):
        payload = {"task_id": "task-9", "attempt": 4}
        _, frames, _ = _serve(_encode("cancel", "req-cancel", payload))
        result = frames[0]
        self.assertEqual(result.op, "result")
        self.assertEqual(result.payload["request_op"], "cancel")
        self.assertFalse(result.payload["data"]["active"])
        self.assertTrue(result.payload["data"]["exited"])

    def test_cancel_missing_attempt_is_rejected(self):
        _, frames, _ = _serve(_encode("cancel", "req-c", {"task_id": "task-9"}))
        self.assertEqual(frames[0].payload["code"], ERR_INVALID_REQUEST)


class _FakeAsrAdapter:
    adapter_id = "asr"

    def describe(self):
        return {"id": self.adapter_id, "ready": True}

    def probe(self, params=None):
        del params
        return {"ok": True}

    def execute(self, request, *, cancel_event=None, log=None):
        del cancel_event, log
        path = Path(request.output_dir) / "source.srt"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("1\n00:00:00,000 --> 00:00:01,000\nhello\n", encoding="utf-8")
        return {
            "outputs": [{"rel_path": "source.srt", "size_bytes": path.stat().st_size}],
            "executor": "fake",
        }


class AsrRoutingTests(unittest.TestCase):
    def test_execute_routes_to_asr_adapter(self):
        with tempfile.TemporaryDirectory() as tmp:
            output_dir = Path(tmp) / "attempt-1"
            payload = _execute_payload(
                adapter={"id": "asr", "digest": "asr-flashcli-v1"},
                params={"input": "/in.wav", "language": "en"},
                output_dir=str(output_dir),
            )
            service = RunnerService(
                out=io.BytesIO(),
                asr_adapter=_FakeAsrAdapter(),
                media_tools_status=MEDIA_STATUS,
            )
            frames = _serve_live(
                service,
                _encode("execute", "req-asr", payload),
                lambda items: any(item.op in ("result", "error") for item in items),
            )
        self.assertEqual(len(frames), 1, frames)
        frame = frames[0]
        self.assertEqual(frame.op, "result", frame.payload)
        self.assertEqual(frame.payload["request_op"], "execute")
        self.assertEqual(frame.payload["data"]["outputs"][0]["rel_path"], "source.srt")


class BadFrameTests(unittest.TestCase):
    def test_malformed_json(self):
        _, frames, _ = _serve(b"{not json\n")
        self.assertEqual(len(frames), 1)
        self.assertEqual(frames[0].payload["code"], ERR_MALFORMED_FRAME)
        self.assertEqual(frames[0].req_id, "unknown")

    def test_unknown_op_keeps_req_id(self):
        raw = json.dumps({"v": 1, "op": "bogus", "req_id": "req-x"}).encode() + b"\n"
        _, frames, _ = _serve(raw)
        self.assertEqual(frames[0].payload["code"], ERR_UNKNOWN_OP)
        self.assertEqual(frames[0].req_id, "req-x")

    def test_oversized_frame_does_not_desync_stream(self):
        oversized = (
            b'{"v":1,"op":"hello","req_id":"big","payload":{"x":"'
            + b"a" * (MAX_FRAME_BYTES + 100)
            + b'"}}\n'
        )
        _, frames, _ = _serve(oversized + _encode("hello", "req-ok"))
        self.assertEqual(len(frames), 2)
        self.assertEqual(frames[0].payload["code"], ERR_FRAME_TOO_LARGE)
        self.assertEqual(frames[1].payload["request_op"], "hello")

    def test_stdout_contains_only_frames(self):
        malformed = b"plain text that is not a frame\n"
        _, frames, raw = _serve(malformed + _encode("hello", "req-ok"))
        self.assertEqual(len(frames), 2)
        for line in raw.splitlines():
            json.loads(line.decode("utf-8"))
        self.assertNotIn(b"Traceback", raw)
        self.assertNotIn(b"INFO", raw)


def _serve_with_service(input_bytes, service):
    out = service._out  # noqa: SLF001 - 读取测试注入的内存输出流
    code = service.serve(io.BytesIO(input_bytes))
    raw = out.getvalue()
    frames = [
        decode_frame(line, direction="event")
        for line in raw.splitlines(keepends=True)
    ]
    return code, frames, raw


class _LiveInput:
    """可增量写入的协议输入；EOF 前让异步执行有机会发送终帧。"""

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


def _serve_live(service, frame_bytes, done, timeout=15.0):
    """在独立线程运行服务，等待 predicate(done) 成立后再关闭输入。"""

    out = service._out  # noqa: SLF001 - 测试注入的内存输出流
    inp = _LiveInput()
    thread = threading.Thread(target=service.serve, args=(inp,), daemon=True)
    thread.start()
    inp.feed(frame_bytes)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with service._send_lock:  # noqa: SLF001 - 与写入互斥读取
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
    thread.join(timeout=10)
    with service._send_lock:  # noqa: SLF001
        raw = out.getvalue()
    frames = [decode_frame(line, direction="event") for line in raw.splitlines(keepends=True)]
    return frames


def _write_fake_ffmpeg(directory: str, body: str) -> str:
    path = Path(directory) / "fake_ffmpeg"
    path.write_text("#!/bin/sh\n" + body + "\n", encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return str(path)


# 假 ffmpeg：把最后一个参数当作输出路径并写入内容。
_WRITE_LAST_ARG = 'for last in "$@"; do :; done; printf "fake-media-data" > "$last"'


@unittest.skipIf(os.name == "nt", "fake tools use POSIX shell")
class FfmpegRoutingTests(unittest.TestCase):
    def setUp(self):
        self._temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp_dir.cleanup)
        self.temp = Path(self._temp_dir.name)
        self.output_dir = self.temp / "attempt-1"
        self.output_dir.mkdir()
        self.ffmpeg = _write_fake_ffmpeg(self._temp_dir.name, _WRITE_LAST_ARG)
        self.adapter = FfmpegAdapter(MediaToolPaths(ffmpeg=self.ffmpeg, ffprobe=None))

    def _service(self):
        return RunnerService(
            out=io.BytesIO(),
            ffmpeg_adapter=self.adapter,
            media_tools_status=MEDIA_STATUS,
        )

    def _execute_payload(self, params, adapter_id="ffmpeg", task_id="task-9", attempt=4):
        return _execute_payload(
            adapter={"id": adapter_id, "digest": "sha256:ff"},
            params=params,
            output_dir=str(self.output_dir),
            task_id=task_id,
            attempt=attempt,
        )

    def test_hello_declares_ffmpeg_adapter(self):
        _, frames, _ = _serve_with_service(_encode("hello", "req-hello"), self._service())
        adapters = frames[0].payload["data"]["adapters"]
        self.assertEqual(
            [item["id"] for item in adapters],
            ["flashcli", "ffmpeg", "separate", "voice_clone", "translate", "asr"],
        )
        self.assertTrue(adapters[1]["ready"])
        self.assertTrue(adapters[2]["ready"])
        self.assertTrue(adapters[3]["ready"])

    def test_mix_execute_returns_outputs_with_binding(self):
        input_path = str(self.temp / "voice.wav")
        payload = self._execute_payload(
            {
                "op": "mix",
                "inputs": [{"path": input_path, "volume": 1.0, "start_ms": 500}],
                "out": "out.wav",
                "audio_codec": "pcm_s16le",
            }
        )
        frames = _serve_live(
            self._service(),
            _encode("execute", "req-exec", payload),
            lambda items: any(item.op in ("result", "error") for item in items),
        )
        self.assertEqual(len(frames), 1)
        frame = frames[0]
        self.assertEqual(frame.op, "result", frame.payload)
        self.assertEqual(frame.payload["request_op"], "execute")
        output = frame.payload["data"]["outputs"][0]
        self.assertEqual(output["rel_path"], "out.wav")
        self.assertGreater(output["size_bytes"], 0)
        self.assertEqual(
            frame.payload["binding"]["task_id"], "task-9"
        )
        produced = self.output_dir / "out.wav"
        self.assertTrue(produced.exists())
        self.assertEqual(produced.read_bytes(), b"fake-media-data")

    def test_output_escape_is_rejected(self):
        payload = self._execute_payload(
            {
                "op": "mix",
                "inputs": [{"path": str(self.temp / "voice.wav")}],
                "out": "../escape.wav",
            }
        )
        frames = _serve_live(
            self._service(),
            _encode("execute", "req-exec", payload),
            lambda items: any(item.op in ("result", "error") for item in items),
        )
        self.assertEqual(frames[0].op, "error")
        self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_INVALID_PARAMS")
        self.assertFalse((self.temp / "escape.wav").exists())

    def test_absolute_output_is_rejected(self):
        payload = self._execute_payload(
            {
                "op": "mix",
                "inputs": [{"path": str(self.temp / "voice.wav")}],
                "out": "/tmp/yaomu-escape.wav",
            }
        )
        frames = _serve_live(
            self._service(),
            _encode("execute", "req-exec", payload),
            lambda items: any(item.op in ("result", "error") for item in items),
        )
        self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_INVALID_PARAMS")

    def test_unknown_param_is_rejected(self):
        payload = self._execute_payload(
            {
                "op": "mix",
                "inputs": [{"path": str(self.temp / "voice.wav")}],
                "out": "out.wav",
                "unexpected": True,
            }
        )
        frames = _serve_live(
            self._service(),
            _encode("execute", "req-exec", payload),
            lambda items: any(item.op in ("result", "error") for item in items),
        )
        self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_INVALID_PARAMS")

    def test_cancel_terminates_running_media_process(self):
        self.adapter = FfmpegAdapter(
            MediaToolPaths(
                ffmpeg=_write_fake_ffmpeg(self._temp_dir.name, "sleep 30"),
                ffprobe=None,
            )
        )
        service = self._service()
        execute = _encode(
            "execute",
            "req-exec",
            self._execute_payload(
                {
                    "op": "mix",
                    "inputs": [{"path": str(self.temp / "voice.wav")}],
                    "out": "out.wav",
                }
            ),
        )
        cancel = _encode("cancel", "req-cancel", {"task_id": "task-9", "attempt": 4})
        started = time.monotonic()
        _, frames, _ = _serve_with_service(execute + cancel, service)
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 20, "cancel must not wait for the media timeout")
        by_op = {frame.op: frame for frame in frames}
        self.assertIn("result", by_op)
        self.assertEqual(by_op["result"].payload["request_op"], "cancel")
        self.assertTrue(by_op["result"].payload["data"]["active"])
        self.assertTrue(by_op["result"].payload["data"]["exited"])
        self.assertIn("error", by_op)
        self.assertEqual(by_op["error"].payload["code"], "MEDIA_TOOL_CANCELED")

    def test_execute_without_ffmpeg_is_a_readable_error(self):
        payload = self._execute_payload(
            {"op": "mix", "inputs": [], "out": "out.wav"}
        )
        with mock.patch.dict(os.environ, {"FFMPEG_BIN": "", "FFPROBE_BIN": ""}):
            service = RunnerService(out=io.BytesIO(), media_tools_status=MEDIA_STATUS)
            _, frames, _ = _serve_with_service(
                _encode("execute", "req-exec", payload), service
            )
        self.assertEqual(frames[0].op, "error")
        self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_UNAVAILABLE")
        self.assertEqual(frames[0].payload["action_ids"], ["open_engine_setup"])

    def test_eof_terminates_running_media_process(self):
        self.adapter = FfmpegAdapter(
            MediaToolPaths(
                ffmpeg=_write_fake_ffmpeg(self._temp_dir.name, "sleep 30"),
                ffprobe=None,
            )
        )
        service = self._service()
        payload = self._execute_payload(
            {"op": "mix", "inputs": [{"path": str(self.temp / "voice.wav")}], "out": "out.wav"}
        )
        started = time.monotonic()
        # 输入在 execute 后立即 EOF：退出前必须先终止并确认媒体进程树。
        _, frames, _ = _serve_with_service(
            _encode("execute", "req-exec", payload), service
        )
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 20, "EOF must not wait for the media timeout")
        self.assertEqual(frames[0].op, "error")
        self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_CANCELED")
        self.assertIsNone(service._active)  # noqa: SLF001

    def test_thread_start_failure_releases_active_slot(self):
        service = self._service()
        payload = self._execute_payload(
            {"op": "mix", "inputs": [], "out": "out.wav"}
        )
        with mock.patch(
            "yaomustudio_runner.main.threading.Thread",
            side_effect=RuntimeError("cannot start thread"),
        ):
            _, frames, _ = _serve_with_service(
                _encode("execute", "req-exec", payload), service
            )
        self.assertEqual(frames[0].op, "error")
        self.assertEqual(frames[0].payload["code"], "INTERNAL_ERROR")
        # 未启动的线程不能留在登记表里，否则后续 execute/cancel 与 EOF 清理都会卡住。
        self.assertIsNone(service._active)  # noqa: SLF001

    def test_unsupported_adapter_id_still_rejected(self):
        payload = self._execute_payload(
            {"op": "mix", "inputs": [], "out": "out.wav"},
            adapter_id="other",
        )
        _, frames, _ = _serve_with_service(
            _encode("execute", "req-exec", payload), self._service()
        )
        self.assertEqual(frames[0].payload["code"], ERR_UNSUPPORTED_ADAPTER)

    def test_unconfirmed_stop_keeps_execution_registered(self):
        payload = self._execute_payload({"op": "mix", "inputs": [], "out": "out.wav"})
        service = self._service()
        with mock.patch.object(self.adapter, "execute", side_effect=MediaToolStopUnconfirmed("injected unknown tree")) as execute:
            with mock.patch.object(self.adapter, "cancel", return_value=False):
                frames = _serve_live(
                    service,
                    _encode("execute", "req-first", payload),
                    lambda items: any(item.req_id == "req-first" for item in items),
                )
                self.assertEqual(frames[0].payload["code"], "MEDIA_TOOL_STOP_UNCONFIRMED")
                self.assertIsNotNone(service._active)  # noqa: SLF001
                service._handle_line(_encode("execute", "req-second", payload))  # noqa: SLF001
                service._handle_line(_encode("cancel", "req-cancel", {  # noqa: SLF001
                    "task_id": "task-9", "step_id": "step-9", "attempt": 4,
                }))
        self.assertEqual(execute.call_count, 1)
        with service._send_lock:  # noqa: SLF001
            received = [decode_frame(line, direction="event") for line in service._out.getvalue().splitlines(keepends=True)]
        self.assertEqual(received[-2].payload["code"], ERR_INVALID_REQUEST)
        self.assertFalse(received[-1].payload["data"]["exited"])


class TranslateRoutingTests(unittest.TestCase):
    def setUp(self):
        self._temp = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp.cleanup)
        self.temp = Path(self._temp.name)
        self.output_dir = self.temp / "attempt-1"
        self.output_dir.mkdir()
        self.source = self.temp / "source.srt"
        self.source.write_text("1\n00:00:00,000 --> 00:00:01,000\n你好\n\n", encoding="utf-8")

    def _service(self):
        return RunnerService(out=io.BytesIO(), media_tools_status=MEDIA_STATUS)

    def _payload(self):
        return _execute_payload(
            adapter={"id": "translate", "digest": "mlstools-subtitle-v1"},
            params={"subtitle_url": str(self.source), "language": "zh-CN"},
            output_dir=str(self.output_dir),
            task_id="task-translate",
            attempt=1,
        )

    def test_hello_declares_translate_adapter(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("MLSTOOLS_BIN", None)
            _, frames, _ = _serve_with_service(
                _encode("hello", "req-hello"), self._service()
            )
        adapters = {item["id"]: item for item in frames[0].payload["data"]["adapters"]}
        self.assertIn("translate", adapters)
        self.assertFalse(adapters["translate"]["ready"])

    def test_probe_reports_missing_mlstools(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("MLSTOOLS_BIN", None)
            _, frames, _ = _serve_with_service(
                _encode("probe", "req-probe", {"adapter": {"id": "translate"}}),
                self._service(),
            )
        self.assertEqual(frames[0].payload["code"], "MODEL_RUNTIME_UNAVAILABLE")
        self.assertEqual(frames[0].payload["action_ids"], ["open_model_setup"])

    def test_execute_without_mlstools_is_readable_error(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("MLSTOOLS_BIN", None)
            frames = _serve_live(
                self._service(),
                _encode("execute", "req-translate", self._payload()),
                lambda items: any(item.op in ("result", "error") for item in items),
            )
        self.assertEqual(frames[0].op, "error")
        self.assertEqual(frames[0].payload["code"], "MODEL_RUNTIME_UNAVAILABLE")
        self.assertEqual(frames[0].payload["retryable"], False)
        self.assertEqual(frames[0].payload["action_ids"], ["open_model_setup"])
        self.assertFalse((self.output_dir / "translated.srt").exists())


class ModuleEntryTests(unittest.TestCase):
    def _run_module(self, args=None, input_bytes=b""):
        env = dict(os.environ)
        existing = env.get("PYTHONPATH")
        env["PYTHONPATH"] = (
            str(_helpers.SRC) if not existing else str(_helpers.SRC) + os.pathsep + existing
        )
        return subprocess.run(
            [sys.executable, "-m", "yaomustudio_runner", *(args or [])],
            input=input_bytes,
            capture_output=True,
            timeout=30,
            env=env,
            cwd=str(_helpers.RUNNER_ROOT),
        )

    def test_module_entry_speaks_protocol(self):
        result = self._run_module(
            input_bytes=_encode("hello", "req-1") + _encode("shutdown", "req-2")
        )
        self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8", "replace"))
        lines = result.stdout.splitlines(keepends=True)
        frames = [decode_frame(line, direction="event") for line in lines]
        self.assertEqual([frame.payload["request_op"] for frame in frames], ["hello", "shutdown"])
        self.assertNotIn(b"Traceback", result.stderr)

    def test_module_rejects_cli_arguments(self):
        result = self._run_module(args=["unexpected-flag"])
        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main()
