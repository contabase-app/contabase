package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestScheduledPaymentExecutorDoneWaitsForActiveRun(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	done := startScheduledPaymentExecutor(ctx, db, time.Hour, func(context.Context, *sql.DB, time.Time) {
		close(entered)
		<-release
	})
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatalf("executor finished before active run returned")
	default:
	}
	close(release)
	<-done
}
