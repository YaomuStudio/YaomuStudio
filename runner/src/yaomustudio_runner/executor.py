"""子进程监管：启动、超时、取消（TERM -> KILL 进程树）、心跳与 stdout 帧管道。

边界（``docs/local-api-contract.md`` §6）：

- stdout 只接受协议帧；非帧输出视为协议违规并终止该进程树。
- 心跳只上报观察结果，不判断进程死亡；终态由 Core 决定。
- 取消必须确认本应用拥有的进程树退出后才返回成功；未确认时调用方不得
  释放资源槽。
- 不猜测 FlashCLI 的取消接口：需要协议级取消时由适配器注入 ``cancel_hook``，
  之后仍按 TERM -> KILL 兜底。

本模块不向 stdout 写任何内容，日志统一走 ``logging``（默认输出到 stderr）。
"""

from __future__ import annotations

import logging
import os
import signal
import subprocess
import threading
import time
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any

from .execution_log import CHILD_STDERR_OMITTED
from .protocol import (
    MAX_FRAME_BYTES,
    Frame,
    ProtocolError,
    decode_frame,
)
from .windows_job import WindowsProcessJob

LOGGER = logging.getLogger("yaomustudio_runner.executor")

DEFAULT_CANCEL_GRACE_S = 3.0
DEFAULT_KILL_GRACE_S = 5.0
MAX_STDERR_LINE_BYTES = 8192
MAX_DRAINED_LINE_BYTES = 1024 * 1024
DEFAULT_MAX_BUFFERED_FRAMES = 1024
_READER_JOIN_TIMEOUT_S = 1.0
_DROPPABLE_OPS = frozenset({"progress", "heartbeat", "log"})


@dataclass(frozen=True)
class Heartbeat:
    """一次心跳观察；不代表进程仍然健康。"""

    pid: int
    uptime_ms: int


@dataclass(frozen=True)
class ExecutionOutcome:
    argv: tuple[str, ...]
    exit_code: int | None
    timed_out: bool
    canceled: bool
    cancel_confirmed: bool
    protocol_violation: str | None
    duration_ms: int
    frames: tuple[Frame, ...]
    stderr_lines: int

    @property
    def exited(self) -> bool:
        return self.exit_code is not None


def isolated_process_kwargs(*, suspend_windows: bool = False) -> dict[str, Any]:
    """让子进程成为独立会话/进程组，取消时只影响本应用拉起的进程树。"""

    if os.name == "nt":  # pragma: no cover - Windows 执行侧由适配层另行验证
        flags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        if suspend_windows:
            flags |= getattr(subprocess, "CREATE_SUSPENDED", 0)
        return {"creationflags": flags}
    return {"start_new_session": True}


def _group_alive(pgid: int) -> bool:
    """进程组内是否仍有成员（含僵尸）；Windows 下不按进程组检查。"""

    if os.name == "nt":  # pragma: no cover - Windows 执行侧由适配层另行验证
        return False
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _signal_tree(
    proc: subprocess.Popen[bytes],
    sig: int,
    logger: logging.Logger,
    *,
    pgid: int | None = None,
) -> None:
    """向 POSIX 进程组发信号；pgid 显式传入时即使组长已退出也继续终止残留成员。"""

    pid = proc.pid
    if pgid is None:
        try:
            pgid = os.getpgid(pid)
        except ProcessLookupError:
            if proc.poll() is None:
                try:
                    proc.send_signal(sig)
                except ProcessLookupError:
                    pass
            return
    try:
        os.killpg(pgid, sig)
    except ProcessLookupError:
        return
    except PermissionError:
        logger.warning("无权向进程组发信号，退化为只终止主进程 pid=%s", pid)
        if proc.poll() is None:
            try:
                proc.send_signal(sig)
            except ProcessLookupError:
                pass


def _wait_tree_exit(
    proc: subprocess.Popen[bytes],
    pgid: int | None,
    timeout_s: float,
) -> bool:
    """等待组长退出且进程组清空；pgid 未知时只确认组长退出。"""

    deadline = time.monotonic() + timeout_s
    while True:
        leader_exited = proc.poll() is not None
        group_exited = pgid is None or not _group_alive(pgid)
        if leader_exited and group_exited:
            return True
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False
        time.sleep(min(0.02, remaining))


