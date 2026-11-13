package api

import (
	"net/http"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handlePlanInstallation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slot domain.CapabilitySlotKey `json:"slot"`
	}
	if _, err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	plan, err := s.svc.PlanInstallation(r.Context(), req.Slot)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleListInstallations(w http.ResponseWriter, r *http.Request) {
	installs, err := s.svc.ListInstallations(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, installs)
}

func (s *Server) handleCreateInstallation(w http.ResponseWriter, r *http.Request) {
	var req service.CreateInstallationRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CreateInstallation(r.Context(), idem, req)
	})
}

func (s *Server) handleGetInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	inst, err := s.svc.GetInstallation(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (s *Server) handleCancelInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CancelInstallation(r.Context(), idem, id)
	})
}

func (s *Server) handleDeleteInstallation(w http.ResponseWriter, r *http.Request) {
	slot, err := pathID(r, "slot")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.DeleteInstalledPackage(r.Context(), idem, slot)
	})
}

func (s *Server) handleRollbackInstallation(w http.ResponseWriter, r *http.Request) {
	slot, err := pathID(r, "slot")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.RollbackInstalledPackage(r.Context(), idem, slot)
	})
}

func (s *Server) handleGetEngineConfig(w http.ResponseWriter, r *http.Request) {
	slot, err := pathID(r, "slot")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	cfg, err := s.svc.GetEngineConfig(r.Context(), slot)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleUpdateEngineConfig(w http.ResponseWriter, r *http.Request) {
	slot, err := pathID(r, "slot")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.EngineConfigUpdate
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.UpdateEngineConfig(r.Context(), idem, slot, req)
	})
}

func (s *Server) handleListExports(w http.ResponseWriter, r *http.Request) {
	records, err := s.svc.ListExports(r.Context(), queryValue(r, "project_id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleCreateExport(w http.ResponseWriter, r *http.Request) {
	var req service.ExportRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CreateExport(r.Context(), idem, req)
	})
}
