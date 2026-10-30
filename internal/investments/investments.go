// Package investments implements the beta.5 patrimony domain: pure
// calculations over investments and their valuations. It intentionally has no
// dependencies on HTTP, templates, database/sql or internal/handlers — every
// function here is testable as a pure function.
//
// Money is always integer cents (int64). Every arithmetic path is checked and
// fails closed: on overflow the operation returns an error and the caller must
// abandon the calculation (and, when persisted, the transaction). No float is
// used anywhere in this package, and overflow never saturates or clamps.
//
// Availability is explicit: an unavailable IR (MANUAL regime) or an
// unavailable monthly variation is reported as such and its numeric fields
// must never be read as a zero estimate — unavailable is not zero.
package investments

import (
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

// Checked arithmetic. These helpers are deliberately private duplicates of the
// ones in internal/handlers: this package must not depend on handlers, and
// exporting/refactoring shared helpers is out of scope for I1 (a future
// consolidation is a separate phase with the existing areas' test suites).

func addInt64Checked(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("overflow de soma: %d + %d", a, b)
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("overflow de soma: %d + %d", a, b)
	}
	return a + b, nil
}

func subInt64Checked(a, b int64) (int64, error) {
	if b > 0 && a < math.MinInt64+b {
		return 0, fmt.Errorf("overflow de subtração: %d - %d", a, b)
	}
	if b < 0 && a > math.MaxInt64+b {
		return 0, fmt.Errorf("overflow de subtração: %d - %d", a, b)
	}
	return a - b, nil
}

func mulInt64Checked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a == math.MinInt64 && b == -1 || b == math.MinInt64 && a == -1 {
		return 0, fmt.Errorf("overflow de multiplicação: %d * %d", a, b)
	}
	result := a * b
	if result/b != a {
		return 0, fmt.Errorf("overflow de multiplicação: %d * %d", a, b)
	}
	return result, nil
}

// CurrentValueCents resolves the frozen "valor atual" rule: the most recent
// valuation wins; with no valuation the lot falls back to principal_cents.
// Nothing is persisted — there is no current_cents column.
//
// Fail closed: any valuation date that is not a valid civil date, or two
// valuations sharing one date (a violation of the one-per-day invariant the
// database enforces), returns an error instead of picking an arbitrary row.
func CurrentValueCents(inv models.Investment, valuations []models.InvestmentValuation) (int64, error) {
	best, err := latestValuation(valuations, nil)
	if err != nil {
		return 0, err
	}
	if best == nil {
		return inv.PrincipalCents, nil
	}
	return best.AmountCents, nil
}

// YieldCents returns current - principal with checked subtraction. It may be
// negative (loss); only overflow fails.
func YieldCents(currentCents, principalCents int64) (int64, error) {
	return subInt64Checked(currentCents, principalCents)
}

// AccumulatedReturnBasisPoints is the nominal yield relative to the original
// principal. A non-positive principal has no meaningful percentage. The
// intermediate product uses arbitrary precision so large valid positions do
// not wrap int64; a result outside int64 fails closed.
func AccumulatedReturnBasisPoints(yieldCents, principalCents int64) (int64, bool, error) {
	if principalCents <= 0 {
		return 0, false, nil
	}
	scaled := new(big.Int).Mul(big.NewInt(yieldCents), big.NewInt(10000))
	scaled.Quo(scaled, big.NewInt(principalCents))
	if !scaled.IsInt64() {
		return 0, false, fmt.Errorf("overflow da rentabilidade acumulada")
	}
	return scaled.Int64(), true, nil
}

// AdjustedVariation is a nominal comparison of two recorded positions, never
// realized profit or percentage return. Numeric fields are valid only if Available.
type AdjustedVariation struct {
	Available                 bool
	Reason                    string
	PreviousDate, CurrentDate string
	DeltaCents                int64
}

const (
	AdjustedVariationNeedsPositions = "Registre duas posições para comparar"
	AdjustedVariationAmbiguousDates = "Existem operações na mesma data de uma posição"
	AdjustedVariationMissingHistory = "Histórico insuficiente para comparação"
	AdjustedVariationUnsafe         = "Não foi possível calcular com segurança"
)

