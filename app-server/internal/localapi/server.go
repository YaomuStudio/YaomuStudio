package localapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// 兼容端点的稳定路径（统计按此聚合）。
const (
	pathTranscriptions = "/v1/audio/transcriptions"
	pathSpeech         = "/v1/audio/speech"
	maxUploadBytes     = 64 << 20
)

// handler 返回第三方端口路由：只有 OpenAI 兼容子集端点，不暴露管理面。
func (m *Manager) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+pathTranscriptions, m.guard(pathTranscriptions, m.handleTranscriptions))
	mux.HandleFunc("POST "+pathSpeech, m.guard(pathSpeech, m.handleSpeech))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "localapi.endpoint_not_found")
	})
	return hostGuard(mux)
}

// hostGuard 只接受回环 Host，拒绝 DNS rebinding 目标。
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			writeError(w, http.StatusForbidden, "LOCAL_ACCESS_DENIED", "localapi.host_denied")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guard 依次执行 Bearer 鉴权、并发上限与统计。
func (m *Manager) guard(endpoint string, next func(http.ResponseWriter, *http.Request) (int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !m.tokenMatches(token) {
			m.recordCall(endpoint, string(domain.ErrorCodeLocalAuthRequired))
			writeError(w, http.StatusUnauthorized, string(domain.ErrorCodeLocalAuthRequired), "localapi.auth_required")
			return
		}
		if !m.acquireConcurrency() {
			m.recordCall(endpoint, string(domain.ErrorCodeResourceExhausted))
			writeError(w, http.StatusConflict, string(domain.ErrorCodeResourceExhausted), "localapi.concurrency_limit")
			return
		}
		defer m.releaseConcurrency()

		status, errorCode := next(w, r)
		m.recordCall(endpoint, errorCode)
		_ = status
	}
}

// handleTranscriptions 是 OpenAI 兼容转写的兼容子集入口。
//
// 契约要求逐项声明兼容子集后才提供；当前能力目录未声明任何兼容子集，
// 因此通过鉴权后返回 CAPABILITY_UNVERIFIED（409），不伪造转写结果。
func (m *Manager) handleTranscriptions(w http.ResponseWriter, r *http.Request) (int, string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest), "localapi.invalid_multipart")
		return http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest)
	}
	if _, _, err := r.FormFile("file"); err != nil {
		writeError(w, http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest), "localapi.file_required")
		return http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest)
	}
	// 只接受上传流；不接受宿主任意路径或远端 URL。
	writeError(w, http.StatusConflict, string(domain.ErrorCodeCapabilityUnverified), "localapi.transcription_subset_unverified")
	return http.StatusConflict, string(domain.ErrorCodeCapabilityUnverified)
}

// handleSpeech 是 OpenAI 兼容语音合成的兼容子集入口。
func (m *Manager) handleSpeech(w http.ResponseWriter, r *http.Request) (int, string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest), "localapi.invalid_body")
		return http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest)
	}
	var payload struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Input) == "" {
		writeError(w, http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest), "localapi.input_required")
		return http.StatusBadRequest, string(domain.ErrorCodeInvalidRequest)
	}
	// 模型许可仍由 Core 校验；未声明兼容子集前不提供合成。
	writeError(w, http.StatusConflict, string(domain.ErrorCodeCapabilityUnverified), "localapi.speech_subset_unverified")
	return http.StatusConflict, string(domain.ErrorCodeCapabilityUnverified)
}

// bearerToken 读取 Authorization: Bearer <token>。
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// errorEnvelope 与内部 API 保持同一安全错误形状。
type errorEnvelope struct {
	RequestID string           `json:"request_id"`
	Error     domain.ErrorInfo `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, messageKey string) {
	requestID := newRequestID()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		RequestID: requestID,
		Error: domain.ErrorInfo{
			Code:       domain.ErrorCode(code),
			MessageKey: messageKey,
			Retryable:  false,
			ActionIDs:  []domain.ID{},
		},
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-local"
	}
	return "req-" + hex.EncodeToString(b[:])
}

func sortEndpointStats(stats []domain.LocalApiEndpointStats) {
	sort.Slice(stats, func(i, j int) bool { return stats[i].Endpoint < stats[j].Endpoint })
}
