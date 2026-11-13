package api

import (
	"fmt"
	"net/http"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	var req service.CreateWorkflowRunInput
	if _, err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.Preflight(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req service.CreateWorkflowRunInput
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CreateWorkflowRun(r.Context(), idem, req)
	})
}

func (s *Server) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	run, err := s.svc.GetWorkflowRun(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handlePauseWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.PauseWorkflowRun(r.Context(), idem, id)
	})
}

func (s *Server) handleResumeWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.ResumeWorkflowRun(r.Context(), idem, id)
	})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if limit < 1 || limit > 200 {
		s.writeError(w, r, fmt.Errorf("%w: limit must be in [1,200]", domain.ErrInvalidRequest))
		return
	}
	status := domain.TaskStatus(queryValue(r, "status"))
	list, err := s.svc.ListTasks(r.Context(), service.ListTasksQuery{
		ProjectID: queryValue(r, "project_id"),
		RunID:     queryValue(r, "run_id"),
		Status:    status,
		Cursor:    queryValue(r, "cursor"),
		Limit:     limit,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req service.CreateTaskRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CreateTask(r.Context(), idem, req)
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	task, err := s.svc.GetTask(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleGetTaskExecutionLog 返回 cache 中的受控执行过程尾部；为空也返回 200。
func (s *Server) handleGetTaskExecutionLog(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	attempt, err := queryInt(r, "attempt", 0)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	tail, err := queryInt(r, "tail", 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if attempt < 0 || tail < 1 || tail > 1000 {
		s.writeError(w, r, fmt.Errorf("%w: attempt must be >= 0 and tail in [1,1000]", domain.ErrInvalidRequest))
		return
	}
	log, err := s.svc.ExecutionLog(r.Context(), id, attempt, tail)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CancelTask(r.Context(), idem, id)
	})
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.RetryTask(r.Context(), idem, id)
	})
}
