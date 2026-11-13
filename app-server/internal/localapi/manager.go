// Package localapi 是第三方本机 API：独立回环端口、默认关闭、可吊销 Bearer 令牌
// 与本地统计（FR-SET-30/31/32/33）。
//
// 边界（docs/local-api-contract.md §7）：
//   - 总开关默认关闭；关闭即停止第三方端口监听，不影响内部 Shell/Core API；
//   - 开启后单独监听 127.0.0.1，并要求 Bearer 令牌；令牌可吊销/重置；
//   - 统计只留本地，不包含素材/字幕内容；
//   - 第三方端口不暴露管理端点，管理走内部 /api/v1/local-api/*。
package localapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// app_meta 键（默认关闭的开关与令牌哈希只存本地库）。
const (
	metaSettings      = "local_api.settings"
	metaTokenHash     = "local_api.token_hash"
	metaTokenMasked   = "local_api.token_masked"
	metaTokenIssuedAt = "local_api.token_issued_at"
)

// SettingsUpdate 是设置更新输入。
type SettingsUpdate struct {
	Enabled          *bool `json:"enabled"`
	ConcurrencyLimit *int  `json:"concurrency_limit"`
}

type storedSettings struct {
	Enabled          bool `json:"enabled"`
	ConcurrencyLimit int  `json:"concurrency_limit"`
}

// Manager 管理第三方本机 API 的开关、令牌、统计与监听生命周期。
type Manager struct {
	store *store.Store
	log   *logging.Logger
	now   func() time.Time

	mu          sync.Mutex
	settings    storedSettings
	tokenHash   string
	tokenMasked string
	tokenIssued time.Time
	server      *http.Server
	listener    net.Listener
	port        int
	inflight    int64

	statsMu sync.Mutex
	calls   int64
	byPath  map[string]*endpointStat
}

type endpointStat struct {
	calls     int64
	lastError string
}

// Options 是构造参数。
type Options struct {
	Now func() time.Time
}

