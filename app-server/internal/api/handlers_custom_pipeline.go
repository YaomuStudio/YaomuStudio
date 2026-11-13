package api

import (
	"net/http"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handleListCustomPipelineTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.CustomPipelineTemplates(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSaveCustomPipelineTemplates(w http.ResponseWriter, r *http.Request) {
	var req service.CustomPipelineTemplateList
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.SaveCustomPipelineTemplates(r.Context(), idem, req)
	})
}
