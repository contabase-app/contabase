package main

import (
	"database/sql"
	"sync/atomic"
)

// sqlDBProvider publica a conexão SQLite atual do runtime para closures de
// rota registradas uma única vez. O coordinator.Replace troca o ponteiro sob
// seu lock exclusivo; o load/store atômico mantém a leitura livre de data race
// mesmo quando uma rota é exercitada fora do middleware do coordinator.
type sqlDBProvider struct {
	db atomic.Pointer[sql.DB]
}

func newSQLDBProvider(db *sql.DB) *sqlDBProvider {
	provider := &sqlDBProvider{}
	provider.Store(db)
	return provider
}

func (p *sqlDBProvider) Store(db *sql.DB) {
	if p == nil {
		return
	}
	p.db.Store(db)
}

func (p *sqlDBProvider) Load() *sql.DB {
	if p == nil {
		return nil
	}
	return p.db.Load()
}
