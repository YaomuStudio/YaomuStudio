package store

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// AssetsForIntegrityCheck closes the cursor before the caller performs any IO
// or state updates; the Core database has a single connection.
func (q *querier) AssetsForIntegrityCheck(ctx context.Context) ([]domain.Asset, error) {
	rows, err := q.db.QueryContext(ctx, assetSelect)
	if err != nil {
		return nil, fmt.Errorf("list assets for integrity check: %w", err)
	}
	defer rows.Close()
	assets := []domain.Asset{}
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, nil
}
