package scheduler

import (
	"context"
	"math/bits"
	"strconv"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const (
	// taskProgressEventType 是 seq=0 的可丢弃实时进度事件（契约 TaskProgressEvent）。
	taskProgressEventType = "task.progress"
	// progressMinInterval 是同一尝试接受两次进度更新的最小间隔。
	progressMinInterval = 250 * time.Millisecond
	// progressWriteTimeout 限制单次条件更新的数据库等待；执行取消时立即放弃。
	progressWriteTimeout = 2 * time.Second
)

// progressReporter 把一次执行尝试的合法进度做节流、条件快照与可丢弃实时发布。
//
// 边界：
//   - 绑定 claim 的 task/project 与精确 Attempt ID/step/attempt_no/epoch，
//     旧尝试或终态后的回调不能写入另一尝试；
//   - 锁覆盖校验、节流、条件写与 PublishLive，并发回调不能乱序覆盖；
//   - 只有条件写 Applied=true 才推进节流标记并发布快照版本，
//     写失败不发与事实不符的反馈，也不改变模型成功条件；
//   - 不启动 goroutine、不补发 trailing 值；Close 后不再写库或发布。
type progressReporter struct {
	ctx    context.Context
	store  *store.Store
	bus    *events.Bus
	log    *logging.Logger
	taskID domain.ID
	// projectID 只用于事件信封，不写入快照。
	projectID domain.ID
	attemptID domain.ID
	stepID    string
	attemptNo int
	epoch     int64
	now       func() time.Time

	mu       sync.Mutex
	last     time.Time
	lastPct  int
	accepted bool
	closed   bool
}

func newProgressReporter(
	ctx context.Context,
	st *store.Store,
	bus *events.Bus,
	log *logging.Logger,
	cl *claim,
	now func() time.Time,
) *progressReporter {
	return &progressReporter{
		ctx:       ctx,
		store:     st,
		bus:       bus,
		log:       log,
		taskID:    cl.task.ID,
		projectID: cl.projectID,
		attemptID: cl.attempt.ID,
		stepID:    cl.attempt.StepID,
		attemptNo: cl.attempt.AttemptNo,
		epoch:     cl.attempt.Epoch,
		now:       now,
		// 认领/重试事务已把快照写为 0；用 0 作为已知起点，
		// 避免重复 0% 回调在节流窗口内反复触发 no-op 条件写。
		lastPct: 0,
	}
}

// report 接收 Runner 进度（可丢弃）；stage 不进入事件或日志。
func (p *progressReporter) report(_ string, current, total int) {
	if p == nil {
		return
	}
	pct, ok := progressPercent(current, total)
	if !ok {
		// 非法计数不伪造百分比，也不占用节流窗口。
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	now := p.now()
	// 重复/倒退不写库；首条较 0% 起点前进的反馈在无接受历史时立即生效。
	if pct <= p.lastPct {
		return
	}
	if p.accepted && now.Sub(p.last) < progressMinInterval {
		return
	}

	writeCtx := p.ctx
	if writeCtx == nil {
		writeCtx = context.Background()
	}
	writeCtx, cancel := context.WithTimeout(writeCtx, progressWriteTimeout)
	result, err := p.store.UpdateTaskProgress(writeCtx, store.ProgressUpdate{
		TaskID:    p.taskID,
		AttemptID: p.attemptID,
		StepID:    p.stepID,
		AttemptNo: p.attemptNo,
		Epoch:     p.epoch,
		Progress:  pct,
		Now:       now,
	})
	cancel()
	if err != nil {
		p.warn(err)
		return
	}
	if !result.Applied {
		// 任务/尝试已不再匹配，或快照已经领先：不是错误，也不能更新节流标记。
		return
	}
	p.last = now
	p.lastPct = pct
	p.accepted = true
	p.publish(pct, result.StateVersion)
}

// publish 发布快照已落库的 seq=0 实时进度；nil bus 安全。
func (p *progressReporter) publish(pct int, stateVersion int64) {
	if p.bus == nil {
		return
	}
	p.bus.PublishLive(domain.DomainEvent{
		SchemaVersion: 1,
		Type:          taskProgressEventType,
		ProjectID:     p.projectID,
		TaskID:        p.taskID,
		StateVersion:  stateVersion,
		Data: map[string]any{
			"step_id":      p.stepID,
			"attempt":      p.attemptNo,
			"server_epoch": strconv.FormatInt(p.epoch, 10),
			"progress":     pct,
		},
	})
}

// Close 停止后续写库与发布；与 report 共用互斥锁，返回后不会再有本尝试的反馈。
func (p *progressReporter) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func (p *progressReporter) warn(err error) {
	if p.log == nil {
		return
	}
	// 固定安全码：不记录 stage、原始计数、字幕或媒体正文。
	p.log.Warn("scheduler", "scheduler.progress_update_failed", map[string]any{
		"task_id":    string(p.taskID),
		"attempt_no": p.attemptNo,
		"error":      err.Error(),
	})
}

// progressPercent 用 128 位乘法计算 floor(current*100/total)，避免平台整数极值溢出。
func progressPercent(current, total int) (int, bool) {
	if current < 0 || total <= 0 || current > total {
		return 0, false
	}
	if current == total {
		return 100, true
	}
	hi, lo := bits.Mul64(uint64(current), 100)
	pct, _ := bits.Div64(hi, lo, uint64(total))
	return int(pct), true
}
