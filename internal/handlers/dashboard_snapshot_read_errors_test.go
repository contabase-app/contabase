package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"html/template"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type dashboardReadFaultConnector struct {
	base  driver.Driver
	dsn   string
	probe *n24Probe
}

func (c dashboardReadFaultConnector) Driver() driver.Driver { return c.base }
func (c dashboardReadFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &n24Conn{Conn: conn, probe: c.probe}, nil
}

func dashboardReadFaultDB(t *testing.T) (*sql.DB, *sql.DB, *n24Probe) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "read-errors.db")
	pools := openLockingRuntimeDBsAt(t, path, "")
	registration, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	base := registration.Driver()
	registration.Close()
	probe := &n24Probe{}
	db := sql.OpenDB(dashboardReadFaultConnector{base: base, dsn: "file:" + path, probe: probe})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, pools.DB, probe
}
func dashboardReadTarget(query, target string) bool {
	switch target {
	case "accounts":
		return strings.Contains(query, "a.current_balance") && strings.Contains(query, "ORDER BY a.sort_order ASC")
	case "limits":
		return strings.Contains(query, "FROM cost_limits cl")
	}
	return false
}

func TestDashboardSnapshotReadErrors(t *testing.T) {
	for _, target := range []string{"accounts", "limits"} {
		for _, fault := range []string{"next", "scan", "query", "close", "empty", "complete"} {
			t.Run(target+"/"+fault, func(t *testing.T) {
				db, writer, probe := dashboardReadFaultDB(t)
				n24SeedWorkspace(t, writer, "A")
				n24SeedWorkspace(t, writer, "B")
				execTestSQL(t, writer, `INSERT INTO accounts(id,workspace_id,name,type,current_balance) VALUES('bank-A2','A','Second','CHECKING',20000)`)
				execTestSQL(t, writer, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('unconfigured-card','A','No invoice yet','CREDIT_CARD')`)
				for _, ws := range []string{"A", "B"} {
					for _, suffix := range []string{"1", "2"} {
						id := ws + suffix
						execTestSQL(t, writer, `INSERT INTO categories(id,workspace_id,name,type) VALUES(?,?,?,'EXPENSE')`, id, ws, id)
						execTestSQL(t, writer, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES(?,?,?,10000)`, id, ws, id)
					}
				}
				if fault == "empty" {
					execTestSQL(t, writer, `DELETE FROM cost_limits WHERE workspace_id='A'`)
					execTestSQL(t, writer, `DELETE FROM accounts WHERE workspace_id='A'`)
				}
				hits := 0
				probe.before = func(q string, _ []driver.NamedValue) error {
					if dashboardReadTarget(q, target) {
						hits++
						if fault == "query" {
							return errN24Discovery
						}
					}
					return nil
				}
				probe.rowFault = func(q string, _ []driver.NamedValue) string {
					if dashboardReadTarget(q, target) {
						return fault
					}
					return ""
				}
				wantFailure := fault != "empty" && fault != "complete"
				data, err := buildDashboardV2Snapshot(context.Background(), db, "user-A", "A", projectionTestNow(), "6m", "", false)
				if wantFailure {
					if err == nil || !reflect.ValueOf(data).IsZero() {
						t.Errorf("interrupted %s: accounts=%d limits=%d err=%v; want zero composition and error", target, len(data.Accounts), len(data.Limits), err)
					}
					if fault != "scan" && !errors.Is(err, errN24Discovery) {
						t.Errorf("lost injected error: %v", err)
					}
				} else {
					want := 2
					if fault == "empty" {
						want = 0
					}
					if err != nil || len(data.Accounts) != want || len(data.Limits) != want {
						t.Fatalf("valid %s: accounts=%d limits=%d err=%v", fault, len(data.Accounts), len(data.Limits), err)
					}
					for _, account := range data.Accounts {
						if strings.Contains(account.ID, "B") {
							t.Fatal("foreign account returned")
						}
					}
					for _, limit := range data.Limits {
						if !strings.HasPrefix(limit.CategoryName, "A") {
							t.Fatal("foreign limit returned")
						}
					}
				}
				for _, statement := range probe.statements {
					if statement.inTx && !strings.HasPrefix(strings.TrimSpace(statement.query), "SELECT") && !strings.HasPrefix(strings.TrimSpace(statement.query), "WITH") {
						t.Fatalf("snapshot wrote: %s", statement.query)
					}
				}
				if probe.begins != 1 || probe.rollbacks != 1 || probe.commits != 0 {
					t.Fatalf("snapshot transaction begins/rollbacks/commits=%d/%d/%d", probe.begins, probe.rollbacks, probe.commits)
				}
				handler := DashboardV2Handler{DB: db, UserID: "user-A", WorkspaceID: "A", Templates: template.Must(template.New("dashboard-preview-page").Parse(`FINANCIAL-RENDER {{len .Accounts}} {{len .Limits}}`))}
				for _, hx := range []bool{false, true} {
					for _, month := range []string{"", "2026-01"} {
						request := httptest.NewRequest("GET", "/?mes="+month, nil)
						if hx {
							request.Header.Set("HX-Request", "true")
						}
						recorder := httptest.NewRecorder()
						handler.ServeHTTP(recorder, request)
						wantStatus := 200
						if wantFailure {
							wantStatus = 503
							if recorder.Body.String() != "dashboard snapshot unavailable\n" {
								t.Errorf("partial financial response: %q", recorder.Body.String())
							}
							if recorder.Header().Get("HX-Reswap") != "" || recorder.Header().Get("HX-Retarget") != "" {
								t.Fatal("failure overrides HTMX error handling")
							}
						}
						if recorder.Code != wantStatus || recorder.Header().Get("Cache-Control") != "no-store" {
							t.Errorf("hx=%v month=%q: status=%d want=%d", hx, month, recorder.Code, wantStatus)
						}
					}
					if probe.activeTx != 0 || probe.activeRows != 0 || db.Stats().InUse != 0 || probe.rollbacks+probe.commits != probe.begins {
						t.Fatalf("reader leaked: tx=%d rows=%d pool=%+v begins/rollbacks=%d/%d", probe.activeTx, probe.activeRows, db.Stats(), probe.begins, probe.rollbacks)
					}
				}
				if hits == 0 {
					t.Fatal("target reader was not exercised")
				}
			})
		}
	}
}

