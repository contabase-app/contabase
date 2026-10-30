package investments

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

func TestAdjustedPositionVariation(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeManual, 100000)
	buy := func(id, date string, cents int64, initial bool) models.InvestmentPurchase {
		return models.InvestmentPurchase{ID: id, WorkspaceID: inv.WorkspaceID, InvestmentID: inv.ID, PurchaseDate: date, PrincipalCents: cents, IsInitial: initial}
	}
	withdraw := func(id, date string, cents int64) models.InvestmentWithdrawal {
		return models.InvestmentWithdrawal{ID: id, WorkspaceID: inv.WorkspaceID, InvestmentID: inv.ID, WithdrawalDate: date, ReceivedCents: cents,
			Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial", ReceivedCents: cents / 2}, {PurchaseID: "extra", ReceivedCents: cents - cents/2}}}
	}
	initial := buy("initial", "2026-01-01", 100000, true)
	foreign := valuation("2026-04-01", 110000)
	foreign.WorkspaceID = "other"
	foreignPurchase := buy("foreign", "2026-03-01", 100, false)
	foreignPurchase.InvestmentID = "other"
	foreignWithdrawal := withdraw("foreign", "2026-03-15", 100)
	foreignWithdrawal.WorkspaceID = "other"
	for _, tc := range []struct {
		name        string
		current     int64
		positions   []models.InvestmentValuation
		purchases   []models.InvestmentPurchase
		withdrawals []models.InvestmentWithdrawal
		delta       int64
		reason      string
	}{
		{name: "aporte sem valorização", current: 150000, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-03-01", 50000, false)}},
		{name: "retirada sem desvalorização", current: 80000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-03-15", 20000)}},
		{name: "compras e retiradas com principal desconhecido", current: 135000, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-03-01", 50000, false)}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-03-15", 20000)}, delta: 5000},
		{name: "variação negativa combinada", current: 125000, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-03-01", 50000, false)}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-03-15", 20000)}, delta: -5000},
		{name: "variação positiva sem fluxos", current: 110000, purchases: []models.InvestmentPurchase{initial}, delta: 10000},
		{name: "variação negativa sem fluxos", current: 90000, purchases: []models.InvestmentPurchase{initial}, delta: -10000},
		{name: "posição zero registrada", current: 0, purchases: []models.InvestmentPurchase{initial}, delta: -100000},
		{name: "uma posição", positions: []models.InvestmentValuation{valuation("2026-02-01", 100000)}, purchases: []models.InvestmentPurchase{initial}, reason: AdjustedVariationNeedsPositions},
		{name: "sem posições", positions: []models.InvestmentValuation{}, purchases: []models.InvestmentPurchase{initial}, reason: AdjustedVariationNeedsPositions},
		{name: "lacunas e ordem real", positions: []models.InvestmentValuation{valuation("2026-04-01", 110000), valuation("2026-01-15", 90000), valuation("2026-02-01", 100000)}, purchases: []models.InvestmentPurchase{initial}, delta: 10000},
		{name: "operações posteriores não alteram período histórico", current: 110000, purchases: []models.InvestmentPurchase{initial, buy("later", "2026-05-01", 50000, false)}, withdrawals: []models.InvestmentWithdrawal{withdraw("later", "2026-05-02", 20000)}, delta: 10000},
		{name: "compra na posição inicial", current: 110000, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-02-01", 50000, false)}, reason: AdjustedVariationAmbiguousDates},
		{name: "compra na posição final", current: 110000, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-04-01", 50000, false)}, reason: AdjustedVariationAmbiguousDates},
		{name: "retirada na posição inicial", current: 110000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-02-01", 20000)}, reason: AdjustedVariationAmbiguousDates},
		{name: "retirada na posição final", current: 110000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-04-01", 20000)}, reason: AdjustedVariationAmbiguousDates},
		{name: "histórico de compras ausente", current: 110000, reason: AdjustedVariationMissingHistory},
		{name: "posição de outro workspace", positions: []models.InvestmentValuation{valuation("2026-02-01", 100000), foreign}, purchases: []models.InvestmentPurchase{initial}, reason: AdjustedVariationMissingHistory},
		{name: "compra de outro investimento", current: 110000, purchases: []models.InvestmentPurchase{initial, foreignPurchase}, reason: AdjustedVariationMissingHistory},
		{name: "retirada de outro workspace", current: 110000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{foreignWithdrawal}, reason: AdjustedVariationMissingHistory},
		{name: "data civil impossível", positions: []models.InvestmentValuation{valuation("2026-02-31", 100000), valuation("2026-04-01", 110000)}, purchases: []models.InvestmentPurchase{initial}, reason: AdjustedVariationMissingHistory},
		{name: "data de posição repetida", positions: []models.InvestmentValuation{valuation("2026-02-01", 100000), valuation("2026-02-01", 110000)}, purchases: []models.InvestmentPurchase{initial}, reason: AdjustedVariationMissingHistory},
		{name: "compra repetida não duplica aporte", current: 110000, purchases: []models.InvestmentPurchase{initial, initial}, reason: AdjustedVariationMissingHistory},
		{name: "retirada repetida não duplica recebimento", current: 110000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-03-15", 100), withdraw("w", "2026-03-15", 100)}, reason: AdjustedVariationMissingHistory},
		{name: "overflow soma de compras", current: 110000, purchases: []models.InvestmentPurchase{initial, buy("a", "2026-03-01", math.MaxInt64, false), buy("b", "2026-03-02", 1, false)}, reason: AdjustedVariationUnsafe},
		{name: "overflow soma de retiradas", current: 110000, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("a", "2026-03-01", math.MaxInt64), withdraw("b", "2026-03-02", 1)}, reason: AdjustedVariationUnsafe},
		{name: "overflow subtração", positions: []models.InvestmentValuation{valuation("2026-02-01", math.MaxInt64), valuation("2026-04-01", 0)}, purchases: []models.InvestmentPurchase{initial, buy("extra", "2026-03-01", 2, false)}, reason: AdjustedVariationUnsafe},
		{name: "overflow resultado", positions: []models.InvestmentValuation{valuation("2026-02-01", 0), valuation("2026-04-01", math.MaxInt64)}, purchases: []models.InvestmentPurchase{initial}, withdrawals: []models.InvestmentWithdrawal{withdraw("w", "2026-03-01", 1)}, reason: AdjustedVariationUnsafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			positions := tc.positions
			if positions == nil {
				positions = []models.InvestmentValuation{valuation("2026-02-01", 100000), valuation("2026-04-01", tc.current)}
			}
			before := append([]models.InvestmentValuation{}, positions...)
			got := AdjustedPositionVariation(inv, positions, tc.purchases, tc.withdrawals)
			if got.Available != (tc.reason == "") || got.Reason != tc.reason || got.DeltaCents != tc.delta {
				t.Fatalf("variation=%+v, want delta=%d reason=%q", got, tc.delta, tc.reason)
			}
			if got.Available && (got.PreviousDate != "2026-02-01" || got.CurrentDate != "2026-04-01") {
				t.Fatalf("real position dates lost: %+v", got)
			}
			if !reflect.DeepEqual(before, positions) {
				t.Fatal("calculation mutated positions")
			}
		})
	}
}

func mustDate(t *testing.T, value string) civildate.CivilDate {
	t.Helper()
	date, err := civildate.ParseCivilDateInput(value)
	if err != nil {
		t.Fatalf("parse %s: %v", value, err)
	}
	return date
}

func mustDateOffset(t *testing.T, base civildate.CivilDate, days int) civildate.CivilDate {
	t.Helper()
	date, err := civildate.CivilDateAddDays(base, days)
	if err != nil {
		t.Fatalf("offset %d from %s: %v", days, base, err)
	}
	return date
}

func testInvestment(regime string, principal int64) models.Investment {
	return models.Investment{
		ID:             "inv-1",
		WorkspaceID:    "ws-1",
		AccountID:      "acc-1",
		Name:           "CDB Banco X",
		Kind:           "CDB",
		PrincipalCents: principal,
		TaxRegime:      regime,
		AppliedDate:    "2026-01-01",
		Status:         models.InvestmentStatusAtiva,
	}
}

func valuation(date string, amount int64) models.InvestmentValuation {
	return models.InvestmentValuation{
		ID:            "val-" + date,
		WorkspaceID:   "ws-1",
		InvestmentID:  "inv-1",
		ValuationDate: date,
		AmountCents:   amount,
	}
}

// --- checked helpers -------------------------------------------------------

func TestAddInt64Checked(t *testing.T) {
	if got, err := addInt64Checked(2, 3); err != nil || got != 5 {
		t.Fatalf("add(2,3) = %d, %v", got, err)
	}
	if got, err := addInt64Checked(math.MaxInt64, 0); err != nil || got != math.MaxInt64 {
		t.Fatalf("add(MaxInt64,0) = %d, %v", got, err)
	}
	if got, err := addInt64Checked(math.MinInt64, 0); err != nil || got != math.MinInt64 {
		t.Fatalf("add(MinInt64,0) = %d, %v", got, err)
	}
	if _, err := addInt64Checked(math.MaxInt64, 1); err == nil {
		t.Fatal("add(MaxInt64,1) should overflow")
	}
	if _, err := addInt64Checked(math.MinInt64, -1); err == nil {
		t.Fatal("add(MinInt64,-1) should overflow")
	}
}

func TestSubInt64Checked(t *testing.T) {
	if got, err := subInt64Checked(5, 3); err != nil || got != 2 {
		t.Fatalf("sub(5,3) = %d, %v", got, err)
	}
	if got, err := subInt64Checked(3, 5); err != nil || got != -2 {
		t.Fatalf("sub(3,5) = %d, %v (rendimento negativo é válido)", got, err)
	}
	if _, err := subInt64Checked(math.MinInt64, 1); err == nil {
		t.Fatal("sub(MinInt64,1) should overflow")
	}
	if _, err := subInt64Checked(math.MaxInt64, -1); err == nil {
		t.Fatal("sub(MaxInt64,-1) should overflow")
	}
}

func TestMulInt64Checked(t *testing.T) {
	if got, err := mulInt64Checked(0, 7); err != nil || got != 0 {
		t.Fatalf("mul(0,7) = %d, %v", got, err)
	}
	if got, err := mulInt64Checked(-3, 5); err != nil || got != -15 {
		t.Fatalf("mul(-3,5) = %d, %v (positivo × negativo)", got, err)
	}
	if got, err := mulInt64Checked(math.MaxInt64, 1); err != nil || got != math.MaxInt64 {
		t.Fatalf("mul(MaxInt64,1) = %d, %v", got, err)
	}
	if got, err := mulInt64Checked(math.MaxInt64, -1); err != nil || got != -math.MaxInt64 {
		t.Fatalf("mul(MaxInt64,-1) = %d, %v", got, err)
	}
	if _, err := mulInt64Checked(math.MaxInt64, 2); err == nil {
		t.Fatal("mul(MaxInt64,2) should overflow")
	}
	if _, err := mulInt64Checked(math.MaxInt64, -2); err == nil {
		t.Fatal("mul(MaxInt64,-2) should overflow")
	}
	// MinInt64 * -1 wraps to itself in int64 and must fail closed.
	if _, err := mulInt64Checked(math.MinInt64, -1); err == nil {
		t.Fatal("mul(MinInt64,-1) should overflow")
	}
	if _, err := mulInt64Checked(-1, math.MinInt64); err == nil {
		t.Fatal("mul(-1,MinInt64) should overflow")
	}
}

func TestAccumulatorsFailClosed(t *testing.T) {
	var total int64
	var err error
	for i := 0; i < 4; i++ {
		total, err = addInt64Checked(total, math.MaxInt64/4)
		if err != nil {
			t.Fatalf("accumulator step %d: %v", i, err)
		}
	}
	if _, err := addInt64Checked(total, math.MaxInt64/4); err == nil {
		t.Fatal("accumulator should fail closed at MaxInt64 boundary")
	}
}

// --- valor atual / rendimento ----------------------------------------------

func TestCurrentValueCentsPrefersLatestValuation(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 1000)
	valuations := []models.InvestmentValuation{
		valuation("2026-01-10", 900),
		valuation("2026-02-01", 1100),
		valuation("2026-01-25", 950),
	}
	got, err := CurrentValueCents(inv, valuations)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1100 {
		t.Fatalf("valor atual = %d, want 1100 (valuation mais recente)", got)
	}
}

