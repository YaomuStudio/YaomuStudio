"""协议帧编解码、结构与绑定字段校验。"""

from __future__ import annotations

import json
import os
import unittest

import _helpers  # noqa: F401  (导入即设置 src 路径)

from yaomustudio_runner.protocol import (
    ERR_FRAME_TOO_LARGE,
    ERR_INVALID_REQUEST,
    ERR_MALFORMED_FRAME,
    ERR_UNKNOWN_OP,
    ERR_UNSUPPORTED_VERSION,
    MAX_FRAME_BYTES,
    PROTOCOL_VERSION,
    ProtocolError,
    decode_frame,
    encode_frame,
    error_frame,
    heartbeat_frame,
    log_frame,
    make_frame,
    progress_frame,
    result_frame,
    validate_cancel_payload,
    validate_execute_payload,
    validate_frame,
)


def valid_execute_payload(**overrides):
    payload = {
        "workspace": "ws-1",
        "server_epoch": "epoch-7",
        "task_id": "task-1",
        "step_id": "step-1",
        "attempt": 3,
        "model": {"id": "asr-model", "digest": "sha256:aa"},
        "adapter": {"id": "flashcli", "digest": "sha256:bb"},
        "runtime": {"id": "runtime-1", "digest": "sha256:cc"},
        "params": {"language": "zh"},
        "input_asset_ids": ["asset-1", "asset-2"],
        "output_dir": "/var/lib/yaomu/staging/task-1/step-1",
    }
    payload.update(overrides)
    return payload


class FrameRoundTripTests(unittest.TestCase):
    def test_execute_round_trip_is_single_utf8_line(self):
        payload = valid_execute_payload(params={"text": "你好", "n": 1})
        frame = make_frame("execute", "req-1", payload)
        data = encode_frame(frame)

        self.assertTrue(data.endswith(b"\n"))
        self.assertEqual(data.count(b"\n"), 1)
        self.assertIn("你好".encode("utf-8"), data)
        self.assertNotIn(b"\\u4f60", data)

        decoded = decode_frame(data, direction="request")
        self.assertEqual(decoded.v, PROTOCOL_VERSION)
        self.assertEqual(decoded.op, "execute")
        self.assertEqual(decoded.req_id, "req-1")
        self.assertEqual(dict(decoded.payload), payload)

    def test_frame_without_payload_defaults_to_empty_mapping(self):
        decoded = decode_frame('{"v":1,"op":"hello","req_id":"r-1"}', direction="request")
        self.assertEqual(dict(decoded.payload), {})

    def test_payload_is_immutable(self):
        frame = make_frame("hello", "r-1", {"a": 1})
        with self.assertRaises(TypeError):
            frame.payload["a"] = 2  # type: ignore[index]

    def test_result_and_error_frames(self):
        result = result_frame(
            "r-2",
            request_op="hello",
            data={"runner_version": "0.1.0"},
            binding={"attempt": 1},
        )
        self.assertEqual(result.op, "result")
        self.assertEqual(result.payload["request_op"], "hello")
        self.assertEqual(result.payload["status"], "ok")

        error = error_frame(
            "r-3",
            code="MODEL_RUNTIME_UNAVAILABLE",
            message="not ready",
            retryable=False,
            action_ids=["open_model_setup"],
            binding={"task_id": "t", "attempt": 2},
        )
        self.assertEqual(error.op, "error")
        self.assertEqual(error.payload["code"], "MODEL_RUNTIME_UNAVAILABLE")
        self.assertFalse(error.payload["retryable"])
        self.assertEqual(error.payload["action_ids"], ["open_model_setup"])

    def test_result_frame_rejects_non_request_op(self):
        with self.assertRaises(ValueError):
            result_frame("r", request_op="heartbeat")

    def test_make_frame_rejects_unknown_op(self):
        with self.assertRaises(ProtocolError) as raised:
            make_frame("definitely-not-an-op", "r")
        self.assertEqual(raised.exception.code, ERR_UNKNOWN_OP)


