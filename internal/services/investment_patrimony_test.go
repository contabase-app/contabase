package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
	"modernc.org/sqlite"
)

func patrimonyDatabaseState(t *testing.T, db *sql.DB) string {
	t.Helper()
	state := make(map[string][][]any)
	for _, table := range []string{"accounts", "transactions", "box_virtual_ledger", "account_balance_adjustments", "investments", "investment_purchases", "investment_valuations", "investment_withdrawals", "investment_withdrawal_allocations", "schema_migrations"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			state[table] = append(state[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestInvestmentPatrimonyEligibilityAndReadOnly(t *testing.T) {
	for _, scenario := range []struct {
		name, positionDate, operation, reason string
		amount                                int64
		eligible                              bool
	}{
		{"known", "2026-02-01", "", "", 200000, true},
		{"known-zero", "2026-02-01", "", "", 0, true},
		{"missing", "", "", "Sem posição registrada", 0, false},
		{"purchase-after", "2026-02-01", "purchase", "posterior", 200000, false},
		{"withdrawal-after", "2026-02-01", "withdrawal", "posterior", 200000, false},
		{"initial-same-day", "2026-01-15", "", "mesma data", 200000, false},
		{"purchase-same-day", "2026-03-01", "purchase", "mesma data", 200000, false},
		{"withdrawal-same-day", "2026-03-15", "withdrawal", "mesma data", 200000, false},
		{"total-withdrawal", "2026-02-01", "total", "posterior", 200000, false},
		{"total-with-new-position", "2026-04-01", "total", "integralmente retiradas", 200000, false},
		{"administrative-closure", "2026-02-01", "close", "Acompanhamento encerrado", 200000, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pools, _, inv := newInvestmentLifecycleDB(t)
			ctx := context.Background()
			if scenario.positionDate != "" {
				insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, scenario.positionDate, scenario.amount)
			}
			switch scenario.operation {
			case "purchase":
				if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, investmentPurchaseInput(inv.ID)); err != nil {
					t.Fatal(err)
				}
			case "withdrawal", "total":
				input := withdrawalInput(inv.ID, "initial:"+inv.ID)
				if scenario.operation == "total" {
					input.Mode = InvestmentWithdrawalTotal
				}
				if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, input); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := RedeemInvestment(ctx, pools.Immediate, investmentRedeemInput(inv.ID)); err != nil {
					t.Fatal(err)
				}
			}
			exec(t, pools.Immediate, `UPDATE accounts SET current_balance=7654321 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
			before := patrimonyDatabaseState(t, pools.DB)
			got, err := ReadAccountInvestmentPatrimony(ctx, pools.DB, "ws-a", "acc-inv-a")
			if err != nil || len(got.Investments) != 1 {
				t.Fatalf("projection: %+v %v", got, err)
			}
			item := got.Investments[0]
			if item.Eligible != scenario.eligible || !strings.Contains(item.Reason, scenario.reason) {
				t.Fatalf("eligibility: %+v", item)
			}
			if scenario.positionDate == "" && item.Position != nil {
				t.Fatal("unknown position fell back to principal")
			}
			want := int64(0)
			coverage := InvestmentPatrimonyUnavailable
			if scenario.eligible {
				want, coverage = scenario.amount, InvestmentPatrimonyPartial
			}
			if got.SubtotalCents != want || got.Coverage != coverage {
				t.Fatalf("subtotal/coverage: %+v", got)
			}
			if before != patrimonyDatabaseState(t, pools.DB) {
				t.Fatal("projection mutated persisted state")
			}
		})
	}
}

func TestInvestmentPatrimonyAccountsDatesArchiveAndTenantIsolation(t *testing.T) {
	pools, _, first := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	second := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	missing := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	exec(t, pools.Immediate, `INSERT INTO accounts(id,workspace_id,name,type,current_balance) VALUES('other-account','ws-a','Outra corretora','INVESTMENT',123456)`)
	other := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "other-account"))
	foreign := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
	for _, seed := range []struct {
		inv    models.Investment
		date   string
		amount int64
	}{
		{first, "2026-02-01", 200000}, {second, "2026-03-01", 90000},
		{other, "2026-04-01", 50000}, {foreign, "2026-04-01", 999999999},
	} {
		insertValuationRow(t, pools.Immediate, "position:"+seed.inv.ID, seed.inv.WorkspaceID, seed.inv.ID, seed.date, seed.amount)
	}
	exec(t, pools.Immediate, `UPDATE accounts SET archived_at=100 WHERE workspace_id='ws-a' AND id='other-account'`)
	before := patrimonyDatabaseState(t, pools.DB)
	got, err := ReadWorkspaceInvestmentPatrimony(ctx, pools.DB, "ws-a")
	if err != nil || got.SubtotalCents != 340000 || got.EligibleCount != 3 || got.WithoutValidPosition != 1 || got.ArchivedMaintainedAccountCount != 1 || got.Coverage != InvestmentPatrimonyPartial {
		t.Fatalf("workspace: %+v %v", got, err)
	}
	account, err := ReadAccountInvestmentPatrimony(ctx, pools.DB, "ws-a", "acc-inv-a")
	if err != nil || account.SubtotalCents != 290000 || account.WithoutValidPosition != 1 || len(account.Investments) != 3 {
		t.Fatalf("account: %+v %v", account, err)
	}
	dates := make(map[string]string)
	for _, position := range account.Investments {
		if position.Position != nil {
			dates[position.Investment.ID] = position.Position.ValuationDate
		}
	}
	if !reflect.DeepEqual(dates, map[string]string{first.ID: "2026-02-01", second.ID: "2026-03-01"}) || dates[missing.ID] != "" {
		t.Fatalf("position dates conflated: %+v", dates)
	}
	for _, id := range []string{"acc-inv-b", "acc-checking-a", "unknown"} {
		if _, err := ReadAccountInvestmentPatrimony(ctx, pools.DB, "ws-a", id); !errors.Is(err, ErrInvestmentNotFound) {
			t.Fatalf("foreign/invalid account %s: %v", id, err)
		}
	}
	if before != patrimonyDatabaseState(t, pools.DB) {
		t.Fatal("projection changed persisted state")
	}
}

func TestInvestmentPatrimonyOverflow(t *testing.T) {
	for _, separateAccounts := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "workspace"}[separateAccounts], func(t *testing.T) {
			pools, _, first := newInvestmentLifecycleDB(t)
			accountID := first.AccountID
			if separateAccounts {
				accountID = "second-account"
				exec(t, pools.Immediate, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('second-account','ws-a','Segunda','INVESTMENT')`)
			}
			second := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", accountID))
			insertValuationRow(t, pools.Immediate, "max", "ws-a", first.ID, "2026-02-01", math.MaxInt64)
			insertValuationRow(t, pools.Immediate, "one", "ws-a", second.ID, "2026-02-01", 1)
			before := patrimonyDatabaseState(t, pools.DB)
			got, err := ReadWorkspaceInvestmentPatrimony(context.Background(), pools.DB, "ws-a")
			if !errors.Is(err, ErrBalanceAggregateOutOfRange) || got.EligibleCount != 0 || got.SubtotalCents != 0 {
				t.Fatalf("overflow exposed partial numeric result: %+v %v", got, err)
			}
			if before != patrimonyDatabaseState(t, pools.DB) {
				t.Fatal("overflow changed state")
			}
		})
	}
}

