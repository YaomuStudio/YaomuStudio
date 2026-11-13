package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const assetSelect = `
	SELECT a.id, a.project_id, a.type, a.role, a.language, a.run_id,
		a.name, a.size_bytes, a.sha256, a.meta_json,
		a.status, a.imported_at,
		(SELECT l.path FROM asset_locations l WHERE l.asset_id = a.id
			ORDER BY l.created_at, l.id LIMIT 1)
	FROM assets a`

// CreateAsset 登记资产元数据；文件本身不写入 SQLite。
func (q *querier) CreateAsset(ctx context.Context, a domain.Asset) error {
	if a.ID == "" || a.ProjectID == "" || a.Name == "" {
		return fmt.Errorf("%w: asset id/project_id/name required", domain.ErrInvalidRequest)
	}
	if !a.Type.Valid() {
		return fmt.Errorf("%w: invalid asset type %q", domain.ErrInvalidRequest, a.Type)
	}
	if !a.Role.Valid() {
		return fmt.Errorf("%w: invalid asset role %q", domain.ErrInvalidRequest, a.Role)
	}
	if !a.Status.Valid() {
		return fmt.Errorf("%w: invalid asset status %q", domain.ErrInvalidRequest, a.Status)
	}
	if a.ImportedAt.IsZero() {
		return fmt.Errorf("%w: asset imported_at required", domain.ErrInvalidRequest)
	}
	var meta any
	if a.Meta != nil {
		m, err := marshalJSON(a.Meta)
		if err != nil {
			return err
		}
		meta = m
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO assets
			(id, project_id, type, role, language, run_id, name, size_bytes, sha256, meta_json, status, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, string(a.Type), nullString(string(a.Role)), nullString(a.Language),
		nullString(string(a.RunID)), a.Name, a.SizeBytes, a.SHA256, meta,
		string(a.Status), formatTime(a.ImportedAt),
	)
	if err != nil {
		return fmt.Errorf("insert asset: %w", err)
	}
	return nil
}

// GetAsset 读取单个资产；Path 取第一条登记位置用于展示。
func (q *querier) GetAsset(ctx context.Context, id domain.ID) (domain.Asset, error) {
	row := q.db.QueryRowContext(ctx, assetSelect+` WHERE a.id = ?`, id)
	a, err := scanAsset(row)
	if err != nil {
		return domain.Asset{}, wrapNotFound(err)
	}
	return a, nil
}

// ListProjectAssets 列出项目资产。
func (q *querier) ListProjectAssets(ctx context.Context, projectID domain.ID) ([]domain.Asset, error) {
	rows, err := q.db.QueryContext(ctx, assetSelect+` WHERE a.project_id = ? ORDER BY a.imported_at, a.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project assets: %w", err)
	}
	defer rows.Close()

	assets := make([]domain.Asset, 0)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return assets, nil
}

func scanAsset(row rowScanner) (domain.Asset, error) {
	var (
		a          domain.Asset
		assetType  string
		role       sql.NullString
		language   sql.NullString
		runID      sql.NullString
		status     string
		meta       sql.NullString
		importedAt string
		path       sql.NullString
	)
	if err := row.Scan(&a.ID, &a.ProjectID, &assetType, &role, &language, &runID,
		&a.Name, &a.SizeBytes, &a.SHA256, &meta, &status, &importedAt, &path); err != nil {
		return domain.Asset{}, err
	}
	a.Type = domain.AssetType(assetType)
	if !a.Type.Valid() {
		return domain.Asset{}, fmt.Errorf("asset %s has invalid type %q", a.ID, assetType)
	}
	if role.Valid {
		a.Role = domain.AssetRole(role.String)
	}
	if !a.Role.Valid() {
		return domain.Asset{}, fmt.Errorf("asset %s has invalid role %q", a.ID, role.String)
	}
	if language.Valid {
		a.Language = language.String
	}
	if runID.Valid {
		a.RunID = runID.String
	}
	a.Status = domain.AssetStatus(status)
	if !a.Status.Valid() {
		return domain.Asset{}, fmt.Errorf("asset %s has invalid status %q", a.ID, status)
	}
	var err error
	if a.ImportedAt, err = parseTime(importedAt); err != nil {
		return domain.Asset{}, err
	}
	if path.Valid {
		a.Path = path.String
	}
	if meta.Valid {
		var m domain.MediaMeta
		if err := unmarshalNull(meta, &m); err != nil {
			return domain.Asset{}, err
		}
		a.Meta = &m
	}
	return a, nil
}

// SetAssetStatus 更新资产可用状态。
func (q *querier) SetAssetStatus(ctx context.Context, id domain.ID, status domain.AssetStatus) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid asset status %q", domain.ErrInvalidRequest, status)
	}
	res, err := q.db.ExecContext(ctx, `UPDATE assets SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("update asset status: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// SetAssetMeta 更新探测得到的媒体元数据与文件大小（重定位后刷新）。
func (q *querier) SetAssetMeta(ctx context.Context, id domain.ID, meta *domain.MediaMeta, size int64) error {
	var encoded any
	if meta != nil {
		m, err := marshalJSON(meta)
		if err != nil {
			return err
		}
		encoded = m
	}
	res, err := q.db.ExecContext(ctx, `
		UPDATE assets SET meta_json = ?, size_bytes = ? WHERE id = ?`, encoded, size, id)
	if err != nil {
		return fmt.Errorf("update asset meta: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// RelocateAssetLocation 把外部引用位置指向新路径并标记可用；
// 只更新既有位置，不新增资产行，资产 ID/项目/轨道引用保持不变。
func (q *querier) RelocateAssetLocation(ctx context.Context, locationID domain.ID, path string) error {
	res, err := q.db.ExecContext(ctx, `
		UPDATE asset_locations SET path = ?, available = 1 WHERE id = ?`, path, locationID)
	if err != nil {
		return fmt.Errorf("relocate asset location: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// AddAssetLocation 登记资产的一个文件位置（原件引用与应用副本分别标记）。
func (q *querier) AddAssetLocation(ctx context.Context, loc domain.AssetLocation) error {
	if loc.ID == "" || loc.AssetID == "" || loc.Path == "" {
		return fmt.Errorf("%w: location id/asset_id/path required", domain.ErrInvalidRequest)
	}
	if loc.Source == "" || loc.Owner == "" {
		return fmt.Errorf("%w: location source/owner required", domain.ErrInvalidRequest)
	}
	if loc.CreatedAt.IsZero() {
		return fmt.Errorf("%w: location created_at required", domain.ErrInvalidRequest)
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO asset_locations (id, asset_id, path, source, owner, available, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		loc.ID, loc.AssetID, loc.Path, loc.Source, loc.Owner, boolInt(loc.Available),
		formatTime(loc.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert asset location: %w", err)
	}
	return nil
}

// ListAssetLocations 返回资产的已登记位置。
func (q *querier) ListAssetLocations(ctx context.Context, assetID domain.ID) ([]domain.AssetLocation, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, asset_id, path, source, owner, available, created_at
		FROM asset_locations WHERE asset_id = ? ORDER BY created_at, id`, assetID)
	if err != nil {
		return nil, fmt.Errorf("query asset locations: %w", err)
	}
	defer rows.Close()

	locations := make([]domain.AssetLocation, 0)
	for rows.Next() {
		var (
			loc       domain.AssetLocation
			available int
			created   string
		)
		if err := rows.Scan(&loc.ID, &loc.AssetID, &loc.Path, &loc.Source, &loc.Owner,
			&available, &created); err != nil {
			return nil, fmt.Errorf("scan asset location: %w", err)
		}
		loc.Available = available != 0
		t, err := parseTime(created)
		if err != nil {
			return nil, err
		}
		loc.CreatedAt = t
		locations = append(locations, loc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset locations: %w", err)
	}
	return locations, nil
}

// SetAssetLocationAvailable 标记位置是否可用（原件移动/损坏时更新）。
func (q *querier) SetAssetLocationAvailable(ctx context.Context, locationID domain.ID, available bool) error {
	res, err := q.db.ExecContext(ctx, `
		UPDATE asset_locations SET available = ? WHERE id = ?`, boolInt(available), locationID)
	if err != nil {
		return fmt.Errorf("update asset location: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}
