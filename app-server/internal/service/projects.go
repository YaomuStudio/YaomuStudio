package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// System 返回实例握手信息。
func (s *Service) System(_ context.Context) (domain.SystemInfo, error) {
	return domain.SystemInfo{
		AppID:           s.opts.AppID,
		AppVersion:      s.opts.AppVersion,
		ProtocolVersion: s.opts.ProtocolVersion,
		InstanceID:      s.opts.InstanceID,
		WorkspaceID:     s.opts.WorkspaceID,
		ServerEpoch:     s.opts.ServerEpoch,
		CoreReady:       true,
	}, nil
}

// CreateProjectRequest 是创建项目的输入。
type CreateProjectRequest struct {
	Name string `json:"name"`
}

// projectSchemaVersion 是当前项目数据 schema 版本（迁移与降级检查）。
const projectSchemaVersion = 1

// ensureProjectDir 创建并返回应用管理的项目目录。
//
// 目录是 Project.directory 的派生值，不写数据库；创建失败返回可读的
// PERSISTENCE_UNAVAILABLE，不继续登记项目。
func (s *Service) ensureProjectDir(projectID string) (string, error) {
	directory := s.projectDir(projectID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("%w: create project directory: %v", domain.ErrPersistenceUnavailable, err)
	}
	return directory, nil
}

// ListProjects 返回工作区项目摘要。
func (s *Service) ListProjects(ctx context.Context) ([]domain.ProjectSummary, error) {
	summaries, err := s.store.ListProjectSummaries(ctx, s.opts.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if summaries == nil {
		summaries = []domain.ProjectSummary{}
	}
	return summaries, nil
}

// CreateProject 创建本地项目（201）。
func (s *Service) CreateProject(ctx context.Context, idem Idem, req CreateProjectRequest) (Response, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Response{}, invalidRequestf("project name required")
	}
	if len([]rune(name)) > 30 {
		return Response{}, invalidRequestf("project name exceeds 30 characters")
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "projects.create", idem, 201, func(ctx context.Context, tx *store.Tx) (any, error) {
		now := s.Now()
		projectID := store.NewID("proj")
		directory, err := s.ensureProjectDir(projectID)
		if err != nil {
			return nil, err
		}
		project := domain.Project{
			ID:            projectID,
			WorkspaceID:   s.opts.WorkspaceID,
			EpisodeID:     store.NewID("ep"),
			Name:          name,
			Directory:     directory,
			SchemaVersion: projectSchemaVersion,
			EditVersion:   1,
			CreatedAt:     now,
			UpdatedAt:     now,
			Assets:        []domain.Asset{},
			Tracks:        []domain.SubtitleTrack{},
			Runs:          []domain.WorkflowRun{},
		}
		if err := tx.CreateProject(ctx, project); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "project.created",
			ProjectID: project.ID,
			Data:      map[string]any{"name": project.Name},
		}); err != nil {
			return nil, err
		}
		return project, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// UpdateProjectRequest 是修改项目元数据的输入；目前只允许修改名称。
type UpdateProjectRequest struct {
	Name        string `json:"name"`
	EditVersion int64  `json:"edit_version"`
}

// validateProjectName 去空格后校验 1–30 个 Unicode 字符，并拒绝控制字符。
func validateProjectName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: project name required",
			&domain.Error{Code: domain.ErrorCodeInvalidRequest, MessageKey: "error.project_name_required"})
	}
	if utf8.RuneCountInString(name) > 30 {
		return "", fmt.Errorf("%w: project name exceeds 30 characters",
			&domain.Error{Code: domain.ErrorCodeInvalidRequest, MessageKey: "error.project_name_too_long"})
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: project name contains control characters",
				&domain.Error{Code: domain.ErrorCodeInvalidRequest, MessageKey: "error.project_name_invalid"})
		}
	}
	return name, nil
}

// UpdateProject 修改项目名称（200）。
//
// edit_version 大于 0 时必须与当前值一致，否则返回 409；成功后编辑版本递增。
// 与 GET 一样返回完整项目视图，目录字段在事务内补齐。
func (s *Service) UpdateProject(
	ctx context.Context,
	idem Idem,
	id string,
	req UpdateProjectRequest,
) (Response, error) {
	if id == "" {
		return Response{}, invalidRequestf("project id required")
	}
	if req.EditVersion < 0 {
		return Response{}, invalidRequestf("edit_version must be non-negative")
	}
	name, err := validateProjectName(req.Name)
	if err != nil {
		return Response{}, err
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "projects.update", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			return nil, err
		}
		if req.EditVersion > 0 && project.EditVersion != req.EditVersion {
			return nil, fmt.Errorf("%w: project %s edit_version != %d",
				&domain.Error{Code: domain.ErrorCodeRevisionConflict, MessageKey: "error.project_name_conflict"},
				id, req.EditVersion)
		}
		now := s.Now()
		newVersion, err := tx.UpdateProjectName(ctx, id, name, project.EditVersion, now)
		if err != nil {
			return nil, err
		}
		directory, err := s.ensureProjectDir(project.ID)
		if err != nil {
			return nil, err
		}
		project.Name = name
		project.Directory = directory
		project.EditVersion = newVersion
		project.UpdatedAt = now
		if project.Assets == nil {
			project.Assets = []domain.Asset{}
		}
		if project.Tracks == nil {
			project.Tracks = []domain.SubtitleTrack{}
		}
		if project.Runs == nil {
			project.Runs = []domain.WorkflowRun{}
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "project.updated",
			ProjectID: project.ID,
			Data:      map[string]any{"name": name, "edit_version": newVersion},
		}); err != nil {
			return nil, err
		}
		return project, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// GetProject 返回项目视图（资产/字幕轨/运行状态）。