// AdjustedPositionVariation compares the two latest recorded positions without
// changing the supplied history. The caller supplies complete purchases and
// withdrawals from one scoped snapshot. Only flows strictly inside the period
// count; endpoint flows have unknown intraday order. Later flows do not change
// this historical comparison. Neither receipts' allocations nor INCOME enter it.
func AdjustedPositionVariation(inv models.Investment, valuations []models.InvestmentValuation, purchases []models.InvestmentPurchase, withdrawals []models.InvestmentWithdrawal) AdjustedVariation {
	unavailable := AdjustedVariation{Reason: AdjustedVariationMissingHistory}
	if inv.WorkspaceID == "" || inv.ID == "" {
		return unavailable
	}
	var previous, current *models.InvestmentValuation
	dates := make(map[string]bool)
	for i := range valuations {
		v := &valuations[i]
		if _, err := civildate.ParseCivilDateInput(v.ValuationDate); err != nil || v.WorkspaceID != inv.WorkspaceID || v.InvestmentID != inv.ID || v.AmountCents < 0 || dates[v.ValuationDate] {
			return unavailable
		}
		dates[v.ValuationDate] = true
		if current == nil || v.ValuationDate > current.ValuationDate {
			previous, current = current, v
		} else if previous == nil || v.ValuationDate > previous.ValuationDate {
			previous = v
		}
	}
	if previous == nil {
		return AdjustedVariation{Reason: AdjustedVariationNeedsPositions}
	}
	var purchasesCents, receivedCents int64
	initials := 0
	seen := make(map[string]bool)
	for _, p := range purchases {
		if _, err := civildate.ParseCivilDateInput(p.PurchaseDate); err != nil || p.WorkspaceID != inv.WorkspaceID || p.InvestmentID != inv.ID || p.ID == "" || seen[p.ID] || p.PrincipalCents <= 0 {
			return unavailable
		}
		seen[p.ID] = true
		if p.IsInitial {
			initials++
		}
		if p.PurchaseDate == previous.ValuationDate || p.PurchaseDate == current.ValuationDate {
			return AdjustedVariation{Reason: AdjustedVariationAmbiguousDates}
		}
		if p.PurchaseDate > previous.ValuationDate && p.PurchaseDate < current.ValuationDate {
			var err error
			purchasesCents, err = addInt64Checked(purchasesCents, p.PrincipalCents)
			if err != nil {
				return AdjustedVariation{Reason: AdjustedVariationUnsafe}
			}
		}
	}
	if initials != 1 {
		return unavailable
	}
	seen = make(map[string]bool)
	for _, w := range withdrawals {
		if _, err := civildate.ParseCivilDateInput(w.WithdrawalDate); err != nil || w.WorkspaceID != inv.WorkspaceID || w.InvestmentID != inv.ID || w.ID == "" || seen[w.ID] || w.ReceivedCents < 0 {
			return unavailable
		}
		seen[w.ID] = true
		if w.WithdrawalDate == previous.ValuationDate || w.WithdrawalDate == current.ValuationDate {
			return AdjustedVariation{Reason: AdjustedVariationAmbiguousDates}
		}
		if w.WithdrawalDate > previous.ValuationDate && w.WithdrawalDate < current.ValuationDate {
			var err error
			receivedCents, err = addInt64Checked(receivedCents, w.ReceivedCents)
			if err != nil {
				return AdjustedVariation{Reason: AdjustedVariationUnsafe}
			}
		}
	}
	delta, err := subInt64Checked(current.AmountCents, previous.AmountCents)
	if err == nil {
		delta, err = subInt64Checked(delta, purchasesCents)
	}
	if err == nil {
		delta, err = addInt64Checked(delta, receivedCents)
	}
	if err != nil {
		return AdjustedVariation{Reason: AdjustedVariationUnsafe}
	}
	return AdjustedVariation{Available: true, PreviousDate: previous.ValuationDate, CurrentDate: current.ValuationDate, DeltaCents: delta}
}

// IR rate bands (basis points) frozen for beta.5, by holding days.
const (
	irBandUntil180     int64 = 2250
	irBandUntil360     int64 = 2000
	irBandUntil720     int64 = 1750
	irBandBeyond720    int64 = 1500
	irRoundingHalfCent int64 = 5000
	irBasisPointScale  int64 = 10000
)

