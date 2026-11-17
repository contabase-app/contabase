package main

// R1.3-D5-SEC-R2-RED — A3: validação do journal de restore pode impedir a
// recuperação pós-crash.
//
// Defeito (D5-SEC-A): os writers reais de restore estagiam o candidato como
// <dbdir>/.contabase-import-<rand>/<nome-original-do-upload>.db (idem
// .contabase-bootstrap-restore-*), e o handler aceita uploads com extensão
// .db OU .DB (strings.ToLower / EqualFold). Mas validateRestoreJournal exige
// que o candidato esteja DIRETO no diretório do banco (filepath.Dir ==
// dbDir) E que o BASE NAME tenha sufixo ".db" sensível a caixa com prefixo
// "contabase-import-"/"contabase-bootstrap-restore-". Ou seja, para o layout
// real de staging a validação falha no cheque de diretório ("escaped the
// database directory") para QUALQUER nome de upload; mesmo achatando o
// arquivo no diretório, nomes comuns (ex.: backup.db) falham no prefixo e
// uploads .DB (aceitos no upload) falham no sufixo. Resultado: um crash entre
// restore_journal_prepared e a remoção do journal deixa o servidor incapaz de
// iniciar — a validação fail-closed rejeita journals produzidos pelos próprios
// writers, exatamente quando a recuperação é necessária. Os testes históricos
// usam candidato plano "contabase-import-*.db" no diretório do banco — único
// formato que passa — e nunca exercitaram o staging real no recovery.
//
// Contrato esperado (fica GREEN após a correção A3):
//   - crash com staging REAL dos writers (qualquer nome de arquivo aceito no
//     upload) em fase prepared deve ser recuperável (rollback para o original);
//   - crash em fase validated deve ser aceito (banco candidato instalado);
//   - as garantias fail-closed permanecem: journal não regular (symlink/fifo),
//     caminhos fora do diretório do banco, backup inutilizável e proteção
//     contra inicialização vazia continuam bloqueando;
//   - a recuperação é idempotente e repetível.
//
// Hoje os testes RED falham de forma determinística exibindo o defeito.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type realStagingFixture struct {
	root      string
	live      string
	backupDir string
}

func newRealStagingFixture(t *testing.T) *realStagingFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	backupDir := filepath.Join(root, "backups", "pre-restore")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &realStagingFixture{
		root:      root,
		live:      filepath.Join(root, "contabase.db"),
		backupDir: backupDir,
	}
}

// stageRealAdminUpload grava um upload administrativo REAL (writer de produção)
// e devolve o path do candidato estagiado, como o handler entregaria ao swap.
func (f *realStagingFixture) stageRealAdminUpload(t *testing.T, filename, sourceDB string) string {
	t.Helper()
	return f.stageRealUpload(t, filename, sourceDB, saveAdminBackupImportUpload)
}

func (f *realStagingFixture) stageRealBootstrapUpload(t *testing.T, filename, sourceDB string) string {
	t.Helper()
	return f.stageRealUpload(t, filename, sourceDB, saveBootstrapBackupRestoreUpload)
}

func (f *realStagingFixture) stageRealUpload(t *testing.T, filename, sourceDB string, save func(string, string, io.Reader) (string, func(), error)) string {
	t.Helper()
	file, err := os.Open(sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stagingPath, _, err := save(f.live, filename, file)
	if err != nil {
		t.Fatalf("stage real upload %q: %v", filename, err)
	}
	return stagingPath
}

func (f *realStagingFixture) seedBackup(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(f.backupDir, name)
	seedRestoreRecoveryDatabase(t, path, "original")
	return path
}

// crashPrepared materializa o estado de crash após o checkpoint
// restore_journal_prepared: banco ativo renomeado para working copy, journal
// prepared com os paths REAIS dos writers (staging + backup).
func (f *realStagingFixture) crashPrepared(t *testing.T, stagingPath, backupPath, oldName string) string {
	t.Helper()
	oldPath := filepath.Join(f.root, oldName)
	if err := os.Rename(f.live, oldPath); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{
		Version:              restoreJournalVersion,
		Phase:                restoreJournalPhasePrepared,
		DatabasePath:         f.live,
		OriginalPath:         oldPath,
		CandidatePath:        stagingPath,
		PreRestoreBackupPath: backupPath,
	}
	if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
		t.Fatalf("write restore journal: %v", err)
	}
	return oldPath
}

