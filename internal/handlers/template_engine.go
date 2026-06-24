package handlers

import (
	"html/template"
	"io"
)

type TemplateEngine interface {
	ExecuteTemplate(wr io.Writer, name string, data any) error
	Lookup(name string) *template.Template
}
