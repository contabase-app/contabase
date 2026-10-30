package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/services"
)

// The series deliberately reuses buildAccountClosingBalancesTx, the same
// canonical inverse used by the closing report. It never invents movements or
// applies a client-supplied amount. Account inclusion follows the same policy
// as the Dashboard: active real accounts only, credit cards excluded.
const (
	balanceSeriesCurrency        = "BRL"
	balanceSeriesUnit            = "cents"
	balanceSeriesMaxPoints       = 48
	balanceSeriesPercentCapTenth = 9990
)

type balanceSeriesPeriod struct {
	Key   string
	Label string
	Days  int
}

var balanceSeriesPeriods = map[string]balanceSeriesPeriod{
	"30d": {Key: "30d", Label: "30 dias", Days: 30},
	"90d": {Key: "90d", Label: "90 dias", Days: 90},
	"6m":  {Key: "6m", Label: "6 meses", Days: 180},
	"12m": {Key: "12m", Label: "12 meses", Days: 365},
}

// DashboardBalancePoint is one reconstructed end-of-day consolidated balance.
// BalanceCents is a decimal string to preserve int64 exactness in JavaScript.
type DashboardBalancePoint struct {
	Date         string `json:"date"`
	BalanceCents string `json:"balance_cents"`
}

// DashboardBalanceLimitation is a human-readable precision caveat. Codes are
// stable for tests and future clients; messages are already localized.
type DashboardBalanceLimitation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DashboardBalanceVariation compares the first and last comparable points with
// integer arithmetic. It is intentionally separate from the KPI trend, which
// keeps its own 30-day methodology.
type DashboardBalanceVariation struct {
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	BaseDate      string `json:"base_date,omitempty"`
	BaseCents     string `json:"base_cents,omitempty"`
	LatestDate    string `json:"latest_date,omitempty"`
	LatestCents   string `json:"latest_cents,omitempty"`
	ChangeCents   string `json:"change_cents,omitempty"`
	PercentTenths int    `json:"percent_tenths,omitempty"`
	Capped        bool   `json:"capped"`
	Direction     string `json:"direction,omitempty"`
}

type DashboardBalanceSeries struct {
	Status             string                       `json:"status"`
	Reason             string                       `json:"reason,omitempty"`
	Period             string                       `json:"period"`
	PeriodLabel        string                       `json:"period_label"`
	Currency           string                       `json:"currency"`
	Unit               string                       `json:"unit"`
	RequestedStart     string                       `json:"requested_start"`
	CoverageStart      string                       `json:"coverage_start,omitempty"`
	CoverageEnd        string                       `json:"coverage_end,omitempty"`
	SampleIntervalDays int                          `json:"sample_interval_days"`
	Points             []DashboardBalancePoint      `json:"points"`
	Variation          *DashboardBalanceVariation   `json:"variation,omitempty"`
	Reconciled         bool                         `json:"reconciled"`
	Limitations        []DashboardBalanceLimitation `json:"limitations"`
}

type balanceSeriesAccountLifecycle struct {
	CreatedAt  int64
	ArchivedAt sql.NullInt64
}