// crashInstalled materializa o crash após candidate_installed: candidato já
// renomeado sobre o banco ativo (staging não contém mais o arquivo .db),
// working copy e journal prepared ainda presentes.
func (f *realStagingFixture) crashInstalled(t *testing.T, stagingPath, backupPath, oldName string) string {
	t.Helper()
	oldPath := filepath.Join(f.root, oldName)
	if err := os.Rename(f.live, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stagingPath, f.live); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{
		Version:              restoreJournalVersion,
		Phase:                restoreJournalPhasePrepared,
		DatabasePath:         f.live,
		OriginalPath:         oldPath,
		CandidatePath:        stagingPath,
		PreRestoreBackupPath: backupPath,
	}
	if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
		t.Fatalf("write restore journal: %v", err)
	}
	return oldPath
}

func (f *realStagingFixture) seedSyntheticCandidate(t *testing.T) string {
	t.Helper()
	path := filepath.Join(f.root, "candidate-source.db")
	seedRestoreRecoveryDatabase(t, path, "candidate")
	return path
}

// flattenCandidate copia o candidato estagiado para um arquivo PLANO no
// diretório do banco com nome no formato reservado — o único layout que a
// validação atual aceita (o mesmo dos testes históricos). Usado apenas nos
// controles PASS.
func (f *realStagingFixture) flattenCandidate(t *testing.T, stagingPath, flatName string) string {
	t.Helper()
	data, err := os.ReadFile(stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	flat := filepath.Join(f.root, flatName)
	if err := os.WriteFile(flat, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return flat
}

// ---------------------------------------------------------------------------
// RED — staging real dos writers com nome de arquivo comum
// ---------------------------------------------------------------------------

// Upload administrativo "backup.db" (nome mais natural do mundo), crash após
// o journal prepared: a recuperação deve fazer rollback do original. Hoje a
// validação rejeita o journal (basename "backup.db" não tem prefixo
// contabase-import-) e o servidor não sobe.
func TestRestoreRecoveryRealAdminStagingPreparedJournalRecovers(t *testing.T) {
	f := newRealStagingFixture(t)
	seedRestoreRecoveryDatabase(t, f.live, "original")
	staging := f.stageRealAdminUpload(t, "backup.db", f.seedSyntheticCandidate(t))
	backup := f.seedBackup(t, "contabase-pre-restore-20261114-120000.db")
	oldPath := f.crashPrepared(t, staging, backup, "contabase.db.restore-current-20261114-120000.db")

	if err := recoverInterruptedRestore(f.live); err != nil {
		t.Fatalf("RED A3: recuperação pós-crash com staging real do upload administrativo falhou (journal produzido pelos próprios writers foi rejeitado): %v", err)
	}
	assertRestoreRecoveryResult(t, f.live, "original")
	assertRestorePathMissing(t, oldPath)
}

// Upload com extensão .DB é aceito pelo handler (strings.ToLower no multipart e
// EqualFold no sanitize), mas o journal valida o sufixo sensível à caixa: crash
// com esse upload legítimo também brica o boot.
func TestRestoreRecoveryRealAdminStagingUppercaseDBRecovers(t *testing.T) {
	f := newRealStagingFixture(t)
	seedRestoreRecoveryDatabase(t, f.live, "original")
	staging := f.stageRealAdminUpload(t, "backup.DB", f.seedSyntheticCandidate(t))
	backup := f.seedBackup(t, "contabase-pre-restore-20261114-120001.db")
	oldPath := f.crashPrepared(t, staging, backup, "contabase.db.restore-current-20261114-120001.db")

	if err := recoverInterruptedRestore(f.live); err != nil {
		t.Fatalf("RED A3: upload .DB é aceito pelo handler, mas o journal o rejeita na recuperação: %v", err)
	}
	assertRestoreRecoveryResult(t, f.live, "original")
	assertRestorePathMissing(t, oldPath)
}

// O writer de bootstrap (.contabase-bootstrap-restore-*) estagia o upload com o
// nome original dentro do diretório: mesmo defeito, mesma consequência.
func TestRestoreRecoveryRealBootstrapStagingPreparedJournalRecovers(t *testing.T) {
	f := newRealStagingFixture(t)
	seedRestoreRecoveryDatabase(t, f.live, "original")
	staging := f.stageRealBootstrapUpload(t, "restauracao.db", f.seedSyntheticCandidate(t))
	backup := f.seedBackup(t, "contabase-pre-restore-20261114-120002.db")
	oldPath := f.crashPrepared(t, staging, backup, "contabase.db.restore-current-20261114-120002.db")

	if err := recoverInterruptedRestore(f.live); err != nil {
		t.Fatalf("RED A3: recuperação com staging real do bootstrap falhou: %v", err)
	}
	assertRestoreRecoveryResult(t, f.live, "original")
	assertRestorePathMissing(t, oldPath)
}

// Crash na fase validated (candidato instalado e reaberto com sucesso, journal
// ainda não removido): a recuperação deve ACEITAR o banco instalado.
func TestRestoreRecoveryRealAdminStagingValidatedJournalAccepts(t *testing.T) {
	f := newRealStagingFixture(t)
	seedRestoreRecoveryDatabase(t, f.live, "original")
	staging := f.stageRealAdminUpload(t, "backup.db", f.seedSyntheticCandidate(t))
	backup := f.seedBackup(t, "contabase-pre-restore-20261114-120003.db")
	oldPath := filepath.Join(f.root, "contabase.db.restore-current-20261114-120003.db")
	if err := os.Rename(f.live, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, f.live); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{
		Version:              restoreJournalVersion,
		Phase:                restoreJournalPhaseValidated,
		DatabasePath:         f.live,
		OriginalPath:         oldPath,
		CandidatePath:        staging,
		PreRestoreBackupPath: backup,
	}
	if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
		t.Fatalf("write restore journal: %v", err)
	}

	if err := recoverInterruptedRestore(f.live); err != nil {
		t.Fatalf("RED A3: journal validated com staging real deve ser aceito na recuperação: %v", err)
	}
	assertRestoreRecoveryResult(t, f.live, "candidate")
	assertRestorePathMissing(t, oldPath)
}

// ---------------------------------------------------------------------------
// PASS — controles (provam que o maquinário funciona; o defeito é do layout)
// ---------------------------------------------------------------------------

// Controle: o único layout aceito hoje (arquivo plano no diretório do banco com
// nome no formato reservado — o mesmo dos testes históricos) recupera
// normalmente em ambos os pontos de crash. Demonstra que o maquinário de
// recuperação funciona e que o defeito é determinístico, dependente do layout
// de staging produzido pelos writers reais.
func TestRestoreRecoveryFlatReservedLayoutStillRecovers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		crash  string
		marker string
	}{
		{name: "prepared", crash: "prepared", marker: "original"},
		{name: "installed", crash: "installed", marker: "original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRealStagingFixture(t)
			seedRestoreRecoveryDatabase(t, f.live, "original")
			staging := f.stageRealAdminUpload(t, "contabase-import-backup.db", f.seedSyntheticCandidate(t))
			flat := f.flattenCandidate(t, staging, "contabase-import-flat-backup.db")
			backup := f.seedBackup(t, "contabase-pre-restore-20261114-130000.db")

			var oldPath string
			if tc.crash == "prepared" {
				oldPath = f.crashPrepared(t, flat, backup, "contabase.db.restore-current-20261114-130000.db")
			} else {
				oldPath = f.crashInstalled(t, flat, backup, "contabase.db.restore-current-20261114-130000.db")
			}

			if err := recoverInterruptedRestore(f.live); err != nil {
				t.Fatalf("controle com layout plano reservado deve recuperar: %v", err)
			}
			assertRestoreRecoveryResult(t, f.live, tc.marker)
			assertRestorePathMissing(t, oldPath)
			working, err := legacyRestoreWorkingFiles(f.live)
			if err != nil {
				t.Fatal(err)
			}
			if len(working) != 0 {
				t.Fatalf("working copies remanescentes: %v", working)
			}
		})
	}
}

