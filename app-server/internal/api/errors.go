package api

// HTTP 错误映射：只返回契约定义的安全错误码与本地化消息键。
// 原始错误细节只进本地诊断，不通过网络返回。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// errorEnvelope 是契约的 ErrorResponse 形状。
type errorEnvelope struct {
	RequestID string           `json:"request_id"`
	Error     domain.ErrorInfo `json:"error"`
}

// statusFor 把安全错误码映射为 HTTP 状态。
func statusFor(code domain.ErrorCode) int {
	switch code {
	case domain.ErrorCodeInvalidRequest:
		return http.StatusBadRequest
	case domain.ErrorCodeLocalAuthRequired:
		return http.StatusUnauthorized
	case domain.ErrorCodeLocalAccessDenied:
		return http.StatusForbidden
	case domain.ErrorCodeNotFound:
		return http.StatusNotFound
	case domain.ErrorCodeRevisionConflict, domain.ErrorCodePreflightStale,
		domain.ErrorCodeIdempotencyConflict, domain.ErrorCodeModelNotInstalled,
		domain.ErrorCodeRuntimeIncompatible, domain.ErrorCodeCapabilityUnverified,
		domain.ErrorCodeEventCursorExpired, domain.ErrorCodeReviewRequired:
		return http.StatusConflict
	case domain.ErrorCodeInsufficientDisk:
		return http.StatusInsufficientStorage
	case domain.ErrorCodeCoreNotReady, domain.ErrorCodePersistenceUnavailable,
		domain.ErrorCodeWorkerExited, domain.ErrorCodeResourceExhausted:
		return http.StatusServiceUnavailable
	default:
		return http.StatusServiceUnavailable
	}
}

// errUpdatePreparing 是更新准备封闸期间拒绝普通请求的有限安全错误。
// 不暴露内部阶段、路径或业务正文。
func errUpdatePreparing() error {
	return &domain.Error{
		Code:       domain.ErrorCodeCoreNotReady,
		MessageKey: "error.update_preparing",
		Retryable:  true,
	}
}

// errorInfoOf 提取可安全返回的错误信息；非领域错误按持久化不可用处理。
func errorInfoOf(err error) (domain.ErrorCode, domain.ErrorInfo) {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		info := domainErr.Info()
		return info.Code, info
	}
	if errors.Is(err, store.ErrSealed) {
		info := errUpdatePreparing().(*domain.Error).Info()
		return info.Code, info
	}
	if strings.Contains(err.Error(), "database or disk is full") {
		info := (&domain.Error{Code: domain.ErrorCodeInsufficientDisk,
			MessageKey: "error.insufficient_disk"}).Info()
		return info.Code, info
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		info := (&domain.Error{Code: domain.ErrorCodeCoreNotReady,
			MessageKey: "error.request_canceled"}).Info()
		return info.Code, info
	}
	info := (&domain.Error{Code: domain.ErrorCodePersistenceUnavailable,
		MessageKey: "error.persistence_unavailable", Retryable: true}).Info()
	return info.Code, info
}

// writeError 写契约错误体，并回显 request_id。
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, info := errorInfoOf(err)
	status := statusFor(code)
	if s != nil && s.log != nil {
		fields := map[string]any{
			"request_id": requestIDFrom(r),
			"method":     r.Method,
			"path":       r.URL.Path,
			"status":     status,
			"code":       code,
		}
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			// 客户端提前断开（含客户端超时）不是服务端故障：事务回滚是预期行为。
			// 记独立 warn 事件，避免本地日志把一次取消误报成 CORE_NOT_READY 错误。
			fields["error"] = logging.RedactText(err.Error())
			s.log.Warn("api", "api.request_canceled", fields)
		} else {
			s.log.Error("api", "api.error", err, fields)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{RequestID: requestIDFrom(r), Error: info})
}

// writeJSON 写成功响应；与幂等重放保持相同的字节形态（Marshal 而非 Encode）。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return
	}
	_, _ = w.Write(encoded)
}

// writeServiceResponse 写服务层响应；幂等重放时原样返回持久化的响应体。
func writeServiceResponse(w http.ResponseWriter, resp serviceResponse) {
	if resp.Status == http.StatusNoContent || (resp.Status == 0 && resp.Body == nil) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	if raw, ok := resp.Body.(json.RawMessage); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	writeJSON(w, status, resp.Body)
}
