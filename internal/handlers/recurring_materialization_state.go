package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

// recurringMaterializationHorizonMonths é a janela móvel padrão de materialização.
const recurringMaterializationHorizonMonths = 15

// recurringMaterializationLockTTL é a validade padrão do lock. Após esse período,
// um lock preso (ex.: processo morto no meio da execução) pode ser retomado.
const recurringMaterializationLockTTL = 10 * time.Minute

// RecurringMaterializationState espelha uma linha de recurring_materialization_state.
type RecurringMaterializationState struct {
	WorkspaceID            string
	MaterializedUntil      int64
	MaterializedUntilCivil sql.NullString
	LastAttemptAt          int64
	LastSuccessAt          int64
	LastError              sql.NullString
	Status                 string
	LockedUntil            int64
	LockOwner              sql.NullString
}

// defaultRecurringHorizon calcula o horizonte padrão usando o timezone da aplicação:
// hoje civil em America/Sao_Paulo + 15 meses civis. Retorna CivilWriteDate com
// INTEGER em 12:00 UTC.
func defaultRecurringHorizon(now int64) (CivilWriteDate, error) {
	return recurrenceCivilHorizon(time.Unix(now, 0).UTC())
}

// ensureRecurringMaterializationStateRowTx garante que exista uma linha de estado
// para o workspace. Não sobrescreve estado existente.
func ensureRecurringMaterializationStateRowTx(tx *sql.Tx, workspaceID string, now int64) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace_id obrigatorio")
	}
	_, err := tx.Exec(`
		INSERT INTO recurring_materialization_state (
			workspace_id, materialized_until, last_attempt_at, last_success_at,
			last_error, status, locked_until, lock_owner, created_at, updated_at
		) VALUES (?, 0, 0, 0, NULL, 'idle', 0, NULL, ?, ?)
		ON CONFLICT(workspace_id) DO NOTHING
	`, workspaceID, now, now)
	return err
}