func TestCurrentValueCentsFallsBackToPrincipal(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 1000)
	got, err := CurrentValueCents(inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1000 {
		t.Fatalf("fallback = %d, want principal 1000", got)
	}
}

func TestCurrentValueCentsRejectsBrokenValuations(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 1000)
	if _, err := CurrentValueCents(inv, []models.InvestmentValuation{valuation("2026-13-40", 100)}); err == nil {
		t.Fatal("data civil inválida deve falhar (fail closed)")
	}
	duplicated := []models.InvestmentValuation{
		valuation("2026-01-10", 900),
		valuation("2026-01-10", 950),
	}
	if _, err := CurrentValueCents(inv, duplicated); err == nil {
		t.Fatal("duas valuations no mesmo dia devem falhar (invariante 1/dia)")
	}
}

func TestYieldCents(t *testing.T) {
	cases := []struct {
		name                     string
		current, principal, want int64
	}{
		{"positivo", 1100, 1000, 100},
		{"zero", 1000, 1000, 0},
		{"negativo", 900, 1000, -100},
	}
	for _, tc := range cases {
		got, err := YieldCents(tc.current, tc.principal)
		if err != nil || got != tc.want {
			t.Fatalf("%s: YieldCents = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestAccumulatedReturnBasisPoints(t *testing.T) {
	for _, tc := range []struct {
		yield, principal, want int64
		available              bool
	}{
		{25000, 1000000, 250, true},
		{-20000, 1000000, -200, true},
		{0, 1000000, 0, true},
		{0, 0, 0, false},
		{100, -1, 0, false},
		{4_000_000_000_000_000_000, 4_000_000_000_000_000_000, 10000, true},
	} {
		got, available, err := AccumulatedReturnBasisPoints(tc.yield, tc.principal)
		if err != nil || got != tc.want || available != tc.available {
			t.Fatalf("yield=%d principal=%d: %d, %v, %v", tc.yield, tc.principal, got, available, err)
		}
	}
	if _, _, err := AccumulatedReturnBasisPoints(math.MaxInt64, 1); err == nil {
		t.Fatal("rentabilidade não representável deve falhar")
	}
	if _, err := YieldCents(math.MaxInt64, -1); err == nil {
		t.Fatal("ganho com overflow deve falhar")
	}
}

// --- IR ---------------------------------------------------------------------

func TestEstimateTaxIRRegressivoBands(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	cases := []struct {
		days int
		rate int64
	}{
		{180, irBandUntil180},
		{181, irBandUntil360},
		{360, irBandUntil360},
		{361, irBandUntil720},
		{720, irBandUntil720},
		{721, irBandBeyond720},
	}
	const base = int64(100_000) // R$ 1.000,00 de ganho
	for _, tc := range cases {
		asOf := mustDateOffset(t, applied, tc.days)
		tax, err := EstimateTax(models.InvestmentTaxRegimeIRRegressivo, base, applied, asOf)
		if err != nil {
			t.Fatalf("%d dias: %v", tc.days, err)
		}
		want := (base*tc.rate + irRoundingHalfCent) / irBasisPointScale
		if !tax.Available || tax.TaxCents != want {
			t.Fatalf("%d dias: tax = %+v, want available com %d", tc.days, tax, want)
		}
	}
}

func TestEstimateTaxIRRegressivoRounding(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 30)
	// 12345 × 2250 = 27776250; +5000 = 27781250; /10000 = 2778
	// (truncamento puro daria 2777; arredondamento ao centavo dá 2778)
	tax, err := EstimateTax(models.InvestmentTaxRegimeIRRegressivo, 12345, applied, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if !tax.Available || tax.TaxCents != 2778 {
		t.Fatalf("arredondamento: %+v, want 2778", tax)
	}
}

func TestEstimateTaxIRRegressivoNonPositiveYield(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 30)
	for _, yield := range []int64{0, -500} {
		tax, err := EstimateTax(models.InvestmentTaxRegimeIRRegressivo, yield, applied, asOf)
		if err != nil {
			t.Fatalf("yield %d: %v", yield, err)
		}
		if !tax.Available || tax.TaxCents != 0 {
			t.Fatalf("yield %d: rendimento sem ganho estimado deve ser disponível e zero, got %+v", yield, tax)
		}
	}
}

func TestEstimateTaxIRRegressivoOverflowFailsClosed(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 30)
	if _, err := EstimateTax(models.InvestmentTaxRegimeIRRegressivo, math.MaxInt64, applied, asOf); err == nil {
		t.Fatal("MaxInt64 × 2250bp deve falhar (fail closed)")
	}
}

func TestEstimateTaxIRRegressivoRejectsBackwardsDate(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDate(t, "2025-12-31")
	if _, err := EstimateTax(models.InvestmentTaxRegimeIRRegressivo, 1000, applied, asOf); err == nil {
		t.Fatal("referência anterior à aplicação deve falhar (fail closed)")
	}
}

func TestEstimateTaxIsentoPF(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 100)
	tax, err := EstimateTax(models.InvestmentTaxRegimeIsentoPF, 5000, applied, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if !tax.Available || tax.TaxCents != 0 {
		t.Fatalf("ISENTO_PF deve ser disponível com zero legítimo, got %+v", tax)
	}
}

func TestEstimateTaxManualIsUnavailableNeverZero(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 100)
	tax, err := EstimateTax(models.InvestmentTaxRegimeManual, 5000, applied, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if tax.Available {
		t.Fatal("MANUAL deve ser indisponível")
	}
	// Disponibilidade explícita: o campo existe mas não tem significado.
	// Nenhum caminho pode ler TaxCents sem checar Available.
}

func TestEstimateTaxUnknownRegimeFailsClosed(t *testing.T) {
	applied := mustDate(t, "2026-01-01")
	asOf := mustDateOffset(t, applied, 100)
	if _, err := EstimateTax("TRIBUTADO", 5000, applied, asOf); err == nil {
		t.Fatal("regime fora da lista congelada deve falhar (sem aliases)")
	}
}

// --- líquido -----------------------------------------------------------------

func TestEstimateLiquid(t *testing.T) {
	liquid, err := EstimateLiquid(1000, models.TaxEstimate{Available: true, TaxCents: 250})
	if err != nil || !liquid.Available || liquid.NetCents != 750 {
		t.Fatalf("líquido disponível: %+v, %v", liquid, err)
	}
	liquid, err = EstimateLiquid(1000, models.TaxEstimate{Available: false})
	if err != nil || liquid.Available {
		t.Fatalf("IR indisponível deve tornar líquido indisponível: %+v, %v", liquid, err)
	}
}

// --- variação mensal ----------------------------------------------------------

func TestMonthlyVariationGainWithPreviousValuation(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-03-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-05-20", 1000),
		valuation("2026-06-25", 1100),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.June)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available {
		t.Fatalf("variação deve estar disponível: %+v", variation)
	}
	if variation.PreviousCents != 1000 || variation.CurrentCents != 1100 {
		t.Fatalf("bases: %+v", variation)
	}
	if variation.DeltaCents != 100 || variation.BasisPoints != 1000 {
		t.Fatalf("ganho: delta=%d bps=%d, want 100/1000", variation.DeltaCents, variation.BasisPoints)
	}
}

