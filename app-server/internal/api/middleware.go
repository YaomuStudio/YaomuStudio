package api

// 中间件：request_id、panic 恢复、Host/Origin 校验、实例令牌鉴权与访问日志。
//
// 所有业务路径强制校验实例令牌；只有 /health/live 不要求（最小存活检查）。
// 随机端口与 CORS 都不能代替授权（docs/local-api-contract.md §1）。

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

type contextKey int

const requestIDKey contextKey = iota

func requestIDFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// newRequestID 生成不透明请求 ID。
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return "req-" + hex.EncodeToString(b[:])
}

// requestIDMiddleware 读取调用方 request_id 或生成一个，并放入上下文。
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("request_id"))
		if id == "" || len(id) > 200 {
			id = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverMiddleware 把 panic 转为安全 503，不向客户端泄漏堆栈。
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if s.log != nil {
					s.log.Error("api", "api.panic", errFromPanic(rec), map[string]any{
						"request_id": requestIDFrom(r),
						"path":       r.URL.Path,
					})
				}
				s.writeError(w, r, &domain.Error{
					Code:       domain.ErrorCodeCoreNotReady,
					MessageKey: "error.internal",
					Retryable:  true,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type panicError string

func (p panicError) Error() string { return string(p) }

func errFromPanic(rec any) error {
	if err, ok := rec.(error); ok {
		return err
	}
	return panicError("panic")
}

// hostOriginMiddleware 校验回环 Host 与可信 Origin，防止 DNS rebinding。
func (s *Server) hostOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			s.writeError(w, r, domain.ErrLocalAccessDenied)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !isAllowedOrigin(origin) {
				s.writeError(w, r, domain.ErrLocalAccessDenied)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func isLoopbackHost(host string) bool {
	host = hostOnly(host)
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func isAllowedOrigin(origin string) bool {
	if origin == "null" {
		// file:// 渲染上下文；仍需实例令牌，不能单独构成授权。
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return isLoopbackHost(parsed.Host)
}

// authMiddleware 校验实例令牌；/health/live 豁免。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get(tokenHeader)
		if token == "" {
			s.writeError(w, r, domain.ErrLocalAuthRequired)
			return
		}
		if s.token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			s.writeError(w, r, domain.ErrLocalAuthRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admissionMiddleware 为普通（业务）请求提供全程准入：handler 内的事务外
// 文件 IO、GET 副作用与 DoTx 都计入同一个共享准入，更新准备只能在没有任何
// 普通 handler 在飞时封闸。
//
// 排除项与契约一致：health、纯 system 身份/握手、SSE 与 update-preparation。
// 封闸或独占准备阶段，新普通请求 fail-fast 返回 CORE_NOT_READY，不等待、
// 不被接受进队列。
func (s *Server) admissionMiddleware(next http.Handler) http.Handler {
	if s.updates == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if updateAdmissionExcluded(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.updates.BeginRequest() {
			s.writeError(w, r, errUpdatePreparing())
			return
		}
		defer s.updates.EndRequest()
		next.ServeHTTP(w, r)
	})
}

// updateAdmissionExcluded 是全程准入的唯一排除表。
func updateAdmissionExcluded(r *http.Request) bool {
	switch r.URL.Path {
	case "/health/live":
		return r.Method == http.MethodGet
	case "/api/v1/system", "/api/v1/events":
		return r.Method == http.MethodGet
	case "/api/v1/system/update-preparation":
		return r.Method == http.MethodPost
	default:
		return false
	}
}

// accessLog 记录方法、路径、状态与耗时；不记录请求体或令牌。
func (s *Server) accessLog(next http.Handler) http.Handler {
	if s.log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if r.URL.Path == "/health/live" && recorder.status == http.StatusOK {
			return
		}
		s.log.Info("api", "api.request", map[string]any{
			"request_id":  requestIDFrom(r),
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      recorder.status,
			"duration_ms": time.Since(started).Milliseconds(),
		})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush 保留 SSE 所需的 Flusher 能力。
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
