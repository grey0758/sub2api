package repository

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Claim/release only the exact still-active hold and credential identity observed
// by the worker. Other runtime limits, disabled flags and ledger rows are never
// cleared. The outbox event is in the same SQL statement as the state change.
func (r *accountRepository) CompareAndSwapKrill403Hold(ctx context.Context, a *service.Account, reason string, until *time.Time) (bool, error) {
	credentials, err := json.Marshal(a.Credentials)
	if err != nil {
		return false, err
	}
	groups := append([]int64(nil), a.GroupIDs...)
	if len(groups) == 0 {
		for _, binding := range a.AccountGroups {
			groups = append(groups, binding.GroupID)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	result, err := r.sql.ExecContext(ctx, `
	WITH changed AS (
	 UPDATE accounts SET temp_unschedulable_reason=NULLIF($1,''),temp_unschedulable_until=$2,updated_at=NOW()
	 WHERE id=$3 AND deleted_at IS NULL AND platform='openai' AND type='apikey'
	 AND status='active' AND schedulable=true AND credentials=$4::jsonb
	 AND proxy_id IS NOT DISTINCT FROM $5
	 AND temp_unschedulable_reason=$6
	 AND temp_unschedulable_until IS NOT DISTINCT FROM $7
	 AND ARRAY(SELECT group_id FROM account_groups WHERE account_id=$3 ORDER BY group_id)=$8::bigint[]
	 AND (expires_at IS NULL OR expires_at>NOW())
	 RETURNING id
	)
	INSERT INTO scheduler_outbox(event_type,account_id,created_at)
	SELECT 'account_changed',id,NOW() FROM changed
	`, reason, until, a.ID, string(credentials), a.ProxyID, a.TempUnschedulableReason, a.TempUnschedulableUntil, pq.Array(groups))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		r.syncSchedulerAccountSnapshotDetached(ctx, a.ID)
	}
	return n == 1, nil
}
