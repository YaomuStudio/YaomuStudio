package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	manager, err := NewManager(st, logger, Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, st
}

// TestLocalApiDefaultOffNoListener 覆盖总开关默认关闭：不监听第三方端口。
func TestLocalApiDefaultOffNoListener(t *testing.T) {
	manager, _ := newTestManager(t)
	settings, err := manager.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.TokenPresent || settings.TokenMasked != "" {
		t.Fatalf("default settings = %+v", settings)
	}
	if manager.Port() != 0 {
		t.Fatalf("port = %d, want 0 (no listener)", manager.Port())
	}
	stats, err := manager.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Enabled || stats.TotalCalls != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

// TestLocalApiBearerLifecycle 覆盖开启监听、Bearer 401、吊销后旧令牌失效与关闭停止监听。
func TestLocalApiBearerLifecycle(t *testing.T) {
	manager, _ := newTestManager(t)
	ctx := context.Background()

	token, err := manager.IssueToken(ctx)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token.Token == "" || token.IssuedAt.IsZero() {
		t.Fatalf("token = %+v", token)
	}
	if _, err := manager.UpdateSettings(ctx, SettingsUpdate{Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	var port int
	deadline := time.Now().Add(3 * time.Second)
	for port == 0 {
		port = manager.Port()
		if time.Now().After(deadline) {
			t.Fatal("manager did not start listening")
		}
		time.Sleep(10 * time.Millisecond)
	}

	base := "http://127.0.0.1:" + itoa(port)
	post := func(path string, bearer string, body []byte) *http.Response {
		req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		return resp
	}

	speech := []byte(`{"input":"hello"}`)
	if resp := post("/v1/audio/speech", "", speech); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer status = %d", resp.StatusCode)
	}
	if resp := post("/v1/audio/speech", token.Token, speech); resp.StatusCode != http.StatusConflict {
		t.Fatalf("bearer status = %d", resp.StatusCode)
	}

	if err := manager.RevokeToken(ctx); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if resp := post("/v1/audio/speech", token.Token, speech); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", resp.StatusCode)
	}

	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Enabled || stats.TotalCalls < 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(stats.ByEndpoint) != 1 || stats.ByEndpoint[0].Endpoint != "/v1/audio/speech" {
		t.Fatalf("by_endpoint = %+v", stats.ByEndpoint)
	}
	if stats.ByEndpoint[0].LastError != string(domain.ErrorCodeLocalAuthRequired) {
		t.Fatalf("last_error = %s", stats.ByEndpoint[0].LastError)
	}

	if _, err := manager.UpdateSettings(ctx, SettingsUpdate{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for manager.Port() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("port = %d after disable", manager.Port())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLocalApiSettingsPersistAcrossManagers 覆盖开关与令牌哈希持久化。
func TestLocalApiSettingsPersistAcrossManagers(t *testing.T) {
	manager, st := newTestManager(t)
	ctx := context.Background()
	if _, err := manager.IssueToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateSettings(ctx, SettingsUpdate{
		Enabled:          boolPtr(true),
		ConcurrencyLimit: intPtr(3),
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}

	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core2.log")})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	second, err := NewManager(st, logger, Options{})
	if err != nil {
		t.Fatalf("NewManager(second): %v", err)
	}
	defer second.Close(ctx)
	settings, err := second.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.ConcurrencyLimit != 3 || !settings.TokenPresent {
		t.Fatalf("reloaded settings = %+v", settings)
	}
	if err := second.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for second.Port() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("second manager did not listen after Apply")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLocalApiConcurrencyLimit 覆盖并发上限只约束新受理请求。
func TestLocalApiConcurrencyLimit(t *testing.T) {
	manager, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := manager.IssueToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateSettings(ctx, SettingsUpdate{
		Enabled:          boolPtr(true),
		ConcurrencyLimit: intPtr(1),
	}); err != nil {
		t.Fatal(err)
	}
	// 占满唯一名额后，新请求被拒绝（409 RESOURCE_EXHAUSTED）。
	if !manager.acquireConcurrency() {
		t.Fatal("first acquire failed")
	}
	if manager.acquireConcurrency() {
		t.Fatal("second acquire succeeded beyond limit")
	}
	manager.releaseConcurrency()
	if !manager.acquireConcurrency() {
		t.Fatal("acquire after release failed")
	}
	manager.releaseConcurrency()
}

func boolPtr(v bool) *bool { return &v }

func intPtr(v int) *int { return &v }

func itoa(v int) string {
	data, _ := json.Marshal(v)
	return string(data)
}
