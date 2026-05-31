//go:build integration

package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/model"
)

func makeDeposit(chainID int64, tx string, logIndex int) model.Deposit {
	return model.Deposit{
		ChainID:  chainID,
		TxHash:   tx,
		LogIndex: logIndex,
		Amount:   "1.0",
		Symbol:   "ETH",
		Status:   model.StatusConfirmed,
	}
}

func TestSaveAndAdvance_AtomicityCommit(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	deposits := []model.Deposit{
		makeDeposit(1, "0xaaa", 0),
		makeDeposit(1, "0xbbb", 1),
	}
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, deposits))

	// 둘 다 INSERT 되어야 하고 cursor도 100
	pending, err := repo.Pending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	cursor, err := repo.Cursor(ctx, 1, "log")
	require.NoError(t, err)
	require.EqualValues(t, 100, cursor)
}

func TestSaveAndAdvance_EmptyDepositsAdvancesCursor(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// 빈 deposits로도 커서 전진해야 함 (빈 블록 처리)
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 500, nil))

	cursor, err := repo.Cursor(ctx, 1, "log")
	require.NoError(t, err)
	require.EqualValues(t, 500, cursor)

	pending, err := repo.Pending(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestSaveAndAdvance_UniqueConflictNoOp(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// 동일 (chain, tx, logIndex)를 두 번 SaveAndAdvance — UNIQUE로 두 번째 INSERT skip
	d := makeDeposit(1, "0xdupe", 5)
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, []model.Deposit{d}))
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 101, []model.Deposit{d}))

	// 1건만 존재
	pending, err := repo.Pending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// 커서는 두 번째 호출의 101로 전진
	cursor, err := repo.Cursor(ctx, 1, "log")
	require.NoError(t, err)
	require.EqualValues(t, 101, cursor)
}

func TestSaveAndAdvance_PerScannerCursorIndependent(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, nil))
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "trace", 200, nil))

	logCursor, err := repo.Cursor(ctx, 1, "log")
	require.NoError(t, err)
	require.EqualValues(t, 100, logCursor)

	traceCursor, err := repo.Cursor(ctx, 1, "trace")
	require.NoError(t, err)
	require.EqualValues(t, 200, traceCursor)
}

func TestSaveAndAdvance_PerChainIndependent(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, []model.Deposit{makeDeposit(1, "0xa", 0)}))
	require.NoError(t, repo.SaveAndAdvance(ctx, 2, "log", 100, []model.Deposit{makeDeposit(2, "0xa", 0)}))

	// 같은 (tx, logIdx)지만 다른 chain → 둘 다 저장
	p1, _ := repo.Pending(ctx, 1)
	p2, _ := repo.Pending(ctx, 2)
	require.Len(t, p1, 1)
	require.Len(t, p2, 1)
}

// 동시 SaveAndAdvance — 서로 다른 (chain, scanner)면 충돌 없음
func TestSaveAndAdvance_ConcurrentDifferentKeys(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	const goroutines = 5
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			chain := int64(idx + 1)
			d := makeDeposit(chain, "0xconc", 0)
			errs <- repo.SaveAndAdvance(ctx, chain, "log", 100, []model.Deposit{d})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// 모든 chain별 cursor가 정확히 100
	for i := 0; i < goroutines; i++ {
		cursor, err := repo.Cursor(ctx, int64(i+1), "log")
		require.NoError(t, err)
		require.EqualValues(t, 100, cursor)
	}
}

func TestAck_RemovesRow(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, []model.Deposit{
		makeDeposit(1, "0xaaa", 0),
		makeDeposit(1, "0xbbb", 1),
	}))

	require.NoError(t, repo.Ack(ctx, 1, "0xaaa", 0))

	pending, err := repo.Pending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "0xbbb", pending[0].TxHash)
}

func TestAck_NoErrorOnUnknown(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// 존재하지 않는 row에 Ack — 에러 없이 통과
	require.NoError(t, repo.Ack(ctx, 1, "0xnone", 0))
}

func TestPendingInRange_FiltersByBlock(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// 블록 100/200/300 에 각각 1건 적재
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, []model.Deposit{makeDeposit(1, "0xaaa", 0)}))
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 200, []model.Deposit{makeDeposit(1, "0xbbb", 1)}))
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 300, []model.Deposit{makeDeposit(1, "0xccc", 2)}))

	tests := []struct {
		name      string
		from, to  uint64
		wantCount int
	}{
		{"단일 블록 매칭", 200, 200, 1},
		{"범위 매칭", 100, 200, 2},
		{"전 범위", 50, 350, 3},
		{"범위 외", 1000, 2000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.PendingInRange(ctx, 1, tt.from, tt.to)
			require.NoError(t, err)
			require.Len(t, got, tt.wantCount)
		})
	}
}

func TestPendingAll_LimitOrdered(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", uint64(100+i), []model.Deposit{
			makeDeposit(1, "tx", i),
		}))
	}

	all, err := repo.PendingAll(ctx, 100)
	require.NoError(t, err)
	require.Len(t, all, 10)

	limited, err := repo.PendingAll(ctx, 3)
	require.NoError(t, err)
	require.Len(t, limited, 3)
	// id 오름차순 — 첫 3개는 logIndex 0,1,2
	for i := 0; i < 3; i++ {
		require.Equal(t, i, limited[i].LogIndex)
	}
}

func TestCursor_DefaultZeroForNewChain(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	cursor, err := repo.Cursor(ctx, 999, "log")
	require.NoError(t, err)
	require.EqualValues(t, 0, cursor)
}

func TestSaveAndAdvance_NotifyOnNonEmpty(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// LISTEN deposits 등록 (별도 conn)
	conn, err := testPool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "LISTEN deposits")
	require.NoError(t, err)

	// 빈 deposits → NOTIFY 없어야 함 (짧은 타임아웃)
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, nil))
	emptyCtx, emptyCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = conn.Conn().WaitForNotification(emptyCtx)
	emptyCancel()
	require.Error(t, err, "빈 deposits는 NOTIFY 안 보내야 함")

	// 비빈 deposits → NOTIFY 발송
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 101, []model.Deposit{makeDeposit(1, "0xaaa", 0)}))
	notifyCtx, notifyCancel := context.WithTimeout(ctx, 2*time.Second)
	notif, err := conn.Conn().WaitForNotification(notifyCtx)
	notifyCancel()
	require.NoError(t, err)
	require.Equal(t, "deposits", notif.Channel)
}

func TestStats_EmptyAndWithData(t *testing.T) {
	cleanDB(t)
	repo := NewBufferRepo(testPool)
	ctx := context.Background()

	// 빈 상태
	st, err := repo.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, st.PendingCount)
	require.EqualValues(t, 0, st.OldestAgeSeconds)

	// 데이터 적재 후
	require.NoError(t, repo.SaveAndAdvance(ctx, 1, "log", 100, []model.Deposit{
		makeDeposit(1, "0xaaa", 0),
		makeDeposit(1, "0xbbb", 1),
	}))

	st, err = repo.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, st.PendingCount)
	require.GreaterOrEqual(t, st.OldestAgeSeconds, 0.0)
}