class FrameDecodeRejectionTests(unittest.TestCase):
    def assert_rejected(self, data, code):
        with self.assertRaises(ProtocolError) as raised:
            decode_frame(data)
        self.assertEqual(raised.exception.code, code, raised.exception.message)

    def test_empty_and_non_object(self):
        self.assert_rejected(b"", ERR_MALFORMED_FRAME)
        self.assert_rejected(b"[]", ERR_MALFORMED_FRAME)
        self.assert_rejected(b'"hello"', ERR_MALFORMED_FRAME)
        self.assert_rejected(b"{not json}", ERR_MALFORMED_FRAME)

    def test_invalid_utf8(self):
        self.assert_rejected(b"\xff\xfe", ERR_MALFORMED_FRAME)

    def test_multi_line_is_rejected(self):
        pretty = json.dumps({"v": 1, "op": "hello", "req_id": "r"}, indent=2)
        self.assert_rejected(pretty.encode("utf-8"), ERR_MALFORMED_FRAME)

    def test_missing_required_keys(self):
        self.assert_rejected(b'{"op":"hello","req_id":"r"}', ERR_MALFORMED_FRAME)
        self.assert_rejected(b'{"v":1,"req_id":"r"}', ERR_MALFORMED_FRAME)
        self.assert_rejected(b'{"v":1,"op":"hello"}', ERR_MALFORMED_FRAME)

    def test_wrong_version(self):
        self.assert_rejected(
            b'{"v":2,"op":"hello","req_id":"r"}', ERR_UNSUPPORTED_VERSION
        )
        self.assert_rejected(
            b'{"v":"1","op":"hello","req_id":"r"}', ERR_MALFORMED_FRAME
        )

    def test_unknown_op_and_direction(self):
        self.assert_rejected(
            b'{"v":1,"op":"nope","req_id":"r"}', ERR_UNKNOWN_OP
        )
        with self.assertRaises(ProtocolError) as raised:
            decode_frame(
                b'{"v":1,"op":"result","req_id":"r","payload":{}}',
                direction="request",
            )
        self.assertEqual(raised.exception.code, ERR_UNKNOWN_OP)
        with self.assertRaises(ProtocolError):
            decode_frame(b'{"v":1,"op":"hello","req_id":"r"}', direction="event")

    def test_bad_req_id_and_payload(self):
        self.assert_rejected(
            b'{"v":1,"op":"hello","req_id":"has space"}', ERR_INVALID_REQUEST
        )
        self.assert_rejected(
            b'{"v":1,"op":"hello","req_id":""}', ERR_INVALID_REQUEST
        )
        self.assert_rejected(
            b'{"v":1,"op":"hello","req_id":"r","payload":[]}', ERR_INVALID_REQUEST
        )

    def test_unknown_top_level_key(self):
        self.assert_rejected(
            b'{"v":1,"op":"hello","req_id":"r","extra":1}', ERR_INVALID_REQUEST
        )

    def test_oversized_frame(self):
        oversized = b'{"v":1,"op":"hello","req_id":"r","payload":{"x":"' + (
            b"a" * MAX_FRAME_BYTES
        ) + b'"}}'
        self.assert_rejected(oversized, ERR_FRAME_TOO_LARGE)

    def test_encode_enforces_size_limit(self):
        frame = make_frame("hello", "r", {"x": "a" * MAX_FRAME_BYTES})
        with self.assertRaises(ProtocolError) as raised:
            encode_frame(frame)
        self.assertEqual(raised.exception.code, ERR_FRAME_TOO_LARGE)