// Recuperação repetida (idempotência) no layout suportado hoje.
func TestRestoreRecoveryRealStagingRetryIsRepeatable(t *testing.T) {
	f := newRealStagingFixture(t)
	seedRestoreRecoveryDatabase(t, f.live, "original")
	staging := f.stageRealAdminUpload(t, "contabase-import-backup.db", f.seedSyntheticCandidate(t))
	flat := f.flattenCandidate(t, staging, "contabase-import-flat-backup.db")
	backup := f.seedBackup(t, "contabase-pre-restore-20261114-130001.db")
	oldPath := f.crashPrepared(t, flat, backup, "contabase.db.restore-current-20261114-130001.db")

	for attempt := 1; attempt <= 2; attempt++ {
		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatalf("recuperação #%d falhou: %v", attempt, err)
		}
	}
	assertRestoreRecoveryResult(t, f.live, "original")
	assertRestorePathMissing(t, oldPath)
}

// Restore completo (sem crash) com staging real do writer: o candidato é
// instalado, o journal some, o working copy some, o backup pré-restore fica em
// backups/pre-restore e o cleanup remove o diretório de staging.
func TestRestoreSuccessfulWithRealStagingCleansArtifacts(t *testing.T) {
	f := newRealStagingFixture(t)
	current := createRestoreTestServerRuntime(t, f.live, "original")

	candidateSrc := f.seedSyntheticCandidate(t)
	file, err := os.Open(candidateSrc)
	if err != nil {
		t.Fatal(err)
	}
	stagingPath, cleanup, err := saveAdminBackupImportUpload(f.live, "backup.db", file)
	file.Close()
	if err != nil {
		t.Fatalf("stage real upload: %v", err)
	}
	stagingDir := filepath.Dir(stagingPath)

	result, next, err := restoreSQLiteBackupFromFileWithOps(
		context.Background(),
		current,
		"file:"+f.live,
		f.live,
		stagingPath,
		openRestoreTestRuntimeDBs,
		startRestoreTestServerRuntime,
		validateSQLiteBackupCandidate,
		nil,
	)
	if err != nil {
		t.Fatalf("restore com staging real falhou: %v (result=%+v)", err, result)
	}
	if next != nil {
		defer next.Close()
	}
	if result.RuntimeState != adminRuntimeRestored {
		t.Fatalf("runtime state = %s, esperado restored", result.RuntimeState)
	}
	assertRestoreMarker(t, f.live, "candidate")
	assertRestorePathMissing(t, restoreJournalPath(f.live))
	if working, err := legacyRestoreWorkingFiles(f.live); err != nil || len(working) != 0 {
		t.Fatalf("working copies após restore: %v (err=%v)", working, err)
	}
	backupEntries, err := os.ReadDir(f.backupDir)
	if err != nil {
		t.Fatal(err)
	}
	foundBackup := false
	for _, e := range backupEntries {
		if strings.HasPrefix(e.Name(), "contabase-pre-restore-") && strings.HasSuffix(e.Name(), ".db") {
			foundBackup = true
		}
	}
	if !foundBackup {
		t.Fatalf("backup pré-restore ausente em %s", f.backupDir)
	}
	cleanup()
	assertRestorePathMissing(t, stagingDir)
}

