package api

// 应用更新准备（Main-only 私有控制面）。
//
// 契约（contracts/app-update-installation.md §Core preparation endpoint）：
//   - 仅经既有实例令牌与 Host/Origin 校验访问，不进入 Renderer 通用代理或
//     第三方端口；
//   - request_id 头必填；mode=check 只读，mode=prepare 原子复核并封闸；
//   - 结果只含固定身份回显、state 与有限 reason，不暴露路径/凭据/业务正文。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/updateguard"
)

// updatePreparationResult 是 UpdatePreparationResult 的精确 wire 形状。
type updatePreparationResult struct {
	SchemaVersion int     `json:"schema_version"`
	OperationID   string  `json:"operation_id"`
	WorkspaceID   string  `json:"workspace_id"`
	InstanceID    string  `json:"instance_id"`
	ServerEpoch   int64   `json:"server_epoch"`
	State         string  `json:"state"`
	Reason        *string `json:"reason"`
}

func (s *Server) handleUpdatePreparation(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		// 未装配准备控制器时 fail-closed：不伪造 idle/prepared。
		s.writeError(w, r, domain.ErrCoreNotReady)
		return
	}
	if strings.TrimSpace(r.Header.Get("request_id")) == "" {
		s.writeError(w, r, fmt.Errorf("%w: request_id header required", domain.ErrInvalidRequest))
		return
	}
	var req updateguard.Request
	if _, err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.updates.Handle(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := updatePreparationResult{
		SchemaVersion: 1,
		OperationID:   req.OperationID,
		WorkspaceID:   req.WorkspaceID,
		InstanceID:    req.InstanceID,
		ServerEpoch:   req.ServerEpoch,
		State:         result.State,
	}
	if result.Reason != "" {
		reason := result.Reason
		out.Reason = &reason
	}
	writeJSON(w, http.StatusOK, out)
}
