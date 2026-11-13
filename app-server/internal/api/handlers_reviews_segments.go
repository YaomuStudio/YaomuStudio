package api

import (
	"net/http"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handleListReviews(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	gates, err := s.svc.ListProjectReviews(r.Context(), projectID, queryValue(r, "run_id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gates)
}

func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	gate, err := s.svc.GetReview(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gate)
}

func (s *Server) handleApproveReview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.ApproveReviewRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.ApproveReview(r.Context(), idem, id, req)
	})
}

func (s *Server) handleListCandidates(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	candidates, err := s.svc.ListCandidates(r.Context(), id, queryValue(r, "language"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

func (s *Server) handleGenerateCandidates(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.CreateCandidatesRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.GenerateCandidates(r.Context(), idem, id, req)
	})
}

func (s *Server) handleGetSelection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	selection, err := s.svc.GetCandidateSelection(r.Context(), id, queryValue(r, "language"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, selection)
}

func (s *Server) handleSelectCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.SelectCandidateRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.SelectCandidate(r.Context(), idem, id, req)
	})
}

func (s *Server) handleSaveSubtitleRevision(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.SaveSubtitleRevisionRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.SaveSubtitleRevision(r.Context(), idem, id, req)
	})
}

func (s *Server) handleImportSubtitleTrack(w http.ResponseWriter, r *http.Request) {
	var req service.ImportSubtitleTrackInput
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.ImportSubtitleTrack(r.Context(), idem, req)
	})
}