class ExecutePayloadTests(unittest.TestCase):
    def test_valid_payload_and_binding(self):
        request = validate_execute_payload(valid_execute_payload())
        self.assertEqual(request.attempt, 3)
        self.assertEqual(request.server_epoch, "epoch-7")
        self.assertEqual(request.model.digest, "sha256:aa")
        self.assertEqual(request.input_asset_ids, ("asset-1", "asset-2"))
        self.assertEqual(
            request.output_dir,
            os.path.normpath("/var/lib/yaomu/staging/task-1/step-1"),
        )
        self.assertEqual(
            request.binding(),
            {
                "workspace": "ws-1",
                "server_epoch": "epoch-7",
                "task_id": "task-1",
                "step_id": "step-1",
                "attempt": 3,
            },
        )

    def test_each_required_key_is_enforced(self):
        required = (
            "workspace",
            "server_epoch",
            "task_id",
            "step_id",
            "attempt",
            "model",
            "adapter",
            "runtime",
            "params",
            "input_asset_ids",
            "output_dir",
        )
        for key in required:
            with self.subTest(key=key):
                payload = valid_execute_payload()
                del payload[key]
                with self.assertRaises(ProtocolError) as raised:
                    validate_execute_payload(payload)
                self.assertEqual(raised.exception.code, ERR_INVALID_REQUEST)

    def test_attempt_must_be_monotonic_positive_int(self):
        for attempt in (0, -1, True, "1", 1.5):
            with self.subTest(attempt=attempt):
                with self.assertRaises(ProtocolError):
                    validate_execute_payload(valid_execute_payload(attempt=attempt))

    def test_server_epoch_accepts_integer(self):
        request = validate_execute_payload(valid_execute_payload(server_epoch=7))
        self.assertEqual(request.server_epoch, "7")

    def test_component_refs_require_id_and_digest(self):
        with self.assertRaises(ProtocolError):
            validate_execute_payload(
                valid_execute_payload(model={"id": "m"})
            )
        with self.assertRaises(ProtocolError):
            validate_execute_payload(
                valid_execute_payload(runtime={"id": "r", "digest": "d", "extra": 1})
            )

    def test_output_dir_must_be_absolute_and_not_escape(self):
        for bad in ("relative/out", "/var/tmp/../etc", "/", ""):
            with self.subTest(output_dir=bad):
                with self.assertRaises(ProtocolError):
                    validate_execute_payload(valid_execute_payload(output_dir=bad))

    def test_input_asset_ids_validation(self):
        with self.assertRaises(ProtocolError):
            validate_execute_payload(valid_execute_payload(input_asset_ids="asset-1"))
        with self.assertRaises(ProtocolError):
            validate_execute_payload(
                valid_execute_payload(input_asset_ids=["asset-1", "asset-1"])
            )
        with self.assertRaises(ProtocolError):
            validate_execute_payload(valid_execute_payload(input_asset_ids=["ok", 3]))
        # TTS 等无输入资产的任务允许空数组，但键必须存在。
        request = validate_execute_payload(valid_execute_payload(input_asset_ids=[]))
        self.assertEqual(request.input_asset_ids, ())

    def test_params_and_unknown_keys(self):
        with self.assertRaises(ProtocolError):
            validate_execute_payload(valid_execute_payload(params=[]))
        with self.assertRaises(ProtocolError):
            validate_execute_payload(valid_execute_payload(unexpected="x"))

    def test_workspace_and_ids_reject_whitespace(self):
        with self.assertRaises(ProtocolError):
            validate_execute_payload(valid_execute_payload(task_id="task 1"))


class CancelPayloadTests(unittest.TestCase):
    def test_valid_cancel(self):
        request = validate_cancel_payload({"task_id": "t-1", "attempt": 2})
        self.assertEqual(request.binding(), {"task_id": "t-1", "attempt": 2})
        request = validate_cancel_payload(
            {"task_id": "t-1", "attempt": 2, "step_id": "s-1"}
        )
        self.assertEqual(request.binding()["step_id"], "s-1")

    def test_cancel_requires_task_and_attempt(self):
        for payload in (
            {"attempt": 1},
            {"task_id": "t"},
            {"task_id": "t", "attempt": 0},
            {"task_id": "t", "attempt": 1, "extra": True},
        ):
            with self.subTest(payload=payload):
                with self.assertRaises(ProtocolError):
                    validate_cancel_payload(payload)


