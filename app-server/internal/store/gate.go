package store

// 业务准入闸门：普通请求处理与普通业务事务共享准入；应用更新准备以独占
// 准入完成「空闲复核 + 封闸」，避免检查与创建/认领之间的 TOCTOU。
//
// 边界（contracts/app-update-installation.md §Atomicity and failure behavior）：
//   - 共享准入覆盖 HTTP 普通 handler 全程（含事务外文件 IO/GET 副作用）与 DoTx；
//   - 独占准入只在没有任何共享持有者时成功，封闸标记在释放独占前生效；
//   - 封闸是实例内内存状态：不持久化、不跨重启，也不是第二业务队列；
//   - ReadTx 不受闸门影响，供控制面只读证明使用。

import (
	"errors"
	"sync"
)

var (
	// ErrSealed 表示本实例已为应用更新封闸（或正处于独占准备阶段）：
	// 普通业务写与普通请求必须拒绝，不能继续排队或写入。
	ErrSealed = errors.New("store: business admission is sealed for update preparation")
	// ErrAdmissionBusy 表示封闸尝试时仍有普通准入持有者（请求或事务在飞）。
	// 调用方按「request_in_flight」快速返回，不等待、不打断在飞操作。
	ErrAdmissionBusy = errors.New("store: business admission is busy")
)

// WriteGate 协调普通共享准入与更新准备的独占准入。
//
// 零值可用；Store 构造时创建。方法可在 nil 接收者上安全调用（测试便利）。
type WriteGate struct {
	mu        sync.Mutex
	shared    int
	exclusive bool
	sealed    bool
}

// NewWriteGate 返回未封闸的闸门。
func NewWriteGate() *WriteGate { return &WriteGate{} }

// AcquireShared 获取普通准入；封闸或独占阶段返回 ErrSealed。
func (g *WriteGate) AcquireShared() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed || g.exclusive {
		return ErrSealed
	}
	g.shared++
	return nil
}

// ReleaseShared 释放普通准入。
func (g *WriteGate) ReleaseShared() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.shared > 0 {
		g.shared--
	}
	g.mu.Unlock()
}

// TryExclusive 尝试获取独占准入：已有共享持有者或独占持有者时返回
// ErrAdmissionBusy；已封闸返回 ErrSealed。
func (g *WriteGate) TryExclusive() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		return ErrSealed
	}
	if g.exclusive || g.shared > 0 {
		return ErrAdmissionBusy
	}
	g.exclusive = true
	return nil
}

// Seal 在持有独占准入时置封闸标记；调用后普通准入持续被拒绝。
func (g *WriteGate) Seal() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exclusive {
		g.sealed = true
	}
}

// ReleaseExclusive 释放独占准入；封闸标记保持。
func (g *WriteGate) ReleaseExclusive() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.exclusive = false
	g.mu.Unlock()
}

// Sealed 报告本实例是否已封闸。
func (g *WriteGate) Sealed() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sealed
}

// Idle 报告当前没有普通请求/写得持有准入。
func (g *WriteGate) Idle() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shared == 0 && !g.exclusive
}
