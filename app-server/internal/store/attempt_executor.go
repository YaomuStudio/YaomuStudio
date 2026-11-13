package store

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// BindAttemptExecutor freezes the actual Core epoch and Runner launch identity
// in the dispatch transaction, including attempts queued before a Core restart.
func (tx *Tx) BindAttemptExecutor(ctx context.Context, id domain.ID, epoch int64, executorID string) error {
	result, err := tx.tx.ExecContext(ctx,
		`UPDATE attempts SET epoch = ?, executor_id = ? WHERE id = ?`, epoch, nullString(executorID), id)
	if err != nil {
		return fmt.Errorf("bind attempt executor: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("bind attempt executor count: %w", err)
	}
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}
