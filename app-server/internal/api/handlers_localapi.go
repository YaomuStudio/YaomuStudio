package api

import (
	"context"
	"net/http"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/localapi"
)

func (s *Server) handleGetLocalApiSettings(w http.ResponseWriter, r *http.Request) {
	if s.local == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	settings, err := s.local.Settings(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateLocalApiSettings(w http.ResponseWriter, r *http.Request) {
	if s.local == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	var req localapi.SettingsUpdate
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Enabled == nil && req.ConcurrencyLimit == nil {
		s.writeError(w, r, &domain.Error{Code: domain.ErrorCodeInvalidRequest,
			MessageKey: "error.invalid_request", ActionIDs: []domain.ID{}})
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.PerformIdempotentEffect(r.Context(), "local-api.settings", idem,
			http.StatusOK, func(ctx context.Context) (any, error) {
				return s.local.UpdateSettings(ctx, req)
			})
	})
}

func (s *Server) handleIssueLocalApiToken(w http.ResponseWriter, r *http.Request) {
	if s.local == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	// 明文令牌只在本次响应出现一次，绝不写入幂等回执（会持久化到 SQLite）。
	if _, err := idemFrom(r, nil, true); err != nil {
		s.writeError(w, r, err)
		return
	}
	token, err := s.local.IssueToken(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) handleRevokeLocalApiToken(w http.ResponseWriter, r *http.Request) {
	if s.local == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.PerformIdempotentEffect(r.Context(), "local-api.token.revoke", idem,
			http.StatusNoContent, func(ctx context.Context) (any, error) {
				if err := s.local.RevokeToken(ctx); err != nil {
					return nil, err
				}
				return nil, nil
			})
	})
}

func (s *Server) handleLocalApiStats(w http.ResponseWriter, r *http.Request) {
	if s.local == nil {
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	stats, err := s.local.Stats(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