func TestMonthlyVariationLoss(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-03-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-05-20", 1000),
		valuation("2026-06-25", 900),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.June)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available || variation.DeltaCents != -100 || variation.BasisPoints != -1000 {
		t.Fatalf("perda: %+v, %v", variation, err)
	}
}

func TestMonthlyVariationFallsBackToPrincipalWhenLotExisted(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 900)
	inv.AppliedDate = "2025-03-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-06-25", 1100),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.June)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available || variation.PreviousCents != 900 || variation.DeltaCents != 200 {
		t.Fatalf("fallback principal: %+v, %v", variation, err)
	}
	// 200 × 10000 / 900 = 2222 (truncado, sem float)
	if variation.BasisPoints != 2222 {
		t.Fatalf("bps truncado = %d, want 2222", variation.BasisPoints)
	}
}

func TestMonthlyVariationUnavailableWhenAppliedInTargetMonth(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 900)
	inv.AppliedDate = "2026-06-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-06-25", 1100),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.June)
	if err != nil {
		t.Fatal(err)
	}
	if variation.Available {
		t.Fatalf("aplicação nascida no próprio mês não tem base anterior: %+v", variation)
	}
}

func TestMonthlyVariationMonthRolloverBoundary(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-01-10"
	// A valuation de 2026-02-28 é a base de fevereiro; a de 01/03 não entra.
	valuations := []models.InvestmentValuation{
		valuation("2026-02-28", 700),
		valuation("2026-03-01", 999),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.March)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available || variation.PreviousCents != 700 {
		t.Fatalf("virada de mês deve usar 2026-02-28 como base: %+v", variation)
	}
}

