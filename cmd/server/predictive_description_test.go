package main

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestAppShellLoadsPredictiveDescription(t *testing.T) {
	tpl := template.Must(template.New("predictive-shell").Funcs(buildFuncMap()).ParseFiles("../../templates/components/app_shell.html"))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "app-v2-document-end", nil); err != nil {
		t.Fatal(err)
	}
	const asset = "/assets/js/predictive-description.js"
	if strings.Count(out.String(), `src="`+asset+`"`) != 1 {
		t.Fatal("app shell must load the description input listeners exactly once")
	}
	if _, err := os.Stat("../.." + asset); err != nil {
		t.Fatalf("predictive script referenced by shell must exist: %v", err)
	}
}