// EstimateTax estimates IR for a yield according to the lot's tax regime.
//
//   - IR_REGRESSIVO: base = max(yield, 0); yield <= 0 estimates a legitimate
//     zero. Holding days come from civildate (applied → asOf). Rounding is
//     (base × rate_bp + 5000) / 10000, every intermediate step checked.
//   - ISENTO_PF: available with a legitimate zero.
//   - MANUAL: unavailable — Available=false and TaxCents is meaningless.
//
// An unknown regime string fails closed (it means corrupted data, since the
// database CHECK admits exactly the three tokens above).
func EstimateTax(taxRegime string, yieldCents int64, applied, asOf civildate.CivilDate) (models.TaxEstimate, error) {
	switch taxRegime {
	case models.InvestmentTaxRegimeManual:
		return models.TaxEstimate{Available: false}, nil
	case models.InvestmentTaxRegimeIsentoPF:
		return models.TaxEstimate{Available: true, TaxCents: 0}, nil
	case models.InvestmentTaxRegimeIRRegressivo:
		if yieldCents <= 0 {
			return models.TaxEstimate{Available: true, TaxCents: 0}, nil
		}
		days := civildate.CivilDateDaysBetween(applied, asOf)
		if days < 0 {
			return models.TaxEstimate{}, fmt.Errorf("data de referência %s anterior à aplicação %s", asOf, applied)
		}
		rate := irRateBasisPoints(days)
		scaled, err := mulInt64Checked(yieldCents, rate)
		if err != nil {
			return models.TaxEstimate{}, err
		}
		rounded, err := addInt64Checked(scaled, irRoundingHalfCent)
		if err != nil {
			return models.TaxEstimate{}, err
		}
		return models.TaxEstimate{Available: true, TaxCents: rounded / irBasisPointScale}, nil
	default:
		return models.TaxEstimate{}, fmt.Errorf("regime de IR desconhecido: %q", taxRegime)
	}
}

func irRateBasisPoints(days int64) int64 {
	switch {
	case days <= 180:
		return irBandUntil180
	case days <= 360:
		return irBandUntil360
	case days <= 720:
		return irBandUntil720
	default:
		return irBandBeyond720
	}
}

// EstimateLiquid returns current - tax with checked subtraction. An
// unavailable tax implies an unavailable net value (never a zero estimate).
func EstimateLiquid(currentCents int64, tax models.TaxEstimate) (models.LiquidEstimate, error) {
	if !tax.Available {
		return models.LiquidEstimate{Available: false}, nil
	}
	net, err := subInt64Checked(currentCents, tax.TaxCents)
	if err != nil {
		return models.LiquidEstimate{}, err
	}
	return models.LiquidEstimate{Available: true, NetCents: net}, nil
}

// MonthlyVariation compares the position at the close of `month/year` against
// the position at the close of the previous month (V_prev).
//
// Contract (frozen):
//   - V_current = latest valuation up to the last day of the target month,
//     falling back to principal only if the lot already existed at that close.
//   - V_prev = latest valuation up to the last day of the previous month,
//     with the same principal fallback.
//   - A lot applied in the target month or later has no previous basis:
//     Available=false with the reason.
//   - V_prev <= 0 (e.g. a zero valuation) is an invalid denominator for the
//     percentage: Available=false (fail closed, not an error).
//   - Delta = V_current - V_prev, checked; BasisPoints = delta × 10000 / V_prev,
//     checked multiplication, truncated integer division (no float).
//   - Overflow or invalid dates return an error.
func MonthlyVariation(inv models.Investment, valuations []models.InvestmentValuation, year int, month time.Month) (models.MonthlyVariation, error) {
	applied, err := civildate.ParseCivilDateInput(inv.AppliedDate)
	if err != nil {
		return models.MonthlyVariation{}, fmt.Errorf("data de aplicação do investimento %s: %w", inv.ID, err)
	}
	monthFirst, monthEndExclusive, err := civildate.CivilMonthBounds(year, month)
	if err != nil {
		return models.MonthlyVariation{}, fmt.Errorf("mês de referência inválido: %w", err)
	}
	// CivilMonthBounds returns the exclusive next-month start; the frozen rule
	// closes on the last civil day of the target month, so derive the inclusive
	// close before using it as a cutoff.
	monthClose, err := civildate.CivilDateAddDays(monthEndExclusive, -1)
	if err != nil {
		return models.MonthlyVariation{}, fmt.Errorf("fechamento do mês de referência inválido: %w", err)
	}
	prevLast, err := civildate.CivilDateAddDays(monthFirst, -1)
	if err != nil {
		return models.MonthlyVariation{}, fmt.Errorf("fechamento do mês anterior inválido: %w", err)
	}

	currentVal, err := latestValuation(valuations, &monthClose)
	if err != nil {
		return models.MonthlyVariation{}, err
	}
	currentCents := inv.PrincipalCents
	if currentVal != nil {
		currentCents = currentVal.AmountCents
	} else if civildate.CivilDateDaysBetween(applied, monthClose) < 0 {
		return models.MonthlyVariation{Available: false, Reason: "investimento ainda não existia no mês de referência"}, nil
	}

	previousCents := inv.PrincipalCents
	previousVal, err := latestValuation(valuations, &prevLast)
	if err != nil {
		return models.MonthlyVariation{}, err
	}
	if previousVal != nil {
		previousCents = previousVal.AmountCents
	} else if civildate.CivilDateDaysBetween(applied, prevLast) < 0 {
		return models.MonthlyVariation{Available: false, Reason: "sem base anterior ao mês de referência (aplicação nasceu no próprio mês)"}, nil
	}

	delta, err := subInt64Checked(currentCents, previousCents)
	if err != nil {
		return models.MonthlyVariation{}, err
	}
	if previousCents <= 0 {
		return models.MonthlyVariation{Available: false, Reason: "denominador inválido para variação percentual"}, nil
	}
	scaled, err := mulInt64Checked(delta, 10000)
	if err != nil {
		return models.MonthlyVariation{}, err
	}
	return models.MonthlyVariation{
		Available:     true,
		PreviousCents: previousCents,
		CurrentCents:  currentCents,
		DeltaCents:    delta,
		BasisPoints:   scaled / previousCents,
	}, nil
}