func TestMonthlyVariationYearRolloverBoundary(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-06-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-12-31", 800),
		valuation("2027-01-02", 1200),
	}
	variation, err := MonthlyVariation(inv, valuations, 2027, time.January)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available || variation.PreviousCents != 800 {
		t.Fatalf("virada de ano deve usar 2026-12-31 como base: %+v", variation)
	}
}

func TestMonthlyVariationLeapYearBoundary(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2023-06-10"
	valuations := []models.InvestmentValuation{
		valuation("2024-02-29", 750),
	}
	variation, err := MonthlyVariation(inv, valuations, 2024, time.March)
	if err != nil {
		t.Fatal(err)
	}
	if !variation.Available || variation.PreviousCents != 750 {
		t.Fatalf("ano bissexto deve usar 2024-02-29 como base: %+v", variation)
	}
}

func TestMonthlyVariationExcludesFirstDayOfNextMonth(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-06-10"
	cases := []struct {
		name        string
		month       time.Month
		valuations  []models.InvestmentValuation
		wantCurrent int64
	}{
		{
			name:  "fevereiro não absorve 1º de março",
			month: time.February,
			valuations: []models.InvestmentValuation{
				valuation("2026-02-28", 700),
				valuation("2026-03-01", 999),
			},
			wantCurrent: 700,
		},
		{
			name:  "dezembro não absorve 1º de janeiro seguinte",
			month: time.December,
			valuations: []models.InvestmentValuation{
				valuation("2026-12-31", 800),
				valuation("2027-01-01", 1200),
			},
			wantCurrent: 800,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variation, err := MonthlyVariation(inv, tc.valuations, 2026, tc.month)
			if err != nil {
				t.Fatal(err)
			}
			if !variation.Available {
				t.Fatalf("variação deve estar disponível: %+v", variation)
			}
			if variation.CurrentCents != tc.wantCurrent {
				t.Fatalf("fechamento = %d, want %d (valuation do mês seguinte não entra no mês alvo)", variation.CurrentCents, tc.wantCurrent)
			}
			if variation.PreviousCents != 500 {
				t.Fatalf("base anterior = %d, want principal 500", variation.PreviousCents)
			}
		})
	}
}

