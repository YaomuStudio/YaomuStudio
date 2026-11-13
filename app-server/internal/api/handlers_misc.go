package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	info, err := s.svc.System(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.svc.ListCapabilities(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.svc.ListDevices(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleEnvironment(w http.ResponseWriter, r *http.Request) {
	force, _ := strconv.ParseBool(queryValue(r, "refresh"))
	environment, err := s.svc.Environment(r.Context(), force)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, environment)
}

func (s *Server) handleStorageUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.svc.StorageUsage(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleCleanupStorage(w http.ResponseWriter, r *http.Request) {
	var req service.StorageCleanupRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CleanupStorage(r.Context(), idem, req)
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	projectID := queryValue(r, "project_id")
	if projectID == "" {
		s.writeError(w, r, fmt.Errorf("%w: project_id query parameter required", domain.ErrInvalidRequest))
		return
	}
	snapshot, err := s.svc.Snapshot(r.Context(), projectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleEventCursor(w http.ResponseWriter, r *http.Request) {
	cursor, err := s.svc.EventCursor(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cursor)
}

// handleEvents 是 SSE 增量事件：先校验游标，再注册订阅、补读历史、转实时。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	afterSeq, ok, err := queryInt64(r, "after_seq")
	if err != nil || !ok || afterSeq < 0 {
		s.writeError(w, r, fmt.Errorf("%w: after_seq must be a non-negative integer", domain.ErrInvalidRequest))
		return
	}
	if s.bus == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	// 游标过期在写 SSE 头之前返回 409，客户端可重新获取快照。
	if err := s.bus.CheckCursor(r.Context(), afterSeq); err != nil {
		s.writeError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var writeMu sync.Mutex
	writeFrame := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := w.Write(payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	stopKeepalive := func() { closeOnce.Do(func() { close(done) }) }
	keepaliveExited := make(chan struct{})
	go func() {
		defer close(keepaliveExited)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := writeFrame([]byte(": keepalive\n\n")); err != nil {
					return
				}
			}
		}
	}()
	defer func() {
		// 处理器返回前先停止再等待 keepalive 协程，避免响应结束后继续写。
		stopKeepalive()
		<-keepaliveExited
	}()

	err = s.bus.Stream(r.Context(), afterSeq, func(ev domain.DomainEvent) error {
		data, err := json.Marshal(ev)
		if err != nil {
			return nil
		}
		frame := make([]byte, 0, len(data)+16)
		frame = append(frame, []byte("data: ")...)
		frame = append(frame, data...)
		frame = append(frame, '\n', '\n')
		return writeFrame(frame)
	})
	if err != nil && s.log != nil {
		s.log.Warn("api", "api.events_stream_ended", map[string]any{
			"request_id": requestIDFrom(r),
			"error":      err.Error(),
		})
	}
}
