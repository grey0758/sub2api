package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestKrill403HoldCASIncludesAllRecoveryFences(t *testing.T) {
	for _, rows := range []int64{0, 1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		r := newAccountRepositoryWithSQL(nil, db, nil)
		until := time.Now()
		a := &service.Account{ID: 11, Credentials: map[string]any{"api_key": "test"}, GroupIDs: []int64{8}, TempUnschedulableReason: "old", TempUnschedulableUntil: &until}
		mock.ExpectExec(`(?s)WITH changed AS .*status='active' AND schedulable=true AND credentials=\$4::jsonb.*proxy_id IS NOT DISTINCT FROM \$5.*temp_unschedulable_reason=\$6.*temp_unschedulable_until IS NOT DISTINCT FROM \$7.*account_groups.*\$8::bigint\[\].*INSERT INTO scheduler_outbox`).WithArgs("", nil, int64(11), `{"api_key":"test"}`, nil, "old", until, "{8}").WillReturnResult(sqlmock.NewResult(0, rows))
		applied, err := r.CompareAndSwapKrill403Hold(context.Background(), a, "", nil)
		require.NoError(t, err)
		require.Equal(t, rows == 1, applied)
		require.NoError(t, mock.ExpectationsWereMet())
		db.Close()
	}
}