// BuildDashboardBalanceSeries reconstructs the consolidated real-account
// balance for sampled end-of-day dates in the requested period.
//
// The method is the inverse of the canonical balance write path:
// current_balance minus every STANDARD paid movement and audited adjustment
// whose effective date is after the point. Pending and CLASSIFICATION_ONLY
// movements have no balance effect and are ignored by the same view used by the
// closing report. Credit-card accounts are excluded from the consolidated total.
//
// The last point must reconcile exactly with the canonical active-account sum.
// If it does not, no series is returned: an unanchored line would be an
// approximation presented as fact, and the contract explicitly forbids that.
func BuildDashboardBalanceSeries(db *sql.DB, workspaceID string, period balanceSeriesPeriod, now time.Time, loc *time.Location) (DashboardBalanceSeries, error) {
	result := DashboardBalanceSeries{
		Status:             "unavailable",
		Period:             period.Key,
		PeriodLabel:        period.Label,
		Currency:           balanceSeriesCurrency,
		Unit:               balanceSeriesUnit,
		SampleIntervalDays: 1,
		Points:             []DashboardBalancePoint{},
		Limitations:        []DashboardBalanceLimitation{},
	}
	if db == nil {
		return result, fmt.Errorf("banco obrigatório")
	}
	if strings.TrimSpace(workspaceID) == "" {
		return result, fmt.Errorf("workspace obrigatório")
	}
	if loc == nil {
		loc = time.UTC
	}
	if period.Days <= 0 {
		return result, fmt.Errorf("período inválido")
	}

	endDate := civildate.CivilDateAt(now, loc)
	startDate, err := civildate.CivilDateAddDays(endDate, -(period.Days - 1))
	if err != nil {
		return result, fmt.Errorf("início do período: %w", err)
	}
	result.RequestedStart = startDate.String()

	sampleDates, step, err := balanceSeriesSampleDates(startDate, endDate, balanceSeriesMaxPoints)
	if err != nil {
		return result, err
	}
	result.SampleIntervalDays = step

	ctx := context.Background()
	readTx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("iniciar leitura da série: %w", err)
	}
	defer readTx.Rollback()

	lifecycles, err := queryBalanceSeriesLifecycles(ctx, readTx, workspaceID)
	if err != nil {
		return result, err
	}
	canonicalCurrent, err := queryCanonicalActiveRealBalance(ctx, readTx, workspaceID)
	if err != nil {
		return result, err
	}

	requestedStartUnix, err := civilDateStartOfDayUnix(startDate, loc)
	if err != nil {
		return result, err
	}
	requestedEndUnix, err := civilDateEndOfDayUnix(endDate, loc)
	if err != nil {
		return result, err
	}

	points := make([]DashboardBalancePoint, 0, len(sampleDates))
	var lastTotal int64
	coverageStarted := false
	for _, date := range sampleDates {
		pointEnd, err := civilDateEndOfDayUnix(date, loc)
		if err != nil {
			return result, err
		}
		balances, err := buildAccountClosingBalancesTx(ctx, readTx, workspaceID, pointEnd)
		if err != nil {
			return result, fmt.Errorf("reconstruir %s: %w", date.String(), err)
		}
		total, included, err := sumBalanceSeriesPoint(balances, lifecycles, pointEnd)
		if err != nil {
			return result, err
		}
		// Omit only the leading span before the first account existed. Once the
		// workspace has coverage, zero totals are legitimate (for example, all
		// accounts archived) and are kept.
		if !coverageStarted && included == 0 {
			continue
		}
		coverageStarted = true
		points = append(points, DashboardBalancePoint{Date: date.String(), BalanceCents: formatCents(total)})
		lastTotal = total
	}

	if len(points) == 0 {
		result.Status = "unavailable"
		result.Reason = "no_accounts"
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "no_accounts",
			Message: "Não há contas reais com histórico disponível neste workspace.",
		})
		return result, nil
	}

	result.CoverageStart = points[0].Date
	result.CoverageEnd = points[len(points)-1].Date
	result.Points = points

	if lastTotal != canonicalCurrent {
		result.Status = "unavailable"
		result.Reason = "reconciliation_mismatch"
		result.CoverageStart = ""
		result.CoverageEnd = ""
		result.Points = []DashboardBalancePoint{}
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "reconciliation_mismatch",
			Message: "A reconstrução não reconciliou com o saldo consolidado atual; a série não foi exibida.",
		})
		return result, nil
	}
	result.Reconciled = true

	result.Limitations = append(result.Limitations,
		DashboardBalanceLimitation{
			Code:    "reconstruction",
			Message: "Histórico reconstruído a partir dos saldos atuais e dos registros financeiros disponíveis; não é um snapshot histórico certificado.",
		},
		DashboardBalanceLimitation{
			Code:    "retroactive_changes",
			Message: "Edições, exclusões e correções retroativas de lançamentos podem alterar pontos anteriores.",
		},
	)
	if step > 1 {
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "sampled_series",
			Message: fmt.Sprintf("Série amostrada a cada %d dias para manter a leitura e a consulta leves.", step),
		})
	}

	status := "reconstructed"
	lifecycleChanged := false
	archivedInPeriod := false
	for _, lifecycle := range lifecycles {
		if lifecycle.CreatedAt >= requestedStartUnix && lifecycle.CreatedAt <= requestedEndUnix {
			lifecycleChanged = true
		}
		if lifecycle.ArchivedAt.Valid && lifecycle.ArchivedAt.Int64 >= requestedStartUnix && lifecycle.ArchivedAt.Int64 <= requestedEndUnix {
			lifecycleChanged = true
			archivedInPeriod = true
		}
	}
	if archivedInPeriod {
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "account_archived_in_period",
			Message: "Contas arquivadas deixaram de compor o total a partir do arquivamento; isso pode aparecer como queda sem movimentação financeira.",
		})
	}
	if lifecycleChanged {
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "account_lifecycle",
			Message: "Contas criadas ou arquivadas no período entram ou saem do total conforme created_at/archived_at. Reativações não preservam o histórico anterior de arquivamento.",
		})
	}
	if result.CoverageStart != result.RequestedStart {
		status = "partial"
		result.Limitations = append(result.Limitations, DashboardBalanceLimitation{
			Code:    "partial_coverage",
			Message: fmt.Sprintf("A série começa em %s, depois do início solicitado; os primeiros pontos disponíveis delimitam a cobertura.", formatBalanceSeriesDateLabel(result.CoverageStart)),
		})
	}
	result.Status = status
	result.Variation = buildBalanceSeriesVariation(points)
	return result, nil
}

