package handlers

import "html/template"

func withAppShellTestFuncs(funcs template.FuncMap) template.FuncMap {
	if funcs == nil {
		funcs = template.FuncMap{}
	}
	if _, exists := funcs["assetPath"]; !exists {
		funcs["assetPath"] = func(path string) string { return path }
	}
	funcs["SetupTokenStillConfigured"] = func(any) bool { return false }
	funcs["appCurrentTime"] = func() any { return nil }
	funcs["appVersion"] = func() string { return "test" }
	funcs["publicURL"] = func(path string) string { return "https://contabase.test" + path }
	funcs["sub"] = func(a, b int) int { return a - b }
	for name, fn := range IconTemplateFuncs() {
		if _, exists := funcs[name]; !exists {
			funcs[name] = fn
		}
	}
	return funcs
}

func addAppShellTestStubs(t *template.Template) {
	template.Must(t.New("app-v2-document-head-start").Parse("<html><body>"))
	template.Must(t.New("app-v2-document-head-end").Parse(""))
	template.Must(t.New("app-v2-appbar").Parse(""))
	template.Must(t.New("app-v2-setup-token-notice").Parse(""))
	template.Must(t.New("app-v2-shell-after-main").Parse(""))
	template.Must(t.New("app-v2-document-end").Parse("</body></html>"))
}
