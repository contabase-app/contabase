package database

import (
	"database/sql/driver"
	"errors"
	"math/big"

	"modernc.org/sqlite"
)

// ErrTransactionRefundInvariant contains no amounts or customer identifiers.
var ErrTransactionRefundInvariant = errors.New("invalid transaction refund invariant")

// These connection-local SQL functions keep the existing statement snapshot,
// filters and cardinality. No callback performs SQL or loads a refund slice.
func init() {
	sqlite.MustRegisterFunction("contabase_refund_sum", &sqlite.FunctionImpl{
		NArgs: 1,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &exactTransactionRefunds{}, nil
		},
	})
	sqlite.MustRegisterDeterministicScalarFunction("contabase_refund_net", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			gross, ok := args[0].(int64)
			if !ok {
				return nil, ErrTransactionRefundInvariant
			}
			var total big.Int
			if args[1] != nil {
				text, ok := args[1].(string)
				if !ok {
					return nil, ErrTransactionRefundInvariant
				}
				if _, ok := total.SetString(text, 10); !ok {
					return nil, ErrTransactionRefundInvariant
				}
			}
			return transactionRefundNet(gross, &total)
		})
}

// The aggregate returns an exact decimal intermediate, never a SQLite INTEGER
// sum. Conversion to int64 happens only after comparison with the gross amount.
type exactTransactionRefunds struct {
	total big.Int
	err   error
}

func (a *exactTransactionRefunds) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if a.err != nil {
		return a.err
	}
	amount, ok := args[0].(int64)
	if !ok || amount <= 0 {
		a.err = ErrTransactionRefundInvariant
		return a.err
	}
	a.total.Add(&a.total, big.NewInt(amount))
	return nil
}

func (a *exactTransactionRefunds) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if a.err != nil {
		return a.err
	}
	amount, ok := args[0].(int64)
	if !ok || amount <= 0 || a.total.Cmp(big.NewInt(amount)) < 0 {
		a.err = ErrTransactionRefundInvariant
		return a.err
	}
	a.total.Sub(&a.total, big.NewInt(amount))
	return nil
}

func (a *exactTransactionRefunds) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.total.String(), nil
}

func (*exactTransactionRefunds) Final(*sqlite.FunctionContext) {}

func transactionRefundNet(gross int64, refunds *big.Int) (int64, error) {
	var net big.Int
	net.SetInt64(gross)
	if refunds.Sign() < 0 || (refunds.Sign() > 0 && (gross <= 0 || refunds.Cmp(&net) > 0)) {
		return 0, ErrTransactionRefundInvariant
	}
	net.Sub(&net, refunds)
	if !net.IsInt64() {
		return 0, ErrTransactionRefundInvariant
	}
	return net.Int64(), nil
}
