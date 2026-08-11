package handlers

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"
)

// recurringMaterializationFallbackCooldown evita disparo repetido de goroutines de
// fallback para o mesmo workspace em rajada de requests. O lock/throttle no banco
// já impede execução duplicada do motor; este cooldown apenas reduz a criação de
// goroutines redundantes por request.
const recurringMaterializationFallbackCooldown = 1 * time.Minute

// RunRecurringMaterializationSweep itera os workspaces com regras recorrentes ativas
// e executa RunRecurringMaterializationForWorkspace para cada um. Erros por workspace
// são logados e não interrompem os demais. Retorna quantos workspaces foram
// processados sem erro.
func RunRecurringMaterializationSweep(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	return runRecurringMaterializationSweep(ctx, db, now, RunRecurringMaterializationForWorkspace)
}

// recurringWorkspaceRunner executa a materialização de um único workspace. Injetável
// para testes do sweep/job sem depender do motor real.
type recurringWorkspaceRunner func(ctx context.Context, db *sql.DB, workspaceID string, now int64) error

func runRecurringMaterializationSweep(ctx context.Context, db *sql.DB, now time.Time, runWorkspace recurringWorkspaceRunner) (int, error) {
	if db == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT workspace_id
		FROM recurring_rules
		WHERE active = 1
		ORDER BY workspace_id ASC
	`)
	if err != nil {
		return 0, err
	}
	var workspaceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		workspaceIDs = append(workspaceIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	processed := 0
	for _, workspaceID := range workspaceIDs {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if err := runWorkspace(ctx, db, workspaceID, now.Unix()); err != nil {
			log.Printf("recurring materialization sweep: workspace %s failed: %v", workspaceID, err)
			continue
		}
		processed++
	}
	return processed, nil
}

// StartRecurringMaterializationJob liga o job diário do materializador recorrente.
// A primeira varredura roda de forma assíncrona logo após o boot (sem bloquear a
// aplicação) e, em seguida, um ticker dispara varreduras periódicas. Erros são
// logados e nunca derrubam o processo.
func StartRecurringMaterializationJob(ctx context.Context, db *sql.DB, interval time.Duration) <-chan struct{} {
	return startRecurringMaterializationJob(ctx, db, interval, runRecurringMaterializationSweepLogged)
}

func startRecurringMaterializationJob(ctx context.Context, db *sql.DB, interval time.Duration, run func(context.Context, *sql.DB, time.Time)) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		defer close(done)
		// Boot assíncrono: primeira varredura após startup, sem bloquear.
		run(ctx, db, time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				run(ctx, db, now)
			}
		}
	}()
	return done
}

func runRecurringMaterializationSweepLogged(ctx context.Context, db *sql.DB, now time.Time) {
	count, err := RunRecurringMaterializationSweep(ctx, db, now)
	if err != nil {
		log.Printf("recurring materialization sweep failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("recurring materialization sweep processed %d workspace(s)", count)
	}
}

// recurringStateIsStaleForWorkspace faz uma leitura barata do estado para decidir se
// o materializador precisa rodar. Estado ausente é tratado como stale.
func recurringStateIsStaleForWorkspace(db *sql.DB, workspaceID string, now int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	state, err := readRecurringMaterializationStateTx(tx, workspaceID)
	if err != nil {
		return false, err
	}

	horizon, err := defaultRecurringHorizon(now)
	if err != nil {
		return false, err
	}

	fresh, err := recurringStateIsFresh(state, horizon)
	if err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return !fresh, nil
}

// recurringMaterializationScheduler coordena o fallback leve por tela, evitando
// disparar goroutines redundantes para o mesmo workspace em rajada de requests.
type RecurringMaterializationFallback struct {
	db       *sql.DB
	cooldown time.Duration
	ctx      context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	closed    bool
	active    sync.WaitGroup
	lastFired map[string]int64

	// dispatch é injetável para testes; em produção agenda uma goroutine.
	dispatch func(ctx context.Context, workspaceID string, now int64)
}

type recurringMaterializationScheduler = RecurringMaterializationFallback

func newRecurringMaterializationScheduler(db *sql.DB) *RecurringMaterializationFallback {
	ctx, cancel := context.WithCancel(context.Background())
	s := &RecurringMaterializationFallback{
		db:        db,
		cooldown:  recurringMaterializationFallbackCooldown,
		ctx:       ctx,
		cancel:    cancel,
		lastFired: make(map[string]int64),
	}
	s.dispatch = s.dispatchSync
	return s
}

func (s *RecurringMaterializationFallback) dispatchSync(ctx context.Context, workspaceID string, now int64) {
	if err := RunRecurringMaterializationForWorkspace(ctx, s.db, workspaceID, now); err != nil && ctx.Err() == nil {
		log.Printf("recurring materialization fallback: workspace %s failed: %v", workspaceID, err)
	}
}

// maybeSchedule aplica o cooldown in-memory e decide se dispara o materializador.
// Retorna true quando o dispatch foi acionado.
func (s *RecurringMaterializationFallback) maybeSchedule(workspaceID string, now int64) bool {
	if s.db == nil || workspaceID == "" {
		return false
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.active.Add(1)
	ctx := s.ctx
	dispatch := s.dispatch
	s.mu.Unlock()
	defer s.active.Done()

	s.mu.Lock()
	last, ok := s.lastFired[workspaceID]
	if ok && now-last < int64(s.cooldown.Seconds()) {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	stale, err := recurringStateIsStaleForWorkspace(s.db, workspaceID, now)
	if err != nil {
		log.Printf("recurring materialization fallback: stale check for workspace %s failed: %v", workspaceID, err)
		return false
	}
	if !stale {
		return false
	}

	s.mu.Lock()
	s.lastFired[workspaceID] = now
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.active.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.active.Done()
		dispatch(ctx, workspaceID, now)
	}()
	return true
}

// packageRecurringMaterializationScheduler é o scheduler global usado pelo fallback
// de tela. É inicializado por InitRecurringMaterializationFallback no boot.
var (
	packageSchedulerMu        sync.RWMutex
	packageRecurringScheduler *RecurringMaterializationFallback
)

// InitRecurringMaterializationFallback prepara o scheduler global de fallback leve.
// Deve ser chamada uma vez no boot. Sem essa inicialização, o fallback é no-op.
func InitRecurringMaterializationFallback(db *sql.DB) *RecurringMaterializationFallback {
	scheduler := newRecurringMaterializationScheduler(db)
	packageSchedulerMu.Lock()
	packageRecurringScheduler = scheduler
	packageSchedulerMu.Unlock()
	return scheduler
}

// Close removes this fallback generation from the package-level dispatcher,
// prevents new operations, cancels active materializations and waits until no
// stale check or dispatch can still use its database.
func (s *RecurringMaterializationFallback) Close() {
	if s == nil {
		return
	}
	packageSchedulerMu.Lock()
	if packageRecurringScheduler == s {
		packageRecurringScheduler = nil
	}
	packageSchedulerMu.Unlock()

	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.active.Wait()
}

// ScheduleRecurringMaterializationFallback é o gancho leve para telas críticas
// (Dashboard, Lançamentos). Faz uma checagem barata de frescor e, se stale, agenda a
// materialização em background. Nunca bloqueia o request nem propaga erro.
func ScheduleRecurringMaterializationFallback(workspaceID string) {
	packageSchedulerMu.RLock()
	scheduler := packageRecurringScheduler
	packageSchedulerMu.RUnlock()
	if scheduler == nil {
		return
	}
	scheduler.maybeSchedule(workspaceID, time.Now().Unix())
}