// Real SQLite commits between the first and second projection queries prove
// that all subsequent domain reads remain on the original committed snapshot.
type patrimonySnapshotConnector struct {
	dsn   string
	once  sync.Once
	write func()
}

func (c *patrimonySnapshotConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *patrimonySnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &patrimonySnapshotConn{Conn: conn, connector: c}, nil
}

type patrimonySnapshotConn struct {
	driver.Conn
	connector *patrimonySnapshotConnector
}

func (c *patrimonySnapshotConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *patrimonySnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, " FROM investments\n") {
		c.connector.once.Do(c.connector.write)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestInvestmentPatrimonyConsistentSnapshot(t *testing.T) {
	pools, path, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "before", "ws-a", inv.ID, "2026-02-01", 110000)
	wrote := false
	reader := sql.OpenDB(&patrimonySnapshotConnector{dsn: path + "?_pragma=foreign_keys(1)", write: func() {
		if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, investmentPurchaseInput(inv.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", ValuationDate: "2026-04-01", AmountCents: 320000}); err != nil {
			t.Fatal(err)
		}
		wrote = true
	}})
	defer reader.Close()
	before, err := ReadAccountInvestmentPatrimony(ctx, reader, "ws-a", inv.AccountID)
	if err != nil || !wrote || before.SubtotalCents != 110000 || before.Investments[0].Position.ValuationDate != "2026-02-01" {
		t.Fatalf("mixed snapshot: %+v wrote=%v err=%v", before, wrote, err)
	}
	after, err := ReadAccountInvestmentPatrimony(ctx, pools.DB, "ws-a", inv.AccountID)
	if err != nil || after.SubtotalCents != 320000 || after.Investments[0].Position.ValuationDate != "2026-04-01" {
		t.Fatalf("new committed snapshot: %+v %v", after, err)
	}
}