func parseBalanceSeriesPeriod(raw string) (balanceSeriesPeriod, bool) {
	period, ok := balanceSeriesPeriods[strings.ToLower(strings.TrimSpace(raw))]
	return period, ok
}

func balanceSeriesSampleDates(start, end civildate.CivilDate, maxPoints int) ([]civildate.CivilDate, int, error) {
	totalDays := int(civildate.CivilDateDaysBetween(start, end)) + 1
	if totalDays <= 0 {
		return nil, 0, fmt.Errorf("período vazio")
	}
	step := (totalDays + maxPoints - 1) / maxPoints
	if step < 1 {
		step = 1
	}
	dates := make([]civildate.CivilDate, 0, (totalDays/step)+2)
	for offset := 0; offset < totalDays; offset += step {
		date, err := civildate.CivilDateAddDays(start, offset)
		if err != nil {
			return nil, 0, err
		}
		dates = append(dates, date)
	}
	last := dates[len(dates)-1]
	if civildate.CivilDateDaysBetween(last, end) != 0 {
		dates = append(dates, end)
	}
	return dates, step, nil
}

func civilDateStartOfDayUnix(date civildate.CivilDate, loc *time.Location) (int64, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date.String(), loc)
	if err != nil {
		return 0, fmt.Errorf("data civil inválida: %w", err)
	}
	return parsed.Unix(), nil
}

func civilDateEndOfDayUnix(date civildate.CivilDate, loc *time.Location) (int64, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date.String(), loc)
	if err != nil {
		return 0, fmt.Errorf("data civil inválida: %w", err)
	}
	return parsed.AddDate(0, 0, 1).Add(-time.Second).Unix(), nil
}

func queryBalanceSeriesLifecycles(ctx context.Context, tx *sql.Tx, workspaceID string) (map[string]balanceSeriesAccountLifecycle, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(created_at, 0), archived_at
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD'
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("consultar ciclo de vida das contas: %w", err)
	}
	defer rows.Close()

	lifecycles := make(map[string]balanceSeriesAccountLifecycle)
	for rows.Next() {
		var id string
		var lifecycle balanceSeriesAccountLifecycle
		if err := rows.Scan(&id, &lifecycle.CreatedAt, &lifecycle.ArchivedAt); err != nil {
			return nil, fmt.Errorf("ler ciclo de vida da conta: %w", err)
		}
		lifecycles[id] = lifecycle
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("percorrer ciclo de vida das contas: %w", err)
	}
	return lifecycles, nil
}

