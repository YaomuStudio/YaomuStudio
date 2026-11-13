package execadapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Executor 把 Client 包装成调度层可用的执行器。
//
// 取消语义：调用方取消 ctx 时，先发送 cancel 并在超时后终止本应用拥有的
// 进程树；确认退出后才返回，确保调度层不会把资源槽交给第二个任务。
type Executor struct {
	Client       *Client
	mu           sync.Mutex
	isolationErr error
}

// Capacity covers all operations sharing this single Runner, including exports.
func (*Executor) Capacity() int { return 1 }

// IsolationErr is permanent for this Runner instance. Recovery requires a new,
// safely acquired workspace lease rather than another execute on the old stream.
func (e *Executor) IsolationErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.isolationErr
}

func (e *Executor) isolate(err error) {
	e.mu.Lock()
	e.isolationErr = err
	e.mu.Unlock()
}

// Identity is diagnostic identity, never authorization to kill a PID on restart.
func (e *Executor) Identity() string { return e.Client.Identity() }

// heartbeatTimeout 是上报心跳失联的阈值；超过只记录警告。
const heartbeatTimeout = 30 * time.Second

func (e *Executor) watchHeartbeat(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	reported := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			idle := e.Client.IdleFor()
			if idle > heartbeatTimeout {
				if !reported {
					// 心跳丢失不能证明进程已死：只上报，由 Core 继续核对执行侧状态。
					if e.Client.log != nil {
						e.Client.log.Warn("runner", "runner.heartbeat_missing", map[string]any{
							"idle_ms": idle.Milliseconds(),
						})
					}
					reported = true
				}
				continue
			}
			reported = false
		}
	}
}

// Execute 实现调度器的执行接口。
func (e *Executor) Execute(ctx context.Context, req Request, progress ProgressFunc, logf LogFunc) Result {
	if e == nil || e.Client == nil {
		return Result{TransportErr: context.Canceled}
	}
	if err := e.IsolationErr(); err != nil {
		return Result{TransportErr: err, StopUnconfirmed: e.Client.ExecutionUnconfirmed()}
	}
	if err := ctx.Err(); err != nil {
		// 取消在派发前已受理：不向执行侧发起新任务。
		return Result{TransportErr: err}
	}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 心跳失联只上报观察结果，不取消执行/不判定进程死亡。
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go e.watchHeartbeat(execCtx, stopWatch)

	done := make(chan Result, 1)
	go func() {
		done <- e.Client.Execute(execCtx, req, progress, logf)
	}()

	var result Result
	select {
	case <-ctx.Done():
		result = <-done
	case result = <-done:
	}

	validTerminal := result.TransportErr == nil && result.Binding.Matches(req)
	unsafeTerminal := result.Error != nil && result.Error.Code == "MEDIA_TOOL_STOP_UNCONFIRMED"
	if validTerminal && !unsafeTerminal {
		return result
	}
	// Any stream failure, malformed binding or unconfirmed media stop leaves the
	// execution unknown, independently of whether the caller canceled its context.
	cancelResult, cancelErr := e.Client.Cancel(context.WithoutCancel(ctx), req.TaskID, req.Attempt, req.StepID)
	if cancelErr != nil || !cancelResult.Exited {
		if err := e.Client.Kill(); err != nil {
			cancelErr = errors.Join(cancelErr, err)
		}
	}
	cause := result.TransportErr
	if cause == nil {
		cause = fmt.Errorf("runner returned an invalid or unsafe execution terminal")
	}
	e.isolate(errors.Join(cause, cancelErr))
	if e.Client.ExecutionUnconfirmed() {
		result.StopUnconfirmed = true
		result.TransportErr = errors.Join(cause, cancelErr, ErrStopUnconfirmed)
	}
	return result
}
