package main

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/contabase-app/contabase"
)

type AppTemplateEngine struct {
	mu      sync.RWMutex
	tpl     *template.Template
	debug   bool
	funcMap template.FuncMap
}

func newAppTemplateEngine(debug bool, funcMap template.FuncMap) (*AppTemplateEngine, error) {
	engine := &AppTemplateEngine{
		debug:   debug,
		funcMap: funcMap,
	}
	if err := engine.reload(); err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *AppTemplateEngine) reload() error {
	var fileSystem fs.FS
	if e.debug {
		if stat, err := os.Stat("templates"); err == nil && stat.IsDir() {
			fileSystem = os.DirFS(".")
		} else {
			_, filename, _, _ := runtime.Caller(0)
			projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
			fileSystem = os.DirFS(projectRoot)
		}
	} else {
		fileSystem = contabase.TemplatesFS
	}

	patterns := []string{
		"templates/layout.html",
		"templates/pages/*.html",
		"templates/components/*.html",
	}

	tpl := template.New("").Funcs(e.funcMap)

	for _, p := range patterns {
		matches, err := fs.Glob(fileSystem, p)
		if err != nil {
			return fmt.Errorf("glob %s: %w", p, err)
		}
		for _, match := range matches {
			b, err := fs.ReadFile(fileSystem, match)
			if err != nil {
				return err
			}
			_, err = tpl.New(filepath.Base(match)).Parse(string(b))
			if err != nil {
				return err
			}
		}
	}

	e.mu.Lock()
	e.tpl = tpl
	e.mu.Unlock()
	return nil
}

func (e *AppTemplateEngine) ExecuteTemplate(wr io.Writer, name string, data any) error {
	if e.debug {
		if err := e.reload(); err != nil {
			slog.Error("failed to reload templates", "error", err)
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tpl.ExecuteTemplate(wr, name, data)
}

func (e *AppTemplateEngine) Lookup(name string) *template.Template {
	if e.debug {
		if err := e.reload(); err != nil {
			slog.Error("failed to reload templates", "error", err)
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tpl.Lookup(name)
}
