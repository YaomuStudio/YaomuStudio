package api

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.svc.ListProjects(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req service.CreateProjectRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.CreateProject(r.Context(), idem, req)
	})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	project, err := s.svc.GetProject(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.UpdateProjectRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.UpdateProject(r.Context(), idem, id, req)
	})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, nil, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.DeleteProject(r.Context(), idem, id)
	})
}

func (s *Server) handleImportAssets(w http.ResponseWriter, r *http.Request) {
	var req service.AssetImportRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.ImportAssets(r.Context(), idem, req)
	})
}

// handleRelocateAsset 把缺失的外部引用指向用户重新选择的新路径。
// 只接受系统选择器签发的 file grant；摘要不一致/非缺失资产一律拒绝。
func (s *Server) handleRelocateAsset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req service.RelocateAssetRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeCall(w, r, body, true, func(idem serviceIdem) (serviceResponse, error) {
		return s.svc.RelocateAsset(r.Context(), idem, id, req)
	})
}

// handleAssetContent 以流式/Range 读取资产；只读取已登记位置，不接受任意路径。
func (s *Server) handleAssetContent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	content, err := s.svc.AssetContentFor(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	file, err := os.Open(content.Path)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer file.Close()
	if contentType := mime.TypeByExtension(filepath.Ext(content.Name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, content.Name, content.ModTime, file)
}