func TestMonthlyVariationInvalidDenominatorFailsClosed(t *testing.T) {
	inv := testInvestment(models.InvestmentTaxRegimeIRRegressivo, 500)
	inv.AppliedDate = "2025-03-10"
	valuations := []models.InvestmentValuation{
		valuation("2026-05-20", 0),
		valuation("2026-06-25", 100),
	}
	variation, err := MonthlyVariation(inv, valuations, 2026, time.June)
	if err != nil {
		t.Fatal(err)
	}
	if variation.Available {
		t.Fatalf("denominador zero deve marcar indisponível, não dividir: %+v", variation)
	}
}

// --- agregações ----------------------------------------------------------------

func TestAggregatePortfolioSumsWithCheckedArithmetic(t *testing.T) {
	items := []PortfolioItem{
		{
			Investment: testInvestment(models.InvestmentTaxRegimeIRRegressivo, 1000),
			Valuations: []models.InvestmentValuation{valuation("2026-03-01", 2000)},
		},
		{
			Investment: testInvestment(models.InvestmentTaxRegimeIsentoPF, 500),
			Valuations: []models.InvestmentValuation{valuation("2026-03-01", 400)},
		},
	}
	asOf := mustDate(t, "2026-03-31")
	aggregate, err := AggregatePortfolio(items, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.PrincipalCents != 1500 || aggregate.CurrentCents != 2400 || aggregate.YieldCents != 900 {
		t.Fatalf("somas: %+v", aggregate)
	}
	if !aggregate.TaxAvailable || !aggregate.NetAvailable {
		t.Fatalf("sem MANUAL o agregado de IR/líquido é completo: %+v", aggregate)
	}
	// Lote 1: ganho 1000 em ≤180d → (1000×2250 + 5000)/10000 = 225 centavos.
	// Lote 2 (ISENTO_PF): perda −100 → IR zero legítimo.
	if aggregate.TaxCents != 225 {
		t.Fatalf("IR agregado = %d, want 225", aggregate.TaxCents)
	}
	if aggregate.NetCents != 2400-225 {
		t.Fatalf("líquido agregado = %d, want %d", aggregate.NetCents, 2400-225)
	}
}

func TestAggregatePortfolioMarksPartialWhenManualPresent(t *testing.T) {
	items := []PortfolioItem{
		{
			Investment: testInvestment(models.InvestmentTaxRegimeIRRegressivo, 1000),
			Valuations: []models.InvestmentValuation{valuation("2026-03-01", 2000)},
		},
		{
			Investment: testInvestment(models.InvestmentTaxRegimeManual, 500),
			Valuations: []models.InvestmentValuation{valuation("2026-03-01", 700)},
		},
	}
	aggregate, err := AggregatePortfolio(items, mustDate(t, "2026-03-31"))
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.PrincipalCents != 1500 || aggregate.CurrentCents != 2700 || aggregate.YieldCents != 1200 {
		t.Fatalf("somas exatas continuam: %+v", aggregate)
	}
	if aggregate.TaxAvailable || aggregate.NetAvailable {
		t.Fatalf("com MANUAL o agregado de IR/líquido é parcial: %+v", aggregate)
	}
}

func TestAggregatePortfolioFailsClosedOnOverflow(t *testing.T) {
	items := []PortfolioItem{
		{Investment: testInvestment(models.InvestmentTaxRegimeIsentoPF, math.MaxInt64)},
		{Investment: testInvestment(models.InvestmentTaxRegimeIsentoPF, math.MaxInt64)},
	}
	if _, err := AggregatePortfolio(items, mustDate(t, "2026-03-31")); err == nil {
		t.Fatal("agregação deve falhar com overflow, nunca saturar")
	}
}