class HeartbeatAndProgressTests(unittest.TestCase):
    def test_heartbeat_frame(self):
        frame = heartbeat_frame(
            "r-hb",
            server_epoch="epoch-1",
            task_id="task-1",
            step_id="step-1",
            attempt=1,
            pid=1234,
            uptime_ms=500,
        )
        self.assertEqual(frame.op, "heartbeat")
        self.assertEqual(frame.payload["pid"], 1234)
        self.assertEqual(frame.payload["attempt"], 1)

    def test_heartbeat_rejects_bad_attempt(self):
        with self.assertRaises(ProtocolError):
            heartbeat_frame(
                "r",
                server_epoch="e",
                task_id="t",
                step_id=None,
                attempt=0,
                pid=1,
                uptime_ms=0,
            )

    def test_progress_frame(self):
        frame = progress_frame(
            "r-p",
            server_epoch="e",
            task_id="t",
            step_id="s",
            attempt=1,
            stage="decode",
            current=3,
            total=10,
        )
        self.assertEqual(frame.payload["stage"], "decode")
        self.assertEqual(frame.payload["total"], 10)

    def test_progress_rejects_inconsistent_counts(self):
        with self.assertRaises(ProtocolError):
            progress_frame(
                "r",
                server_epoch="e",
                task_id="t",
                step_id="s",
                attempt=1,
                stage="decode",
                current=11,
                total=10,
            )
        with self.assertRaises(ProtocolError):
            progress_frame(
                "r",
                server_epoch="e",
                task_id="t",
                step_id="s",
                attempt=1,
                stage="decode",
                total=10,
            )
        with self.assertRaises(ProtocolError):
            progress_frame(
                "r",
                server_epoch="e",
                task_id="t",
                step_id="s",
                attempt=1,
                stage="has space",
            )


class LogFrameTests(unittest.TestCase):
    def _frame(self, **overrides):
        values = {
            "server_epoch": "epoch-7",
            "task_id": "task-1",
            "step_id": "step-1",
            "attempt": 1,
            "stream": "stderr",
            "line": "FlashCLI 启动",
            "seq": 3,
        }
        values.update(overrides)
        return log_frame("req-1", **values)

    def test_log_round_trip_as_event(self):
        data = encode_frame(self._frame())
        frame = decode_frame(data, direction="event")
        self.assertEqual(frame.op, "log")
        self.assertEqual(frame.payload["line"], "FlashCLI 启动")
        self.assertEqual(frame.payload["seq"], 3)
        self.assertEqual(frame.payload["stream"], "stderr")

    def test_log_rejects_bad_stream(self):
        with self.assertRaises(ProtocolError):
            self._frame(stream="stdin")

    def test_log_rejects_empty_control_and_oversized_line(self):
        for line in ("", "a\nb", "a\x07b", "x" * 513):
            with self.subTest(line=line[:10]):
                with self.assertRaises(ProtocolError):
                    self._frame(line=line)

    def test_log_seq_is_optional(self):
        frame = log_frame(
            "req-1",
            server_epoch="epoch-7",
            task_id="task-1",
            step_id="step-1",
            attempt=1,
            stream="runner",
            line="启动",
        )
        self.assertNotIn("seq", frame.payload)


class ValidateFrameDirectTests(unittest.TestCase):
    def test_validate_frame_returns_frozen_frame(self):
        frame = validate_frame(
            {"v": 1, "op": "shutdown", "req_id": "r", "payload": {}}
        )
        self.assertEqual(frame.op, "shutdown")

    def test_validate_frame_rejects_bad_direction(self):
        with self.assertRaises(ValueError):
            validate_frame({"v": 1, "op": "hello", "req_id": "r"}, direction="bogus")


if __name__ == "__main__":
    unittest.main()
