"""子进程监管：帧管道、超时、取消进程树、心跳与日志分流。"""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

import _helpers  # noqa: F401

from yaomustudio_runner import executor as executor_module
from yaomustudio_runner.executor import (
    ManagedProcess,
    isolated_process_kwargs,
    run_process,
    terminate_process_tree,
)
from yaomustudio_runner.protocol import decode_frame, encode_frame, result_frame
from yaomustudio_runner.windows_job import WindowsProcessJob


def _wait_for(predicate, timeout_s: float = 5.0, interval_s: float = 0.02) -> bool:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(interval_s)
    return predicate()


def _pid_alive(pid: int) -> bool:
    if os.name == "nt":
        # Windows 没有信号 0：os.kill(pid, 0) 会调用 TerminateProcess。
        # 用 OpenProcess + GetExitCodeProcess 做只读存活检查。
        import ctypes
        from ctypes import wintypes

        process_query_limited_information = 0x1000
        still_active = 259
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.OpenProcess.argtypes = [
            wintypes.DWORD,
            wintypes.BOOL,
            wintypes.DWORD,
        ]
        kernel32.OpenProcess.restype = wintypes.HANDLE
        kernel32.GetExitCodeProcess.argtypes = [
            wintypes.HANDLE,
            ctypes.POINTER(wintypes.DWORD),
        ]
        kernel32.GetExitCodeProcess.restype = wintypes.BOOL
        kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel32.CloseHandle.restype = wintypes.BOOL
        handle = kernel32.OpenProcess(
            process_query_limited_information, False, pid
        )
        if not handle:
            return False
        try:
            exit_code = wintypes.DWORD()
            if not kernel32.GetExitCodeProcess(handle, ctypes.byref(exit_code)):
                return False
            return exit_code.value == still_active
        finally:
            kernel32.CloseHandle(handle)
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _kill_if_alive(pid: int) -> None:
    if os.name == "nt":
        subprocess.run(
            ["taskkill", "/PID", str(pid), "/T", "/F"],
            shell=False,
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        return
    try:
        os.kill(pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass


def _sleep_script(seconds: int = 60) -> list[str]:
    return [sys.executable, "-c", f"import time; time.sleep({seconds})"]


def _print_frames_script(frames: list[dict]) -> str:
    return (
        "import json, sys\n"
        f"frames = {frames!r}\n"
        "for frame in frames:\n"
        "    sys.stdout.write(json.dumps(frame) + '\\n')\n"
        "sys.stdout.flush()\n"
    )


class WindowsProcessTreeContractTests(unittest.TestCase):
    class _ExitedProcess:
        pid = 123

        def poll(self):
            return 0

    def test_exited_leader_alone_is_not_tree_exit_evidence(self):
        with mock.patch.object(executor_module.os, "name", "nt"):
            self.assertFalse(terminate_process_tree(self._ExitedProcess(), term_grace_s=0.01))

    def test_media_process_starts_suspended_until_job_assignment(self):
        with (
            mock.patch.object(executor_module.os, "name", "nt"),
            mock.patch.object(
                executor_module.subprocess,
                "CREATE_NEW_PROCESS_GROUP",
                0x00000200,
                create=True,
            ),
            mock.patch.object(
                executor_module.subprocess,
                "CREATE_SUSPENDED",
                0x00000004,
                create=True,
            ),
        ):
            self.assertEqual(
                isolated_process_kwargs(suspend_windows=True)["creationflags"],
                0x00000204,
            )

    def test_job_confirms_or_terminates_live_descendants(self):
        state = {"active": 1, "terminated": False}

        def terminate_job() -> bool:
            state["terminated"] = True
            state["active"] = 0
            return True

        job = WindowsProcessJob(
            123,
            lambda: state["active"],
            terminate_job,
            lambda _handle: True,
        )
        with mock.patch.object(executor_module.os, "name", "nt"):
            self.assertTrue(
                terminate_process_tree(
                    self._ExitedProcess(),
                    windows_job=job,
                    term_grace_s=0.01,
                    kill_grace_s=0.1,
                )
            )
        self.assertTrue(state["terminated"])


class ManagedProcessGuardTests(unittest.TestCase):
    def test_argv_must_be_a_list(self):
        with self.assertRaises(TypeError):
            ManagedProcess("python3 -c pass")  # type: ignore[arg-type]

    def test_argv_validation(self):
        with self.assertRaises(ValueError):
            ManagedProcess([])
        with self.assertRaises(ValueError):
            ManagedProcess(["", "arg"])
        with self.assertRaises(ValueError):
            ManagedProcess([sys.executable, "bad\x00arg"])
        with self.assertRaises(TypeError):
            ManagedProcess([sys.executable, 3])  # type: ignore[list-item]

    def test_wait_before_start_raises(self):
        process = ManagedProcess(_sleep_script(1))
        with self.assertRaises(RuntimeError):
            process.wait(timeout=1)


class FramePipelineTests(unittest.TestCase):
    def test_valid_frames_are_decoded(self):
        expected = result_frame("r-1", request_op="hello", data={"ok": True})
        script = _print_frames_script([expected.as_dict()])
        outcome = run_process([sys.executable, "-c", script], timeout_s=10)

        self.assertTrue(outcome.exited)
        self.assertEqual(outcome.exit_code, 0)
        self.assertIsNone(outcome.protocol_violation)
        self.assertEqual(len(outcome.frames), 1)
        frame = outcome.frames[0]
        self.assertEqual(frame.op, "result")
        self.assertEqual(frame.req_id, "r-1")
        self.assertEqual(frame.payload["request_op"], "hello")

    def test_frame_without_trailing_newline_at_eof(self):
        script = (
            "import sys\n"
            "sys.stdout.write('{\"v\":1,\"op\":\"result\",\"req_id\":\"r\",\"payload\":{\"request_op\":\"hello\"}}')\n"
            "sys.stdout.flush()\n"
        )
        outcome = run_process([sys.executable, "-c", script], timeout_s=10)
        self.assertEqual(len(outcome.frames), 1)
        self.assertIsNone(outcome.protocol_violation)

    def test_non_frame_stdout_is_a_violation_and_terminates(self):
        script = "import sys, time\nsys.stdout.write('plain log line\\n')\nsys.stdout.flush()\ntime.sleep(30)\n"
        outcome = run_process(
            [sys.executable, "-c", script],
            timeout_s=10,
            grace_s=0.5,
            kill_grace_s=2.0,
        )
        self.assertIsNotNone(outcome.protocol_violation)
        self.assertTrue(outcome.exited)
        self.assertFalse(outcome.timed_out)

    def test_oversized_frame_is_a_violation(self):
        script = (
            "import sys, time\n"
            "sys.stdout.write('{\"v\":1,\"op\":\"result\",\"req_id\":\"r\",\"payload\":{\"x\":\"')\n"
            "sys.stdout.write('a' * (2 * 1024 * 1024))\n"
            "sys.stdout.write('\"}}\\n')\n"
            "sys.stdout.flush()\n"
            "time.sleep(30)\n"
        )
        outcome = run_process(
            [sys.executable, "-c", script],
            timeout_s=10,
            max_frame_bytes=64 * 1024,
            grace_s=0.5,
            kill_grace_s=2.0,
        )
        self.assertIsNotNone(outcome.protocol_violation)
        self.assertTrue(outcome.exited)

    def test_frame_buffer_keeps_terminal_frames(self):
        frames = [
            {"v": 1, "op": "progress", "req_id": "r", "payload": {"stage": "x"}}
            for _ in range(5)
        ]
        frames.append(
            {"v": 1, "op": "result", "req_id": "r", "payload": {"request_op": "hello"}}
        )
        script = _print_frames_script(frames)
        process = ManagedProcess(
            [sys.executable, "-c", script], max_buffered_frames=2
        )
        process.start()
        self.assertEqual(process.wait(timeout=10), 0)
        buffered = process.frames
        self.assertEqual(len(buffered), 2)
        self.assertTrue(any(frame.op == "result" for frame in buffered))

    def test_on_frame_callback_receives_frames_without_retaining(self):
        expected = result_frame("r-1", request_op="hello")
        script = _print_frames_script([expected.as_dict()])
        seen = []
        outcome = run_process(
            [sys.executable, "-c", script], timeout_s=10, on_frame=seen.append
        )
        self.assertEqual(len(seen), 1)
        self.assertEqual(outcome.frames, ())

    def test_round_trip_through_encoder(self):
        frame = result_frame("r-9", request_op="probe", data={"ok": True})
        self.assertEqual(decode_frame(encode_frame(frame), direction="event"), frame)


class CancelAndTimeoutTests(unittest.TestCase):
    def test_cancel_terminates_process_tree(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            pid_file = Path(temp_dir) / "grandchild.pid"
            script = (
                "import pathlib, subprocess, sys, time\n"
                "child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'])\n"
                "pathlib.Path(sys.argv[1]).write_text(str(child.pid))\n"
                "time.sleep(60)\n"
            )
            process = ManagedProcess(
                [sys.executable, "-c", script, str(pid_file)]
            )
            process.start()
            self.assertTrue(
                _wait_for(pid_file.exists), "grandchild pid file was not written"
            )
            grandchild_pid = int(pid_file.read_text())

            confirmed = process.cancel(grace_s=0.5, kill_grace_s=3.0)
            self.assertTrue(confirmed)
            self.assertIsNotNone(process.poll())
            self.assertTrue(
                _wait_for(lambda: not _pid_alive(grandchild_pid)),
                "grandchild survived cancellation",
            )

    def test_cancel_hook_runs_before_tree_termination(self):
        import threading

        hook_called = threading.Event()
        process = ManagedProcess(_sleep_script(30), cancel_hook=hook_called.set)
        process.start()
        self.assertTrue(process.cancel(grace_s=0.5, kill_grace_s=3.0))
        self.assertTrue(hook_called.is_set())
        self.assertIsNotNone(process.poll())

    @unittest.skipIf(os.name == "nt", "POSIX process groups only")
    def test_cancel_escalates_when_grandchild_ignores_term(self):
        """组长响应 TERM 后不能用它的退出代表整棵进程树已清理。"""

        with tempfile.TemporaryDirectory() as temp_dir:
            pid_file = Path(temp_dir) / "stubborn.pid"
            script = (
                "import pathlib, subprocess, sys, time\n"
                "child = subprocess.Popen([sys.executable, '-c', "
                "'import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); "
                "time.sleep(60)'])\n"
                "pathlib.Path(sys.argv[1]).write_text(str(child.pid))\n"
                "time.sleep(60)\n"
            )
            process = ManagedProcess([sys.executable, "-c", script, str(pid_file)])
            process.start()
            self.assertTrue(
                _wait_for(pid_file.exists), "grandchild pid file was not written"
            )
            grandchild_pid = int(pid_file.read_text())
            self.addCleanup(_kill_if_alive, grandchild_pid)

            confirmed = process.cancel(grace_s=0.3, kill_grace_s=3.0)
            self.assertTrue(confirmed)
            self.assertTrue(
                _wait_for(lambda: not _pid_alive(grandchild_pid)),
                "cancel 在仍有子进程存活时报告了已确认退出",
            )

    def test_timeout_cancels_and_reports(self):
        outcome = run_process(
            _sleep_script(30), timeout_s=0.3, grace_s=0.3, kill_grace_s=3.0
        )
        self.assertTrue(outcome.timed_out)
        self.assertTrue(outcome.canceled)
        self.assertTrue(outcome.cancel_confirmed)
        self.assertTrue(outcome.exited)
        self.assertIsNotNone(outcome.exit_code)

    def test_nonzero_exit_is_reported(self):
        script = "import sys; sys.exit(7)"
        outcome = run_process([sys.executable, "-c", script], timeout_s=10)
        self.assertEqual(outcome.exit_code, 7)
        self.assertFalse(outcome.timed_out)


class HeartbeatAndLoggingTests(unittest.TestCase):
    def test_heartbeat_callback_fires(self):
        beats = []
        process = ManagedProcess(
            _sleep_script(2), heartbeat_interval_s=0.05, on_heartbeat=beats.append
        )
        process.start()
        pid = process.pid
        self.assertEqual(process.wait(timeout=10), 0)
        self.assertGreaterEqual(len(beats), 1)
        self.assertEqual(beats[0].pid, pid)
        self.assertGreaterEqual(beats[0].uptime_ms, 0)

    def test_stderr_content_is_never_written_to_logs(self):
        marker = (
            "虚构字幕：这是不该落盘的一句台词 "
            "FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/ref.wav CUDA_VISIBLE_DEVICES=0"
        )
        script = (
            "import sys\n"
            f"sys.stderr.write({marker!r} + '\\n')\n"
            "sys.stderr.flush()\n"
        )
        with self.assertLogs("yaomustudio_runner.executor", level="WARNING") as captured:
            outcome = run_process([sys.executable, "-c", script], timeout_s=10)
        self.assertEqual(outcome.frames, ())
        self.assertEqual(outcome.stderr_lines, 1)
        joined = "\n".join(captured.output)
        self.assertNotIn("虚构字幕", joined)
        self.assertNotIn("虚构密钥", joined)
        self.assertNotIn("CUDA_VISIBLE_DEVICES", joined)
        self.assertNotIn("/mnt/d/private", joined)
        self.assertIn("子进程 stderr 输出已省略（内容不记录）", joined)


class CallbackLogPrivacyTests(unittest.TestCase):
    RAW = "虚构字幕：这是不该落盘的一句台词 FLASHCLI_TOKEN=虚构密钥 /mnt/d/private"

    def _assert_no_raw(self, captured, stage):
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "/mnt/d/private"):
            self.assertNotIn(marker, joined)
        self.assertIn(stage, joined)
        return joined

    def test_cancel_hook_exception_log_omits_raw_text(self):
        def _raise() -> None:
            raise RuntimeError(self.RAW)

        process = ManagedProcess(_sleep_script(30), cancel_hook=_raise)
        process.start()
        with self.assertLogs("yaomustudio_runner.executor", level="ERROR") as captured:
            self.assertTrue(process.cancel(grace_s=0.5, kill_grace_s=3.0))
        self._assert_no_raw(captured, "取消钩子失败")

    def test_heartbeat_callback_exception_log_omits_raw_text(self):
        def _raise(_beat) -> None:
            raise RuntimeError(self.RAW)

        process = ManagedProcess(
            _sleep_script(1), heartbeat_interval_s=0.05, on_heartbeat=_raise
        )
        process.start()
        with self.assertLogs("yaomustudio_runner.executor", level="ERROR") as captured:
            self.assertEqual(process.wait(timeout=10), 0)
        self._assert_no_raw(captured, "心跳回调失败")

    def test_violation_cancel_exception_log_omits_raw_text(self):
        process = ManagedProcess(_sleep_script(1))
        with mock.patch.object(process, "cancel", side_effect=RuntimeError(self.RAW)):
            with self.assertLogs(
                "yaomustudio_runner.executor", level="ERROR"
            ) as captured:
                process._cancel_after_violation()
        self._assert_no_raw(captured, "协议违规后的进程树终止失败")

    def test_frame_callback_exception_log_omits_raw_text(self):
        def _raise(_frame) -> None:
            raise RuntimeError(self.RAW)

        expected = result_frame("r-1", request_op="hello")
        script = _print_frames_script([expected.as_dict()])
        with self.assertLogs("yaomustudio_runner.executor", level="ERROR") as captured:
            outcome = run_process(
                [sys.executable, "-c", script], timeout_s=10, on_frame=_raise
            )
        self.assertTrue(outcome.exited)
        self._assert_no_raw(captured, "帧回调失败")


if __name__ == "__main__":
    unittest.main()
