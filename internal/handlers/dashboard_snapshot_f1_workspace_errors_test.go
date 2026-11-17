package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"html/template"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func snapshotF1ReadTarget(q, target string) bool {
	if target == "invoices" {
		return strings.Contains(q, "ORDER BY i.id") && strings.Contains(q, "i.due_date_civil")
	}
	return strings.Contains(q, "LEFT JOIN recurring_series rs ON rs.id = t.series_id") && strings.Contains(q, "ORDER BY t.id")
}

func seedSnapshotF1Reads(t *testing.T, db *sql.DB) (f1Base, f1Base) {
	t.Helper()
	a := seedF1Base(t, db, "p2b-a")
	b := seedF1Base(t, db, "p2b-b")
	for i := 1; i <= 2; i++ {
		addF1Invoice(t, db, fmt.Sprintf("p2b-invoice-%d", i), a.card, fmt.Sprintf("2026-%02d", i), "PAID", 0, projectionTestNow().Unix())
		addF1Tx(t, db, f1TxSeed{ID: fmt.Sprintf("p2b-expense-%d", i), WorkspaceID: a.ws, UserID: a.user, AccountID: a.checking, Amount: int64(i * 1000), DueDate: projectionTestNow().Unix(), CreatedAt: projectionTestNow().Unix()})
	}
	addF1Tx(t, db, f1TxSeed{ID: "p2b-foreign", WorkspaceID: b.ws, UserID: b.user, AccountID: b.checking, Amount: 987654, DueDate: projectionTestNow().Unix(), CreatedAt: projectionTestNow().Unix()})
	return a, b
}

