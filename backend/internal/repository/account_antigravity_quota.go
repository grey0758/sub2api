package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ReconcileAntigravityQuota uses optimistic concurrency so a 429 arriving during
// a metadata read cannot be erased by its older successful quota snapshot.
func (r *accountRepository) ReconcileAntigravityQuota(ctx context.Context, id int64, expected, next map[string]any, separate bool) (bool, error) {
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.ReconcileAntigravityQuota(ctx, id, expected, next, separate)
	}
	oldJSON, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	flagJSON, err := json.Marshal(separate)
	if err != nil {
		return false, err
	}
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, `UPDATE accounts SET extra =
 jsonb_set(jsonb_set(COALESCE(extra, '{}'::jsonb), '{model_rate_limits}', $1::jsonb, true),
 '{antigravity_separate_argon_quota}', $2::jsonb, true), updated_at = NOW()
 WHERE id = $3 AND deleted_at IS NULL AND platform = 'antigravity' AND status = 'active' AND schedulable = true
 AND COALESCE(extra->'model_rate_limits', '{}'::jsonb) = $4::jsonb`, string(nextJSON), string(flagJSON), id, string(oldJSON))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return true, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}