// ensureRecurringMaterializationStateRow garante a linha de estado usando uma tx própria.
func ensureRecurringMaterializationStateRow(db *sql.DB, workspaceID string, now int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := ensureRecurringMaterializationStateRowTx(tx, workspaceID, now); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readRecurringMaterializationStateTx lê o estado de um workspace. Retorna (nil, nil)
// quando não há linha (tratado como stale pelo caller).
func readRecurringMaterializationStateTx(tx *sql.Tx, workspaceID string) (*RecurringMaterializationState, error) {
	var s RecurringMaterializationState
	err := tx.QueryRow(`
		SELECT workspace_id, materialized_until, materialized_until_civil, last_attempt_at, last_success_at,
		       last_error, status, locked_until, lock_owner
		FROM recurring_materialization_state
		WHERE workspace_id = ?
	`, workspaceID).Scan(
		&s.WorkspaceID, &s.MaterializedUntil, &s.MaterializedUntilCivil, &s.LastAttemptAt, &s.LastSuccessAt,
		&s.LastError, &s.Status, &s.LockedUntil, &s.LockOwner,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// recurringStateIsFresh retorna true quando materialized_until_civil já cobre o
// horizonte civil. Compara datas civis, não timestamps. Estado ausente é sempre stale.
// Civil persistida inválida retorna erro (não trata como stale).
func recurringStateIsFresh(state *RecurringMaterializationState, horizon CivilWriteDate) (bool, error) {
	if state == nil {
		return false, nil
	}
	var stateCivil string
	if state.MaterializedUntilCivil.Valid {
		if _, err := ParseCivilDateInput(state.MaterializedUntilCivil.String); err != nil {
			return false, fmt.Errorf("materialized_until_civil invalido: %w", err)
		}
		stateCivil = state.MaterializedUntilCivil.String
	} else if state.MaterializedUntil > 0 {
		stateCivil = civildate.LegacyUnixToCivilDateUTC(state.MaterializedUntil).String()
	} else {
		return false, nil
	}
	return stateCivil >= horizon.Civil(), nil
}

// acquireRecurringMaterializationLockTx tenta adquirir o lock de forma atômica via
// UPDATE condicional. O lock é concedido apenas se não houver lock ativo
// (locked_until <= now). Retorna true quando o lock foi adquirido por este owner.
func acquireRecurringMaterializationLockTx(tx *sql.Tx, workspaceID, owner string, now, lockedUntil int64) (bool, error) {
	res, err := tx.Exec(`
		UPDATE recurring_materialization_state
		SET locked_until = ?,
		    lock_owner = ?,
		    status = 'running',
		    last_attempt_at = ?,
		    updated_at = ?
		WHERE workspace_id = ?
		  AND locked_until <= ?
	`, lockedUntil, owner, now, now, workspaceID, now)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// releaseRecurringMaterializationLockSuccessTx registra sucesso e libera o lock.
func releaseRecurringMaterializationLockSuccessTx(tx *sql.Tx, workspaceID, owner string, materializedUntil, now int64, materializedUntilCivil string) error {
	res, err := tx.Exec(`
		UPDATE recurring_materialization_state
		SET status = 'idle',
		    materialized_until = ?,
		    materialized_until_civil = ?,
		    last_success_at = ?,
		    last_error = NULL,
		    locked_until = 0,
		    lock_owner = NULL,
		    updated_at = ?
		WHERE workspace_id = ? AND lock_owner = ?
	`, materializedUntil, materializedUntilCivil, now, now, workspaceID, owner)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("materialization lock lost for workspace %s: lock_owner changed during execution", workspaceID)
	}
	if affected > 1 {
		return fmt.Errorf("materialization state integrity error for workspace %s: %d rows affected", workspaceID, affected)
	}
	return nil
}

// releaseRecurringMaterializationLockErrorTx registra erro e libera o lock, sem panic.
func releaseRecurringMaterializationLockErrorTx(tx *sql.Tx, workspaceID, owner, message string, now int64) error {
	_, err := tx.Exec(`
		UPDATE recurring_materialization_state
		SET status = 'error',
		    last_error = ?,
		    locked_until = 0,
		    lock_owner = NULL,
		    updated_at = ?
		WHERE workspace_id = ? AND lock_owner = ?
	`, message, now, workspaceID, owner)
	return err
}

// recurringMaterializationRunner executa o motor de materialização. É injetável para
// permitir testes sem depender do motor real.
type recurringMaterializationRunner func(tx *sql.Tx, workspaceID string, horizon CivilWriteDate, now int64) error

// defaultRecurringMaterializationRunner delega ao motor central da Fase 7.a.
func defaultRecurringMaterializationRunner(tx *sql.Tx, workspaceID string, horizon CivilWriteDate, now int64) error {
	return EnsureRecurringMaterializedTx(tx, workspaceID, horizon, now)
}

// RunRecurringMaterializationForWorkspace orquestra a materialização recorrente de um
// workspace com estado, lock e throttle. NÃO é chamada automaticamente nesta fase;
// deve ser invocada explicitamente pelo caller (job/boot/fallback futuros).
//
// Concorrência: é seguro chamar esta função a partir de múltiplas goroutines ou
// processos simultaneamente. O lock é adquirido por UPDATE condicional atômico
// (acquireRecurringMaterializationLockTx); apenas um chamador vence e executa o
// motor, os demais retornam sem erro fatal.
//
// TTL do lock: o lock expira após recurringMaterializationLockTTL (10 min). Se um
// runner demorar mais que isso (improvável para a janela de 15 meses), outro
// chamador poderia retomar o lock e iniciar execução paralela. Isso é aceitável
// para a carga esperada; o motor é idempotente e não duplica ocorrências.
//
// Fluxo:
//  1. Garante linha de estado.
//  2. Calcula horizonte padrão (hoje + 15 meses).
//  3. Se fresh, retorna sem executar.
//  4. Se stale, tenta adquirir lock atômico.
//  5. Se lock ativo em outro owner, retorna sem erro fatal.
//  6. Executa o motor; sucesso/erro atualizam o estado e sempre liberam o lock.
func RunRecurringMaterializationForWorkspace(ctx context.Context, db *sql.DB, workspaceID string, now int64) error {
	return runRecurringMaterializationForWorkspace(ctx, db, workspaceID, now, defaultRecurringMaterializationRunner)
}

func runRecurringMaterializationForWorkspace(ctx context.Context, db *sql.DB, workspaceID string, now int64, runner recurringMaterializationRunner) error {
	if db == nil {
		return fmt.Errorf("db obrigatorio")
	}
	if workspaceID == "" {
		return fmt.Errorf("workspace_id obrigatorio")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	horizon, err := defaultRecurringHorizon(now)
	if err != nil {
		return err
	}
	owner := fmt.Sprintf("%s-%d", workspaceID, now)

	// Passo 1-2: garantir estado e checar frescor.
	txCheck, err := db.Begin()
	if err != nil {
		return err
	}
	if err := ensureRecurringMaterializationStateRowTx(txCheck, workspaceID, now); err != nil {
		_ = txCheck.Rollback()
		return err
	}
	state, err := readRecurringMaterializationStateTx(txCheck, workspaceID)
	if err != nil {
		_ = txCheck.Rollback()
		return err
	}
	fresh, err := recurringStateIsFresh(state, horizon)
	if err != nil {
		_ = txCheck.Rollback()
		return err
	}
	if fresh {
		return txCheck.Commit()
	}

	// Passo 4: adquirir lock atômico.
	acquired, err := acquireRecurringMaterializationLockTx(txCheck, workspaceID, owner, now, now+int64(recurringMaterializationLockTTL.Seconds()))
	if err != nil {
		_ = txCheck.Rollback()
		return err
	}
	if !acquired {
		// Passo 5: lock ativo em outro owner; não é erro fatal.
		return txCheck.Commit()
	}
	if err := txCheck.Commit(); err != nil {
		return err
	}

	// Passo 6: executar motor em transação separada. Sucesso e erro sempre liberam o lock.
	txRun, err := db.Begin()
	if err != nil {
		// Tenta liberar o lock em transação própria para não deixá-lo preso.
		_ = releaseLockBestEffort(db, workspaceID, owner, "begin run tx: "+err.Error(), now)
		return err
	}

	runErr := runner(txRun, workspaceID, horizon, now)
	if runErr != nil {
		_ = txRun.Rollback()
		if relErr := releaseLockBestEffort(db, workspaceID, owner, runErr.Error(), now); relErr != nil {
			return fmt.Errorf("run error %v; release error %w", runErr, relErr)
		}
		return runErr
	}

	if err := releaseRecurringMaterializationLockSuccessTx(txRun, workspaceID, owner, horizon.LegacyUnix(), now, horizon.Civil()); err != nil {
		_ = txRun.Rollback()
		_ = releaseLockBestEffort(db, workspaceID, owner, "success update: "+err.Error(), now)
		return err
	}
	if err := txRun.Commit(); err != nil {
		return err
	}
	var frozenCount int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_occurrence_blocks
		WHERE workspace_id = ? AND created_at = ?
	`, workspaceID, now).Scan(&frozenCount); err != nil {
		log.Printf("recurring materialization: committed frozen block count unavailable: %v", err)
	} else if frozenCount > 0 {
		log.Printf("recurring materialization: committed %d frozen invoice occurrence block(s)", frozenCount)
	}
	return nil
}

// releaseLockBestEffort libera o lock em transação própria, registrando erro. Usado
// quando a transação de execução precisou ser revertida.
func releaseLockBestEffort(db *sql.DB, workspaceID, owner, message string, now int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := releaseRecurringMaterializationLockErrorTx(tx, workspaceID, owner, message, now); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