def _taskkill_tree(pid: int) -> bool:
    """强制终止 Windows 进程树；返回 taskkill 是否成功。"""

    try:
        result = subprocess.run(
            ["taskkill", "/PID", str(pid), "/T", "/F"],
            shell=False,
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    except OSError:
        return False
    return result.returncode == 0


def _terminate_windows_tree(
    proc: subprocess.Popen[bytes],
    *,
    kill_grace_s: float,
    logger: logging.Logger,
) -> bool:
    """Windows 取消：直接强制终止整棵进程树。

    Windows 没有 POSIX 信号语义；实测 ``taskkill /T`` 不带 ``/F`` 无法终止
    控制台子进程，而 ``Popen.terminate`` 只结束组长、会留下子进程。因此在
    取消钩子未生效后，只能一次终止整棵树。只有 taskkill 成功且组长退出时
    才返回已确认，避免调用方提前释放资源槽。
    """

    if proc.poll() is not None:
        logger.error("Windows process tree exit is unconfirmed after leader exit pid=%s", proc.pid)
        return False
    killed = _taskkill_tree(proc.pid)
    if not killed:
        logger.error("taskkill 未能终止进程树 pid=%s", proc.pid)
    try:
        proc.wait(timeout=kill_grace_s)
    except subprocess.TimeoutExpired:
        logger.error("进程树在强制终止后仍未退出 pid=%s", proc.pid)
        return False
    return killed


def terminate_process_tree(
    proc: subprocess.Popen[bytes],
    *,
    pgid: int | None = None,
    windows_job: WindowsProcessJob | None = None,
    term_grace_s: float = DEFAULT_CANCEL_GRACE_S,
    kill_grace_s: float = DEFAULT_KILL_GRACE_S,
    logger: logging.Logger | None = None,
) -> bool:
    """先 TERM 再 KILL 本应用进程树；返回是否确认整棵进程树退出。

    POSIX 下 ``pgid`` 由调用方在启动时记录（``start_new_session=True`` 时等于
    子进程 PID）；组长先退出时仍按该进程组继续确认/终止残留成员，避免把仍然
    存活的子进程当成已清理并释放资源槽。Windows 没有 POSIX 信号语义，改为
    交给 ``taskkill /T /F`` 直接终止整棵树。
    """

    log = logger or LOGGER
    if os.name == "nt":
        if windows_job is not None:
            if proc.poll() is None:
                return windows_job.terminate(kill_grace_s)
            if windows_job.confirm(term_grace_s):
                return True
            return windows_job.terminate(kill_grace_s)
        return _terminate_windows_tree(proc, kill_grace_s=kill_grace_s, logger=log)
    if pgid is None:
        try:
            pgid = os.getpgid(proc.pid)
        except ProcessLookupError:
            pgid = None
    if proc.poll() is not None and (pgid is None or not _group_alive(pgid)):
        return True
    _signal_tree(proc, signal.SIGTERM, log, pgid=pgid)
    if _wait_tree_exit(proc, pgid, term_grace_s):
        return True
    log.warning("进程树未在 %.1fs 内响应 TERM，升级为 KILL", term_grace_s)
    # Windows 已在上面返回；此处 signal.SIGKILL 只在 POSIX 上求值。
    _signal_tree(proc, signal.SIGKILL, log, pgid=pgid)
    if _wait_tree_exit(proc, pgid, kill_grace_s):
        return True
    log.error("进程树在 KILL 后仍未退出；不得释放资源槽")
    return False


class ManagedProcess:
    """受控子进程；只接受 argv 列表，绝不接受命令字符串。"""

    def __init__(
        self,
        argv: Sequence[str],
        *,
        max_frame_bytes: int = MAX_FRAME_BYTES,
        heartbeat_interval_s: float | None = None,
        on_frame: Callable[[Frame], None] | None = None,
        on_heartbeat: Callable[[Heartbeat], None] | None = None,
        cancel_hook: Callable[[], None] | None = None,
        max_buffered_frames: int = DEFAULT_MAX_BUFFERED_FRAMES,
        logger: logging.Logger | None = None,
    ) -> None:
        if isinstance(argv, (str, bytes)):
            raise TypeError("argv 必须是参数列表，不能是命令字符串")
        if not isinstance(argv, Sequence) or not argv:
            raise ValueError("argv 必须是非空参数列表")
        normalized: list[str] = []
        for part in argv:
            if not isinstance(part, str):
                raise TypeError("argv 每一项都必须是字符串")
            if "\x00" in part:
                raise ValueError("argv 不得包含 NUL 字符")
            normalized.append(part)
        if not normalized[0]:
            raise ValueError("argv[0] 不能为空")
        if max_frame_bytes < 1:
            raise ValueError("max_frame_bytes 必须为正数")
        if heartbeat_interval_s is not None and heartbeat_interval_s <= 0:
            raise ValueError("heartbeat_interval_s 必须为正数")
        if max_buffered_frames < 1:
            raise ValueError("max_buffered_frames 必须为正数")

        self._argv = tuple(normalized)
        self._max_frame_bytes = max_frame_bytes
        self._heartbeat_interval_s = heartbeat_interval_s
        self._on_frame = on_frame
        self._on_heartbeat = on_heartbeat
        self._cancel_hook = cancel_hook
        self._max_buffered_frames = max_buffered_frames
        self._logger = logger or LOGGER

        self._lock = threading.RLock()
        self._cancel_lock = threading.Lock()
        self._frames_lock = threading.Lock()
        self._frames: list[Frame] = []
        self._proc: subprocess.Popen[bytes] | None = None
        self._pgid: int | None = None
        self._started_monotonic: float | None = None
        self._exit_monotonic: float | None = None
        self._exit_recorded = False
        self._cancel_requested = False
        self._protocol_violation: str | None = None
        self._stderr_lines = 0
        self._stderr_notice_logged = False
        self._stdout_thread: threading.Thread | None = None
        self._stderr_thread: threading.Thread | None = None
        self._heartbeat_thread: threading.Thread | None = None
        self._stop_heartbeat = threading.Event()

    # -- 生命周期 ---------------------------------------------------------

    @property
    def argv(self) -> tuple[str, ...]:
        return self._argv

    @property
    def pid(self) -> int:
        proc = self._require_started()
        return proc.pid

    @property
    def protocol_violation(self) -> str | None:
        with self._lock:
            return self._protocol_violation

    @property
    def stderr_lines(self) -> int:
        with self._lock:
            return self._stderr_lines

    @property
    def frames(self) -> tuple[Frame, ...]:
        with self._frames_lock:
            return tuple(self._frames)

    @property
    def cancel_requested(self) -> bool:
        with self._lock:
            return self._cancel_requested

    def start(self) -> ManagedProcess:
        with self._lock:
            if self._proc is not None:
                raise RuntimeError("进程已经启动")
            self._proc = subprocess.Popen(
                list(self._argv),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                bufsize=0,
                close_fds=True,
                shell=False,
                **isolated_process_kwargs(),
            )
            self._started_monotonic = time.monotonic()
            proc = self._proc
            # start_new_session 使子进程成为会话/进程组组长，pgid == pid；
            # 记录后即使组长退出也能继续清理和确认残留子进程。
            self._pgid = None if os.name == "nt" else proc.pid
        self._stdout_thread = threading.Thread(
            target=self._pump_stdout,
            name=f"yaomu-stdout-{proc.pid}",
            daemon=True,
        )
        self._stderr_thread = threading.Thread(
            target=self._pump_stderr,
            name=f"yaomu-stderr-{proc.pid}",
            daemon=True,
        )
        self._stdout_thread.start()
        self._stderr_thread.start()
        if self._heartbeat_interval_s is not None and self._on_heartbeat is not None:
            self._heartbeat_thread = threading.Thread(
                target=self._heartbeat_loop,
                name=f"yaomu-heartbeat-{proc.pid}",
                daemon=True,
            )
            self._heartbeat_thread.start()
        return self

    def poll(self) -> int | None:
        with self._lock:
            proc = self._proc
        if proc is None:
            return None
        code = proc.poll()
        if code is not None:
            self._on_exit(code)
        return code

    def wait(self, timeout: float | None = None) -> int | None:
        proc = self._require_started()
        try:
            code = proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            return None
        self._on_exit(code)
        return code

    def cancel(
        self,
        *,
        grace_s: float = DEFAULT_CANCEL_GRACE_S,
        kill_grace_s: float = DEFAULT_KILL_GRACE_S,
    ) -> bool:
        """取消执行；只有确认进程树退出才返回 True。

        顺序：适配器提供的协议级取消钩子（若配置）-> 等待 -> TERM 进程树
        -> 等待 -> KILL 进程树 -> 等待。未配置钩子时直接进入 TERM 阶段。
        """

        with self._cancel_lock:
            with self._lock:
                proc = self._proc
                if proc is None:
                    return True
                self._cancel_requested = True
                already_exited = proc.poll() is not None
                code = proc.returncode
                pgid = self._pgid
            if already_exited and (pgid is None or not _group_alive(pgid)):
                self._on_exit(code)
                return True

            hook = self._cancel_hook
            if hook is not None:
                try:
                    hook()
                except Exception:
                    # 异常栈可能携带模型原文；只记固定阶段，保留后续终止语义。
                    self._logger.error("取消钩子失败，继续按进程树终止")
                if self._wait_exit(grace_s):
                    self._on_exit(proc.returncode)
                    return True

            confirmed = terminate_process_tree(
                proc,
                pgid=pgid,
                term_grace_s=grace_s,
                kill_grace_s=kill_grace_s,
                logger=self._logger,
            )
            self._on_exit(proc.returncode)
            return confirmed

    def close(self) -> None:
        self._stop_heartbeat.set()
        proc = self._proc
        if proc is not None:
            if proc.poll() is None:
                self.cancel()
            for stream in (proc.stdout, proc.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except (OSError, ValueError):
                        pass
        for thread in (self._stdout_thread, self._stderr_thread, self._heartbeat_thread):
            if thread is not None and thread is not threading.current_thread():
                thread.join(timeout=_READER_JOIN_TIMEOUT_S)

    # -- 内部 -------------------------------------------------------------

    def _require_started(self) -> subprocess.Popen[bytes]:
        with self._lock:
            proc = self._proc
        if proc is None:
            raise RuntimeError("进程尚未启动")
        return proc

    def _wait_exit(self, timeout: float) -> bool:
        proc = self._require_started()
        return _wait_tree_exit(proc, self._pgid, timeout)

    def _on_exit(self, code: int | None) -> None:
        if code is None:
            return
        with self._lock:
            if self._exit_recorded:
                return
            self._exit_recorded = True
            self._exit_monotonic = time.monotonic()
        self._stop_heartbeat.set()
        for thread in (self._stdout_thread, self._stderr_thread):
            if thread is not None and thread is not threading.current_thread():
                thread.join(timeout=_READER_JOIN_TIMEOUT_S)

    def _heartbeat_loop(self) -> None:
        interval = self._heartbeat_interval_s or 0.0
        callback = self._on_heartbeat
        if callback is None:
            return
        while not self._stop_heartbeat.wait(interval):
            with self._lock:
                proc = self._proc
                started = self._started_monotonic
            if proc is None or started is None or proc.poll() is not None:
                return
            heartbeat = Heartbeat(
                pid=proc.pid,
                uptime_ms=int((time.monotonic() - started) * 1000),
            )
            try:
                callback(heartbeat)
            except Exception:
                # 只记固定阶段，不输出回调异常栈；心跳循环继续。
                self._logger.error("心跳回调失败")

    def _pump_stdout(self) -> None:
        proc = self._proc
        if proc is None or proc.stdout is None:
            return
        stream = proc.stdout
        try:
            while True:
                line = stream.readline(self._max_frame_bytes + 2)
                if not line:
                    return
                if len(line) > self._max_frame_bytes + 1:
                    self._note_protocol_violation(
                        f"stdout 帧超过 {self._max_frame_bytes} 字节上限"
                    )
                    return
                try:
                    frame = decode_frame(line, direction="event")
                except ProtocolError as exc:
                    self._note_protocol_violation(f"stdout 非协议帧: {exc.code}")
                    return
                self._dispatch_frame(frame)
        except (OSError, ValueError):
            return
        finally:
            try:
                stream.close()
            except (OSError, ValueError):
                pass

    def _pump_stderr(self) -> None:
        proc = self._proc
        if proc is None or proc.stderr is None:
            return
        stream = proc.stderr
        try:
            while True:
                line = stream.readline(MAX_STDERR_LINE_BYTES + 1)
                if not line:
                    return
                truncated = len(line) > MAX_STDERR_LINE_BYTES
                with self._lock:
                    self._stderr_lines += 1
                    first_notice = not self._stderr_notice_logged
                    self._stderr_notice_logged = True
                if truncated:
                    # 只排空超长行，不保留内容；避免阻塞子进程。
                    self._drain_stderr_line(stream)
                if first_notice:
                    # 子进程 stderr 可能含字幕/凭据/环境/路径，默认不落正文。
                    self._logger.warning(CHILD_STDERR_OMITTED)
        except (OSError, ValueError):
            return
        finally:
            try:
                stream.close()
            except (OSError, ValueError):
                pass

    @staticmethod
    def _drain_stderr_line(stream: Any) -> None:
        drained = 0
        while drained < MAX_DRAINED_LINE_BYTES:
            chunk = stream.readline(MAX_DRAINED_LINE_BYTES)
            if not chunk or chunk.endswith(b"\n"):
                return
            drained += len(chunk)

    def _note_protocol_violation(self, message: str) -> None:
        with self._lock:
            if self._protocol_violation is not None:
                return
            self._protocol_violation = message
        self._logger.error("stdout 协议违规（%s），终止该进程树", message)
        # 读取线程不能 join 自己，交给独立线程执行取消与回收。
        threading.Thread(
            target=self._cancel_after_violation,
            name="yaomu-violation-cancel",
            daemon=True,
        ).start()

    def _cancel_after_violation(self) -> None:
        try:
            self.cancel(grace_s=0.5, kill_grace_s=2.0)
        except Exception:
            # 只记固定阶段，不输出终止异常栈；隔离标记保持原语义。
            self._logger.error("协议违规后的进程树终止失败")

    def _dispatch_frame(self, frame: Frame) -> None:
        callback = self._on_frame
        if callback is not None:
            try:
                callback(frame)
            except Exception:
                # 只记固定阶段，不输出帧回调异常栈；回调失败仍被吞掉。
                self._logger.error("帧回调失败")
            return
        with self._frames_lock:
            if len(self._frames) >= self._max_buffered_frames:
                for index, existing in enumerate(self._frames):
                    if existing.op in _DROPPABLE_OPS:
                        del self._frames[index]
                        break
                else:
                    self._logger.warning("帧缓冲已满且无可丢弃帧，后续帧被丢弃")
                    return
            self._frames.append(frame)


def run_process(
    argv: Sequence[str],
    *,
    timeout_s: float | None = None,
    grace_s: float = DEFAULT_CANCEL_GRACE_S,
    kill_grace_s: float = DEFAULT_KILL_GRACE_S,
    max_frame_bytes: int = MAX_FRAME_BYTES,
    heartbeat_interval_s: float | None = None,
    on_frame: Callable[[Frame], None] | None = None,
    on_heartbeat: Callable[[Heartbeat], None] | None = None,
    cancel_hook: Callable[[], None] | None = None,
    logger: logging.Logger | None = None,
) -> ExecutionOutcome:
    """启动并等待一个受控进程，超时则取消并确认进程树状态。"""

    managed = ManagedProcess(
        argv,
        max_frame_bytes=max_frame_bytes,
        heartbeat_interval_s=heartbeat_interval_s,
        on_frame=on_frame,
        on_heartbeat=on_heartbeat,
        cancel_hook=cancel_hook,
        logger=logger,
    )
    started = time.monotonic()
    managed.start()
    timed_out = False
    canceled = False
    cancel_confirmed = False
    exit_code = managed.wait(timeout=timeout_s)
    if exit_code is None:
        timed_out = True
        canceled = True
        cancel_confirmed = managed.cancel(grace_s=grace_s, kill_grace_s=kill_grace_s)
        if cancel_confirmed and managed.poll() is None:
            managed.wait(timeout=kill_grace_s)
        exit_code = managed.poll()
    duration_ms = int((time.monotonic() - started) * 1000)
    return ExecutionOutcome(
        argv=managed.argv,
        exit_code=exit_code,
        timed_out=timed_out,
        canceled=canceled,
        cancel_confirmed=cancel_confirmed,
        protocol_violation=managed.protocol_violation,
        duration_ms=duration_ms,
        frames=managed.frames,
        stderr_lines=managed.stderr_lines,
    )