// ---------------------------------------------------------------------------
// PASS — invariante fail-closed preservada (a correção não pode enfraquecer)
// ---------------------------------------------------------------------------

func TestRestoreRecoverySecurityInvariantsHold(t *testing.T) {
	t.Run("candidate path outside database directory is rejected", func(t *testing.T) {
		f := newRealStagingFixture(t)
		seedRestoreRecoveryDatabase(t, f.live, "original")
		staging := f.stageRealAdminUpload(t, "contabase-import-backup.db", f.seedSyntheticCandidate(t))
		backup := f.seedBackup(t, "contabase-pre-restore-20261114-140000.db")
		oldPath := f.crashPrepared(t, staging, backup, "contabase.db.restore-current-20261114-140000.db")

		outside := filepath.Join(f.root, "outside", "contabase-import-evil.db")
		if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
			t.Fatal(err)
		}
		journal := restoreJournal{
			Version:              restoreJournalVersion,
			Phase:                restoreJournalPhasePrepared,
			DatabasePath:         f.live,
			OriginalPath:         oldPath,
			CandidatePath:        outside,
			PreRestoreBackupPath: backup,
		}
		if err := writeRestoreJournal(restoreJournalPath(f.live), journal); err != nil {
			t.Fatal(err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("journal com candidato fora do diretório do banco deve ser rejeitado")
		}
		assertRestorePathPresent(t, oldPath)
	})

	t.Run("journal symlink is rejected", func(t *testing.T) {
		f := newRealStagingFixture(t)
		seedRestoreRecoveryDatabase(t, f.live, "original")
		target := filepath.Join(f.root, "journal-target")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, restoreJournalPath(f.live)); err != nil {
			t.Fatal(err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("journal symlink deve falhar fechado: %v", err)
		}
	})

	t.Run("journal fifo is rejected", func(t *testing.T) {
		f := newRealStagingFixture(t)
		seedRestoreRecoveryDatabase(t, f.live, "original")
		if err := syscall.Mkfifo(restoreJournalPath(f.live), 0o600); err != nil {
			t.Skipf("mkfifo indisponível: %v", err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("journal fifo deve falhar fechado: %v", err)
		}
	})

	t.Run("unusable pre-restore backup fails closed", func(t *testing.T) {
		f := newRealStagingFixture(t)
		seedRestoreRecoveryDatabase(t, f.live, "original")
		staging := f.stageRealAdminUpload(t, "contabase-import-backup.db", f.seedSyntheticCandidate(t))
		flat := f.flattenCandidate(t, staging, "contabase-import-flat-backup.db")
		realBackup := f.seedBackup(t, "contabase-pre-restore-20261114-140001.db")
		backupLink := filepath.Join(f.backupDir, "contabase-pre-restore-20261114-140002.db")
		if err := os.Symlink(realBackup, backupLink); err != nil {
			t.Fatal(err)
		}
		oldPath := f.crashPrepared(t, flat, backupLink, "contabase.db.restore-current-20261114-140002.db")

		err := recoverInterruptedRestore(f.live)
		if err == nil || !strings.Contains(err.Error(), "pre-restore backup is not usable") {
			t.Fatalf("backup symlink deve falhar fechado: %v", err)
		}
		assertRestorePathPresent(t, oldPath)
	})

	t.Run("orphan staging directory does not block startup", func(t *testing.T) {
		f := newRealStagingFixture(t)
		seedRestoreRecoveryDatabase(t, f.live, "original")
		_ = f.stageRealAdminUpload(t, "backup.db", f.seedSyntheticCandidate(t))

		if err := recoverInterruptedRestore(f.live); err != nil {
			t.Fatalf("staging órfão com banco ativo presente não deve bloquear o boot: %v", err)
		}
		assertRestoreMarker(t, f.live, "original")
	})

	t.Run("orphan upload without database refuses empty initialization", func(t *testing.T) {
		f := newRealStagingFixture(t)
		// Banco ativo inexistente + artefato de restore presente → recusar
		// inicialização de banco vazio.
		if err := os.WriteFile(filepath.Join(f.root, "contabase-import-orphan.db"), []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("artefato de upload órfão sem banco ativo deve recusar inicialização vazia")
		} else if !strings.Contains(err.Error(), "restore artifacts exist") {
			t.Fatalf("erro inesperado: %v", err)
		}
		var liveErr error
		if _, liveErr = os.Lstat(f.live); !errors.Is(liveErr, os.ErrNotExist) {
			t.Fatalf("banco não deveria ter sido criado: %v", liveErr)
		}
	})

	t.Run("orphan wal sidecar without database refuses empty initialization", func(t *testing.T) {
		f := newRealStagingFixture(t)
		if err := os.WriteFile(f.live+"-wal", []byte("orphan wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := recoverInterruptedRestore(f.live); err == nil {
			t.Fatal("sidecar -wal órfão deve recusar inicialização vazia")
		}
	})
}