func TestDashboardSnapshotFinancialScanErrors(t *testing.T) {
	for _, tc := range []struct {
		name, queryPart string
	}{
		{"monthly-summary", "SUM(CASE WHEN type = 'INCOME'"},
		{"monthly-forecast", "SELECT type AS kind"},
		{"pending-total", "SELECT COALESCE(SUM(amount), 0), COUNT(*)"},
		{"pending-raw-total", "SELECT COALESCE(SUM(amount), 0)\n\t\tFROM ("},
		{"account-trend", "AND (t.account_id = ? OR t.destination_account_id = ?)"},
		{"balance-trend", "SELECT COALESCE(SUM(net), 0) FROM ("},
		{"health", "SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, writer, probe := dashboardReadFaultDB(t)
			n24SeedWorkspace(t, writer, "A")
			hits := 0
			probe.rowFault = func(q string, _ []driver.NamedValue) string {
				if strings.Contains(q, tc.queryPart) {
					hits++
					return "scan"
				}
				return ""
			}
			data, err := buildDashboardV2Snapshot(context.Background(), db, "user-A", "A", projectionTestNow(), "6m", "", false)
			if hits == 0 {
				t.Fatal("financial reader was not exercised")
			}
			if err == nil || !strings.Contains(err.Error(), "converting NULL") || !reflect.ValueOf(data).IsZero() {
				t.Fatalf("scalar Scan failure returned financial composition: err=%v", err)
			}
			if probe.activeTx != 0 || probe.activeRows != 0 || db.Stats().InUse != 0 || probe.rollbacks != 1 {
				t.Fatal("scalar Scan failure retained the read transaction")
			}
		})
	}
}
