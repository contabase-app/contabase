package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestScheduledPaymentRealBusySnapshot(t *testing.T) {
	db, writer := openInvoicePaymentReversalFileDBs(t)
	row, now := seedStaleScheduledPayment(t, db, "personal")
	ok, err := executeScheduledPaymentAttempt(context.Background(), db, row, now, func() error {
		_, err := writer.Exec(`UPDATE transactions SET amount = 20000 WHERE id = ?`, row.ID)
		return err
	})
	if ok {
		t.Fatal("snapshot conflict must not commit")
	}
	requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_BUSY_SNAPSHOT)
	assertScheduledAccountBalances(t, db, 100000, 50000)
	assertScheduledAttemptEffects(t, db, row.ID, false)
}

func TestScheduledPaymentSnapshotRetryContinuesBatch(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, tc := range []struct {
			name, mutation, account, typ string
			amount, checking, extra      int64
			paid                         bool
		}{
			{"amount", `amount = 20000`, "checking-test", "EXPENSE", 20000, 80100, 50000, true},
			{"account", `account_id = 'checking-extra'`, "checking-extra", "EXPENSE", 10000, 100100, 40000, true},
			{"type", `type = 'INCOME'`, "checking-test", "INCOME", 10000, 110100, 50000, true},
			{"all", `amount = 20000, account_id = 'checking-extra', type = 'INCOME'`, "checking-extra", "INCOME", 20000, 100100, 70000, true},
			{"cancel", `scheduled_payment_at = NULL`, "", "", 0, 100100, 50000, false},
			{"reschedule", `scheduled_payment_at = scheduled_payment_at + 86400`, "", "", 0, 100100, 50000, false},
			{"card", `account_id = 'card-test'`, "", "", 0, 100100, 50000, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				db, writer := openInvoicePaymentReversalFileDBs(t)
				row, now := seedStaleScheduledPayment(t, db, kind)
				seedFollowingScheduledPayment(t, db, row)
				execTestSQL(t, db, `INSERT INTO workspaces (id, name) VALUES ('ws-foreign', 'Foreign')`)
				execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, current_balance) VALUES ('foreign-account', 'ws-foreign', 'Foreign', 'CHECKING', 777)`)
				foreignCandidate := row
				foreignCandidate.WorkspaceID = "ws-foreign"
				if ok, err := executeScheduledPaymentWithRetry(context.Background(), db, foreignCandidate, now, func() error {
					t.Fatal("foreign candidate passed scoped reload")
					return nil
				}); err != nil || ok {
					t.Fatalf("foreign eligible lookup: %t %v", ok, err)
				}
				attempts := 0
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				execute := func(ctx context.Context, db *sql.DB, candidate scheduledPaymentDueRow, now time.Time) (bool, error) {
					if candidate.ID != row.ID {
						return executeScheduledPayment(ctx, db, candidate, now)
					}
					return executeScheduledPaymentWithRetry(ctx, db, candidate, now, func() error {
						attempts++
						if attempts != 1 {
							return nil
						}
						// The hook runs after the reader establishes its snapshot.
						// Exec on the second physical connection commits before return.
						_, err := writer.ExecContext(ctx, `UPDATE transactions SET `+tc.mutation+`, description = 'Current description' WHERE id = ? AND workspace_id = ?`, row.ID, row.WorkspaceID)
						return err
					})
				}
				count, err := runDueScheduledPayments(ctx, db, now, execute)
				wantCount, wantAttempts := 1, 1
				if tc.paid {
					wantCount, wantAttempts = 2, 2
				}
				if err != nil || count != wantCount || attempts != wantAttempts {
					t.Fatalf("count=%d attempts=%d error=%v", count, attempts, err)
				}
				assertScheduledAccountBalances(t, db, tc.checking, tc.extra)
				assertScheduledAttemptEffects(t, db, row.ID, tc.paid)
				assertScheduledAttemptEffects(t, db, "following-scheduled", true)
				if tc.paid {
					var amount int64
					var account, typ, title, message string
					if err := db.QueryRow(`SELECT amount, account_id, type FROM transactions WHERE id = ? AND workspace_id = ?`, row.ID, row.WorkspaceID).Scan(&amount, &account, &typ); err != nil {
						t.Fatal(err)
					}
					if amount != tc.amount || account != tc.account || typ != tc.typ {
						t.Fatalf("stale payload: %d %s %s", amount, account, typ)
					}
					if err := db.QueryRow(`SELECT title, message FROM user_notifications WHERE dedupe_key = ? AND workspace_id = ?`, "scheduled_payment:"+row.ID+":"+strconv.FormatInt(row.ScheduledAt, 10), row.WorkspaceID).Scan(&title, &message); err != nil {
						t.Fatal(err)
					}
					wantTitle := "Pagamento automático executado"
					if tc.typ == "INCOME" {
						wantTitle = "Recebimento automático executado"
					}
					if title != wantTitle || !strings.Contains(message, "Current description") {
						t.Fatalf("stale notification: %s %s", title, message)
					}
				}
				if count, err := RunDueScheduledPayments(ctx, db, now); err != nil || count != 0 {
					t.Fatalf("duplicate run: %d %v", count, err)
				}
				assertScheduledAccountBalances(t, db, tc.checking, tc.extra)
				foreign := row
				foreign.WorkspaceID = "ws-foreign"
				if ok, err := executeScheduledPayment(ctx, db, foreign, now); err != nil || ok {
					t.Fatalf("foreign lookup: %t %v", ok, err)
				}
				var foreignBalance, foreignEffects int64
				if err := db.QueryRow(`SELECT current_balance, (SELECT COUNT(*) FROM transaction_status_history WHERE workspace_id = 'ws-foreign') + (SELECT COUNT(*) FROM user_notifications WHERE workspace_id = 'ws-foreign') FROM accounts WHERE id = 'foreign-account'`).Scan(&foreignBalance, &foreignEffects); err != nil || foreignBalance != 777 || foreignEffects != 0 {
					t.Fatalf("foreign effects: %d %d %v", foreignBalance, foreignEffects, err)
				}
			})
		}
	}
}

func TestScheduledPaymentConcurrentExecutorPaysOnce(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			db, writer := openInvoicePaymentReversalFileDBs(t)
			row, now := seedStaleScheduledPayment(t, db, kind)
			seedFollowingScheduledPayment(t, db, row)
			attempts := 0
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			count, err := runDueScheduledPayments(ctx, db, now, func(ctx context.Context, db *sql.DB, candidate scheduledPaymentDueRow, now time.Time) (bool, error) {
				if candidate.ID != row.ID {
					return executeScheduledPayment(ctx, db, candidate, now)
				}
				return executeScheduledPaymentWithRetry(ctx, db, candidate, now, func() error {
					attempts++
					ok, err := executeScheduledPayment(ctx, writer, candidate, now)
					if err != nil || !ok {
						t.Fatalf("competing executor: %t %v", ok, err)
					}
					return nil
				})
			})
			if err != nil || count != 1 || attempts != 1 {
				t.Fatalf("count=%d attempts=%d error=%v", count, attempts, err)
			}
			assertScheduledAccountBalances(t, db, 90100, 50000)
			assertScheduledAttemptEffects(t, db, row.ID, true)
			assertScheduledAttemptEffects(t, db, "following-scheduled", true)
			if count, err := RunDueScheduledPayments(ctx, db, now); err != nil || count != 0 {
				t.Fatalf("duplicate run: %d %v", count, err)
			}
			assertScheduledAccountBalances(t, db, 90100, 50000)
			assertScheduledAttemptEffects(t, db, row.ID, true)
		})
	}
}

func TestScheduledPaymentSnapshotRetryBoundedAndFailFast(t *testing.T) {
	for _, mode := range []string{"repeated-snapshot", "history-failure", "cancelled-context"} {
		t.Run(mode, func(t *testing.T) {
			db, writer := openInvoicePaymentReversalFileDBs(t)
			row, now := seedStaleScheduledPayment(t, db, "business")
			seedFollowingScheduledPayment(t, db, row)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "history-failure" {
				execTestSQL(t, db, `CREATE TRIGGER reject_history BEFORE INSERT ON transaction_status_history BEGIN SELECT RAISE(ABORT, 'persistence failure'); END`)
			}
			attempts := 0
			count, err := runDueScheduledPayments(ctx, db, now, func(ctx context.Context, db *sql.DB, candidate scheduledPaymentDueRow, now time.Time) (bool, error) {
				return executeScheduledPaymentWithRetry(ctx, db, candidate, now, func() error {
					attempts++
					if mode == "history-failure" {
						return nil
					}
					_, err := writer.ExecContext(ctx, `UPDATE transactions SET amount = amount + 1 WHERE id = ?`, row.ID)
					if mode == "cancelled-context" {
						cancel()
					}
					return err
				})
			})
			if count != 0 || err == nil {
				t.Fatalf("expected fail-fast: %d %v", count, err)
			}
			wantAttempts := 1
			if mode == "repeated-snapshot" {
				wantAttempts = 3
				requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_BUSY_SNAPSHOT)
			}
			if mode == "cancelled-context" && !errors.Is(err, context.Canceled) {
				t.Fatalf("context error = %v", err)
			}
			if attempts != wantAttempts {
				t.Fatalf("attempts=%d want=%d", attempts, wantAttempts)
			}
			assertScheduledAttemptEffects(t, db, row.ID, false)
			assertScheduledAttemptEffects(t, db, "following-scheduled", false)
			assertScheduledAccountBalances(t, db, 100000, 50000)
		})
	}
}

func seedFollowingScheduledPayment(t *testing.T, db *sql.DB, row scheduledPaymentDueRow) {
	t.Helper()
	insertScheduledPaymentTestTransaction(t, db, "following-scheduled", "INCOME", "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET amount = 100, scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-07-18', created_at = (SELECT created_at + 1 FROM transactions WHERE id = ?) WHERE id = 'following-scheduled'`, row.ScheduledAt, row.ID)
}

func assertScheduledAttemptEffects(t *testing.T, db *sql.DB, id string, paid bool) {
	t.Helper()
	var status string
	var executed, paidAt sql.NullInt64
	var history, notifications int
	if err := db.QueryRow(`SELECT status, scheduled_payment_executed_at, paid_at,
		(SELECT COUNT(*) FROM transaction_status_history WHERE transaction_id = t.id AND workspace_id = t.workspace_id),
		(SELECT COUNT(*) FROM user_notifications WHERE dedupe_key = 'scheduled_payment:' || t.id || ':' || t.scheduled_payment_at AND workspace_id = t.workspace_id)
		FROM transactions t WHERE id = ?`, id).Scan(&status, &executed, &paidAt, &history, &notifications); err != nil {
		t.Fatal(err)
	}
	wantStatus, wantEffects := "pending", 0
	if paid {
		wantStatus, wantEffects = "paid", 1
	}
	if status != wantStatus || executed.Valid != paid || paidAt.Valid != paid || history != wantEffects || notifications != wantEffects {
		t.Fatalf("%s effects: status=%s executed=%v paidAt=%v history=%d notifications=%d", id, status, executed, paidAt, history, notifications)
	}
}