// PortfolioItem pairs a lot with its valuations for aggregation.
type PortfolioItem struct {
	Investment models.Investment
	Valuations []models.InvestmentValuation
}

// PortfolioAggregate sums principal/current/yield (always exact) and tax/net.
// Any MANUAL lot makes the tax and net totals partial: TaxAvailable and
// NetAvailable become false and their cent fields must not be read — an
// aggregate never pretends to be complete.
type PortfolioAggregate struct {
	PrincipalCents int64
	CurrentCents   int64
	YieldCents     int64
	TaxAvailable   bool
	TaxCents       int64
	NetAvailable   bool
	NetCents       int64
}

// AggregatePortfolio folds every lot with checked arithmetic and per-lot tax
// estimates at asOf. Overflow, invalid dates and unknown regimes fail closed.
func AggregatePortfolio(items []PortfolioItem, asOf civildate.CivilDate) (PortfolioAggregate, error) {
	aggregate := PortfolioAggregate{TaxAvailable: true, NetAvailable: true}
	for _, item := range items {
		inv := item.Investment
		applied, err := civildate.ParseCivilDateInput(inv.AppliedDate)
		if err != nil {
			return PortfolioAggregate{}, fmt.Errorf("data de aplicação do investimento %s: %w", inv.ID, err)
		}
		current, err := CurrentValueCents(inv, item.Valuations)
		if err != nil {
			return PortfolioAggregate{}, err
		}
		yield, err := YieldCents(current, inv.PrincipalCents)
		if err != nil {
			return PortfolioAggregate{}, err
		}
		if aggregate.PrincipalCents, err = addInt64Checked(aggregate.PrincipalCents, inv.PrincipalCents); err != nil {
			return PortfolioAggregate{}, err
		}
		if aggregate.CurrentCents, err = addInt64Checked(aggregate.CurrentCents, current); err != nil {
			return PortfolioAggregate{}, err
		}
		if aggregate.YieldCents, err = addInt64Checked(aggregate.YieldCents, yield); err != nil {
			return PortfolioAggregate{}, err
		}
		tax, err := EstimateTax(inv.TaxRegime, yield, applied, asOf)
		if err != nil {
			return PortfolioAggregate{}, err
		}
		if !tax.Available {
			aggregate.TaxAvailable = false
			aggregate.NetAvailable = false
			continue
		}
		if aggregate.TaxCents, err = addInt64Checked(aggregate.TaxCents, tax.TaxCents); err != nil {
			return PortfolioAggregate{}, err
		}
		liquid, err := EstimateLiquid(current, tax)
		if err != nil {
			return PortfolioAggregate{}, err
		}
		if aggregate.NetCents, err = addInt64Checked(aggregate.NetCents, liquid.NetCents); err != nil {
			return PortfolioAggregate{}, err
		}
	}
	return aggregate, nil
}

// latestValuation scans for the most recent valuation at or before cutoff
// (nil cutoff = no limit), validating every civil date in the set. A
// duplicated date among the considered rows violates the one-per-day policy
// and fails closed.
func latestValuation(valuations []models.InvestmentValuation, cutoff *civildate.CivilDate) (*models.InvestmentValuation, error) {
	var best *models.InvestmentValuation
	var bestDate civildate.CivilDate
	for i := range valuations {
		date, err := civildate.ParseCivilDateInput(valuations[i].ValuationDate)
		if err != nil {
			return nil, fmt.Errorf("data de valuation inválida: %w", err)
		}
		if cutoff != nil && civildate.CivilDateDaysBetween(date, *cutoff) < 0 {
			continue
		}
		if best != nil && civildate.CivilDateDaysBetween(bestDate, date) == 0 {
			return nil, fmt.Errorf("duas valuations para %s violam a política de uma por dia", valuations[i].ValuationDate)
		}
		if best == nil || civildate.CivilDateDaysBetween(bestDate, date) > 0 {
			best = &valuations[i]
			bestDate = date
		}
	}
	return best, nil
}