func checkSnapshotHTTPReadResult(t *testing.T, db *sql.DB, base f1Base, failure bool, reset func()) {
	t.Helper()
	h := DashboardV2Handler{DB: db, UserID: base.user, WorkspaceID: base.ws, Templates: template.Must(template.New("dashboard-preview-page").Parse(`FINANCIAL-RENDER business={{.IsBusiness}} {{.Health.SavingsRateLabel}}`))}
	for _, hx := range []bool{false, true} {
		reset()
		r := httptest.NewRequest("GET", "/", nil)
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if failure {
			if w.Code != 503 || w.Body.String() != "dashboard snapshot unavailable\n" {
				t.Errorf("hx=%v: status=%d body=%q, want 503 without financial render", hx, w.Code, w.Body.String())
			}
			if w.Header().Get("HX-Reswap") != "" || w.Header().Get("HX-Retarget") != "" {
				t.Error("failure overrides HTMX no-swap contract")
			}
		} else if w.Code != 200 || !strings.Contains(w.Body.String(), "FINANCIAL-RENDER") {
			t.Errorf("valid hx=%v: status=%d body=%q", hx, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || db.Stats().InUse != 0 {
			t.Error("response cached or reader connection retained")
		}
	}
}

func TestDashboardSnapshotF1ReadFailures(t *testing.T) {
	for _, target := range []string{"invoices", "expenses"} {
		for _, fault := range []string{"query", "scan", "next", "close", "cancel"} {
			for _, at := range []int{1, 2} {
				t.Run(fmt.Sprintf("%s/%s/path-%d", target, fault, at), func(t *testing.T) {
					db, writer, probe := dashboardReadFaultDB(t)
					a, _ := seedSnapshotF1Reads(t, writer)
					baseline, err := buildDashboardV2Snapshot(context.Background(), db, a.user, a.ws, projectionTestNow(), "6m", "", false)
					if err != nil || !baseline.Balance.ObligationsAvailable || baseline.Balance.Obligations.rawCents() != 3000 {
						t.Fatalf("bad F1 baseline: obligations=%+v err=%v", baseline.Balance, err)
					}
					hits, injectAt := 0, at
					armed := false
					reset := func() { hits, armed = 0, false }
					probe.before = func(q string, args []driver.NamedValue) error {
						if snapshotF1ReadTarget(q, target) {
							if len(args) == 0 || args[0].Value != a.ws {
								t.Error("F1 query lost workspace binding")
							}
							hits++
							armed = hits == injectAt
							if armed && fault == "cancel" {
								return context.Canceled
							}
							if armed && fault == "query" {
								return errN24Discovery
							}
						}
						return nil
					}
					probe.rowFault = func(q string, _ []driver.NamedValue) string {
						if snapshotF1ReadTarget(q, target) && armed {
							return fault
						}
						return ""
					}
					data, err := buildDashboardV2Snapshot(context.Background(), db, a.user, a.ws, projectionTestNow(), "6m", "", false)
					if hits < at || err == nil || !reflect.ValueOf(data).IsZero() {
						t.Errorf("path=%d hits=%d err=%v; technical failure returned financial data", at, hits, err)
					}
					cause := errN24Discovery
					if fault == "cancel" {
						cause = context.Canceled
					}
					if fault != "scan" && !errors.Is(err, cause) {
						t.Errorf("technical cause discarded: %v", err)
					}
					checkSnapshotHTTPReadResult(t, db, a, true, reset)
					if probe.activeTx != 0 || probe.activeRows != 0 || probe.begins != probe.rollbacks+probe.commits {
						t.Fatal("failed F1 snapshot retained rows or transaction")
					}

					// Outside the strict snapshot, preserve the existing loader and
					// standalone projection contracts for shared consumers.
					injectAt = 1
					reset()
					tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
					if err != nil {
						t.Fatal(err)
					}
					legacy, legacyErr := loadWorkspaceObligationsF1(context.Background(), tx, a.ws)
					tx.Rollback()
					if legacyErr != nil || len(legacy.Reasons) == 0 {
						t.Fatalf("shared loader contract changed: %+v %v", legacy, legacyErr)
					}
					reset()
					projection, err := BuildDashboardFinancialProjection(context.Background(), db, a.ws, appMonthStartUTC(projectionTestNow()), projectionTestNow())
					if err != nil || projection.ObligationsAvailable {
						t.Fatalf("standalone projection contract changed: available=%v err=%v", projection.ObligationsAvailable, err)
					}
				})
			}
		}
	}
}

func TestDashboardSnapshotF1TransactionFailures(t *testing.T) {
	db, writer, _ := dashboardReadFaultDB(t)
	a := seedF1Base(t, writer, "p2b-tx")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := buildDashboardV2Snapshot(ctx, db, a.user, a.ws, projectionTestNow(), "6m", "", false)
	if !errors.Is(err, context.Canceled) || !reflect.ValueOf(data).IsZero() || db.Stats().InUse != 0 {
		t.Fatalf("canceled snapshot: err=%v pool=%+v", err, db.Stats())
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, err = buildDashboardV2DataFromSnapshot(context.Background(), tx, a.user, a.ws, projectionTestNow(), "6m", "")
	if !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("finished transaction error lost: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = buildDashboardV2Snapshot(context.Background(), db, a.user, a.ws, projectionTestNow(), "6m", "", false)
	if err == nil || !reflect.ValueOf(data).IsZero() {
		t.Fatal("failed BeginTx returned financial data")
	}
	checkSnapshotHTTPReadResult(t, db, a, true, func() {})
}

func TestDashboardSnapshotWorkspaceTypeAndDomainAvailability(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, scenario := range []string{"valid", "domain-unavailable", "scan", "query"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				db, writer, probe := dashboardReadFaultDB(t)
				a, b := seedSnapshotF1Reads(t, writer)
				execTestSQL(t, writer, `UPDATE workspaces SET type=? WHERE id=?`, kind, a.ws)
				if scenario == "domain-unavailable" {
					execTestSQL(t, writer, `UPDATE transactions SET destination_account_id=? WHERE workspace_id=? AND id='p2b-expense-1'`, a.card, a.ws)
				}
				hits := 0
				typeFailed, inferredHealthReads := false, 0
				probe.before = func(q string, args []driver.NamedValue) error {
					if typeFailed && strings.Contains(q, "SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME'") && args[0].Value == a.ws {
						inferredHealthReads++
					}
					if q == `SELECT COALESCE(type, 'personal') FROM workspaces WHERE id = ?` && args[0].Value == a.ws {
						hits++
						if scenario == "query" {
							typeFailed = true
							return errN24Discovery
						}
					}
					return nil
				}
				probe.rowFault = func(q string, args []driver.NamedValue) string {
					if scenario == "scan" && q == `SELECT COALESCE(type, 'personal') FROM workspaces WHERE id = ?` && args[0].Value == a.ws {
						typeFailed = true
						return "scan"
					}
					return ""
				}
				failure := scenario == "scan" || scenario == "query"
				data, err := buildDashboardV2Snapshot(context.Background(), db, a.user, a.ws, projectionTestNow(), "6m", "", false)
				if failure {
					if err == nil || !reflect.ValueOf(data).IsZero() {
						t.Errorf("workspace failure returned health with inferred type: business=%v health=%+v err=%v", data.IsBusiness, data.Health, err)
					}
				} else if err != nil || data.IsBusiness != (kind == "business") || data.Financial.Error || data.Balance.ObligationsAvailable != (scenario == "valid") {
					t.Fatalf("legitimate workspace/domain result changed: business=%v obligations=%v financial=%+v err=%v", data.IsBusiness, data.Balance.ObligationsAvailable, data.Financial, err)
				}
				checkSnapshotHTTPReadResult(t, db, a, failure, func() {})
				if inferredHealthReads != 0 {
					t.Fatal("health was calculated after the workspace type read failed")
				}
				if hits == 0 {
					t.Fatal("workspace reader not exercised")
				}
				if failure && workspaceType(db, a.ws) != "personal" {
					t.Fatal("legacy workspace fallback changed outside snapshot")
				}
				foreign, err := buildDashboardV2Snapshot(context.Background(), db, b.user, b.ws, projectionTestNow(), "6m", "", false)
				if err != nil || !foreign.Balance.ObligationsAvailable || foreign.Balance.Obligations.rawCents() != 987654 {
					t.Fatalf("other workspace affected: obligations=%+v err=%v", foreign.Balance, err)
				}
				if probe.activeTx != 0 || probe.activeRows != 0 || db.Stats().InUse != 0 {
					t.Fatal("workspace read retained rows or transaction")
				}
			})
		}
	}
}