// queryCanonicalActiveRealBalance mirrors the account filter used by
// services.CalculateWorkspaceReserveBalance for the real balance: every active
// non-credit-card account. It runs in the same read transaction as the points so
// the reconciliation anchor and the series share one snapshot.
func queryCanonicalActiveRealBalance(ctx context.Context, tx *sql.Tx, workspaceID string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT current_balance
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL
	`, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("consultar saldo canônico: %w", err)
	}
	defer rows.Close()

	total := new(big.Int)
	value := new(big.Int)
	for rows.Next() {
		var cents int64
		if err := rows.Scan(&cents); err != nil {
			return 0, fmt.Errorf("ler saldo canônico: %w", err)
		}
		total.Add(total, value.SetInt64(cents))
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("percorrer saldo canônico: %w", err)
	}
	if !total.IsInt64() {
		return 0, services.ErrBalanceAggregateOutOfRange
	}
	return total.Int64(), nil
}

func sumBalanceSeriesPoint(balances []AccountClosingBalance, lifecycles map[string]balanceSeriesAccountLifecycle, pointEnd int64) (int64, int, error) {
	byID := make(map[string]int64, len(balances))
	for _, account := range balances {
		byID[account.ID] = account.Balance
	}

	total := new(big.Int)
	value := new(big.Int)
	included := 0
	for id, lifecycle := range lifecycles {
		if lifecycle.CreatedAt > pointEnd {
			continue
		}
		if lifecycle.ArchivedAt.Valid && lifecycle.ArchivedAt.Int64 <= pointEnd {
			continue
		}
		included++
		if balance, ok := byID[id]; ok {
			total.Add(total, value.SetInt64(balance))
		}
	}
	if !total.IsInt64() {
		return 0, 0, services.ErrBalanceAggregateOutOfRange
	}
	return total.Int64(), included, nil
}

func buildBalanceSeriesVariation(points []DashboardBalancePoint) *DashboardBalanceVariation {
	variation := &DashboardBalanceVariation{}
	if len(points) < 2 {
		variation.Reason = "insufficient_points"
		return variation
	}
	base, ok1 := parseCentsString(points[0].BalanceCents)
	latest, ok2 := parseCentsString(points[len(points)-1].BalanceCents)
	if !ok1 || !ok2 {
		variation.Reason = "invalid_series"
		return variation
	}
	variation.BaseDate = points[0].Date
	variation.BaseCents = formatCents(base)
	variation.LatestDate = points[len(points)-1].Date
	variation.LatestCents = formatCents(latest)
	change := new(big.Int).Sub(big.NewInt(latest), big.NewInt(base))
	variation.ChangeCents = change.String()

	baseAbs := new(big.Int).Abs(big.NewInt(base))
	if baseAbs.Cmp(big.NewInt(10)) < 0 {
		variation.Reason = "insufficient_base"
		return variation
	}
	absChange := new(big.Int).Abs(new(big.Int).Set(change))
	numerator := new(big.Int).Mul(absChange, big.NewInt(1000))
	numerator.Add(numerator, new(big.Int).Quo(baseAbs, big.NewInt(2)))
	percent := new(big.Int).Quo(numerator, baseAbs)
	if percent.Cmp(big.NewInt(balanceSeriesPercentCapTenth)) > 0 {
		percent.SetInt64(balanceSeriesPercentCapTenth)
		variation.Capped = true
	}

	variation.Available = true
	variation.PercentTenths = int(percent.Int64())
	switch change.Sign() {
	case 1:
		variation.Direction = "up"
	case -1:
		variation.Direction = "down"
	default:
		variation.Direction = "flat"
	}
	return variation
}

func parseCentsString(value string) (int64, bool) {
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || !parsed.IsInt64() {
		return 0, false
	}
	return parsed.Int64(), true
}

func formatCents(cents int64) string {
	return big.NewInt(cents).String()
}

func formatBalanceSeriesDateLabel(value string) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return parsed.Format("02/01/2006")
}

// DashboardBalanceSeriesHandler is bound only by an authenticated route that
// already resolved the active workspace and financial read permission.
type DashboardBalanceSeriesHandler struct {
	DB          *sql.DB
	WorkspaceID string
	Location    *time.Location
	Now         func() time.Time
}

func (h *DashboardBalanceSeriesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeBalanceSeriesJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "metodo_invalido"})
		return
	}
	period, ok := parseBalanceSeriesPeriod(r.URL.Query().Get("period"))
	if !ok {
		writeBalanceSeriesJSON(w, http.StatusBadRequest, map[string]string{"error": "periodo_invalido"})
		return
	}
	if h == nil || h.DB == nil || strings.TrimSpace(h.WorkspaceID) == "" {
		writeBalanceSeriesJSON(w, http.StatusInternalServerError, map[string]string{"error": "indisponivel"})
		return
	}
	loc := h.Location
	if loc == nil {
		loc = appTimeLocation()
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}

	data, err := BuildDashboardBalanceSeries(h.DB, h.WorkspaceID, period, now, loc)
	if err != nil {
		if errors.Is(err, services.ErrBalanceAggregateOutOfRange) {
			log.Printf("dashboard balance series aggregate out of range")
		} else {
			log.Printf("dashboard balance series unavailable")
		}
		writeBalanceSeriesJSON(w, http.StatusInternalServerError, map[string]string{"error": "indisponivel"})
		return
	}
	writeBalanceSeriesJSON(w, http.StatusOK, data)
}

func writeBalanceSeriesJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