func (s *Service) GetProject(ctx context.Context, id string) (domain.Project, error) {
	if id == "" {
		return domain.Project{}, invalidRequestf("project id required")
	}
	project, err := s.store.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	// 旧项目没有持久化目录字段：读取时派生并补齐，失败不返回半成品视图。
	directory, err := s.ensureProjectDir(project.ID)
	if err != nil {
		return domain.Project{}, err
	}
	project.Directory = directory
	if project.Assets == nil {
		project.Assets = []domain.Asset{}
	}
	// 外部引用可能被移动/删除：读取项目时刷新可用性与 missing/ready 状态。
	// 只处理 external_reference；copy/generated 不随外部文件变化。
	s.refreshAssetAvailability(ctx, project.Assets)
	if project.Tracks == nil {
		project.Tracks = []domain.SubtitleTrack{}
	}
	if project.Runs == nil {
		project.Runs = []domain.WorkflowRun{}
	}
	for i := range project.Runs {
		normalizeRun(&project.Runs[i])
	}
	return project, nil
}

// refreshAssetAvailability 以磁盘事实校正外部引用位置与资产状态。
//
// 只处理 external_reference：存在任一可读文件时 missing 恢复 ready；
// 全部不可读时 ready/missing 归为 missing。copy/generated 不参与。
// 写库失败只记警告，不阻断项目读取。
func (s *Service) refreshAssetAvailability(ctx context.Context, assets []domain.Asset) {
	for i := range assets {
		locations, err := s.store.ListAssetLocations(ctx, assets[i].ID)
		if err != nil {
			s.warn("assets.availability_refresh_failed", map[string]any{"asset_id": string(assets[i].ID)})
			continue
		}
		external := 0
		anyAvailable := false
		for _, location := range locations {
			if location.Source != domain.LocationSourceExternalReference {
				continue
			}
			external++
			info, statErr := os.Stat(location.Path)
			available := statErr == nil && info.Mode().IsRegular()
			if available {
				anyAvailable = true
			}
			if location.Available != available {
				if err := s.store.SetAssetLocationAvailable(ctx, location.ID, available); err != nil {
					s.warn("assets.location_refresh_failed", map[string]any{"asset_id": string(assets[i].ID)})
				}
			}
		}
		if external == 0 {
			continue
		}
		if anyAvailable {
			if assets[i].Status != domain.AssetStatusMissing {
				continue
			}
			if err := s.store.SetAssetStatus(ctx, assets[i].ID, domain.AssetStatusReady); err != nil {
				s.warn("assets.status_refresh_failed", map[string]any{"asset_id": string(assets[i].ID)})
				continue
			}
			assets[i].Status = domain.AssetStatusReady
			continue
		}
		// 全部外部位置不可用：只把 ready/missing 收敛为 missing；
		// corrupt 保留原状态，避免覆盖“内容损坏”的原因。
		if assets[i].Status != domain.AssetStatusReady && assets[i].Status != domain.AssetStatusMissing {
			continue
		}
		if assets[i].Status == domain.AssetStatusReady {
			if err := s.store.SetAssetStatus(ctx, assets[i].ID, domain.AssetStatusMissing); err != nil {
				s.warn("assets.status_refresh_failed", map[string]any{"asset_id": string(assets[i].ID)})
				continue
			}
		}
		assets[i].Status = domain.AssetStatusMissing
	}
}

// DeleteProject 删除项目（204）；存在活跃任务时拒绝。
func (s *Service) DeleteProject(ctx context.Context, idem Idem, id string) (Response, error) {
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "projects.delete", idem, 204, func(ctx context.Context, tx *store.Tx) (any, error) {
		if _, err := tx.GetProject(ctx, id); err != nil {
			return nil, err
		}
		active, err := tx.CountActiveTasks(ctx, id)
		if err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, errResourceBusy("project has active tasks")
		}
		if err := tx.DeleteProject(ctx, id); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "project.deleted",
			ProjectID: id,
		}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}