// NewManager 读取持久化的设置与令牌哈希；不自动开始监听。
func NewManager(st *store.Store, log *logging.Logger, opts Options) (*Manager, error) {
	m := &Manager{
		store:  st,
		log:    log,
		now:    opts.Now,
		byPath: make(map[string]*endpointStat),
	}
	if m.now == nil {
		m.now = func() time.Time { return time.Now().UTC() }
	}
	if err := m.load(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load(ctx context.Context) error {
	if raw, ok, err := m.store.GetMeta(ctx, metaSettings); err != nil {
		return err
	} else if ok {
		var settings storedSettings
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			// 损坏的设置值不能阻断 Core 启动：回落到默认（总开关关闭），
			// 由下一次显式保存覆盖。
			if m.log != nil {
				m.log.Warn("localapi", "localapi.settings_corrupt", map[string]any{"error": err.Error()})
			}
		} else {
			m.settings = settings
		}
	}
	if hash, ok, err := m.store.GetMeta(ctx, metaTokenHash); err != nil {
		return err
	} else if ok {
		m.tokenHash = hash
	}
	if masked, ok, err := m.store.GetMeta(ctx, metaTokenMasked); err != nil {
		return err
	} else if ok {
		m.tokenMasked = masked
	}
	if issued, ok, err := m.store.GetMeta(ctx, metaTokenIssuedAt); err != nil {
		return err
	} else if ok {
		if t, err := time.Parse(time.RFC3339Nano, issued); err == nil {
			m.tokenIssued = t
		}
	}
	return nil
}

// Apply 依据当前设置启动/停止第三方端口（启动时调用一次）。
func (m *Manager) Apply(ctx context.Context) error {
	m.mu.Lock()
	enabled := m.settings.Enabled
	m.mu.Unlock()
	if !enabled {
		return nil
	}
	return m.startServer(ctx)
}

// Settings 返回当前设置（不返回明文令牌）。
func (m *Manager) Settings(context.Context) (domain.LocalApiSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settingsLocked(), nil
}

func (m *Manager) settingsLocked() domain.LocalApiSettings {
	return domain.LocalApiSettings{
		Enabled:          m.settings.Enabled,
		ConcurrencyLimit: m.settings.ConcurrencyLimit,
		TokenPresent:     m.tokenHash != "",
		TokenMasked:      m.tokenMasked,
	}
}

// UpdateSettings 持久化设置并立即应用监听变化。
func (m *Manager) UpdateSettings(ctx context.Context, update SettingsUpdate) (domain.LocalApiSettings, error) {
	if update.ConcurrencyLimit != nil && *update.ConcurrencyLimit < 0 {
		return domain.LocalApiSettings{}, &domain.Error{
			Code:       domain.ErrorCodeInvalidRequest,
			MessageKey: "error.invalid_request",
			ActionIDs:  []domain.ID{},
		}
	}
	m.mu.Lock()
	if update.Enabled != nil {
		m.settings.Enabled = *update.Enabled
	}
	if update.ConcurrencyLimit != nil {
		m.settings.ConcurrencyLimit = *update.ConcurrencyLimit
	}
	settings := m.settings
	m.mu.Unlock()

	encoded, err := json.Marshal(settings)
	if err != nil {
		return domain.LocalApiSettings{}, fmt.Errorf("encode settings: %w", err)
	}
	if err := m.store.DoTx(ctx, func(tx *store.Tx) error {
		return tx.SetMeta(ctx, metaSettings, string(encoded))
	}); err != nil {
		return domain.LocalApiSettings{}, err
	}

	if settings.Enabled {
		if err := m.startServer(ctx); err != nil {
			return domain.LocalApiSettings{}, err
		}
	} else {
		m.stopServer(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settingsLocked(), nil
}

// IssueToken 签发新令牌并立即吊销旧令牌；明文只在本次响应出现一次。
func (m *Manager) IssueToken(ctx context.Context) (domain.LocalApiToken, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return domain.LocalApiToken{}, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := hashToken(token)
	masked := maskToken(token)
	issued := m.now().UTC()

	m.mu.Lock()
	m.tokenHash = hash
	m.tokenMasked = masked
	m.tokenIssued = issued
	m.mu.Unlock()

	if err := m.store.DoTx(ctx, func(tx *store.Tx) error {
		if err := tx.SetMeta(ctx, metaTokenHash, hash); err != nil {
			return err
		}
		if err := tx.SetMeta(ctx, metaTokenMasked, masked); err != nil {
			return err
		}
		return tx.SetMeta(ctx, metaTokenIssuedAt, issued.Format(time.RFC3339Nano))
	}); err != nil {
		return domain.LocalApiToken{}, err
	}
	return domain.LocalApiToken{Token: token, IssuedAt: issued}, nil
}

// RevokeToken 立即吊销当前令牌；第三方后续请求返回 401。
func (m *Manager) RevokeToken(ctx context.Context) error {
	m.mu.Lock()
	m.tokenHash = ""
	m.tokenMasked = ""
	m.tokenIssued = time.Time{}
	m.mu.Unlock()
	return m.store.DoTx(ctx, func(tx *store.Tx) error {
		for _, key := range []string{metaTokenHash, metaTokenMasked, metaTokenIssuedAt} {
			if err := tx.SetMeta(ctx, key, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

// Stats 返回本地统计（不含素材内容）。
func (m *Manager) Stats(context.Context) (domain.LocalApiStats, error) {
	m.mu.Lock()
	enabled := m.settings.Enabled
	m.mu.Unlock()

	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	stats := domain.LocalApiStats{
		Enabled:    enabled,
		TotalCalls: m.calls,
		ByEndpoint: make([]domain.LocalApiEndpointStats, 0, len(m.byPath)),
	}
	for endpoint, stat := range m.byPath {
		stats.ByEndpoint = append(stats.ByEndpoint, domain.LocalApiEndpointStats{
			Endpoint:  endpoint,
			Calls:     stat.calls,
			LastError: stat.lastError,
		})
	}
	sortEndpointStats(stats.ByEndpoint)
	return stats, nil
}

// Enabled 报告第三方端口当前是否应当监听。
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Enabled
}

// Port 返回第三方端口；未监听时为 0。
func (m *Manager) Port() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.port
}

// Close 停止监听（不改变持久化开关）。
func (m *Manager) Close(ctx context.Context) error {
	m.stopServer(ctx)
	return nil
}

func (m *Manager) startServer(ctx context.Context) error {
	m.mu.Lock()
	if m.listener != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen local api: %w", err)
	}
	m.mu.Lock()
	if m.listener != nil {
		m.mu.Unlock()
		_ = listener.Close()
		return nil
	}
	m.mu.Unlock()
	server := &http.Server{
		Handler:           m.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}

	m.mu.Lock()
	m.listener = listener
	m.server = server
	m.port = listener.Addr().(*net.TCPAddr).Port
	port := m.port
	m.mu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if m.log != nil {
				m.log.Error("localapi", "localapi.serve_failed", err, nil)
			}
		}
	}()
	if m.log != nil {
		m.log.Info("localapi", "localapi.listening", map[string]any{"port": port})
	}
	_ = ctx
	return nil
}

func (m *Manager) stopServer(ctx context.Context) {
	m.mu.Lock()
	server := m.server
	listener := m.listener
	m.server = nil
	m.listener = nil
	m.port = 0
	m.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if server != nil {
		_ = server.Shutdown(shutdownCtx)
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func maskToken(token string) string {
	if len(token) <= 4 {
		return "****"
	}
	return "…" + token[len(token)-4:]
}

func (m *Manager) tokenMatches(candidate string) bool {
	m.mu.Lock()
	stored := m.tokenHash
	m.mu.Unlock()
	if stored == "" {
		return false
	}
	got := hashToken(candidate)
	return subtle.ConstantTimeCompare([]byte(stored), []byte(got)) == 1
}

func (m *Manager) recordCall(endpoint, errorCode string) {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	m.calls++
	stat, ok := m.byPath[endpoint]
	if !ok {
		stat = &endpointStat{}
		m.byPath[endpoint] = stat
	}
	stat.calls++
	if errorCode != "" {
		stat.lastError = errorCode
	}
}

func (m *Manager) concurrencyLimit() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.ConcurrencyLimit
}

// acquireConcurrency 约束新受理的第三方请求；0 表示不额外限制。
func (m *Manager) acquireConcurrency() bool {
	limit := m.concurrencyLimit()
	if limit <= 0 {
		atomic.AddInt64(&m.inflight, 1)
		return true
	}
	for {
		current := atomic.LoadInt64(&m.inflight)
		if current >= int64(limit) {
			return false
		}
		if atomic.CompareAndSwapInt64(&m.inflight, current, current+1) {
			return true
		}
	}
}

func (m *Manager) releaseConcurrency() { atomic.AddInt64(&m.inflight, -1) }
