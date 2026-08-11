package civildateplan

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// ========== V2C.7: typed architecture checker (semantic) ==========

type typedSource struct {
	name string
	src  string // empty means read from file; non-empty is synthetic
}

type typedArchitectureMode int

const (
	modeProductionCapabilities typedArchitectureMode = iota
	modeSyntheticCapabilities
	modeGoldenFrozenRoot
	modeForbiddenProbeRoot
	modeSealAndCapability
)

type resolvedCapability struct {
	name     string
	baseName string
	isAlias  bool
	filename string
}

type typedArchitectureReport struct {
	violations           []architectureViolation
	resolvedCapabilities []resolvedCapability
}

type typedSourceLoader func() ([]typedSource, error)

type typedArchitectureAnalysis struct {
	report         typedArchitectureReport
	pkg            *types.Package
	graph          map[*types.Func]*funcRefNode
	analyzedFiles  []string
	capabilityRoot string
}

type productionGateEvidence struct {
	checked        bool
	mode           typedArchitectureMode
	packagePath    string
	analyzedFiles  []string
	capabilityRoot string
}

type productionGateResult struct {
	report   typedArchitectureReport
	evidence productionGateEvidence
}

type goldenGateEvidence struct {
	checked                 bool
	analyzedFiles           []string
	frozenRoot              string
	corruptionRoot          string
	frozenReachesHexDecode  bool
	corruptionReachesFrozen bool
}

type goldenGateResult struct {
	report   typedArchitectureReport
	evidence goldenGateEvidence
}

func (r typedArchitectureReport) has(category, filename, symbol, field string) bool {
	for _, v := range r.violations {
		match := true
		if category != "" && v.category != category {
			match = false
		}
		if filename != "" && v.filename != filename {
			match = false
		}
		if symbol != "" && v.symbol != symbol {
			match = false
		}
		if field != "" && v.field != field {
			match = false
		}
		if match {
			return true
		}
	}
	return false
}

func (r typedArchitectureReport) countViolations(category, filename, symbol, field string) int {
	n := 0
	for _, v := range r.violations {
		match := true
		if category != "" && v.category != category {
			match = false
		}
		if filename != "" && v.filename != filename {
			match = false
		}
		if symbol != "" && v.symbol != symbol {
			match = false
		}
		if field != "" && v.field != field {
			match = false
		}
		if match {
			n++
		}
	}
	return n
}

func (r typedArchitectureReport) hasTypeName(category, filename, symbol, typeName string) bool {
	for _, v := range r.violations {
		if category != "" && v.category != category {
			continue
		}
		if filename != "" && v.filename != filename {
			continue
		}
		if symbol != "" && v.symbol != symbol {
			continue
		}
		if typeName != "" && v.typeName != typeName {
			continue
		}
		return true
	}
	return false
}

// ---------- package loading ----------

func loadProductionFiles() ([]typedSource, error) {
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		return nil, fmt.Errorf("import dir: %w", err)
	}
	var files []typedSource
	for _, name := range pkg.GoFiles {
		files = append(files, typedSource{name: name})
	}
	return files, nil
}

func currentProductionAnalyzedFiles() []string {
	base := []string{
		"authorized.go", "digest_v1.go", "doc.go", "json_text.go", "load_authorized.go",
		"load_errors.go", "load_file.go", "load_prepare.go", "optional.go", "parser.go", "schema_v1.go", "snapshot.go",
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		base = append(base, "load_file_unix.go")
	} else {
		base = append(base, "load_file_unsupported.go")
	}
	sort.Strings(base)
	return base
}

func currentTestAnalyzedFiles() []string {
	base := []string{
		"authorized.go", "authorized_test.go", "digest_v1.go", "digest_v1_test.go",
		"doc.go", "json_text.go", "load_authorized.go", "load_authorized_test.go",
		"load_errors.go", "load_file.go", "load_file_test.go",
		"load_prepare.go", "load_prepare_test.go", "optional.go", "parser.go",
		"parser_fuzz_test.go", "parser_test.go", "schema_v1.go", "snapshot.go",
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		base = append(base, "load_file_unix.go", "load_file_unix_test.go")
	} else {
		base = append(base, "load_file_unsupported.go")
	}
	sort.Strings(base)
	return base
}

func loadTestFiles() ([]typedSource, error) {
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		return nil, fmt.Errorf("import dir: %w", err)
	}
	var files []typedSource
	for _, name := range pkg.GoFiles {
		files = append(files, typedSource{name: name})
	}
	for _, name := range pkg.TestGoFiles {
		if name == "architecture_test.go" {
			continue
		}
		files = append(files, typedSource{name: name})
	}
	return files, nil
}

// ---------- module-aware importer ----------

type localModuleImporter struct {
	fset  *token.FileSet
	cache map[string]*types.Package
}

func (mi *localModuleImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := mi.cache[path]; ok {
		return pkg, nil
	}
	stdImp := importer.ForCompiler(mi.fset, "gc", nil)
	pkg, err := stdImp.Import(path)
	if err == nil {
		mi.cache[path] = pkg
		return pkg, nil
	}
	// Resolve local module packages relative to working directory
	dir, err := resolveModuleDir(path)
	if err != nil {
		return nil, fmt.Errorf("cannot find %s: %w", path, err)
	}
	pkgs, parseErr := parser.ParseDir(mi.fset, dir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go")
	}, 0)
	if parseErr != nil {
		return nil, fmt.Errorf("parse %s: %w", path, parseErr)
	}
	var astFiles []*ast.File
	var pkgName string
	for name, parsed := range pkgs {
		pkgName = name
		for _, f := range parsed.Files {
			astFiles = append(astFiles, f)
		}
	}
	if len(astFiles) == 0 {
		return nil, fmt.Errorf("no go files in %s", dir)
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	conf := types.Config{Importer: mi}
	imported, checkErr := conf.Check(pkgName, mi.fset, astFiles, info)
	if checkErr != nil {
		return nil, fmt.Errorf("check %s: %w", path, checkErr)
	}
	mi.cache[path] = imported
	return imported, nil
}

func resolveModuleDir(importPath string) (string, error) {
	// Walk up from current directory to find go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	var modData []byte
	var modRoot string
	for {
		modFile := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modFile); err == nil {
			modData = data
			modRoot = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
	modLine := strings.SplitN(string(modData), "\n", 2)[0]
	modPath := strings.TrimPrefix(modLine, "module ")
	modPath = strings.TrimSpace(modPath)

	if !strings.HasPrefix(importPath, modPath+"/") {
		return "", fmt.Errorf("import %s is not under module %s", importPath, modPath)
	}
	relDir := strings.TrimPrefix(importPath, modPath+"/")
	return filepath.Join(modRoot, relDir), nil
}

// ---------- core type-checking ----------

func typeCheckSources(sources []typedSource) (*token.FileSet, []*ast.File, *types.Package, *types.Info, error) {
	fset := token.NewFileSet()
	imp := &localModuleImporter{fset: fset, cache: make(map[string]*types.Package)}
	conf := types.Config{
		Importer: imp,
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	var astFiles []*ast.File
	for _, s := range sources {
		var file *ast.File
		var err error
		if s.src != "" {
			file, err = parser.ParseFile(fset, s.name, s.src, 0)
		} else {
			data, readErr := os.ReadFile(s.name)
			if readErr != nil {
				return nil, nil, nil, nil, fmt.Errorf("read %s: %w", s.name, readErr)
			}
			file, err = parser.ParseFile(fset, s.name, data, 0)
		}
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("parse %s: %w", s.name, err)
		}
		astFiles = append(astFiles, file)
	}
	var collectErr error
	conf.Error = func(err error) {
		if collectErr == nil {
			collectErr = err
		}
	}
	pkg, err := conf.Check("civildateplan", fset, astFiles, info)
	if collectErr != nil || err != nil {
		return nil, nil, nil, nil, fmt.Errorf("type-check: %w; first error: %w", err, collectErr)
	}
	if pkg == nil {
		return nil, nil, nil, nil, fmt.Errorf("type-check: package is nil")
	}
	return fset, astFiles, pkg, info, nil
}

// ---------- capability set by type identity ----------

func buildCapabilitySet(astFiles []*ast.File, info *types.Info, pkg *types.Package) map[*types.TypeName]bool {
	capSet := make(map[*types.TypeName]bool)
	if pkg == nil {
		return capSet
	}
	arpObj := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if arpObj == nil {
		return capSet
	}
	arpTN, ok := arpObj.(*types.TypeName)
	if !ok {
		return capSet
	}
	capSet[arpTN] = true

	type typeDecl struct {
		name *types.TypeName
		rhs  types.Type
	}
	var declarations []typeDecl
	for _, file := range astFiles {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				tn, ok := info.Defs[ts.Name].(*types.TypeName)
				if ok && tn.Pkg() == pkg {
					declarations = append(declarations, typeDecl{name: tn, rhs: info.TypeOf(ts.Type)})
				}
			}
		}
	}

	changed := true
	for changed {
		changed = false
		for _, decl := range declarations {
			tn := decl.name
			if capSet[tn] || tn.Name() == "AuthorizedRepairPlan" {
				continue
			}
			t := decl.rhs
			u := types.Unalias(t)
			for {
				p, ok := u.(*types.Pointer)
				if !ok {
					break
				}
				u = p.Elem()
				u = types.Unalias(u)
			}
			if n, ok := u.(*types.Named); ok && capSet[n.Obj()] {
				capSet[tn] = true
				changed = true
			}
		}
	}
	return capSet
}

func isCapabilityType(t types.Type, capSet map[*types.TypeName]bool) bool {
	if t == nil {
		return false
	}
	u := types.Unalias(t)
	for {
		p, ok := u.(*types.Pointer)
		if !ok {
			break
		}
		u = p.Elem()
		u = types.Unalias(u)
	}
	n, ok := u.(*types.Named)
	if !ok {
		return false
	}
	return capSet[n.Obj()]
}

// ---------- forbidden objects ----------

func buildForbiddenSet(info *types.Info) map[types.Object]bool {
	objs := make(map[types.Object]bool)
	for _, obj := range info.Uses {
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		if obj.Parent() != obj.Pkg().Scope() {
			continue // skip non-package-level objects (locals, closures)
		}
		pkgPath := obj.Pkg().Path()
		if strings.HasPrefix(obj.Name(), "encode") || obj.Name() == "computeAuthorizedPlanDigestV1" || obj.Name() == "fullGoldenState" || obj.Name() == "simpleState" {
			objs[obj] = true
		}
		if pkgPath == "encoding/json" && (obj.Name() == "Marshal" || obj.Name() == "Unmarshal") {
			objs[obj] = true
		}
	}
	return objs
}

// ---------- architecture violation ----------

type architectureViolation struct {
	category string
	filename string
	symbol   string
	typeName string
	field    string
}

func findViolations(vs []architectureViolation, category, filename, symbol, field string) []architectureViolation {
	var result []architectureViolation
	for _, v := range vs {
		match := true
		if category != "" && v.category != category {
			match = false
		}
		if filename != "" && v.filename != filename {
			match = false
		}
		if symbol != "" && v.symbol != symbol {
			match = false
		}
		if field != "" && v.field != field {
			match = false
		}
		if match {
			result = append(result, v)
		}
	}
	return result
}

// ---------- type name resolution ----------

func resolveTypeDeclName(t types.Type) string {
	u := types.Unalias(t)
	for {
		p, ok := u.(*types.Pointer)
		if !ok {
			break
		}
		u = p.Elem()
		u = types.Unalias(u)
	}
	n, ok := u.(*types.Named)
	if !ok {
		return types.TypeString(t, nil)
	}
	return n.Obj().Name()
}

// ---------- AST helper ----------

func unparenExpr(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func replaceExactlyOnce(source, old, replacement string) (string, error) {
	if old == replacement {
		return source, fmt.Errorf("replaceExactlyOnce: old and replacement are identical")
	}
	count := strings.Count(source, old)
	if count == 0 {
		return source, fmt.Errorf("replaceExactlyOnce: target not found")
	}
	if count > 1 {
		return source, fmt.Errorf("replaceExactlyOnce: target found %d times, expected exactly 1", count)
	}
	result := strings.Replace(source, old, replacement, 1)
	if result == source {
		return source, fmt.Errorf("replaceExactlyOnce: replacement produced no change")
	}
	return result, nil
}

func mustReplaceExactlyOnce(t *testing.T, source, old, replacement, step string) string {
	t.Helper()
	result, err := replaceExactlyOnce(source, old, replacement)
	if err != nil {
		t.Fatalf("step %q: %v", step, err)
	}
	return result
}

func isAuthorizedPlanSealObject(expr ast.Expr, info *types.Info, seal types.Object) bool {
	id, ok := unparenExpr(expr).(*ast.Ident)
	return ok && seal != nil && info.Uses[id] == seal
}

func isPredeclaredNil(info *types.Info, id *ast.Ident) bool {
	if id.Name != "nil" {
		return false
	}
	nilObj := types.Universe.Lookup("nil")
	if nilObj != nil && info.Uses[id] == nilObj {
		return true
	}
	return info.Defs[id] == nil && info.Uses[id] == nil
}

// ---------- function use graph (forbidden deps) ----------

type funcRefNode struct {
	fn       *types.Func
	filename string
	refs     []types.Object // package-level funcs and forbidden objects referenced in body
}

func buildFuncRefGraph(astFiles []*ast.File, info *types.Info, pkg *types.Package, forbiddenSet map[types.Object]bool, sourceASTs map[string]*ast.File) map[*types.Func]*funcRefNode {
	graph := make(map[*types.Func]*funcRefNode)
	for _, af := range astFiles {
		filename := ""
		for name, f := range sourceASTs {
			if f == af {
				filename = name
				break
			}
		}
		for _, decl := range af.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			obj := info.Defs[fd.Name]
			fn, ok := obj.(*types.Func)
			if !ok || fn.Pkg() != pkg {
				continue
			}
			node := &funcRefNode{fn: fn, filename: filename}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if selectionExpr, ok := n.(*ast.SelectorExpr); ok {
					if selection := info.Selections[selectionExpr]; selection != nil {
						if selectedFn, ok := selection.Obj().(*types.Func); ok {
							node.refs = append(node.refs, selectedFn)
						}
					}
				}
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				use := info.Uses[id]
				if use == nil {
					return true
				}
				usedFn, isFunc := use.(*types.Func)
				if forbiddenSet[use] || (isFunc && usedFn.Pkg() != nil && usedFn.Parent() == usedFn.Pkg().Scope()) {
					node.refs = append(node.refs, use)
				}
				return true
			})
			graph[fn] = node
		}
	}
	return graph
}

func collectForbiddenDependencies(root *types.Func, graph map[*types.Func]*funcRefNode, forbiddenSet map[types.Object]bool) map[types.Object]string {
	result := make(map[types.Object]string)
	visited := make(map[*types.Func]bool)
	var dfs func(fn *types.Func, depth int)
	dfs = func(fn *types.Func, depth int) {
		if visited[fn] {
			return
		}
		visited[fn] = true
		node := graph[fn]
		if node == nil {
			return
		}
		for _, obj := range node.refs {
			if forbiddenSet[obj] {
				field := "direct"
				if depth > 0 {
					field = "transitive"
				}
				if _, ok := result[obj]; !ok || field == "direct" {
					result[obj] = field
				}
				continue
			}
			if fn2, ok := obj.(*types.Func); ok {
				if node2 := graph[fn2]; node2 != nil {
					dfs(fn2, depth+1)
				}
			}
		}
	}
	dfs(root, 0)
	return result
}

func packageFunctionByName(graph map[*types.Func]*funcRefNode, pkg *types.Package, name string) (*types.Func, error) {
	var found *types.Func
	for fn := range graph {
		if fn.Pkg() != pkg || fn.Parent() != pkg.Scope() || fn.Name() != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("ambiguous package function %s", name)
		}
		found = fn
	}
	if found == nil {
		return nil, fmt.Errorf("package function %s not found", name)
	}
	return found, nil
}

func reachesFunction(graph map[*types.Func]*funcRefNode, root, target *types.Func) bool {
	visited := make(map[*types.Func]bool)
	var visit func(*types.Func) bool
	visit = func(fn *types.Func) bool {
		if fn == target {
			return true
		}
		if visited[fn] {
			return false
		}
		visited[fn] = true
		for _, ref := range graph[fn].refs {
			if next, ok := ref.(*types.Func); ok && graph[next] != nil && visit(next) {
				return true
			}
		}
		return false
	}
	return root != nil && target != nil && visit(root)
}

func reachesObject(graph map[*types.Func]*funcRefNode, root *types.Func, target func(types.Object) bool) bool {
	visited := make(map[*types.Func]bool)
	var visit func(*types.Func) bool
	visit = func(fn *types.Func) bool {
		if visited[fn] {
			return false
		}
		visited[fn] = true
		node := graph[fn]
		if node == nil {
			return false
		}
		for _, ref := range node.refs {
			if target(ref) {
				return true
			}
			if next, ok := ref.(*types.Func); ok && graph[next] != nil && visit(next) {
				return true
			}
		}
		return false
	}
	return root != nil && visit(root)
}

func forbiddenObjTypeName(obj types.Object) string {
	if obj.Pkg() != nil && obj.Pkg().Path() == "encoding/json" {
		return obj.Pkg().Path() + "." + obj.Name()
	}
	return obj.Name()
}

func normalizeArchitectureReport(report typedArchitectureReport) typedArchitectureReport {
	sort.Slice(report.resolvedCapabilities, func(i, j int) bool {
		a, b := report.resolvedCapabilities[i], report.resolvedCapabilities[j]
		return fmt.Sprintf("%s\x00%s\x00%t\x00%s", a.name, a.baseName, a.isAlias, a.filename) <
			fmt.Sprintf("%s\x00%s\x00%t\x00%s", b.name, b.baseName, b.isAlias, b.filename)
	})
	sort.Slice(report.violations, func(i, j int) bool {
		a, b := report.violations[i], report.violations[j]
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", a.category, a.filename, a.symbol, a.typeName, a.field) <
			fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", b.category, b.filename, b.symbol, b.typeName, b.field)
	})
	return report
}

func assertArchitectureReportEqual(t *testing.T, got, want typedArchitectureReport) {
	t.Helper()
	got = normalizeArchitectureReport(got)
	want = normalizeArchitectureReport(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("architecture report mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func declaredTypeSpelling(expr ast.Expr) string {
	switch e := unparenExpr(expr).(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return declaredTypeSpelling(e.X)
	default:
		return ""
	}
}

func declaredObjectTypes(astFiles []*ast.File, info *types.Info) map[types.Object]string {
	result := make(map[types.Object]string)
	for _, file := range astFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			name := declaredTypeSpelling(field.Type)
			if name == "" {
				return true
			}
			for _, id := range field.Names {
				if obj := info.Defs[id]; obj != nil {
					result[obj] = name
				}
			}
			return true
		})
	}
	return result
}

// ---------- main typed architecture checker ----------

func analyzeTypedArchitecture(sources []typedSource, mode typedArchitectureMode) (typedArchitectureAnalysis, error) {
	if len(sources) == 0 {
		return typedArchitectureAnalysis{}, fmt.Errorf("no sources provided")
	}
	fset, astFiles, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return typedArchitectureAnalysis{}, err
	}
	capSet := buildCapabilitySet(astFiles, info, pkg)
	forbiddenSet := buildForbiddenSet(info)
	sealObj := pkg.Scope().Lookup("authorizedPlanSeal")
	capabilityRoot := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if capabilityRoot == nil || !isCapabilityType(capabilityRoot.Type(), capSet) {
		return typedArchitectureAnalysis{}, fmt.Errorf("AuthorizedRepairPlan capability root not resolved")
	}
	objectTypeNames := declaredObjectTypes(astFiles, info)
	analyzedFiles := make([]string, 0, len(sources))
	for _, source := range sources {
		analyzedFiles = append(analyzedFiles, source.name)
	}
	sort.Strings(analyzedFiles)

	// Build AST map by source name
	sourceASTs := make(map[string]*ast.File)
	for i, s := range sources {
		if i < len(astFiles) {
			sourceASTs[s.name] = astFiles[i]
		}
	}

	var resolvedCaps []resolvedCapability
	capabilityNames := make(map[*types.TypeName]bool)
	for _, af := range astFiles {
		for _, decl := range af.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				tn, ok := info.Defs[ts.Name].(*types.TypeName)
				if !ok || tn.Name() == "AuthorizedRepairPlan" || !capSet[tn] {
					continue
				}
				filename := filepath.Base(fset.Position(ts.Pos()).Filename)
				resolvedCaps = append(resolvedCaps, resolvedCapability{
					name: tn.Name(), baseName: resolveTypeDeclName(info.TypeOf(ts.Type)),
					isAlias: ts.Assign.IsValid(), filename: filename,
				})
				capabilityNames[tn] = true
			}
		}
	}

	var violations []architectureViolation
	add := func(cat, fn, sym, tn, f string) {
		violations = append(violations, architectureViolation{category: cat, filename: fn, symbol: sym, typeName: tn, field: f})
	}
	compositeSymbols := make(map[*ast.CompositeLit]string)
	for _, af := range astFiles {
		ast.Inspect(af, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.ValueSpec:
				for i, value := range node.Values {
					symbol := ""
					if i < len(node.Names) {
						symbol = node.Names[i].Name
					} else if len(node.Names) == 1 {
						symbol = node.Names[0].Name
					}
					ast.Inspect(value, func(child ast.Node) bool {
						if lit, ok := child.(*ast.CompositeLit); ok {
							compositeSymbols[lit] = symbol
						}
						return true
					})
				}
			case *ast.FuncDecl:
				if node.Body != nil {
					ast.Inspect(node.Body, func(child ast.Node) bool {
						if lit, ok := child.(*ast.CompositeLit); ok && compositeSymbols[lit] == "" {
							compositeSymbols[lit] = node.Name.Name
						}
						return true
					})
				}
			}
			return true
		})
	}

	for _, s := range sources {
		fn := s.name
		af := sourceASTs[fn]
		if af == nil {
			continue
		}

		ast.Inspect(af, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.GenDecl:
				if node.Tok != token.TYPE {
					return true
				}
				for _, spec := range node.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					tn, _ := info.Defs[ts.Name].(*types.TypeName)
					if !ok || ts.Name.Name == "AuthorizedRepairPlan" || !capabilityNames[tn] {
						continue
					}
					if ts.Assign > 0 {
						add("prohibited_alias", fn, ts.Name.Name, ts.Name.Name, "")
					} else {
						add("prohibited_defined_type", fn, ts.Name.Name, ts.Name.Name, "")
					}
				}

			case *ast.FuncDecl:
				sym := node.Name.Name

				if node.Body != nil {
					ast.Inspect(node.Body, func(bodyN ast.Node) bool {
						assign, ok := bodyN.(*ast.AssignStmt)
						if !ok {
							return true
						}
						for i, lhs := range assign.Lhs {
							sel, ok := lhs.(*ast.SelectorExpr)
							if !ok {
								continue
							}
							selType := info.TypeOf(sel.X)
							if !isCapabilityType(selType, capSet) {
								continue
							}
							typeName := resolveTypeDeclName(selType)
							if id, ok := unparenExpr(sel.X).(*ast.Ident); ok {
								if declared := objectTypeNames[info.Uses[id]]; declared != "" {
									typeName = declared
								}
							}
							if sel.Sel.Name == "seal" && i < len(assign.Rhs) {
								if isAuthorizedPlanSealObject(assign.Rhs[i], info, sealObj) {
									add("seal_assignment", fn, sym, typeName, "seal")
								}
							}
							if sel.Sel.Name == "digest" {
								add("digest_assignment", fn, sym, typeName, "digest")
							}
						}
						return true
					})
				}

				if node.Type.Results != nil && (ast.IsExported(sym) || node.Recv == nil) {
					for _, res := range node.Type.Results.List {
						resType := info.TypeOf(res.Type)
						if resType != nil && isCapabilityType(resType, capSet) {
							typeName := declaredTypeSpelling(res.Type)
							if typeName == "" {
								typeName = resolveTypeDeclName(resType)
							}
							add("capability_return", fn, sym, typeName, "")
						}
					}
				}

			case *ast.CompositeLit:
				clType := info.TypeOf(node.Type)
				if clType == nil || !isCapabilityType(clType, capSet) {
					return true
				}
				tn := declaredTypeSpelling(node.Type)
				if tn == "" {
					tn = resolveTypeDeclName(clType)
				}
				symbol := compositeSymbols[node]
				for _, elt := range node.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if key.Name == "seal" && isAuthorizedPlanSealObject(kv.Value, info, sealObj) {
						add("keyed_composite", fn, symbol, tn, "seal")
					}
					if key.Name == "digest" {
						add("keyed_composite", fn, symbol, tn, "digest")
					}
				}
				if len(node.Elts) > 0 && isAuthorizedPlanSealObject(node.Elts[0], info, sealObj) {
					add("positional_composite", fn, symbol, tn, "seal")
				}
				if len(node.Elts) > 2 {
					add("positional_composite", fn, symbol, tn, "digest")
				}
			}
			return true
		})
	}

	// Forbidden dependency checking (mode-specific)
	var graph map[*types.Func]*funcRefNode
	if mode == modeGoldenFrozenRoot || mode == modeForbiddenProbeRoot || mode == modeSyntheticCapabilities {
		graph = buildFuncRefGraph(astFiles, info, pkg, forbiddenSet, sourceASTs)
		syntheticFilenames := make(map[string]bool)
		for _, s := range sources {
			if s.src != "" {
				syntheticFilenames[s.name] = true
			}
		}
		var goldenRoots []*types.Func
		if mode == modeGoldenFrozenRoot {
			frozenRoot, err1 := packageFunctionByName(graph, pkg, "frozenCompleteGoldenBytes")
			corruptionRoot, err2 := packageFunctionByName(graph, pkg, "TestIndependentDecoderRejectsCorruption")
			if err1 != nil || err2 != nil {
				return typedArchitectureAnalysis{}, fmt.Errorf("golden root resolution failed: %v; %v", err1, err2)
			}
			goldenRoots = []*types.Func{frozenRoot, corruptionRoot}
		}
		for _, node := range graph {
			isRoot := false
			switch mode {
			case modeGoldenFrozenRoot:
				for _, root := range goldenRoots {
					if node.fn == root {
						isRoot = true
						break
					}
				}
			case modeForbiddenProbeRoot, modeSyntheticCapabilities:
				// Only synthetic files are probe roots; production helpers are followed transitively.
				if syntheticFilenames[node.filename] {
					isRoot = true
				}
			}
			if !isRoot {
				continue
			}
			found := collectForbiddenDependencies(node.fn, graph, forbiddenSet)
			for obj, field := range found {
				add("forbidden_dependency", node.filename, node.fn.Name(), forbiddenObjTypeName(obj), field)
			}
		}
	}

	return typedArchitectureAnalysis{
		report:         normalizeArchitectureReport(typedArchitectureReport{violations: violations, resolvedCapabilities: resolvedCaps}),
		pkg:            pkg,
		graph:          graph,
		analyzedFiles:  analyzedFiles,
		capabilityRoot: capabilityRoot.Name(),
	}, nil
}

func checkTypedArchitecture(sources []typedSource, mode typedArchitectureMode) (typedArchitectureReport, error) {
	analysis, err := analyzeTypedArchitecture(sources, mode)
	if err != nil {
		return typedArchitectureReport{}, err
	}
	return analysis.report, nil
}

// ---------- convenience checkers ----------

func checkSingleSynthetic(src string, mode typedArchitectureMode) (typedArchitectureReport, error) {
	prodFiles, err := loadProductionFiles()
	if err != nil {
		return typedArchitectureReport{}, err
	}
	allFiles := make([]typedSource, len(prodFiles))
	copy(allFiles, prodFiles)
	allFiles = append(allFiles, typedSource{name: "synthetic.go", src: src})
	return checkTypedArchitecture(allFiles, mode)
}

func checkSyntheticMulti(sourceMap map[string]string, mode typedArchitectureMode) (typedArchitectureReport, error) {
	prodFiles, err := loadProductionFiles()
	if err != nil {
		return typedArchitectureReport{}, err
	}
	allFiles := make([]typedSource, len(prodFiles))
	copy(allFiles, prodFiles)
	for name, src := range sourceMap {
		allFiles = append(allFiles, typedSource{name: name, src: src})
	}
	return checkTypedArchitecture(allFiles, mode)
}

func checkSyntheticMultiOrdered(orderedSources []typedSource, mode typedArchitectureMode) (typedArchitectureReport, error) {
	prodFiles, err := loadProductionFiles()
	if err != nil {
		return typedArchitectureReport{}, err
	}
	allFiles := make([]typedSource, 0, len(prodFiles)+len(orderedSources))
	allFiles = append(allFiles, prodFiles...)
	allFiles = append(allFiles, orderedSources...)
	return checkTypedArchitecture(allFiles, mode)
}

func checkProduction(load typedSourceLoader) (productionGateResult, error) {
	if load == nil {
		return productionGateResult{}, fmt.Errorf("production source loader is nil")
	}
	files, err := load()
	if err != nil {
		return productionGateResult{}, fmt.Errorf("load production sources: %w", err)
	}
	analysis, err := analyzeTypedArchitecture(files, modeProductionCapabilities)
	if err != nil {
		return productionGateResult{}, err
	}
	return productionGateResult{
		report: analysis.report,
		evidence: productionGateEvidence{
			checked:        true,
			mode:           modeProductionCapabilities,
			packagePath:    analysis.pkg.Path(),
			analyzedFiles:  append([]string(nil), analysis.analyzedFiles...),
			capabilityRoot: analysis.capabilityRoot,
		},
	}, nil
}

func checkGoldenFrozen(load typedSourceLoader) (goldenGateResult, error) {
	if load == nil {
		return goldenGateResult{}, fmt.Errorf("golden source loader is nil")
	}
	files, err := load()
	if err != nil {
		return goldenGateResult{}, fmt.Errorf("load golden sources: %w", err)
	}
	analysis, err := analyzeTypedArchitecture(files, modeGoldenFrozenRoot)
	if err != nil {
		return goldenGateResult{}, err
	}
	frozen, err := packageFunctionByName(analysis.graph, analysis.pkg, "frozenCompleteGoldenBytes")
	if err != nil {
		return goldenGateResult{}, err
	}
	corruption, err := packageFunctionByName(analysis.graph, analysis.pkg, "TestIndependentDecoderRejectsCorruption")
	if err != nil {
		return goldenGateResult{}, err
	}
	frozenReachesHex := reachesObject(analysis.graph, frozen, func(obj types.Object) bool {
		return obj.Pkg() != nil && obj.Pkg().Path() == "encoding/hex" && obj.Name() == "DecodeString"
	})
	if !frozenReachesHex {
		return goldenGateResult{}, fmt.Errorf("frozenCompleteGoldenBytes does not reach encoding/hex.DecodeString")
	}
	corruptionReachesFrozen := reachesFunction(analysis.graph, corruption, frozen)
	if !corruptionReachesFrozen {
		return goldenGateResult{}, fmt.Errorf("TestIndependentDecoderRejectsCorruption does not reach frozenCompleteGoldenBytes")
	}
	return goldenGateResult{
		report: analysis.report,
		evidence: goldenGateEvidence{
			checked:                 true,
			analyzedFiles:           append([]string(nil), analysis.analyzedFiles...),
			frozenRoot:              frozen.Name(),
			corruptionRoot:          corruption.Name(),
			frozenReachesHexDecode:  true,
			corruptionReachesFrozen: true,
		},
	}, nil
}

func assertProductionGateResultEqual(t *testing.T, got, want productionGateResult) {
	t.Helper()
	assertArchitectureReportEqual(t, got.report, want.report)
	if !reflect.DeepEqual(got.evidence, want.evidence) {
		t.Fatalf("production evidence mismatch\n got: %+v\nwant: %+v", got.evidence, want.evidence)
	}
}

func assertGoldenGateResultEqual(t *testing.T, got, want goldenGateResult) {
	t.Helper()
	assertArchitectureReportEqual(t, got.report, want.report)
	if !reflect.DeepEqual(got.evidence, want.evidence) {
		t.Fatalf("golden evidence mismatch\n got: %+v\nwant: %+v", got.evidence, want.evidence)
	}
}

func baseAuthorizedLoaderViolations() []architectureViolation {
	return []architectureViolation{
		{category: "capability_return", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan"},
		{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
		{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "seal"},
		{category: "positional_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
	}
}

func expectedRealGoldenReport() typedArchitectureReport {
	return typedArchitectureReport{violations: append(baseAuthorizedLoaderViolations(),
		architectureViolation{category: "capability_return", filename: "authorized_test.go", symbol: "testAuthorizedPlan", typeName: "AuthorizedRepairPlan"},
		architectureViolation{category: "digest_assignment", filename: "authorized_test.go", symbol: "TestAuthorizedCopyPlanDigestPreserved", typeName: "AuthorizedRepairPlan", field: "digest"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedDigestNilState", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedEmptyFindings", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedInvalidVariant", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedInvariantDueDateCivilDivergent", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedInvariantDueDateCivilNull", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedStateEmpty", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "TestAuthorizedStateNil", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "keyed_composite", filename: "authorized_test.go", symbol: "testAuthorizedPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
		architectureViolation{category: "keyed_composite", filename: "digest_v1_test.go", symbol: "TestSixPermutationsFindingAt", typeName: "AuthorizedRepairPlan", field: "digest"},
		architectureViolation{category: "seal_assignment", filename: "authorized_test.go", symbol: "testAuthorizedPlan", typeName: "AuthorizedRepairPlan", field: "seal"},
		architectureViolation{category: "seal_assignment", filename: "digest_v1_test.go", symbol: "TestSixPermutationsFindingAt", typeName: "AuthorizedRepairPlan", field: "seal"},
	)}
}

func controlledProductionLoader(extra ...typedSource) typedSourceLoader {
	return func() ([]typedSource, error) {
		files, err := loadProductionFiles()
		if err != nil {
			return nil, err
		}
		return append(files, extra...), nil
	}
}

func loadTestFilesWithTransform(transforms map[string]func(string) (string, error)) typedSourceLoader {
	return func() ([]typedSource, error) {
		files, err := loadTestFiles()
		if err != nil {
			return nil, err
		}
		applied := make(map[string]bool, len(transforms))
		for i := range files {
			transform := transforms[files[i].name]
			if transform == nil {
				continue
			}
			var content string
			if files[i].src != "" {
				content = files[i].src
			} else {
				data, readErr := os.ReadFile(files[i].name)
				if readErr != nil {
					return nil, readErr
				}
				content = string(data)
			}
			changed, transformErr := transform(content)
			if transformErr != nil {
				return nil, transformErr
			}
			if changed == content {
				return nil, fmt.Errorf("transform %s made no change", files[i].name)
			}
			files[i].src = changed
			applied[files[i].name] = true
		}
		for name := range transforms {
			if !applied[name] {
				return nil, fmt.Errorf("transform target %s not found", name)
			}
		}
		return files, nil
	}
}

func semanticGraph(sources []typedSource) (map[*types.Func]*funcRefNode, *types.Package, error) {
	_, files, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return nil, nil, err
	}
	sourceASTs := make(map[string]*ast.File, len(sources))
	for i, source := range sources {
		sourceASTs[source.name] = files[i]
	}
	return buildFuncRefGraph(files, info, pkg, buildForbiddenSet(info), sourceASTs), pkg, nil
}

func capabilityBoundaryViolations(sources []typedSource) ([]architectureViolation, error) {
	fset, files, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return nil, err
	}
	capSet := buildCapabilitySet(files, info, pkg)
	var violations []architectureViolation
	addFields := func(
		filename string,
		symbol string,
		category string,
		fields *ast.FieldList,
	) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			fieldType := info.TypeOf(field.Type)
			if !isCapabilityType(fieldType, capSet) {
				continue
			}
			violations = append(violations, architectureViolation{
				category: category,
				filename: filename,
				symbol:   symbol,
				typeName: resolveTypeDeclName(fieldType),
			})
		}
	}
	for index, file := range files {
		filename := sources[index].name
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				addFields(filename, typed.Name.Name, "capability_parameter", typed.Type.Params)
				addFields(filename, typed.Name.Name, "capability_return", typed.Type.Results)
			case *ast.FuncLit:
				symbol := fmt.Sprintf("func literal at %s", fset.Position(typed.Pos()))
				addFields(filename, symbol, "capability_parameter", typed.Type.Params)
				addFields(filename, symbol, "capability_return", typed.Type.Results)
			case *ast.ValueSpec:
				if typed.Type != nil && isCapabilityType(info.TypeOf(typed.Type), capSet) {
					for _, name := range typed.Names {
						violations = append(violations, architectureViolation{
							category: "capability_global",
							filename: filename,
							symbol:   name.Name,
							typeName: resolveTypeDeclName(info.TypeOf(typed.Type)),
						})
					}
				}
				for valueIndex, value := range typed.Values {
					if !isCapabilityType(info.TypeOf(value), capSet) {
						continue
					}
					symbol := ""
					if valueIndex < len(typed.Names) {
						symbol = typed.Names[valueIndex].Name
					}
					violations = append(violations, architectureViolation{
						category: "capability_global",
						filename: filename,
						symbol:   symbol,
						typeName: resolveTypeDeclName(info.TypeOf(value)),
					})
				}
			}
			return true
		})
	}
	return normalizeArchitectureReport(typedArchitectureReport{
		violations: violations,
	}).violations, nil
}

func packageFunctionReferencesObject(
	sources []typedSource,
	functionName string,
	objectName string,
) (bool, error) {
	_, files, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return false, err
	}
	functionObject := pkg.Scope().Lookup(functionName)
	targetObject := pkg.Scope().Lookup(objectName)
	if functionObject == nil || targetObject == nil {
		return false, fmt.Errorf("function or target object not found")
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || info.Defs[function.Name] != functionObject || function.Body == nil {
				continue
			}
			found := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if ok && info.Uses[identifier] == targetObject {
					found = true
					return false
				}
				return !found
			})
			return found, nil
		}
	}
	return false, fmt.Errorf("package function %s body not found", functionName)
}

func packageFunctionDirectCallCount(
	sources []typedSource,
	functionName string,
	targetName string,
) (int, error) {
	_, files, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return 0, err
	}
	functionObject := pkg.Scope().Lookup(functionName)
	targetObject := pkg.Scope().Lookup(targetName)
	if functionObject == nil || targetObject == nil {
		return 0, fmt.Errorf("function or target object not found")
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || info.Defs[function.Name] != functionObject || function.Body == nil {
				continue
			}
			count := 0
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := unparenExpr(call.Fun).(*ast.Ident)
				if ok && info.Uses[identifier] == targetObject {
					count++
				}
				return true
			})
			return count, nil
		}
	}
	return 0, fmt.Errorf("package function %s body not found", functionName)
}

func validatedPlanRouteViolations(sources []typedSource) ([]string, error) {
	_, files, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return nil, err
	}
	markerObject, ok := pkg.Scope().Lookup("validatedPlanV1").(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("validatedPlanV1 type not found")
	}
	markerType := markerObject.Type()
	allowedReturns := map[string]bool{
		"parseAndValidatePlanV1": true,
		"validateParsedPlanV1":   true,
	}
	var violations []string
	for _, file := range files {
		for _, decl := range file.Decls {
			switch typed := decl.(type) {
			case *ast.FuncDecl:
				function, ok := info.Defs[typed.Name].(*types.Func)
				if !ok {
					continue
				}
				signature, ok := function.Type().(*types.Signature)
				if !ok {
					continue
				}
				for index := 0; index < signature.Params().Len(); index++ {
					if types.Identical(signature.Params().At(index).Type(), markerType) &&
						function.Name() != "buildAuthorizedPlanState" {
						violations = append(
							violations,
							"validatedPlanV1 parameter accepted by "+function.Name(),
						)
					}
				}
				for index := 0; index < signature.Results().Len(); index++ {
					if types.Identical(signature.Results().At(index).Type(), markerType) &&
						!allowedReturns[function.Name()] {
						violations = append(
							violations,
							"validatedPlanV1 returned by "+function.Name(),
						)
					}
				}
				if typed.Body == nil {
					continue
				}
				ast.Inspect(typed.Body, func(node ast.Node) bool {
					literal, ok := node.(*ast.CompositeLit)
					if !ok || len(literal.Elts) == 0 {
						return true
					}
					if types.Identical(info.TypeOf(literal), markerType) &&
						function.Name() != "validateParsedPlanV1" {
						violations = append(
							violations,
							"validatedPlanV1 constructed by "+function.Name(),
						)
					}
					return true
				})
			case *ast.GenDecl:
				if typed.Tok != token.VAR {
					continue
				}
				for _, spec := range typed.Specs {
					valueSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range valueSpec.Names {
						if object := info.Defs[name]; object != nil &&
							types.Identical(object.Type(), markerType) {
							violations = append(
								violations,
								"validatedPlanV1 stored globally as "+name.Name,
							)
						}
					}
				}
			}
		}
	}
	sort.Strings(violations)
	return violations, nil
}

// ========== Existing: exported symbol / type allowlist ==========

func TestArchitectureExportedSymbolAllowlist(t *testing.T) {
	dir := "."
	matches, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	var files []string
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		files = append(files, m)
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	allowed := map[string]bool{
		"AuthorizedRepairPlan":     true,
		"PlanMetadataSnapshot":     true,
		"RepairFindingSnapshot":    true,
		"TransactionStateSnapshot": true,
		"RuleStateSnapshot":        true,
		"SeriesStateSnapshot":      true,
		"OptionalString":           true,
		"OptionalInt64":            true,
		"OptionalBool":             true,
	}

	allowedFuncs := map[string]bool{
		"LoadAuthorizedRepairPlan": true,
	}

	var exported []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && ast.IsExported(d.Name.Name) && !allowedFuncs[d.Name.Name] {
						exported = append(exported, "func "+d.Name.Name)
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok && ast.IsExported(ts.Name.Name) {
							if !allowed[ts.Name.Name] {
								exported = append(exported, "type "+ts.Name.Name)
							}
						}
						if vs, ok := spec.(*ast.ValueSpec); ok {
							for _, id := range vs.Names {
								if ast.IsExported(id.Name) {
									exported = append(exported, "value/const "+id.Name)
								}
							}
						}
					}
				}
			}
		}
	}

	if len(exported) > 0 {
		t.Fatalf("unexpected exported symbols: %v", exported)
	}
}

// ---------- Existing: snapshot/authorized fields by value ----------

func TestArchitectureSnapshotFieldsByValue(t *testing.T) {
	dir := "."
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	snapshotTypes := map[string]bool{
		"PlanMetadataSnapshot":       true,
		"RepairFindingSnapshot":      true,
		"TransactionStateSnapshot":   true,
		"RuleStateSnapshot":          true,
		"SeriesStateSnapshot":        true,
		"OptionalString":             true,
		"OptionalInt64":              true,
		"OptionalBool":               true,
		"authorizedFinding":          true,
		"authorizedTransactionState": true,
		"authorizedRuleState":        true,
		"authorizedSeriesState":      true,
	}

	forbidden := map[string]bool{
		"Pointer":       true,
		"Slice":         true,
		"Map":           true,
		"Interface":     true,
		"Func":          true,
		"Chan":          true,
		"UnsafePointer": true,
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				if !snapshotTypes[ts.Name.Name] {
					return true
				}
				for _, field := range st.Fields.List {
					typeStr := typeString(field.Type)
					for _, fb := range field.Names {
						if strings.HasPrefix(typeStr, "*") && !strings.Contains(typeStr, "authorizedPlanState") && !strings.Contains(typeStr, "authorizationSeal") {
							if fb.Name == "seal" || fb.Name == "state" {
								continue
							}
							t.Errorf("%s.%s is pointer %q (not by value)", ts.Name.Name, fb.Name, typeStr)
						}
						if strings.HasPrefix(typeStr, "[]") {
							if fb.Name == "findings" && ts.Name.Name == "authorizedPlanState" {
								continue
							}
							t.Errorf("%s.%s is slice %q (not by value)", ts.Name.Name, fb.Name, typeStr)
						}
						for fwd := range forbidden {
							if strings.HasPrefix(typeStr, fwd) {
								t.Errorf("%s.%s is %s %q", ts.Name.Name, fb.Name, fwd, typeStr)
							}
						}
					}
				}
				return true
			})
		}
	}
}

// ---------- Existing: seal non-zero ----------

func TestArchitectureSealNonZero(t *testing.T) {
	if authorizedPlanSeal == nil {
		t.Fatal("authorizedPlanSeal is nil")
	}
	if authorizedPlanSeal.marker == 0 {
		t.Fatal("authorizedPlanSeal.marker is zero")
	}
}

// ---------- Existing: seal private ----------

func TestArchitectureSealPrivate(t *testing.T) {
	fset := token.NewFileSet()
	dir := "."
	pkgs, _ := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				if strings.ToLower(ts.Name.Name) == "authorizationseal" && ast.IsExported(ts.Name.Name) {
					t.Error("authorizationSeal should not be exported")
				}
				return true
			})
			ast.Inspect(file, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for _, id := range vs.Names {
					if id.Name == "authorizedPlanSeal" && ast.IsExported(id.Name) {
						t.Error("authorizedPlanSeal should not be exported")
					}
				}
				return true
			})
		}
	}
}

// ---------- Existing: AuthorizedRepairPlan fields private ----------

func TestArchitectureAuthorizedRepairPlanFieldsPrivate(t *testing.T) {
	fset := token.NewFileSet()
	dir := "."
	pkgs, _ := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				if ts.Name.Name != "AuthorizedRepairPlan" {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range st.Fields.List {
					for _, id := range field.Names {
						if ast.IsExported(id.Name) {
							t.Errorf("AuthorizedRepairPlan.%s is exported", id.Name)
						}
					}
				}
				return true
			})
		}
	}
}

// ---------- typeString helper ----------

func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeString(t.Elt)
		}
		return fmt.Sprintf("[%s]%s", nodeString(t.Len), typeString(t.Elt))
	case *ast.MapType:
		return "Map"
	case *ast.InterfaceType:
		return "Interface"
	case *ast.FuncType:
		return "Func"
	case *ast.ChanType:
		return "Chan"
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	default:
		return "?"
	}
}

func nodeString(n ast.Node) string {
	switch t := n.(type) {
	case *ast.BasicLit:
		return t.Value
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}

// ========== V2B.2: exported methods allowlist ==========

var allowedExportedMethods = map[string]map[string]bool{
	"AuthorizedRepairPlan": {
		"IsAuthorized": true, "Digest": true, "Metadata": true, "FindingCount": true, "FindingAt": true,
	},
	"PlanMetadataSnapshot": {
		"SchemaVersion": true, "PlanID": true, "Scope": true, "ToolVersion": true,
		"ClassifierVersion": true, "DatabaseSchemaVersion": true, "Timezone": true,
		"LogicalFingerprintVersion": true, "SourceLogicalFingerprint": true,
		"AuditSetDigest": true, "GeneratedAt": true,
	},
	"RepairFindingSnapshot": {
		"Variant": true, "Source": true, "WorkspaceID": true, "SourceRowID": true,
		"AuditedField": true, "ReasonCode": true, "RepairOutcome": true,
		"ExpectedLegacyValue": true, "ExpectedCivilState": true, "ProposedCivilValue": true,
		"Timezone": true, "ClassifierVersion": true,
		"TransactionState": true, "RuleState": true, "SeriesState": true,
	},
	"TransactionStateSnapshot": {
		"ID": true, "WorkspaceID": true, "Status": true, "Type": true,
		"DateUnix": true, "DueDateUnix": true, "RecurringRuleID": true,
		"SeriesID": true, "RecurrenceSequence": true,
		"IsRecurringException": true, "RecurringExceptionReason": true,
	},
	"RuleStateSnapshot": {
		"ID": true, "WorkspaceID": true, "SeriesID": true,
		"ParentRuleID": true, "EffectiveStartSequence": true, "EffectiveEndSequence": true,
		"Active": true,
	},
	"SeriesStateSnapshot": {
		"ID": true, "WorkspaceID": true, "Active": true,
	},
	"OptionalString": {
		"IsNull": true, "Value": true,
	},
	"OptionalInt64": {
		"IsNull": true, "Value": true,
	},
	"OptionalBool": {
		"IsNull": true, "Value": true,
	},
}

var allowedExportTypeNames = map[string]bool{
	"AuthorizedRepairPlan":     true,
	"PlanMetadataSnapshot":     true,
	"RepairFindingSnapshot":    true,
	"TransactionStateSnapshot": true,
	"RuleStateSnapshot":        true,
	"SeriesStateSnapshot":      true,
	"OptionalString":           true,
	"OptionalInt64":            true,
	"OptionalBool":             true,
}

var allowedPrivateInterfaceMethods = map[string]map[string]bool{
	"loadError": {
		"Error": true,
	},
	"planStageError": {
		"Error": true,
	},
}

type methodCheckResult struct {
	methodViolations []string
}

var allowedExportedFuncs = map[string]bool{
	"LoadAuthorizedRepairPlan": true,
}

func checkFileMethods(file *ast.File) (violations []string) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv == nil && ast.IsExported(fn.Name.Name) {
			if !allowedExportedFuncs[fn.Name.Name] {
				violations = append(violations, "exported func "+fn.Name.Name)
			}
			continue
		}
		if fn.Recv != nil && ast.IsExported(fn.Name.Name) {
			typeName := receiverTypeName(fn.Recv)
			if !allowedExportTypeNames[typeName] {
				if allowedPrivateInterfaceMethods[typeName][fn.Name.Name] {
					continue
				}
				violations = append(violations, fmt.Sprintf("exported method %s on unknown type %s", fn.Name.Name, typeName))
				continue
			}
			if !allowedExportedMethods[typeName][fn.Name.Name] {
				violations = append(violations, fmt.Sprintf("exported method %s.%s not in allowlist", typeName, fn.Name.Name))
			}
		}
	}
	return
}

func receiverTypeName(list *ast.FieldList) string {
	if list == nil || len(list.List) == 0 {
		return ""
	}
	typ := list.List[0].Type
	switch t := typ.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

func TestArchitectureExportedMethodAllowlist(t *testing.T) {
	dir := "."
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var violations []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			violations = append(violations, checkFileMethods(file)...)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("method/function allowlist violations in production files:\n  %v", violations)
	}
}

// ========== V2C.7: semantic seal/capability/composite guards ==========

func checkMethodsInSource(src string) (violations []string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		return []string{fmt.Sprintf("parse error: %v", err)}
	}
	return checkFileMethods(file)
}

func checkSealInSource(src string) []string {
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		return []string{err.Error()}
	}
	var result []string
	for _, v := range report.violations {
		if v.category == "seal_assignment" || v.category == "capability_return" || v.category == "keyed_composite" || v.category == "positional_composite" {
			result = append(result, fmt.Sprintf("%s: %s in %s", v.category, v.symbol, v.filename))
		}
	}
	return result
}

func checkDigestWritesInSource(src string) []string {
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		return []string{err.Error()}
	}
	var result []string
	for _, v := range report.violations {
		if v.category == "digest_assignment" || v.category == "keyed_composite" || v.category == "positional_composite" {
			if v.field == "digest" || v.category == "digest_assignment" {
				result = append(result, fmt.Sprintf("%s: %s", v.category, v.field))
			}
		}
	}
	return result
}

func TestArchitectureSemanticSealAssignmentAndCapabilityGuard(t *testing.T) {
	result, err := checkProduction(loadProductionFiles)
	if err != nil {
		t.Fatalf("production check failed: %v", err)
	}
	for _, v := range result.report.violations {
		if v.filename == "load_authorized.go" {
			continue // authorized constructor; expected violations
		}
		if v.category == "seal_assignment" || v.category == "capability_return" {
			t.Errorf("production seal/capability violation: %+v", v)
		}
	}
}

func TestArchitectureAdversarialDigestMethod(t *testing.T) {
	src := `package civildateplan
func (p *AuthorizedRepairPlan) Digest() string { return "" }
`
	v := checkMethodsInSource(src)
	if len(v) > 0 {
		t.Fatalf("Digest method should be allowed, got violations: %v", v)
	}
}

func TestArchitectureAdversarialUnallowedMethod(t *testing.T) {
	src := `package civildateplan
func (p *AuthorizedRepairPlan) Clone() *AuthorizedRepairPlan { return nil }
`
	v := checkMethodsInSource(src)
	if len(v) == 0 {
		t.Fatal("Clone method should be detected as violation")
	}
}

func TestArchitectureAdversarialStringMethod(t *testing.T) {
	src := `package civildateplan
func (p AuthorizedRepairPlan) String() string { return "" }
`
	v := checkMethodsInSource(src)
	if len(v) == 0 {
		t.Fatal("String method should be detected as violation")
	}
}

// ---------- V2C.7: semantic adversary tests (seal/capability/composite) ----------

func TestArchitectureSemanticDirectSealAssignment(t *testing.T) {
	src := `package civildateplan
func dangerous(p *AuthorizedRepairPlan) {
	p.seal = authorizedPlanSeal
}
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("seal_assignment", "synthetic.go", "dangerous", "seal") {
		t.Fatal("direct seal assignment should be detected")
	}
}

func TestArchitectureSemanticCompositeKeyedSeal(t *testing.T) {
	src := `package civildateplan
func dangerous() *AuthorizedRepairPlan {
	return &AuthorizedRepairPlan{
		seal: authorizedPlanSeal,
	}
}
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("keyed_composite", "synthetic.go", "", "seal") {
		t.Fatal("keyed composite with seal should be detected")
	}
}

func TestArchitectureSemanticCompositePositionalSeal(t *testing.T) {
	src := `package civildateplan
func dangerous() AuthorizedRepairPlan {
	return AuthorizedRepairPlan{
		authorizedPlanSeal,
		nil,
		[32]byte{},
	}
}
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("positional_composite", "synthetic.go", "", "seal") {
		t.Fatal("positional composite with authorizedPlanSeal should be detected")
	}
}

func TestArchitectureSemanticReturnCapability(t *testing.T) {
	src := `package civildateplan
func dangerous() *AuthorizedRepairPlan {
	return nil
}
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("capability_return", "synthetic.go", "dangerous", "") {
		t.Fatal("function returning *AuthorizedRepairPlan should be detected")
	}
}

func TestArchitectureSemanticReturnCapabilityValue(t *testing.T) {
	src := `package civildateplan
func dangerous() AuthorizedRepairPlan {
	return AuthorizedRepairPlan{}
}
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("capability_return", "synthetic.go", "dangerous", "") {
		t.Fatal("function returning AuthorizedRepairPlan should be detected")
	}
}

func TestArchitectureAdversarialAllowedMethod(t *testing.T) {
	src := `package civildateplan
func (p *AuthorizedRepairPlan) IsAuthorized() bool { return false }
`
	v := checkMethodsInSource(src)
	if len(v) > 0 {
		t.Fatalf("IsAuthorized method should not be a violation, got: %v", v)
	}
}

func TestArchitectureAuthorizedRepairPlanDigestField(t *testing.T) {
	fset := token.NewFileSet()
	dir := "."
	pkgs, _ := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "AuthorizedRepairPlan" {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				if len(st.Fields.List) != 3 {
					t.Errorf("AuthorizedRepairPlan must have exactly 3 fields, got %d", len(st.Fields.List))
				}
				seen := make(map[string]string)
				for _, field := range st.Fields.List {
					for _, id := range field.Names {
						tstr := typeString(field.Type)
						seen[id.Name] = tstr
						if ast.IsExported(id.Name) {
							t.Errorf("AuthorizedRepairPlan.%s is exported", id.Name)
						}
					}
				}
				if seen["seal"] != "*authorizationSeal" {
					t.Errorf("seal field type mismatch: %q", seen["seal"])
				}
				if seen["state"] != "*authorizedPlanState" {
					t.Errorf("state field type mismatch: %q", seen["state"])
				}
				dt, ok := seen["digest"]
				if !ok {
					t.Error("digest field missing")
				} else if dt != "[32]byte" {
					t.Errorf("digest must be [32]byte, got %q", dt)
				}
				return true
			})
		}
	}
}

func TestArchitectureDigestWriteGuard(t *testing.T) {
	result, err := checkProduction(loadProductionFiles)
	if err != nil {
		t.Fatalf("production check failed: %v", err)
	}
	for _, v := range result.report.violations {
		if v.category == "digest_assignment" {
			t.Errorf("production digest write: %+v", v)
		}
	}
}

func TestArchitectureDigestWriteGuardSynthetic(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"assignment", `package civildateplan
func dangerous(p *AuthorizedRepairPlan) { p.digest = [32]byte{} }`},
		{"multiple assignment", `package civildateplan
func dangerous(p *AuthorizedRepairPlan) { p.state, p.digest = nil, [32]byte{} }`},
		{"keyed literal", `package civildateplan
func dangerous() AuthorizedRepairPlan {
	return AuthorizedRepairPlan{digest: [32]byte{}}
}`},
		{"positional literal", `package civildateplan
func dangerous() AuthorizedRepairPlan {
	return AuthorizedRepairPlan{nil, nil, [32]byte{}}
}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := checkSingleSynthetic(tc.src, modeSealAndCapability)
			if err != nil {
				t.Fatalf("check failed: %v", err)
			}
			if !report.has("digest_assignment", "synthetic.go", "dangerous", "digest") &&
				!report.has("keyed_composite", "synthetic.go", "", "digest") &&
				!report.has("positional_composite", "synthetic.go", "", "digest") {
				t.Fatalf("dangerous digest write was not detected. violations: %+v", report.violations)
			}
		})
	}
}

func TestArchitectureDigestReadAllowed(t *testing.T) {
	// Reads of .digest should not generate violations.
	// The semantic checker only flags assignments, not reads.
}

// ========== V2C.7: production guard (semantic) ==========

func TestArchitectureAliasProductionGuard(t *testing.T) {
	result, err := checkProduction(loadProductionFiles)
	if err != nil {
		t.Fatalf("production check failed: %v", err)
	}
	want := productionGateResult{
		evidence: productionGateEvidence{
			checked: true, mode: modeProductionCapabilities, packagePath: "civildateplan",
			analyzedFiles:   currentProductionAnalyzedFiles(),
			capabilityRoot: "AuthorizedRepairPlan",
		},
		report: typedArchitectureReport{violations: baseAuthorizedLoaderViolations()},
	}
	assertProductionGateResultEqual(t, result, want)
	if len(result.report.resolvedCapabilities) > 0 {
		names := make([]string, 0, len(result.report.resolvedCapabilities))
		for _, rc := range result.report.resolvedCapabilities {
			names = append(names, rc.name)
		}
		sort.Strings(names)
		t.Fatalf("alias/derived types resolving to AuthorizedRepairPlan found: %v", names)
	}
	if len(result.report.violations) > len(baseAuthorizedLoaderViolations()) {
		t.Fatalf("unexpected alias usage violations in production:\n  %+v", result.report.violations)
	}
}

func TestArchitecturePreparePlanBytesPipeline(t *testing.T) {
	sources, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	graph, pkg, err := semanticGraph(sources)
	if err != nil {
		t.Fatal(err)
	}
	root, err := packageFunctionByName(graph, pkg, "preparePlanBytes")
	if err != nil {
		t.Fatal(err)
	}
	for _, targetName := range []string{
		"parseAndValidatePlanV1",
		"validateParsedPlanV1",
		"validateTopFields",
		"validatePlanSemanticsV1",
		"buildAuthorizedPlanState",
		"finalizePreparedPlanState",
		"computeAuthorizedPlanDigestV1",
	} {
		target, targetErr := packageFunctionByName(graph, pkg, targetName)
		if targetErr != nil {
			t.Fatal(targetErr)
		}
		if !reachesFunction(graph, root, target) {
			t.Fatalf("preparePlanBytes does not reach real %s", targetName)
		}
	}
	legacyValidation, err := packageFunctionByName(graph, pkg, "validatePlanV1")
	if err != nil {
		t.Fatal(err)
	}
	if reachesFunction(graph, root, legacyValidation) {
		t.Fatal("preparePlanBytes must not reach compatibility validator validatePlanV1")
	}
	directCalls := []struct {
		function string
		target   string
		want     int
	}{
		{"preparePlanBytes", "parseAndValidatePlanV1", 1},
		{"preparePlanBytes", "buildAuthorizedPlanState", 1},
		{"preparePlanBytes", "finalizePreparedPlanState", 1},
		{"preparePlanBytes", "validateTopFields", 0},
		{"preparePlanBytes", "validatePlanSemanticsV1", 0},
		{"preparePlanBytes", "validatePlanV1", 0},
		{"preparePlanBytes", "computeAuthorizedPlanDigestV1", 0},
		{"parseAndValidatePlanV1", "parsePlanV1Syntax", 1},
		{"parseAndValidatePlanV1", "validateParsedPlanV1", 1},
		{"validateParsedPlanV1", "validateTopFields", 1},
		{"validateParsedPlanV1", "validatePlanSemanticsV1", 1},
		{"validateParsedPlanV1", "validatePlanV1", 0},
		{"buildAuthorizedPlanState", "validateTopFields", 0},
		{"buildAuthorizedPlanState", "validatePlanSemanticsV1", 0},
		{"buildAuthorizedPlanState", "validatePlanV1", 0},
		{"finalizePreparedPlanState", "computeAuthorizedPlanDigestV1", 1},
		{"validatePlanV1", "validateTopFields", 1},
		{"validatePlanV1", "validatePlanSemanticsV1", 1},
	}
	for _, expected := range directCalls {
		got, countErr := packageFunctionDirectCallCount(
			sources,
			expected.function,
			expected.target,
		)
		if countErr != nil {
			t.Fatal(countErr)
		}
		if got != expected.want {
			t.Fatalf(
				"%s direct calls to %s = %d, want %d",
				expected.function,
				expected.target,
				got,
				expected.want,
			)
		}
	}
	routeViolations, err := validatedPlanRouteViolations(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(routeViolations) != 0 {
		t.Fatalf("validatedPlanV1 has alternate routes: %v", routeViolations)
	}
	referencesSeal, err := packageFunctionReferencesObject(
		sources,
		"preparePlanBytes",
		"authorizedPlanSeal",
	)
	if err != nil {
		t.Fatal(err)
	}
	if referencesSeal {
		t.Fatal("preparePlanBytes must not reference authorizedPlanSeal")
	}

	for _, targetName := range []string{
		"readPlanFileBytes",
		"openPlanFile",
	} {
		target, targetErr := packageFunctionByName(graph, pkg, targetName)
		if targetErr != nil {
			continue
		}
		if reachesFunction(graph, root, target) {
			t.Fatalf("preparePlanBytes must not reach %s", targetName)
		}
	}

	result, err := checkProduction(loadProductionFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.report.violations) != len(baseAuthorizedLoaderViolations()) {
		t.Fatalf("prepare pipeline introduced unexpected capability route: %+v", result.report.violations)
	}
	boundaryViolations, err := capabilityBoundaryViolations(sources)
	if err != nil {
		t.Fatal(err)
	}
	var unexpectedBoundary []architectureViolation
	for _, v := range boundaryViolations {
		if v.filename != "load_authorized.go" {
			unexpectedBoundary = append(unexpectedBoundary, v)
		}
	}
	if len(unexpectedBoundary) != 0 {
		t.Fatalf("production accepts or returns unexpected capability: %+v", unexpectedBoundary)
	}
}

func TestArchitecturePrepareAdversarialCapabilityRoutesRejected(t *testing.T) {
	const synthetic = `package civildateplan
type prepareReceiver struct{}

func forgedPrepareReturn() *AuthorizedRepairPlan { return nil }
func (prepareReceiver) preparePlanBytes() *AuthorizedRepairPlan { return nil }
func forgedPrepareTarget() *AuthorizedRepairPlan { return nil }
var forgedPrepareValue = forgedPrepareTarget
var forgedPrepareGlobal *AuthorizedRepairPlan
var forgedPrepareClosure = func() *AuthorizedRepairPlan {
	return &AuthorizedRepairPlan{seal: authorizedPlanSeal, digest: [32]byte{1}}
}
func forgedPrepareArgument(plan *AuthorizedRepairPlan) {
	plan.seal = authorizedPlanSeal
	plan.digest = [32]byte{1}
}
func forgedLocalHomonym() {
	preparePlanBytes := func() *AuthorizedRepairPlan {
		return &AuthorizedRepairPlan{seal: authorizedPlanSeal}
	}
	_ = preparePlanBytes
}
`
	production, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	sources := append(production, typedSource{name: "prepare_adversarial.go", src: synthetic})

	boundaries, err := capabilityBoundaryViolations(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(findViolations(boundaries, "capability_parameter", "prepare_adversarial.go", "forgedPrepareArgument", "")) == 0 {
		t.Fatalf("capability argument was not rejected: %+v", boundaries)
	}
	if len(findViolations(boundaries, "capability_global", "prepare_adversarial.go", "forgedPrepareGlobal", "")) == 0 {
		t.Fatalf("global capability was not rejected: %+v", boundaries)
	}
	for _, symbol := range []string{
		"forgedPrepareReturn",
		"preparePlanBytes",
		"forgedPrepareTarget",
	} {
		if len(findViolations(boundaries, "capability_return", "prepare_adversarial.go", symbol, "")) == 0 {
			t.Fatalf("capability return %s was not rejected: %+v", symbol, boundaries)
		}
	}
	funcLiteralReturns := findViolations(
		boundaries,
		"capability_return",
		"prepare_adversarial.go",
		"",
		"",
	)
	if len(funcLiteralReturns) < 5 {
		t.Fatalf("function value/closure capability returns were not all rejected: %+v", boundaries)
	}

	report, err := checkTypedArchitecture(sources, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("seal_assignment", "prepare_adversarial.go", "forgedPrepareArgument", "seal") {
		t.Fatalf("seal write was not rejected: %+v", report.violations)
	}
	if !report.has("digest_assignment", "prepare_adversarial.go", "forgedPrepareArgument", "digest") {
		t.Fatalf("digest write was not rejected: %+v", report.violations)
	}
	if !report.has("keyed_composite", "prepare_adversarial.go", "", "seal") ||
		!report.has("keyed_composite", "prepare_adversarial.go", "", "digest") {
		t.Fatalf("closure/local composite was not rejected: %+v", report.violations)
	}
}

// ========== V2D-B.3: file reader architecture gate ==========

// varTargets models every possible object a variable may hold, conservatively.
// Fail-closed: any reachable forbidden object in the set causes a violation.
type varTargets map[*types.Var]map[types.Object]struct{}

func newVarTargets() varTargets {
	return make(map[*types.Var]map[types.Object]struct{})
}

func (vt varTargets) add(v *types.Var, obj types.Object) {
	if v == nil || obj == nil {
		return
	}
	m := vt[v]
	if m == nil {
		m = make(map[types.Object]struct{})
		vt[v] = m
	}
	m[obj] = struct{}{}
}

func (vt varTargets) fixpoint() {
	changed := true
	for changed {
		changed = false
		for v, targets := range vt {
			for obj := range targets {
				vv, ok := obj.(*types.Var)
				if !ok {
					continue
				}
				for next := range vt[vv] {
					if _, exists := targets[next]; !exists {
						vt.add(v, next)
						changed = true
					}
				}
			}
		}
	}
}

func varForIdent(id *ast.Ident, info *types.Info) *types.Var {
	if def := info.Defs[id]; def != nil {
		if v, ok := def.(*types.Var); ok {
			return v
		}
	}
	if use := info.Uses[id]; use != nil {
		if v, ok := use.(*types.Var); ok {
			return v
		}
	}
	return nil
}

func resolveVarChainSet(obj types.Object, localVars, pkgVars varTargets) map[types.Object]struct{} {
	result := make(map[types.Object]struct{})
	visited := make(map[*types.Var]bool)
	var dfs func(o types.Object)
	dfs = func(o types.Object) {
		v, ok := o.(*types.Var)
		if !ok {
			result[o] = struct{}{}
			return
		}
		if visited[v] {
			return
		}
		visited[v] = true
		for target := range localVars[v] {
			dfs(target)
		}
		for target := range pkgVars[v] {
			dfs(target)
		}
	}
	dfs(obj)
	return result
}

func buildPkgVarToFunc(astFiles []*ast.File, info *types.Info) varTargets {
	result := newVarTargets()
	for _, file := range astFiles {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					v := varForIdent(name, info)
					if v == nil {
						continue
					}
					rhsObj := resolveExprToObject(vs.Values[i], info)
					if rhsObj != nil {
						result.add(v, rhsObj)
					}
				}
			}
		}
	}
	result.fixpoint()
	return result
}

func resolveExprToObject(expr ast.Expr, info *types.Info) types.Object {
	switch e := unparenExpr(expr).(type) {
	case *ast.Ident:
		return info.Uses[e]
	case *ast.SelectorExpr:
		if use := info.Uses[e.Sel]; use != nil {
			return use
		}
		if sel := info.Selections[e]; sel != nil {
			return sel.Obj()
		}
		if id, ok := unparenExpr(e.X).(*ast.Ident); ok {
			if pn, ok := info.Uses[id].(*types.PkgName); ok {
				return pn.Pkg().Scope().Lookup(e.Sel.Name)
			}
		}
	}
	return nil
}

func augmentGraphWithVars(
	graph map[*types.Func]*funcRefNode,
	astFiles []*ast.File,
	info *types.Info,
	pkgVars varTargets,
) {
	for _, file := range astFiles {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fnObj, ok := info.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			node := graph[fnObj]
			if node == nil {
				continue
			}
			localVars := newVarTargets()
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				gatherVarAssigns(n, info, localVars)
				return true
			})
			localVars.fixpoint()
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				use := info.Uses[id]
				if use == nil {
					return true
				}
				v, ok := use.(*types.Var)
				if !ok {
					return true
				}
				for target := range resolveVarChainSet(v, localVars, pkgVars) {
					if fn, ok := target.(*types.Func); ok {
						node.refs = append(node.refs, fn)
					}
				}
				return true
			})
		}
	}
}

func isForbiddenName(name string, forbiddenNames []string) bool {
	for _, n := range forbiddenNames {
		if name == n {
			return true
		}
	}
	return false
}

func readPlanFileBytesStatByPathViolations(
	sources []typedSource,
	graph map[*types.Func]*funcRefNode,
	root *types.Func,
) ([]string, error) {
	_, astFiles, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return nil, err
	}

	sourceASTs := make(map[string]*ast.File, len(sources))
	for i, s := range sources {
		sourceASTs[s.name] = astFiles[i]
	}

	forbiddenPkgs := map[string]bool{"os": true, "syscall": true}
	forbiddenNames := []string{"Stat", "Lstat"}

	directForbidden := make(map[types.Object]bool)
	for _, obj := range info.Uses {
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() != nil {
			continue
		}
		if forbiddenPkgs[fn.Pkg().Path()] && isForbiddenName(fn.Name(), forbiddenNames) {
			directForbidden[fn] = true
		}
	}

	localGraph := buildFuncRefGraph(astFiles, info, pkg, nil, sourceASTs)

	rootObj := pkg.Scope().Lookup("readPlanFileBytes")
	rootFn, ok := rootObj.(*types.Func)
	if !ok {
		return nil, fmt.Errorf("readPlanFileBytes not found in package scope")
	}

	pkgVars := buildPkgVarToFunc(astFiles, info)
	augmentGraphWithVars(localGraph, astFiles, info, pkgVars)

	reachable := make(map[*types.Func]bool)
	var dfs func(*types.Func)
	dfs = func(fn *types.Func) {
		if fn == nil || reachable[fn] {
			return
		}
		reachable[fn] = true
		node := localGraph[fn]
		if node == nil {
			return
		}
		for _, ref := range node.refs {
			if next, ok := ref.(*types.Func); ok {
				dfs(next)
			}
		}
	}
	dfs(rootFn)

	var violations []string
	for _, file := range astFiles {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fnObj, isFunc := info.Defs[fn.Name].(*types.Func)
			if !isFunc || !reachable[fnObj] {
				continue
			}

			localVars := newVarTargets()
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				gatherVarAssigns(n, info, localVars)
				return true
			})
			localVars.fixpoint()

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := unparenExpr(call.Fun)
				obj := resolveCallCallee(call, info)
				if obj == nil {
					return true
				}
				for target := range resolveVarChainSet(obj, localVars, pkgVars) {
					if target == nil || target.Pkg() == nil {
						continue
					}
					if !directForbidden[target] {
						continue
					}
					// Only package-level function calls; method calls on receivers are not forbidden.
					if sel, ok := callee.(*ast.SelectorExpr); ok {
						x, ok := unparenExpr(sel.X).(*ast.Ident)
						if !ok {
							continue
						}
						if _, isPkg := info.Uses[x].(*types.PkgName); !isPkg {
							continue
						}
					}
					violations = append(violations,
						fmt.Sprintf("%s.%s() in %s",
							target.Pkg().Path(), target.Name(), fn.Name.Name))
				}
				return true
			})
		}
	}

	return violations, nil
}

func gatherVarAssigns(n ast.Node, info *types.Info, localVars varTargets) {
	switch node := n.(type) {
	case *ast.AssignStmt:
		if node.Tok != token.DEFINE && node.Tok != token.ASSIGN {
			return
		}
		for i, lhs := range node.Lhs {
			if i >= len(node.Rhs) {
				continue
			}
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			v := varForIdent(id, info)
			if v == nil {
				continue
			}
			rhsObj := resolveExprToObject(node.Rhs[i], info)
			if rhsObj != nil {
				localVars.add(v, rhsObj)
			}
		}
	case *ast.DeclStmt:
		gd, ok := node.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				v := varForIdent(name, info)
				if v == nil {
					continue
				}
				rhsObj := resolveExprToObject(vs.Values[i], info)
				if rhsObj != nil {
					localVars.add(v, rhsObj)
				}
			}
		}
	}
}

func resolveCallCallee(call *ast.CallExpr, info *types.Info) types.Object {
	switch fun := unparenExpr(call.Fun).(type) {
	case *ast.SelectorExpr:
		if use := info.Uses[fun.Sel]; use != nil {
			return use
		}
		if sel := info.Selections[fun]; sel != nil {
			return sel.Obj()
		}
	case *ast.Ident:
		return info.Uses[fun]
	}
	return nil
}

func TestArchitectureReadPlanFileBytesPipeline(t *testing.T) {
	sources, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	graph, pkg, err := semanticGraph(sources)
	if err != nil {
		t.Fatal(err)
	}
	root, err := packageFunctionByName(graph, pkg, "readPlanFileBytes")
	if err != nil {
		t.Fatalf("readPlanFileBytes not found: %v", err)
	}
	if ast.IsExported(root.Name()) {
		t.Fatal("readPlanFileBytes must not be exported")
	}
	for _, targetName := range []string{
		"openPlanFile",
	} {
		target, targetErr := packageFunctionByName(graph, pkg, targetName)
		if targetErr != nil {
			t.Fatal(targetErr)
		}
		if !reachesFunction(graph, root, target) {
			t.Fatalf("readPlanFileBytes does not reach %s", targetName)
		}
	}
	for _, targetName := range []string{
		"parsePlanV1Syntax",
		"parseAndValidatePlanV1",
		"parsePlanV1",
		"validateParsedPlanV1",
		"validateTopFields",
		"validatePlanSemanticsV1",
		"validatePlanV1",
		"buildAuthorizedPlanState",
		"computeAuthorizedPlanDigestV1",
		"finalizePreparedPlanState",
		"preparePlanBytes",
	} {
		target, targetErr := packageFunctionByName(graph, pkg, targetName)
		if targetErr != nil {
			t.Fatal(targetErr)
		}
		if reachesFunction(graph, root, target) {
			t.Fatalf("readPlanFileBytes must not reach %s", targetName)
		}
	}
	directCalls := []struct {
		function string
		target   string
		want     int
	}{
		{"readPlanFileBytes", "openPlanFile", 1},
	}
	for _, expected := range directCalls {
		got, countErr := packageFunctionDirectCallCount(
			sources,
			expected.function,
			expected.target,
		)
		if countErr != nil {
			t.Fatal(countErr)
		}
		if got != expected.want {
			t.Fatalf(
				"%s direct calls to %s = %d, want %d",
				expected.function,
				expected.target,
				got,
				expected.want,
			)
		}
	}
		for _, objectName := range []string{
		"authorizedPlanSeal",
		"AuthorizedRepairPlan",
	} {
		references, refErr := packageFunctionReferencesObject(
			sources,
			"readPlanFileBytes",
			objectName,
		)
		if refErr != nil {
			t.Fatal(refErr)
		}
		if references {
			t.Fatalf("readPlanFileBytes must not reference %s", objectName)
		}
	}

	statViolations, statErr := readPlanFileBytesStatByPathViolations(sources, graph, root)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if len(statViolations) != 0 {
		t.Fatalf("readPlanFileBytes must not reach path-based stat: %v", statViolations)
	}

}

// ========== V2C.7: five cross-file gates (semantic) ==========

func TestArchitectureCrossFileGateSealAssignment(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\nfunc dangerous(p B) {\np.seal = authorizedPlanSeal\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("seal_assignment", "use.go", "dangerous", "seal") {
		t.Fatalf("seal_assignment in use.go not detected. got: %+v", report.violations)
	}
	if !report.has("prohibited_alias", "types_a.go", "A", "") {
		t.Fatal("prohibited_alias A not detected")
	}
	if !report.has("prohibited_alias", "types_b.go", "B", "") {
		t.Fatal("prohibited_alias B not detected")
	}

	// Negative control
	srcNoSeal := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\nfunc allowed(p B) {\n_ = p\n}\n",
	}
	report2, err := checkSyntheticMulti(srcNoSeal, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if report2.has("seal_assignment", "use.go", "", "") {
		t.Fatal("seal_assignment should not appear when removed")
	}
	if !report2.has("prohibited_alias", "types_a.go", "A", "") {
		t.Fatal("prohibited_alias should still be detected")
	}
}

func TestArchitectureDefinedTypeChainCapability(t *testing.T) {
	src := map[string]string{
		"types.go": "package civildateplan\ntype D AuthorizedRepairPlan\ntype E D\n",
		"use.go": "package civildateplan\nfunc dangerous(p D) E {\n" +
			"p.seal = authorizedPlanSeal\np.digest = [32]byte{1}\nreturn E{}\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []architectureViolation{
		{category: "prohibited_defined_type", filename: "types.go", symbol: "D", typeName: "D"},
		{category: "prohibited_defined_type", filename: "types.go", symbol: "E", typeName: "E"},
		{category: "seal_assignment", filename: "use.go", symbol: "dangerous", typeName: "D", field: "seal"},
		{category: "digest_assignment", filename: "use.go", symbol: "dangerous", typeName: "D", field: "digest"},
		{category: "capability_return", filename: "use.go", symbol: "dangerous", typeName: "E"},
	} {
		if !report.hasTypeName(want.category, want.filename, want.symbol, want.typeName) {
			t.Fatalf("missing defined-type violation %+v; got %+v", want, report.violations)
		}
	}
}

func TestArchitectureSealSingletonIdentity(t *testing.T) {
	t.Run("parenthesized singleton", func(t *testing.T) {
		report, err := checkSingleSynthetic(`package civildateplan
func dangerous(p *AuthorizedRepairPlan) { p.seal = (((authorizedPlanSeal))) }
`, modeSealAndCapability)
		if err != nil {
			t.Fatal(err)
		}
		if !report.has("seal_assignment", "synthetic.go", "dangerous", "seal") {
			t.Fatalf("parenthesized singleton not detected: %+v", report.violations)
		}
	})
	t.Run("local homonym", func(t *testing.T) {
		report, err := checkSingleSynthetic(`package civildateplan
func allowed(p *AuthorizedRepairPlan) {
	authorizedPlanSeal := (*authorizationSeal)(nil)
	p.seal = authorizedPlanSeal
}
`, modeSealAndCapability)
		if err != nil {
			t.Fatal(err)
		}
		if report.has("seal_assignment", "synthetic.go", "allowed", "seal") {
			t.Fatalf("local homonym must not be the singleton: %+v", report.violations)
		}
	})
	for name, src := range map[string]string{
		"parameter homonym": `package civildateplan
func allowed(p *AuthorizedRepairPlan, authorizedPlanSeal *authorizationSeal) {
	p.seal = authorizedPlanSeal
}
`,
		"range homonym": `package civildateplan
func allowed(p *AuthorizedRepairPlan, seals []*authorizationSeal) {
	for _, authorizedPlanSeal := range seals { p.seal = authorizedPlanSeal }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			report, err := checkSingleSynthetic(src, modeSealAndCapability)
			if err != nil {
				t.Fatal(err)
			}
			if report.has("seal_assignment", "synthetic.go", "allowed", "seal") {
				t.Fatalf("%s must not be the singleton: %+v", name, report.violations)
			}
		})
	}
}

func TestArchitectureGraphFunctionMethodCollision(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"package function", `package civildateplan
type graphCollisionT struct{}
func helperA() { computeAuthorizedPlanDigestV1(nil) }
func (graphCollisionT) helperA() {}
func root() { helperA() }
`},
		{"selected method among homonyms", `package civildateplan
type graphCollisionT struct{}
type graphCollisionU struct{}
func helperA() {}
func (graphCollisionT) helperA() { computeAuthorizedPlanDigestV1(nil) }
func (graphCollisionU) helperA() {}
func root() { graphCollisionT{}.helperA() }
`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := checkSingleSynthetic(tc.src, modeForbiddenProbeRoot)
			if err != nil {
				t.Fatal(err)
			}
			if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "computeAuthorizedPlanDigestV1") {
				t.Fatalf("referenced function object was lost to name collision: %+v", report.violations)
			}
		})
	}
}

// ========== V2C.10: method value / method expression ==========

func TestArchitectureMethodValueTransitive(t *testing.T) {
	src := `package civildateplan
type T struct{}
func (T) helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func root(t T) {
	f := t.helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("method value should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureMethodExpressionTransitive(t *testing.T) {
	src := `package civildateplan
type T struct{}
func (T) helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func root() {
	f := T.helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("method expression should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureMethodValueDistinctReceivers(t *testing.T) {
	src := `package civildateplan
type T1 struct{}
type T2 struct{}
func (T1) helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func (T2) helperA() {}
func root() {
	var t1 T1
	f := t1.helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("selected method object must create dependency. got: %+v", report.violations)
	}
}

func TestArchitectureMethodAndPackageFunctionDistinct(t *testing.T) {
	src := `package civildateplan
type T struct{}
func helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func (T) helperA() { computeAuthorizedPlanDigestV1(nil) }
func root() {
	f := T.helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("method expression must select method object, not package function. got: %+v", report.violations)
	}
	if report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("method expression must not select unrelated package function. got: %+v", report.violations)
	}
}

func TestArchitectureDirectDominatesTransitive(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func root() {
	encodeSeriesObject(0, authorizedSeriesState{})
	helperA()
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	vs := findViolations(report.violations, "forbidden_dependency", "synthetic.go", "root", "")
	encodeCount := 0
	for _, v := range vs {
		if v.typeName == "encodeSeriesObject" {
			encodeCount++
			if v.field != "direct" {
				t.Fatalf("direct must dominate transitive: got %q", v.field)
			}
		}
	}
	if encodeCount != 1 {
		t.Fatalf("expected exactly one encodeSeriesObject violation, got %d: %+v", encodeCount, vs)
	}
}

func TestArchitectureForbiddenObjectsDistinct(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func root() {
	computeAuthorizedPlanDigestV1(nil)
	helperA()
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	rootViolations := findViolations(report.violations, "forbidden_dependency", "synthetic.go", "root", "")
	if len(rootViolations) != 2 {
		t.Fatalf("root must have exactly two forbidden dependencies, got %d: %+v", len(rootViolations), rootViolations)
	}
	fields := map[string]string{}
	for _, v := range rootViolations {
		fields[v.typeName] = v.field
	}
	if fields["computeAuthorizedPlanDigestV1"] != "direct" {
		t.Fatalf("computeAuthorizedPlanDigestV1 must be direct, got %+v", rootViolations)
	}
	if fields["encodeSeriesObject"] != "transitive" {
		t.Fatalf("encodeSeriesObject must be transitive, got %+v", rootViolations)
	}
}

func TestArchitectureCrossFileGateDigestAssignment(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\nfunc dangerous(p B) {\np.digest = [32]byte{1}\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("digest_assignment", "use.go", "dangerous", "digest") {
		t.Fatal("digest_assignment in use.go not detected")
	}

	srcNoDigest := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\nfunc allowed(p B) {\n_ = p.digest\n}\n",
	}
	report2, err := checkSyntheticMulti(srcNoDigest, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if report2.has("digest_assignment", "use.go", "", "") {
		t.Fatal("digest_assignment should not appear when removed")
	}
}

func TestArchitectureCrossFileGateReturn(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\nfunc dangerous() B { return nil }\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("capability_return", "use.go", "dangerous", "") {
		t.Fatal("capability_return in use.go not detected")
	}

	srcNoReturn := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\ntype Safe struct {}\nfunc safe() Safe { return Safe{} }\n",
	}
	report2, err := checkSyntheticMulti(srcNoReturn, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if report2.has("capability_return", "use.go", "", "") {
		t.Fatal("capability_return should not appear for safe type")
	}
}

func TestArchitectureCrossFileGatePositionalComposite(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = A\n",
		"use.go":     "package civildateplan\nvar forged = B{\nauthorizedPlanSeal,\nnil,\n[32]byte{1},\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("positional_composite", "use.go", "", "seal") {
		t.Fatal("positional_composite seal in use.go not detected")
	}
	srcNoPos := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go":     "package civildateplan\ntype Safe struct { x int }\nvar s = Safe{x: 1}\n",
	}
	report2, err := checkSyntheticMulti(srcNoPos, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if report2.has("positional_composite", "use.go", "", "") {
		t.Fatal("positional_composite should not appear for safe type")
	}
}

func TestArchitectureCrossFileGateKeyedComposite(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype C = ((A))\n",
		"use.go":     "package civildateplan\nvar forged = C{\nseal: authorizedPlanSeal,\ndigest: [32]byte{1},\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("keyed_composite", "use.go", "", "seal") {
		t.Fatal("keyed_composite with seal in use.go not detected")
	}
	if !report.has("keyed_composite", "use.go", "", "digest") {
		t.Fatal("keyed_composite with digest in use.go not detected")
	}

	srcNoKeyed := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype C = ((A))\n",
		"use.go":     "package civildateplan\ntype Safe struct { x int }\nvar s = Safe{x: 1}\n",
	}
	report2, err := checkSyntheticMulti(srcNoKeyed, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if report2.has("keyed_composite", "use.go", "", "") {
		t.Fatal("keyed_composite should not appear for safe type")
	}
}

func TestArchitectureCrossFileStructuralReports(t *testing.T) {
	aliasCaps := []resolvedCapability{
		{name: "A", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_a.go"},
		{name: "B", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_b.go"},
	}
	aliasDecls := []architectureViolation{
		{category: "prohibited_alias", filename: "types_a.go", symbol: "A", typeName: "A"},
		{category: "prohibited_alias", filename: "types_b.go", symbol: "B", typeName: "B"},
	}
	tests := []struct {
		name string
		src  map[string]string
		want typedArchitectureReport
	}{
		{
			name: "seal",
			src: map[string]string{
				"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
				"types_b.go": "package civildateplan\ntype B = (*A)\n",
				"use.go":     "package civildateplan\nfunc dangerous(p B) { p.seal = authorizedPlanSeal }\n",
			},
			want: typedArchitectureReport{resolvedCapabilities: aliasCaps, violations: append(append([]architectureViolation{}, aliasDecls...),
				architectureViolation{category: "seal_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "seal"})},
		},
		{
			name: "digest",
			src: map[string]string{
				"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
				"types_b.go": "package civildateplan\ntype B = (*A)\n",
				"use.go":     "package civildateplan\nfunc dangerous(p B) { p.digest = [32]byte{1} }\n",
			},
			want: typedArchitectureReport{resolvedCapabilities: aliasCaps, violations: append(append([]architectureViolation{}, aliasDecls...),
				architectureViolation{category: "digest_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "digest"})},
		},
		{
			name: "return",
			src: map[string]string{
				"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
				"types_b.go": "package civildateplan\ntype B = (*A)\n",
				"use.go":     "package civildateplan\nfunc dangerous() B { return nil }\n",
			},
			want: typedArchitectureReport{resolvedCapabilities: aliasCaps, violations: append(append([]architectureViolation{}, aliasDecls...),
				architectureViolation{category: "capability_return", filename: "use.go", symbol: "dangerous", typeName: "B"})},
		},
		{
			name: "keyed",
			src: map[string]string{
				"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
				"types_b.go": "package civildateplan\ntype C = ((A))\n",
				"use.go":     "package civildateplan\nvar forged = C{seal: authorizedPlanSeal, digest: [32]byte{1}}\n",
			},
			want: typedArchitectureReport{
				resolvedCapabilities: []resolvedCapability{
					{name: "A", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_a.go"},
					{name: "C", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_b.go"},
				},
				violations: []architectureViolation{
					{category: "prohibited_alias", filename: "types_a.go", symbol: "A", typeName: "A"},
					{category: "prohibited_alias", filename: "types_b.go", symbol: "C", typeName: "C"},
					{category: "keyed_composite", filename: "use.go", symbol: "forged", typeName: "C", field: "seal"},
					{category: "keyed_composite", filename: "use.go", symbol: "forged", typeName: "C", field: "digest"},
				},
			},
		},
		{
			name: "positional",
			src: map[string]string{
				"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
				"types_b.go": "package civildateplan\ntype B = A\n",
				"use.go":     "package civildateplan\nvar forged = B{authorizedPlanSeal, nil, [32]byte{1}}\n",
			},
			want: typedArchitectureReport{
				resolvedCapabilities: aliasCaps,
				violations: append(append([]architectureViolation{}, aliasDecls...),
					architectureViolation{category: "positional_composite", filename: "use.go", symbol: "forged", typeName: "B", field: "seal"},
					architectureViolation{category: "positional_composite", filename: "use.go", symbol: "forged", typeName: "B", field: "digest"}),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkSyntheticMulti(tc.src, modeSyntheticCapabilities)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			want.violations = append(baseAuthorizedLoaderViolations(), want.violations...)
			assertArchitectureReportEqual(t, got, want)
		})
	}
}

// ========== V2C.10: complete cross-file matrix (positive / usage-removed / safe-type) ==========

func aliasCaps(names ...string) []resolvedCapability {
	var caps []resolvedCapability
	for _, n := range names {
		base := "AuthorizedRepairPlan"
		if n == "E" {
			base = "D"
		}
		caps = append(caps, resolvedCapability{name: n, baseName: base, isAlias: n == "A" || n == "B" || n == "C", filename: "types.go"})
	}
	return caps
}

func aliasDecls(names ...string) []architectureViolation {
	var decls []architectureViolation
	for _, n := range names {
		decls = append(decls, architectureViolation{category: "prohibited_alias", filename: "types.go", symbol: n, typeName: n})
	}
	return decls
}

func TestArchitectureCrossFileMatrix(t *testing.T) {
	type state struct {
		name string
		src  map[string]string
		want typedArchitectureReport
	}
	states := map[string][]state{
		"seal": {
			{
				name: "positive",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc dangerous(p B) { p.seal = authorizedPlanSeal }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations: append(aliasDecls("A", "B"),
						architectureViolation{category: "seal_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "seal"}),
				},
			},
			{
				name: "usage_removed",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc allowed(p B) { _ = p }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
			{
				name: "safe_type",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\ntype Safe struct { seal int }\n",
					"use.go":   "package civildateplan\nfunc allowed(p *Safe) { p.seal = 1 }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
		},
		"digest": {
			{
				name: "positive",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc dangerous(p B) { p.digest = [32]byte{1} }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations: append(aliasDecls("A", "B"),
						architectureViolation{category: "digest_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "digest"}),
				},
			},
			{
				name: "usage_removed",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc allowed(p B) { _ = p.digest }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
			{
				name: "safe_type",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\ntype Safe struct { digest [32]byte }\n",
					"use.go":   "package civildateplan\nfunc allowed(p *Safe) { p.digest = [32]byte{} }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
		},
		"return": {
			{
				name: "positive",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc dangerous() B { return nil }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations: append(aliasDecls("A", "B"),
						architectureViolation{category: "capability_return", filename: "use.go", symbol: "dangerous", typeName: "B"}),
				},
			},
			{
				name: "usage_removed",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\n",
					"use.go":   "package civildateplan\nfunc allowed() int { return 0 }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
			{
				name: "safe_type",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = (*A)\ntype Safe struct{}\n",
					"use.go":   "package civildateplan\nfunc allowed() Safe { return Safe{} }\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
		},
		"keyed": {
			{
				name: "positive",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype C = ((A))\n",
					"use.go":   "package civildateplan\nvar forged = C{seal: authorizedPlanSeal, digest: [32]byte{1}}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "C"),
					violations: append(aliasDecls("A", "C"),
						architectureViolation{category: "keyed_composite", filename: "use.go", symbol: "forged", typeName: "C", field: "seal"},
						architectureViolation{category: "keyed_composite", filename: "use.go", symbol: "forged", typeName: "C", field: "digest"}),
				},
			},
			{
				name: "usage_removed",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype C = ((A))\n",
					"use.go":   "package civildateplan\nvar forged = C{}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "C"),
					violations:           aliasDecls("A", "C"),
				},
			},
			{
				name: "safe_type",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype C = ((A))\ntype Safe struct { x int }\n",
					"use.go":   "package civildateplan\nvar s = Safe{x: 1}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "C"),
					violations:           aliasDecls("A", "C"),
				},
			},
		},
		"positional": {
			{
				name: "positive",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = A\n",
					"use.go":   "package civildateplan\nvar forged = B{authorizedPlanSeal, nil, [32]byte{1}}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations: append(aliasDecls("A", "B"),
						architectureViolation{category: "positional_composite", filename: "use.go", symbol: "forged", typeName: "B", field: "seal"},
						architectureViolation{category: "positional_composite", filename: "use.go", symbol: "forged", typeName: "B", field: "digest"}),
				},
			},
			{
				name: "usage_removed",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = A\n",
					"use.go":   "package civildateplan\nvar forged = B{}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
			{
				name: "safe_type",
				src: map[string]string{
					"types.go": "package civildateplan\ntype A = AuthorizedRepairPlan\ntype B = A\ntype Safe struct { x int }\n",
					"use.go":   "package civildateplan\nvar s = Safe{1}\n",
				},
				want: typedArchitectureReport{
					resolvedCapabilities: aliasCaps("A", "B"),
					violations:           aliasDecls("A", "B"),
				},
			},
		},
	}
	for gate, cases := range states {
		for _, tc := range cases {
			t.Run(gate+"/"+tc.name, func(t *testing.T) {
				got, err := checkSyntheticMulti(tc.src, modeSyntheticCapabilities)
				if err != nil {
					t.Fatal(err)
				}
				want := tc.want
				want.violations = append(baseAuthorizedLoaderViolations(), want.violations...)
				assertArchitectureReportEqual(t, got, want)
			})
		}
	}
}

// ========== V2C.7: receiver exact test ==========

func TestArchitectureSemanticReceiverExact(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go": "package civildateplan\ntype Unrelated struct {\nseal int\ndigest [32]byte\n}\n" +
			"func dangerous(p B, u *Unrelated) {\np.seal = authorizedPlanSeal\np.digest = [32]byte{1}\nu.seal = 1\nu.digest = [32]byte{2}\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("seal_assignment", "use.go", "dangerous", "seal") {
		t.Fatal("p.seal should be detected for B receiver")
	}
	if !report.has("digest_assignment", "use.go", "dangerous", "digest") {
		t.Fatal("p.digest should be detected for B receiver")
	}
	// u.seal and u.digest on *Unrelated must NOT be detected
	uSeal := findViolations(report.violations, "seal_assignment", "use.go", "dangerous", "seal")
	if len(uSeal) != 1 {
		t.Fatalf("should have exactly 1 seal_assignment for p, got %d: %+v", len(uSeal), uSeal)
	}
	uDigest := findViolations(report.violations, "digest_assignment", "use.go", "dangerous", "digest")
	if len(uDigest) != 1 {
		t.Fatalf("should have exactly 1 digest_assignment for p, got %d: %+v", len(uDigest), uDigest)
	}
}

// ========== V2C.7: cross-file unrelated type test ==========

func TestArchitectureCrossFileUnrelatedTypeNoViolation(t *testing.T) {
	src := map[string]string{
		"types.go": "package civildateplan\ntype Unrelated struct {\nseal int\ndigest [32]byte\n}\n",
		"use.go":   "package civildateplan\nfunc allowed(p *Unrelated) {\np.seal = 1\np.digest = [32]byte{}\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range report.violations {
		if v.filename == "types.go" || v.filename == "use.go" {
			t.Fatalf("unrelated type should not generate violations: %+v", report.violations)
		}
	}
}

// ========== V2C.7: order independence ==========

const orderSeedConst int64 = 42

func TestArchitectureCrossFileOrderIndependence(t *testing.T) {
	base := []typedSource{
		{name: "types_a.go", src: "package civildateplan\ntype A = AuthorizedRepairPlan\n"},
		{name: "types_b.go", src: "package civildateplan\ntype B = (*A)\n"},
		{name: "use.go", src: "package civildateplan\nfunc dangerous() B { return nil }\n"},
	}
	rng := rand.New(rand.NewSource(orderSeedConst))
	permutations := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	baseline, err := checkSyntheticMultiOrdered(base, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, perm := range permutations {
		var ordered []typedSource
		for _, idx := range perm {
			ordered = append(ordered, base[idx])
		}
		report, err := checkSyntheticMultiOrdered(ordered, modeSyntheticCapabilities)
		if err != nil {
			t.Fatalf("order %v: %v", perm, err)
		}
		assertArchitectureReportEqual(t, report, baseline)
	}

	for i := 0; i < 20; i++ {
		shuffled := make([]typedSource, len(base))
		copy(shuffled, base)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		report, err := checkSyntheticMultiOrdered(shuffled, modeSyntheticCapabilities)
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		assertArchitectureReportEqual(t, report, baseline)
	}
	mutated := baseline
	mutated.violations = append([]architectureViolation(nil), baseline.violations...)
	mutated.violations[0].field = "deliberately-wrong"
	if reflect.DeepEqual(normalizeArchitectureReport(mutated), baseline) {
		t.Fatal("integral comparison must reject changed metadata")
	}
}

// ========== V2C.7: shadowing matrix (semantic) ==========

func TestArchitectureSemanticShadowingMatrix(t *testing.T) {
	// A: local variable shadow
	srcA := `package civildateplan
func probe() {
	encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
	encodeAuthorizedPlanStateV1(nil)
}
`
	report, err := checkSingleSynthetic(srcA, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("shadowed local variable should not be flagged")
	}

	// B: parameter shadow
	srcB := `package civildateplan
func probe(encodeAuthorizedPlanStateV1 func(interface{}) ([]byte, error)) {
	encodeAuthorizedPlanStateV1(nil)
}
`
	report, err = checkSingleSynthetic(srcB, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("shadowed parameter should not be flagged")
	}

	// L: real reference outside shadow block
	srcL := `package civildateplan
func probe() {
	if true {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
	encodeAuthorizedPlanStateV1(nil)
}
`
	report, err = checkSingleSynthetic(srcL, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("real reference after shadow block should be flagged")
	}

	// K: closure local shadow
	srcK := `package civildateplan
func probe() {
	f := func() {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
	_ = f
}
`
	report, err = checkSingleSynthetic(srcK, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("shadowed closure variable should not be flagged")
	}
}

// ========== V2C.7: forbidden reference probes A–L ==========

func TestArchitectureSemanticForbiddenRefDirect(t *testing.T) {
	src := `package civildateplan
func probe() { encodeAuthorizedPlanStateV1(nil) }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("direct call should be detected")
	}
}

func TestArchitectureSemanticForbiddenRefFunctionValue(t *testing.T) {
	src := `package civildateplan
func probe() {
	f := encodeAuthorizedPlanStateV1
	_, _ = f(nil)
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "direct") {
		t.Fatalf("function value should be detected. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefArgument(t *testing.T) {
	src := `package civildateplan
func use(fn func(*authorizedPlanState) ([]byte, error)) {}
func probe() { use(encodeAuthorizedPlanStateV1) }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatalf("function as argument should be detected. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefReturn(t *testing.T) {
	src := `package civildateplan
func probe() func(*authorizedPlanState) ([]byte, error) { return encodeAuthorizedPlanStateV1 }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatalf("function as return value should be detected. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefClosure(t *testing.T) {
	src := `package civildateplan
func probe() {
	f := func() { encodeAuthorizedPlanStateV1(nil) }
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatalf("closure should be detected. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefJSONMarshal(t *testing.T) {
	src := `package civildateplan
import "encoding/json"
func probe() { json.Marshal(nil) }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatalf("json.Marshal should be detected. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefJSONAlias(t *testing.T) {
	src := `package civildateplan
import stdjson "encoding/json"
func probe() { stdjson.Marshal(nil) }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatalf("stdjson.Marshal should be detected by import path. got: %+v", report.violations)
	}
}

func TestArchitectureSemanticForbiddenRefEncodeRuleObject(t *testing.T) {
	src := `package civildateplan
func probe() { _ = encodeRuleObject }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("encodeRuleObject reference should be detected")
	}
}

func TestArchitectureSemanticForbiddenRefEncodeSeriesObject(t *testing.T) {
	src := `package civildateplan
func probe() { _ = encodeSeriesObject }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("encodeSeriesObject reference should be detected")
	}
}

func TestArchitectureSemanticForbiddenRefTextNotDetected(t *testing.T) {
	src := `package civildateplan
func probe() {
	_ = "encodeAuthorizedPlanStateV1 is a function"
	// encodeAuthorizedPlanStateV1
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("text in string/comment should not be flagged")
	}
}

func TestArchitectureSemanticForbiddenRefSafeChainNoDetect(t *testing.T) {
	src := `package civildateplan
func helperA() { helperB() }
func helperB() {}
func probe() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "probe", "") {
		t.Fatal("safe chain should not be flagged")
	}
}

func TestArchitectureSemanticForbiddenRefCycleSafeNoHang(t *testing.T) {
	src := `package civildateplan
func helperA() { helperB() }
func helperB() { helperA() }
func probe() { helperA() }
`
	_, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
}

// ========== V2C.7: frozen golden gates ==========

func isStaticStringLiteral(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return false
		}
		return isStaticStringLiteral(e.X) && isStaticStringLiteral(e.Y)
	case *ast.ParenExpr:
		return isStaticStringLiteral(e.X)
	default:
		return false
	}
}

func parseDigestTestFile(t *testing.T) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "digest_v1_test.go", nil, 0)
	if err != nil {
		fset := token.NewFileSet()
		src, readErr := os.ReadFile("digest_v1_test.go")
		if readErr != nil {
			t.Fatalf("cannot read digest_v1_test.go: %v", readErr)
		}
		var parseErr error
		file, parseErr = parser.ParseFile(fset, "digest_v1_test.go", src, 0)
		if parseErr != nil {
			t.Fatalf("parse digest_v1_test.go: %v", parseErr)
		}
	}
	return file
}

func TestFrozenGoldenConstIsStaticLiteral(t *testing.T) {
	file := parseDigestTestFile(t)
	found := false
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == "completeGoldenEncodedHex" {
					found = true
					if len(vs.Values) != 1 {
						t.Fatal("completeGoldenEncodedHex must have exactly one value")
					}
					if !isStaticStringLiteral(vs.Values[0]) {
						t.Fatal("completeGoldenEncodedHex must be a static string literal")
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("completeGoldenEncodedHex const not found")
	}
}

func TestFrozenGoldenHelperCallGraph(t *testing.T) {
	result, err := checkGoldenFrozen(loadTestFiles)
	if err != nil {
		t.Fatalf("golden check failed: %v", err)
	}
	want := goldenGateResult{report: expectedRealGoldenReport(), evidence: goldenGateEvidence{
		checked:                 true,
		analyzedFiles:           currentTestAnalyzedFiles(),
		frozenRoot:              "frozenCompleteGoldenBytes",
		corruptionRoot:          "TestIndependentDecoderRejectsCorruption",
		frozenReachesHexDecode:  true,
		corruptionReachesFrozen: true,
	}}
	assertGoldenGateResultEqual(t, result, want)
	for _, v := range result.report.violations {
		if v.symbol == "frozenCompleteGoldenBytes" && v.category == "forbidden_dependency" {
			t.Fatalf("frozenCompleteGoldenBytes must not reach forbidden: %+v", v)
		}
	}
}

func TestCorruptionTestUsesFrozenBytes(t *testing.T) {
	result, err := checkGoldenFrozen(loadTestFiles)
	if err != nil {
		t.Fatalf("golden check failed: %v", err)
	}
	want := goldenGateResult{report: expectedRealGoldenReport(), evidence: goldenGateEvidence{
		checked:                 true,
		analyzedFiles:           currentTestAnalyzedFiles(),
		frozenRoot:              "frozenCompleteGoldenBytes",
		corruptionRoot:          "TestIndependentDecoderRejectsCorruption",
		frozenReachesHexDecode:  true,
		corruptionReachesFrozen: true,
	}}
	assertGoldenGateResultEqual(t, result, want)
	if !result.evidence.checked || result.evidence.corruptionRoot != "TestIndependentDecoderRejectsCorruption" || !result.evidence.corruptionReachesFrozen {
		t.Fatalf("corruption evidence incomplete: %+v", result.evidence)
	}
	for _, v := range result.report.violations {
		if v.symbol == "TestIndependentDecoderRejectsCorruption" && v.category == "forbidden_dependency" {
			t.Fatalf("TestIndependentDecoderRejectsCorruption must not reach forbidden: %+v", v)
		}
	}
}

func TestArchitectureGoldenRootPackageLevelIdentity(t *testing.T) {
	// Method homonyms must not satisfy the golden root; package-level absence is fail-closed.
	src := `package civildateplan
import "testing"
type T struct{}
func (T) frozenCompleteGoldenBytes() {}
func (T) TestIndependentDecoderRejectsCorruption(t *testing.T) {}
`
	prodFiles, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	_, err = checkTypedArchitecture(append(prodFiles, typedSource{name: "homonym.go", src: src}), modeGoldenFrozenRoot)
	if err == nil {
		t.Fatal("golden root resolution must fail when only method homonyms exist")
	}
}

func TestArchitectureGoldenRootMethodHomonymDoesNotHide(t *testing.T) {
	// A method homonym must not hide the real package-level golden root.
	// The package-level root is safe; the method homonym would be forbidden if selected.
	src := `package civildateplan
import "testing"
type T struct{}
func (T) frozenCompleteGoldenBytes() { encodeAuthorizedPlanStateV1(nil) }
func frozenCompleteGoldenBytes() {}
func TestIndependentDecoderRejectsCorruption(t *testing.T) { frozenCompleteGoldenBytes() }
`
	prodFiles, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	report, err := checkTypedArchitecture(append(prodFiles, typedSource{name: "homonym.go", src: src}), modeGoldenFrozenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.hasTypeName("forbidden_dependency", "homonym.go", "frozenCompleteGoldenBytes", "encodeAuthorizedPlanStateV1") {
		t.Fatal("method homonym must not be selected as golden root")
	}
}

func TestArchitectureCorruptionReachabilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src       string
		forbidden string
	}{
		{"helper compute", `package civildateplan
func helper() { computeAuthorizedPlanDigestV1(nil) }
func TestIndependentDecoderRejectsCorruption() { helper() }
`, "computeAuthorizedPlanDigestV1"},
		{"helper fixture", `package civildateplan
func fullGoldenState() {}
func helper() { fullGoldenState() }
func TestIndependentDecoderRejectsCorruption() { helper() }
`, "fullGoldenState"},
		{"function value fixture", `package civildateplan
func fullGoldenState() {}
func helper() { fullGoldenState() }
func TestIndependentDecoderRejectsCorruption() { f := helper; _ = f }
`, "fullGoldenState"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := checkSingleSynthetic(tc.src, modeForbiddenProbeRoot)
			if err != nil {
				t.Fatal(err)
			}
			if !report.hasTypeName("forbidden_dependency", "synthetic.go", "TestIndependentDecoderRejectsCorruption", tc.forbidden) {
				t.Fatalf("missing corruption prohibition %s: %+v", tc.forbidden, report.violations)
			}
		})
	}

	assertFrozen := func(t *testing.T, src string, want bool) {
		t.Helper()
		sources := []typedSource{{name: "corruption.go", src: src}}
		graph, pkg, err := semanticGraph(sources)
		if err != nil {
			t.Fatal(err)
		}
		root, err := packageFunctionByName(graph, pkg, "TestIndependentDecoderRejectsCorruption")
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := packageFunctionByName(graph, pkg, "frozenCompleteGoldenBytes")
		if err != nil {
			t.Fatal(err)
		}
		if got := reachesFunction(graph, root, frozen); got != want {
			t.Fatalf("frozen reachability=%t, want %t", got, want)
		}
	}
	t.Run("frozen required", func(t *testing.T) {
		assertFrozen(t, `package civildateplan
func frozenCompleteGoldenBytes() {}
func TestIndependentDecoderRejectsCorruption() { frozenCompleteGoldenBytes() }
`, true)
	})
	t.Run("missing frozen rejected", func(t *testing.T) {
		assertFrozen(t, `package civildateplan
func frozenCompleteGoldenBytes() {}
func other() {}
func TestIndependentDecoderRejectsCorruption() { other() }
`, false)
	})
	t.Run("safe cycle reaches frozen", func(t *testing.T) {
		assertFrozen(t, `package civildateplan
func frozenCompleteGoldenBytes() {}
func helperA() { helperB() }
func helperB() { helperA(); frozenCompleteGoldenBytes() }
func TestIndependentDecoderRejectsCorruption() { helperA() }
`, true)
	})
}

// ========== V2C.7: fail-closed tests ==========

func TestArchitectureSemanticFailClosedParseError(t *testing.T) {
	src := `package civildateplan
func broken(`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err == nil {
		t.Fatal("parse error should cause failure")
	}
	assertZeroArchitectureReport(t, report)
}

func TestArchitectureSemanticFailClosedTypeError(t *testing.T) {
	src := `package civildateplan
func probe() { return nil }
`
	report, err := checkSingleSynthetic(src, modeSealAndCapability)
	if err == nil {
		t.Fatal("type error should cause failure")
	}
	assertZeroArchitectureReport(t, report)
}

func assertZeroArchitectureReport(t *testing.T, report typedArchitectureReport) {
	t.Helper()
	if len(report.violations) != 0 || len(report.resolvedCapabilities) != 0 {
		t.Fatalf("fail-closed path returned partial report: %+v", report)
	}
}

func TestArchitectureFailClosedComplete(t *testing.T) {
	tests := []struct {
		name    string
		sources []typedSource
	}{
		{"parser", []typedSource{{name: "broken.go", src: "package civildateplan\nfunc broken("}}},
		{"type", []typedSource{{name: "broken.go", src: "package civildateplan\nfunc broken() { return nil }"}}},
		{"impossible import", []typedSource{{name: "broken.go", src: "package civildateplan\nimport _ \"example.invalid/nonexistent\""}}},
		{"missing file", []typedSource{{name: "/tmp/c1-i2-v2c9-file-that-does-not-exist.go"}}},
		{"partial package", []typedSource{{name: "partial.go", src: "package civildateplan\nvar _ = deliberatelyMissingSymbol"}}},
		{"different package", []typedSource{
			{name: "a.go", src: "package civildateplan\nvar A int"},
			{name: "b.go", src: "package different\nvar B int"},
		}},
		{"production-shaped parse error", []typedSource{{name: "authorized.go", src: "package civildateplan\nfunc broken("}}},
		{"one of multiple sources", []typedSource{
			{name: "ok.go", src: "package civildateplan\nvar OK int"},
			{name: "bad.go", src: "package civildateplan\nvar _ = missingFromSecondSource"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := checkTypedArchitecture(tc.sources, modeSealAndCapability)
			if err == nil {
				t.Fatal("invalid input must fail closed")
			}
			assertZeroArchitectureReport(t, report)
		})
	}
}

func TestArchitectureProductionWrapperFailClosed(t *testing.T) {
	loadErr := func() ([]typedSource, error) { return nil, fmt.Errorf("injected loader error") }
	partial := controlledProductionLoader(typedSource{name: "partial.go", src: "package civildateplan\nvar _ = missingWrapperSymbol"})
	for name, loader := range map[string]typedSourceLoader{"nil loader": nil, "loader error": loadErr, "partial package": partial} {
		t.Run(name, func(t *testing.T) {
			result, err := checkProduction(loader)
			if err == nil {
				t.Fatal("production wrapper must fail closed")
			}
			assertProductionGateResultEqual(t, result, productionGateResult{})
		})
	}
}

func TestArchitectureGoldenWrapperFailClosed(t *testing.T) {
	loadErr := func() ([]typedSource, error) { return nil, fmt.Errorf("injected loader error") }
	rootSource := func(frozenBody, corruptionBody string) typedSourceLoader {
		return controlledProductionLoader(typedSource{name: "roots.go", src: `package civildateplan
import "encoding/hex"
import "testing"
func frozenCompleteGoldenBytes() { ` + frozenBody + ` }
func TestIndependentDecoderRejectsCorruption(t *testing.T) { ` + corruptionBody + ` }
`})
	}
	tests := map[string]typedSourceLoader{
		"nil loader":              nil,
		"loader error":            loadErr,
		"missing roots":           loadProductionFiles,
		"decode disconnected":     rootSource("", "frozenCompleteGoldenBytes()"),
		"corruption disconnected": rootSource(`_, _ = hex.DecodeString("")`, ""),
	}
	for name, loader := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := checkGoldenFrozen(loader)
			if err == nil {
				t.Fatal("golden wrapper must fail closed")
			}
			assertGoldenGateResultEqual(t, result, goldenGateResult{})
		})
	}
}

// ========== V2C.10: combined fixture (single, complete) ==========

func combinedFixtureSources(useRealSingleton, realRefAfterBlock, transitiveHelper bool) map[string]string {
	sealRHS := "(((authorizedPlanSeal)))"
	if !useRealSingleton {
		sealRHS = "authorizedPlanSeal"
	}
	realRef := ""
	if realRefAfterBlock {
		realRef = "\n\tencodeAuthorizedPlanStateV1(nil)"
	}
	helper := ""
	if transitiveHelper {
		helper = "\nfunc helperA() { encodeAuthorizedPlanStateV1(nil) }\n"
	}
	helperUse := ""
	if transitiveHelper {
		helperUse = "\nf := helperA\n_ = f\n"
	}
	return map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\ntype D AuthorizedRepairPlan\ntype E D\n",
		"use.go": "package civildateplan\n" +
			"import stdjson \"encoding/json\"\n" +
			"type Unrelated struct {\nseal int\ndigest [32]byte\n}\n" +
			helper +
			"func dangerous(p B, u *Unrelated) E {\n" +
			"p.seal = " + sealRHS + "\n" +
			"p.digest = [32]byte{1}\n" +
			"u.seal = 1\n" +
			"u.digest = [32]byte{2}\n" +
			"authorizedPlanSeal := &authorizationSeal{}\n" +
			"_ = authorizedPlanSeal\n" +
			"if true {\n" +
			"encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }\n" +
			"encodeAuthorizedPlanStateV1(nil)\n" +
			"}\n" +
			realRef +
			helperUse +
			"\nstdjson.Marshal(nil)\n" +
			"return E{}\n" +
			"}\n",
		"keyed.go":      "package civildateplan\nvar forgedKeyed = E{seal: authorizedPlanSeal, digest: [32]byte{1}}\n",
		"positional.go": "package civildateplan\nvar forgedPositional = E{authorizedPlanSeal, nil, [32]byte{1}}\n",
	}
}

func TestArchitectureCombinedFixtureComplete(t *testing.T) {
	src := combinedFixtureSources(true, true, true)
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	want := typedArchitectureReport{
		resolvedCapabilities: []resolvedCapability{
			{name: "A", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_a.go"},
			{name: "B", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_b.go"},
			{name: "D", baseName: "AuthorizedRepairPlan", isAlias: false, filename: "types_b.go"},
			{name: "E", baseName: "D", isAlias: false, filename: "types_b.go"},
		},
		violations: []architectureViolation{
			{category: "prohibited_alias", filename: "types_a.go", symbol: "A", typeName: "A"},
			{category: "prohibited_alias", filename: "types_b.go", symbol: "B", typeName: "B"},
			{category: "prohibited_defined_type", filename: "types_b.go", symbol: "D", typeName: "D"},
			{category: "prohibited_defined_type", filename: "types_b.go", symbol: "E", typeName: "E"},
			{category: "seal_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "seal"},
			{category: "digest_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "digest"},
			{category: "capability_return", filename: "use.go", symbol: "dangerous", typeName: "E"},
			{category: "keyed_composite", filename: "keyed.go", symbol: "forgedKeyed", typeName: "E", field: "seal"},
			{category: "keyed_composite", filename: "keyed.go", symbol: "forgedKeyed", typeName: "E", field: "digest"},
			{category: "positional_composite", filename: "positional.go", symbol: "forgedPositional", typeName: "E", field: "seal"},
			{category: "positional_composite", filename: "positional.go", symbol: "forgedPositional", typeName: "E", field: "digest"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "helperA", typeName: "encodeAuthorizedPlanStateV1", field: "direct"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "dangerous", typeName: "encodeAuthorizedPlanStateV1", field: "direct"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "dangerous", typeName: "encoding/json.Marshal", field: "direct"},
		},
	}
	want.violations = append(baseAuthorizedLoaderViolations(), want.violations...)
	assertArchitectureReportEqual(t, report, want)
}

func TestArchitectureCombinedFixtureMutations(t *testing.T) {
	// Local singleton declared before use instead of the real one: no seal_assignment.
	t.Run("local_singleton_no_seal", func(t *testing.T) {
		src := map[string]string{
			"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
			"types_b.go": "package civildateplan\ntype B = (*A)\n",
			"use.go": `package civildateplan
func dangerous(p B) {
	authorizedPlanSeal := &authorizationSeal{}
	p.seal = authorizedPlanSeal
}
`,
		}
		report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
		if err != nil {
			t.Fatal(err)
		}
		if report.has("seal_assignment", "use.go", "dangerous", "seal") {
			t.Fatal("local singleton must not produce seal_assignment")
		}
	})

	// Shadowed reference inside block only: no forbidden_dependency on encoder.
	t.Run("shadowed_ref_only", func(t *testing.T) {
		src := map[string]string{
			"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
			"types_b.go": "package civildateplan\ntype B = (*A)\n",
			"use.go": `package civildateplan
func dangerous(p B) {
	if true {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
}
`,
		}
		report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
		if err != nil {
			t.Fatal(err)
		}
		if report.hasTypeName("forbidden_dependency", "use.go", "dangerous", "encodeAuthorizedPlanStateV1") {
			t.Fatal("shadowed reference must not reach real encoder")
		}
	})

	// Swap capability types for safe types: no capability violations.
	t.Run("safe_types", func(t *testing.T) {
		src := map[string]string{
			"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
			"types_b.go": "package civildateplan\ntype B = (*A)\ntype Safe struct{ x int }\n",
			"use.go": `package civildateplan
func dangerous(p *Safe) {
	p.x = 1
}
`,
		}
		report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
		if err != nil {
			t.Fatal(err)
		}
		if report.has("seal_assignment", "use.go", "dangerous", "seal") ||
			report.has("digest_assignment", "use.go", "dangerous", "digest") ||
			report.has("capability_return", "use.go", "dangerous", "") {
			t.Fatalf("safe types must not produce capability violations: %+v", report.violations)
		}
	})
}

// ========== V2C.8: transitive function dependency graph ==========

func TestArchitectureTransitiveOneLevel(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeAuthorizedPlanStateV1(nil) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeAuthorizedPlanStateV1") {
		t.Fatalf("one-level transitive should be detected. got: %+v", report.violations)
	}
	if report.countViolations("forbidden_dependency", "synthetic.go", "root", "transitive") == 0 {
		t.Fatalf("expected transitive classification. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveTwoLevels(t *testing.T) {
	src := `package civildateplan
func helperB() { computeAuthorizedPlanDigestV1(nil) }
func helperA() { helperB() }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("two-level transitive should be detected. got: %+v", report.violations)
	}
	if report.countViolations("forbidden_dependency", "synthetic.go", "root", "transitive") == 0 {
		t.Fatalf("expected transitive classification. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveFunctionValueUnused(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeRuleObject(0, authorizedRuleState{}) }
func root() {
	f := helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeRuleObject") {
		t.Fatalf("function value assignment should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveFunctionValueCalled(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeRuleObject(0, authorizedRuleState{}) }
func root() {
	f := helperA
	f()
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeRuleObject") {
		t.Fatalf("function value call should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveReturnValue(t *testing.T) {
	src := `package civildateplan
func helperA() { computeAuthorizedPlanDigestV1(nil) }
func root() func() { return helperA }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("returned helper should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveSliceElement(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeTransactionObject(0, authorizedTransactionState{}) }
func root() {
	callbacks := []any{helperA}
	_ = callbacks
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeTransactionObject") {
		t.Fatalf("slice element function value should create transitive edge. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveSafeCycleNoHang(t *testing.T) {
	src := `package civildateplan
func helperA() { helperB() }
func helperB() { helperA() }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "root", "") {
		t.Fatalf("safe cycle should not generate forbidden_dependency. got: %+v", report.violations)
	}
}

func TestArchitectureTransitiveCycleWithForbiddenExit(t *testing.T) {
	src := `package civildateplan
func helperA() { helperB() }
func helperB() {
	helperA()
	encodeSeriesObject(0, authorizedSeriesState{})
}
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("cycle with forbidden exit should be transitive. got: %+v", report.violations)
	}
}

// ========== V2C.8: contract probes A-L ==========

func TestArchitectureProbeA_RootViaHelperToEncoder(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeAuthorizedPlanStateV1(nil) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeAuthorizedPlanStateV1") {
		t.Fatalf("probe A failed. got: %+v", report.violations)
	}
	if report.countViolations("forbidden_dependency", "synthetic.go", "root", "transitive") == 0 {
		t.Fatalf("probe A must be transitive. got: %+v", report.violations)
	}
}

func TestArchitectureProbeB_RootViaClosureToCompute(t *testing.T) {
	src := `package civildateplan
func root() {
	f := func() { computeAuthorizedPlanDigestV1(nil) }
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("probe B failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeC_RootViaTwoHelpersToFixture(t *testing.T) {
	src := `package civildateplan
func fullGoldenState() {}
func helperB() { fullGoldenState() }
func helperA() { helperB() }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	vs := findViolations(report.violations, "forbidden_dependency", "synthetic.go", "root", "")
	if len(vs) != 1 {
		t.Fatalf("probe C expects exactly one forbidden_dependency, got %d: %+v", len(vs), vs)
	}
	v := vs[0]
	if v.typeName != "fullGoldenState" {
		t.Fatalf("probe C typeName=%q, want fullGoldenState", v.typeName)
	}
	if v.field != "transitive" {
		t.Fatalf("probe C field=%q, want transitive", v.field)
	}
}

func TestArchitectureProbeD_SafeCycleAllowed(t *testing.T) {
	src := `package civildateplan
func helperA() { helperB() }
func helperB() { helperA() }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "root", "") {
		t.Fatalf("probe D failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeE_TextNotDetected(t *testing.T) {
	src := `package civildateplan
func root() {
	_ = "encodeAuthorizedPlanStateV1 is a function"
	// encodeAuthorizedPlanStateV1
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "root", "") {
		t.Fatalf("probe E failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeF_HelperToTransactionEncoder(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeTransactionObject(0, authorizedTransactionState{}) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeTransactionObject") {
		t.Fatalf("probe F failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeG_HelperToRuleEncoder(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeRuleObject(0, authorizedRuleState{}) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeRuleObject") {
		t.Fatalf("probe G failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeH_HelperToSeriesEncoder(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeSeriesObject(0, authorizedSeriesState{}) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeSeriesObject") {
		t.Fatalf("probe H failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeI_StdjsonMarshalRejected(t *testing.T) {
	src := `package civildateplan
import stdjson "encoding/json"
func root() { stdjson.Marshal(nil) }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encoding/json.Marshal") {
		t.Fatalf("probe I failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeJ_StdjsonUnmarshalRejected(t *testing.T) {
	src := `package civildateplan
import stdjson "encoding/json"
func root() {
	var v any
	stdjson.Unmarshal([]byte{}, &v)
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encoding/json.Unmarshal") {
		t.Fatalf("probe J failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeK_FunctionValueTransitive(t *testing.T) {
	src := `package civildateplan
func helperA() { encodeAuthorizedPlanStateV1(nil) }
func root() {
	f := helperA
	_ = f
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeAuthorizedPlanStateV1") {
		t.Fatalf("probe K failed. got: %+v", report.violations)
	}
}

func TestArchitectureProbeL_ShadowingNoRealReference(t *testing.T) {
	src := `package civildateplan
func root() {
	encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
	encodeAuthorizedPlanStateV1(nil)
}
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "root", "") {
		t.Fatalf("probe L failed. got: %+v", report.violations)
	}
}

// ========== V2C.8: complete shadowing matrix A-L ==========

func runShadowProbe(t *testing.T, name, src string, expectForbidden bool) {
	t.Helper()
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	found := report.has("forbidden_dependency", "synthetic.go", "probe", "")
	if expectForbidden && !found {
		t.Fatalf("%s: real reference should be flagged. got: %+v", name, report.violations)
	}
	if !expectForbidden && found {
		t.Fatalf("%s: shadowed reference should NOT be flagged. got: %+v", name, report.violations)
	}
}

func TestArchitectureShadowA_LocalVariable(t *testing.T) {
	runShadowProbe(t, "A", `package civildateplan
func probe() {
	encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
	encodeAuthorizedPlanStateV1(nil)
}
`, false)
}

func TestArchitectureShadowB_Parameter(t *testing.T) {
	runShadowProbe(t, "B", `package civildateplan
func probe(encodeAuthorizedPlanStateV1 func(interface{}) ([]byte, error)) {
	encodeAuthorizedPlanStateV1(nil)
}
`, false)
}

func TestArchitectureShadowC_Receiver(t *testing.T) {
	runShadowProbe(t, "C", `package civildateplan
type T struct{ encodeAuthorizedPlanStateV1 func(interface{}) ([]byte, error) }
func (t T) probe() {
	t.encodeAuthorizedPlanStateV1(nil)
}
`, false)
}

func TestArchitectureShadowD_RangeVariable(t *testing.T) {
	runShadowProbe(t, "D", `package civildateplan
func probe() {
	items := []func(interface{}) ([]byte, error){func(x interface{}) ([]byte, error) { return nil, nil }}
	for _, encodeAuthorizedPlanStateV1 := range items {
		encodeAuthorizedPlanStateV1(nil)
	}
}
`, false)
}

func TestArchitectureShadowE_TypeSwitchVariable(t *testing.T) {
	runShadowProbe(t, "E", `package civildateplan
func probe(v any) {
	switch encodeAuthorizedPlanStateV1 := v.(type) {
	case func(interface{}) ([]byte, error):
		encodeAuthorizedPlanStateV1(nil)
	}
}
`, false)
}

func TestArchitectureShadowF_ShortReceiveSelect(t *testing.T) {
	runShadowProbe(t, "F", `package civildateplan
func probe(ch chan func(interface{}) ([]byte, error)) {
	select {
	case encodeAuthorizedPlanStateV1 := <-ch:
		encodeAuthorizedPlanStateV1(nil)
	}
}
`, false)
}

func TestArchitectureShadowG_ShortDeclMixed(t *testing.T) {
	runShadowProbe(t, "G", `package civildateplan
func probe() {
	var other int
	encodeAuthorizedPlanStateV1, other := func(x interface{}) ([]byte, error) { return nil, nil }, 1
	_ = other
	encodeAuthorizedPlanStateV1(nil)
}
`, false)
}

func TestArchitectureShadowH_InnerBlock(t *testing.T) {
	runShadowProbe(t, "H", `package civildateplan
func probe() {
	if true {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
}
`, false)
}

func TestArchitectureShadowI_RealReferenceAfterBlock(t *testing.T) {
	runShadowProbe(t, "I", `package civildateplan
func probe() {
	if true {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
	encodeAuthorizedPlanStateV1(nil)
}
`, true)
}

func TestArchitectureShadowJ_LocalFunction(t *testing.T) {
	runShadowProbe(t, "J", `package civildateplan
func probe() {
	encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
	encodeAuthorizedPlanStateV1(nil)
}
`, false)
}

func TestArchitectureShadowK_ClosureLocal(t *testing.T) {
	runShadowProbe(t, "K", `package civildateplan
func probe() {
	f := func() {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
	_ = f
}
`, false)
}

func TestArchitectureShadowL_RealReferenceOutsideScope(t *testing.T) {
	runShadowProbe(t, "L", `package civildateplan
func probe() {
	if true {
		encodeAuthorizedPlanStateV1 := func(x interface{}) ([]byte, error) { return nil, nil }
		encodeAuthorizedPlanStateV1(nil)
	}
	encodeAuthorizedPlanStateV1(nil)
}
`, true)
}

// ========== V2C.8: encoding/json negative controls ==========

func runForbiddenProbe(t *testing.T, name, src, wantTypeName string) {
	t.Helper()
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", wantTypeName) {
		t.Fatalf("%s: expected %s. got: %+v", name, wantTypeName, report.violations)
	}
}

func runAllowedProbe(t *testing.T, name, src string) {
	t.Helper()
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if report.has("forbidden_dependency", "synthetic.go", "root", "") {
		t.Fatalf("%s: should not be flagged. got: %+v", name, report.violations)
	}
}

func TestArchitectureJSONNegative_MarshalAlias(t *testing.T) {
	runForbiddenProbe(t, "stdjson.Marshal", `package civildateplan
import stdjson "encoding/json"
func root() { stdjson.Marshal(nil) }
`, "encoding/json.Marshal")
}

func TestArchitectureJSONNegative_MarshalDot(t *testing.T) {
	runForbiddenProbe(t, "json.Marshal", `package civildateplan
import "encoding/json"
func root() { json.Marshal(nil) }
`, "encoding/json.Marshal")
}

func TestArchitectureJSONNegative_UnmarshalAlias(t *testing.T) {
	runForbiddenProbe(t, "stdjson.Unmarshal", `package civildateplan
import stdjson "encoding/json"
func root() {
	var v any
	stdjson.Unmarshal([]byte{}, &v)
}
`, "encoding/json.Unmarshal")
}

func TestArchitectureJSONNegative_UnmarshalDot(t *testing.T) {
	runForbiddenProbe(t, "json.Unmarshal", `package civildateplan
import "encoding/json"
func root() {
	var v any
	json.Unmarshal([]byte{}, &v)
}
`, "encoding/json.Unmarshal")
}

func TestArchitectureJSONNegative_OtherPackageAllowed(t *testing.T) {
	// A package named json with its own Marshal must be allowed.
	// We simulate this by declaring a local function named Marshal and a local variable json.
	runAllowedProbe(t, "local json variable", `package civildateplan
func root() {
	json := struct{ Marshal func() ([]byte, error) }{Marshal: func() ([]byte, error) { return nil, nil }}
	_, _ = json.Marshal()
}
`)
}

func TestArchitectureJSONNegative_LocalMarshalMethod(t *testing.T) {
	runAllowedProbe(t, "local Marshal method", `package civildateplan
type T struct{}
func (T) Marshal() ([]byte, error) { return nil, nil }
func root() {
	var t T
	_, _ = t.Marshal()
}
`)
}

func TestArchitectureJSONNegative_LocalMarshalFunc(t *testing.T) {
	runAllowedProbe(t, "local Marshal function", `package civildateplan
func Marshal() ([]byte, error) { return nil, nil }
func root() {
	_, _ = Marshal()
}
`)
}

func TestArchitectureJSONNegative_StringAndComment(t *testing.T) {
	runAllowedProbe(t, "string/comment", `package civildateplan
func root() {
	_ = "json.Marshal"
	// json.Unmarshal([]byte{}, nil)
}
`)
}

// ========== V2C.8: golden transitive gates ==========

func TestArchitectureGoldenTransitiveEncoderForbidden(t *testing.T) {
	src := map[string]string{
		"helper.go": `package civildateplan
func helperA() { encodeAuthorizedPlanStateV1(nil) }
`,
		"root.go": `package civildateplan
import "testing"
func frozenCompleteGoldenBytes() { helperA() }
func TestIndependentDecoderRejectsCorruption(t *testing.T) { frozenCompleteGoldenBytes() }
`,
	}
	report, err := checkSyntheticMulti(src, modeGoldenFrozenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "root.go", "frozenCompleteGoldenBytes", "encodeAuthorizedPlanStateV1") {
		t.Fatalf("golden transitive encoder should fail. got: %+v", report.violations)
	}
}

func TestArchitectureGoldenTransitiveFixtureForbidden(t *testing.T) {
	src := map[string]string{
		"helper.go": `package civildateplan
func helperA() { computeAuthorizedPlanDigestV1(nil) }
`,
		"root.go": `package civildateplan
import "testing"
func frozenCompleteGoldenBytes() {}
func TestIndependentDecoderRejectsCorruption(t *testing.T) { helperA() }
`,
	}
	report, err := checkSyntheticMulti(src, modeGoldenFrozenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "root.go", "TestIndependentDecoderRejectsCorruption", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("golden transitive fixture should fail. got: %+v", report.violations)
	}
}

func TestArchitectureGoldenTransitiveFunctionValueForbidden(t *testing.T) {
	src := map[string]string{
		"helper.go": `package civildateplan
func helperA() { computeAuthorizedPlanDigestV1(nil) }
`,
		"root.go": `package civildateplan
import "testing"
func frozenCompleteGoldenBytes() {
	f := helperA
	_ = f
}
func TestIndependentDecoderRejectsCorruption(t *testing.T) { frozenCompleteGoldenBytes() }
`,
	}
	report, err := checkSyntheticMulti(src, modeGoldenFrozenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "root.go", "frozenCompleteGoldenBytes", "computeAuthorizedPlanDigestV1") {
		t.Fatalf("golden transitive function value should fail. got: %+v", report.violations)
	}
}

func TestArchitectureGoldenTransitiveSafeCycleAllowed(t *testing.T) {
	src := map[string]string{
		"helper.go": `package civildateplan
func helperA() { helperB() }
func helperB() { helperA() }
`,
		"root.go": `package civildateplan
import "testing"
func frozenCompleteGoldenBytes() { helperA() }
func TestIndependentDecoderRejectsCorruption(t *testing.T) { frozenCompleteGoldenBytes() }
`,
	}
	report, err := checkSyntheticMulti(src, modeGoldenFrozenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.has("forbidden_dependency", "root.go", "frozenCompleteGoldenBytes", "") {
		t.Fatalf("golden safe cycle should pass. got: %+v", report.violations)
	}
}

// ========== V2C.8: meta-tests for active route ==========

func loadSelfTestFile(t *testing.T) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	data, err := os.ReadFile("architecture_test.go")
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	file, err := parser.ParseFile(fset, "architecture_test.go", data, 0)
	if err != nil {
		t.Fatalf("parse self: %v", err)
	}
	return file
}

func funcDeclCallsTarget(file *ast.File, funcName, targetName string) bool {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != funcName {
			continue
		}
		found := false
		ast.Inspect(fd, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				if f.Name == targetName {
					found = true
					return false
				}
			case *ast.SelectorExpr:
				if f.Sel.Name == targetName {
					found = true
					return false
				}
			}
			return true
		})
		return found
	}
	return false
}

func TestArchitectureMetaProductionRouteActive(t *testing.T) {
	loader := controlledProductionLoader(
		typedSource{name: "types.go", src: "package civildateplan\ntype D AuthorizedRepairPlan\ntype E D\n"},
		typedSource{name: "use.go", src: "package civildateplan\nfunc dangerous() E { return E{} }\n"},
	)
	result, err := checkProduction(loader)
	if err != nil {
		t.Fatal(err)
	}
	want := productionGateResult{
		evidence: productionGateEvidence{
			checked: true, mode: modeProductionCapabilities, packagePath: "civildateplan",
			analyzedFiles: append(
				currentProductionAnalyzedFiles(),
				"types.go", "use.go",
			),
			capabilityRoot: "AuthorizedRepairPlan",
		},
		report: typedArchitectureReport{
			resolvedCapabilities: []resolvedCapability{
				{name: "D", baseName: "AuthorizedRepairPlan", isAlias: false, filename: "types.go"},
				{name: "E", baseName: "D", isAlias: false, filename: "types.go"},
			},
			violations: []architectureViolation{
				{category: "prohibited_defined_type", filename: "types.go", symbol: "D", typeName: "D"},
				{category: "prohibited_defined_type", filename: "types.go", symbol: "E", typeName: "E"},
				{category: "capability_return", filename: "use.go", symbol: "dangerous", typeName: "E"},
				{category: "capability_return", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan"},
				{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
				{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "seal"},
				{category: "positional_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
			},
		},
	}
	assertProductionGateResultEqual(t, result, want)
}

func TestArchitectureCorruptionRootDependencyMatrix(t *testing.T) {
	const filename = "corruption_root_controlled.go"
	const cleanRoots = `
func frozenCompleteGoldenBytes() { _, _ = hex.DecodeString("") }
`
	newViolations := func(extra ...architectureViolation) []architectureViolation {
		return extra
	}

	tests := []struct {
		name           string
		declarations   string
		corruptionBody string
		want           []architectureViolation
	}{
		{name: "clean", corruptionBody: "frozenCompleteGoldenBytes()"},
		{
			name: "helper transitive", declarations: "func fullGoldenState() {}\nfunc helperA() { fullGoldenState() }\n",
			corruptionBody: "frozenCompleteGoldenBytes(); helperA()",
			want: newViolations(architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "fullGoldenState", field: "transitive"}),
		},
		{
			name: "function value transitive", declarations: "func fullGoldenState() {}\nfunc helperA() { fullGoldenState() }\n",
			corruptionBody: "frozenCompleteGoldenBytes(); f := helperA; _ = f",
			want: newViolations(architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "fullGoldenState", field: "transitive"}),
		},
		{
			name: "two helpers transitive", declarations: "func fullGoldenState() {}\nfunc helperB() { fullGoldenState() }\nfunc helperA() { helperB() }\n",
			corruptionBody: "frozenCompleteGoldenBytes(); helperA()",
			want: newViolations(architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "fullGoldenState", field: "transitive"}),
		},
		{
			name: "direct", declarations: "func fullGoldenState() {}\n",
			corruptionBody: "frozenCompleteGoldenBytes(); fullGoldenState()",
			want: newViolations(architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "fullGoldenState", field: "direct"}),
		},
		{
			name: "distinct objects by root",
			declarations: "func fullGoldenState() {}\nfunc helperA() { fullGoldenState() }\n",
			corruptionBody: "frozenCompleteGoldenBytes(); helperA()",
			want: newViolations(
				architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "encodeSeriesObject", field: "transitive"},
				architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "TestIndependentDecoderRejectsCorruption", typeName: "fullGoldenState", field: "transitive"},
				architectureViolation{category: "forbidden_dependency", filename: filename, symbol: "frozenCompleteGoldenBytes", typeName: "encodeSeriesObject", field: "direct"},
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frozenBody := cleanRoots
			if tc.name == "distinct objects by root" {
				frozenBody = `
func frozenCompleteGoldenBytes() {
	_, _ = hex.DecodeString("")
	_, _ = encodeSeriesObject(0, authorizedSeriesState{})
}
`
			}
			source := `package civildateplan
import "encoding/hex"
import "testing"
` + tc.declarations + frozenBody + `
func TestIndependentDecoderRejectsCorruption(t *testing.T) {
	` + tc.corruptionBody + `
}
`
			result, err := checkGoldenFrozen(controlledProductionLoader(typedSource{name: filename, src: source}))
			if err != nil {
				t.Fatal(err)
			}
			expectedFiles := append(currentProductionAnalyzedFiles(), filename)
			sort.Strings(expectedFiles)
			want := goldenGateResult{
				report: typedArchitectureReport{violations: append(baseAuthorizedLoaderViolations(), tc.want...)},
				evidence: goldenGateEvidence{
					checked:       true,
					analyzedFiles: expectedFiles,
					frozenRoot: "frozenCompleteGoldenBytes", corruptionRoot: "TestIndependentDecoderRejectsCorruption",
					frozenReachesHexDecode: true, corruptionReachesFrozen: true,
				},
			}
			assertGoldenGateResultEqual(t, result, want)
		})
	}
}

func TestArchitectureMetaGoldenRouteActive(t *testing.T) {
	baseline, err := checkGoldenFrozen(loadTestFiles)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.evidence.checked || !baseline.evidence.frozenReachesHexDecode || !baseline.evidence.corruptionReachesFrozen {
		t.Fatalf("baseline golden evidence incomplete: %+v", baseline.evidence)
	}

	t.Run("decode disconnected", func(t *testing.T) {
		loader := loadTestFilesWithTransform(map[string]func(string) (string, error){
			"digest_v1_test.go": func(src string) (string, error) {
				old := "encoded, err := hex.DecodeString(completeGoldenEncodedHex)"
				if !strings.Contains(src, old) {
					return "", fmt.Errorf("DecodeString target not found")
				}
			return mustReplaceExactlyOnce(t, src, old, "encoded := []byte{}\\n\\tvar err error","replace"), nil
		},
	})
	result, err := checkGoldenFrozen(loader)
	if err == nil {
		t.Fatal("golden without DecodeString must fail")
	}
	assertGoldenGateResultEqual(t, result, goldenGateResult{})
})

	t.Run("corruption disconnected", func(t *testing.T) {
		loader := loadTestFilesWithTransform(map[string]func(string) (string, error){
			"digest_v1_test.go": func(src string) (string, error) {
				old := "func TestIndependentDecoderRejectsCorruption(t *testing.T) {\n\tgolden := frozenCompleteGoldenBytes(t)"
				if !strings.Contains(src, old) {
					return "", fmt.Errorf("corruption/frozen target not found")
				}
				return mustReplaceExactlyOnce(t, src, old, "func TestIndependentDecoderRejectsCorruption(t *testing.T) {\n\tgolden := []byte{}","replace"), nil
			},
		})
		result, err := checkGoldenFrozen(loader)
		if err == nil {
			t.Fatal("corruption without frozen root must fail")
		}
		assertGoldenGateResultEqual(t, result, goldenGateResult{})
	})

	for _, tc := range []struct {
		name       string
		helperBody string
		wantObject string
	}{
		{"encoder helper", "encodeAuthorizedPlanStateV1(nil)", "encodeAuthorizedPlanStateV1"},
		{"fixture helper", "fullGoldenState()", "fullGoldenState"},
		{"function value helper", "fullGoldenState()", "fullGoldenState"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helperUse := "helperA()"
			if tc.name == "function value helper" {
				helperUse = "f := helperA; _ = f"
			}
			loader := controlledProductionLoader(typedSource{name: "golden_controlled.go", src: `package civildateplan
import "encoding/hex"
import "testing"
func fullGoldenState() {}
func helperA() { ` + tc.helperBody + ` }
func frozenCompleteGoldenBytes() { _, _ = hex.DecodeString(""); ` + helperUse + ` }
func TestIndependentDecoderRejectsCorruption(t *testing.T) { frozenCompleteGoldenBytes() }
`})
			result, err := checkGoldenFrozen(loader)
			if err != nil {
				t.Fatal(err)
			}
			if !result.report.hasTypeName("forbidden_dependency", "golden_controlled.go", "frozenCompleteGoldenBytes", tc.wantObject) ||
				result.report.countViolations("forbidden_dependency", "golden_controlled.go", "frozenCompleteGoldenBytes", "transitive") == 0 {
				t.Fatalf("missing transitive %s dependency: %+v", tc.wantObject, result.report.violations)
			}
		})
	}
}

func TestArchitectureMetaSyntheticRouteActive(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go": "package civildateplan\n" +
			"import stdjson \"encoding/json\"\n" +
			"type Unrelated struct {\nseal int\ndigest [32]byte\n}\n" +
			"func helperA() { encodeAuthorizedPlanStateV1(nil) }\n" +
			"func dangerous(p B, u *Unrelated) {\n" +
			"p.seal = authorizedPlanSeal\n" +
			"p.digest = [32]byte{1}\n" +
			"u.seal = 1\n" +
			"u.digest = [32]byte{2}\n" +
			"f := helperA\n" +
			"_ = f\n" +
			"stdjson.Marshal(nil)\n" +
			"}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	want := typedArchitectureReport{
		resolvedCapabilities: []resolvedCapability{
			{name: "A", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_a.go"},
			{name: "B", baseName: "AuthorizedRepairPlan", isAlias: true, filename: "types_b.go"},
		},
		violations: []architectureViolation{
			{category: "prohibited_alias", filename: "types_a.go", symbol: "A", typeName: "A"},
			{category: "prohibited_alias", filename: "types_b.go", symbol: "B", typeName: "B"},
			{category: "seal_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "seal"},
			{category: "digest_assignment", filename: "use.go", symbol: "dangerous", typeName: "B", field: "digest"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "helperA", typeName: "encodeAuthorizedPlanStateV1", field: "direct"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "dangerous", typeName: "encodeAuthorizedPlanStateV1", field: "transitive"},
			{category: "forbidden_dependency", filename: "use.go", symbol: "dangerous", typeName: "encoding/json.Marshal", field: "direct"},
			{category: "capability_return", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan"},
			{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
			{category: "keyed_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "seal"},
			{category: "positional_composite", filename: "load_authorized.go", symbol: "LoadAuthorizedRepairPlan", typeName: "AuthorizedRepairPlan", field: "digest"},
		},
	}
	assertArchitectureReportEqual(t, report, want)
}

func TestArchitectureMetaFailClosedPropagates(t *testing.T) {
	// This meta-test proves that type errors reach the wrapper callers.
	_, err := checkSingleSynthetic(`package civildateplan
func broken(`, modeSealAndCapability)
	if err == nil {
		t.Fatal("type errors must propagate to wrapper")
	}
}

func TestArchitectureMetaReceiverRequiresSemanticRoute(t *testing.T) {
	// If the receiver heuristic were textual, both p and u would be flagged.
	// The semantic route distinguishes only p.* as capability.
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go": "package civildateplan\ntype Unrelated struct {\nseal int\ndigest [32]byte\n}\n" +
			"func dangerous(p B, u *Unrelated) {\np.seal = authorizedPlanSeal\np.digest = [32]byte{1}\nu.seal = 1\nu.digest = [32]byte{2}\n}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	sealCount := report.countViolations("seal_assignment", "use.go", "dangerous", "seal")
	digestCount := report.countViolations("digest_assignment", "use.go", "dangerous", "digest")
	if sealCount != 1 || digestCount != 1 {
		t.Fatalf("semantic receiver required: seal=%d digest=%d, violations: %+v", sealCount, digestCount, report.violations)
	}
}

func TestArchitectureMetaTransitiveRequiresGraph(t *testing.T) {
	// A direct-only checker would miss this; the graph detects it.
	src := `package civildateplan
func helperA() { encodeAuthorizedPlanStateV1(nil) }
func root() { helperA() }
`
	report, err := checkSingleSynthetic(src, modeForbiddenProbeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.hasTypeName("forbidden_dependency", "synthetic.go", "root", "encodeAuthorizedPlanStateV1") {
		t.Fatalf("transitive graph required. got: %+v", report.violations)
	}
}

func TestArchitectureMetaCombinedFixtureDetectsAllCategories(t *testing.T) {
	src := map[string]string{
		"types_a.go": "package civildateplan\ntype A = AuthorizedRepairPlan\n",
		"types_b.go": "package civildateplan\ntype B = (*A)\n",
		"use.go": "package civildateplan\n" +
			"import stdjson \"encoding/json\"\n" +
			"type Unrelated struct {\nseal int\ndigest [32]byte\n}\n" +
			"func helperA() { encodeAuthorizedPlanStateV1(nil) }\n" +
			"func dangerous(p B, u *Unrelated) {\n" +
			"p.seal = authorizedPlanSeal\n" +
			"p.digest = [32]byte{1}\n" +
			"u.seal = 1\n" +
			"u.digest = [32]byte{2}\n" +
			"f := helperA\n" +
			"_ = f\n" +
			"stdjson.Marshal(nil)\n" +
			"}\n",
	}
	report, err := checkSyntheticMulti(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		cat, filename, symbol, field, typeName string
	}{
		{"prohibited_alias", "types_a.go", "A", "", "A"},
		{"prohibited_alias", "types_b.go", "B", "", "B"},
		{"seal_assignment", "use.go", "dangerous", "seal", "AuthorizedRepairPlan"},
		{"digest_assignment", "use.go", "dangerous", "digest", "AuthorizedRepairPlan"},
		{"forbidden_dependency", "use.go", "dangerous", "", "encodeAuthorizedPlanStateV1"},
		{"forbidden_dependency", "use.go", "dangerous", "", "encoding/json.Marshal"},
	}
	for _, c := range checks {
		if c.cat == "forbidden_dependency" {
			if !report.hasTypeName(c.cat, c.filename, c.symbol, c.typeName) {
				t.Fatalf("missing %s/%s/%s/%s. got: %+v", c.cat, c.filename, c.symbol, c.typeName, report.violations)
			}
			continue
		}
		if !report.has(c.cat, c.filename, c.symbol, c.field) {
			t.Fatalf("missing %s/%s/%s/%s. got: %+v", c.cat, c.filename, c.symbol, c.field, report.violations)
		}
	}
	if report.countViolations("seal_assignment", "use.go", "dangerous", "seal") != 1 {
		t.Fatalf("expected exactly one seal_assignment for p. got: %+v", report.violations)
	}
	if report.countViolations("digest_assignment", "use.go", "dangerous", "digest") != 1 {
		t.Fatalf("expected exactly one digest_assignment for p. got: %+v", report.violations)
	}
}

// ========== V2C.8: meta-test for retired AST checkers ==========

func TestArchitectureMetaNoRetiredASTCheckers(t *testing.T) {
	file := loadSelfTestFile(t)
	retired := []string{
		"typeReference", "parseTypeReference", "buildTypeDeclGraph", "collectARPNames",
		"checkFileAliasUsages", "checkFileAliasUsagesStructured", "checkMultiFileAliasUsages",
		"collectForbiddenRefs", "collectLocalVarNames", "checkForbiddenRefsInSource",
		"checkProductionArchitectureV2C6", "resolveFuncRefsInBody",
	}
	for _, name := range retired {
		if funcDeclCallsTarget(file, name, "") {
			t.Fatalf("retired checker %s should not be declared", name)
		}
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		for _, name := range retired {
			if fd.Name.Name == name {
				t.Fatalf("retired checker %s declared", name)
			}
		}
	}
}

// ========== V2C.7: meta-test: no dead checkers ==========

func TestArchitectureMetaNoDeadCheckerReturned(t *testing.T) {
	src := `package civildateplan
type planAlias = AuthorizedRepairPlan
var forged = planAlias{
	seal: authorizedPlanSeal,
}
`
	report, err := checkSingleSynthetic(src, modeSyntheticCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !report.has("prohibited_alias", "synthetic.go", "planAlias", "") {
		t.Fatal("alias should be detected by semantic route")
	}
	if !report.has("keyed_composite", "synthetic.go", "", "seal") {
		t.Fatal("keyed composite should be detected by semantic route")
	}
	// Verify old functions don't exist (would cause build error)
}

// ========== V2D-B.3: stat(path) gate permanent test ==========

func productionLoaderWithTransform(fileName string, transform func(string) string) typedSourceLoader {
	return func() ([]typedSource, error) {
		files, err := loadProductionFiles()
		if err != nil {
			return nil, err
		}
		for i, f := range files {
			if f.name != fileName {
				continue
			}
			data, err := os.ReadFile(f.name)
			if err != nil {
				return nil, err
			}
			files[i].src = transform(string(data))
			return files, nil
		}
		return nil, fmt.Errorf("file %s not found in production files", fileName)
	}
}

func runGateOnTransform(t *testing.T, transform func(string) string) []string {
	t.Helper()
	loader := productionLoaderWithTransform("load_file.go", transform)
	sources, err := loader()
	if err != nil {
		t.Fatal(err)
	}
	_, astFiles, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	sourceASTs := make(map[string]*ast.File, len(sources))
	for i, s := range sources {
		sourceASTs[s.name] = astFiles[i]
	}
	localGraph := buildFuncRefGraph(astFiles, info, pkg, nil, sourceASTs)
	rootObj := pkg.Scope().Lookup("readPlanFileBytes")
	rootFn, ok := rootObj.(*types.Func)
	if !ok {
		t.Fatal("readPlanFileBytes not found in package scope")
	}
	result, err := readPlanFileBytesStatByPathViolations(sources, localGraph, rootFn)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func assertViolations(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("violation count mismatch: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("violation[%d] mismatch:\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}

// ---------- shared transforms for mutation fixtures ----------
// V2D-B.5: extracted transforms used by both the real matrix and adversarial test.

type sourceTransform func(string) (string, error)

type gateRunner func(string) ([]string, error)

func executeMutationCase(source string, transform sourceTransform, runGate gateRunner) ([]string, error) {
	transformed, err := transform(source)
	if err != nil {
		return nil, err
	}
	return runGate(transformed)
}

func transformShadowSafe(source string) (string, error) {
	s, err := replaceExactlyOnce(source,
		`f, err := openPlanFile(path)`,
		`statFn := safeProbe
	statFn(path)
	f, err := openPlanFile(path)`,
	)
	if err != nil {
		return source, fmt.Errorf("shadow-safe/insert-helper: %w", err)
	}
	s += "\n// SENTINEL-shadow-safe-helper\nfunc safeProbe(p string) { _ = p }\n"
	return s, nil
}

func transformFileStatMethodParen(source string) (string, error) {
	s, err := replaceExactlyOnce(source,
		`f, err := openPlanFile(path)
	if err != nil {
		if _, ok := err.(*loadError); ok {
			return nil, err
		}
		return nil, newLoadError(loadErrorOpen)
	}`,
		`f, err := openPlanFile(path)
	if err != nil {
		if _, ok := err.(*loadError); ok {
			return nil, err
		}
		return nil, newLoadError(loadErrorOpen)
	}
	methodFi, methodErr := (f.Stat)()
	_ = methodFi
	_ = methodErr`,
	)
	if err != nil {
		return source, fmt.Errorf("file-stat-method-paren/replace-open-call: %w", err)
	}
	return s, nil
}

func runGateOnSource(t *testing.T, src string) []string {
	t.Helper()
	files, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, f := range files {
		if f.name == "load_file.go" {
			files[i].src = src
			found = true
			break
		}
	}
	if !found {
		t.Fatal("load_file.go not found in production file list")
	}
	_, astFiles, pkg, info, err := typeCheckSources(files)
	if err != nil {
		t.Fatal(err)
	}
	sourceASTs := make(map[string]*ast.File, len(files))
	for i, s := range files {
		sourceASTs[s.name] = astFiles[i]
	}
	localGraph := buildFuncRefGraph(astFiles, info, pkg, nil, sourceASTs)
	rootObj := pkg.Scope().Lookup("readPlanFileBytes")
	rootFn, ok := rootObj.(*types.Func)
	if !ok {
		t.Fatal("readPlanFileBytes not found in package scope")
	}
	result, err := readPlanFileBytesStatByPathViolations(files, localGraph, rootFn)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func TestArchitectureStatPathGateAllRoutes(t *testing.T) {
	// Negative: production baseline
	t.Run("base", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string { return src })
		assertViolations(t, result)
	})

	// A: direct os.Stat(path) in readPlanFileBytes
	t.Run("direct", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-4")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`if _, err := os.Stat(path); err != nil { return nil, newLoadError(loadErrorOpen) }
	f, err := openPlanFile(path)`, "step-5")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// B: helper package-level reachable calling os.Stat
	t.Run("helper", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-6")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`statProbe(path)
	f, err := openPlanFile(path)`, "step-7")
			src += "\nfunc statProbe(p string) { _, _ = os.Stat(p) }\n"
			return src
		})
		assertViolations(t, result, "os.Stat() in statProbe")
	})

	// C: function value local
	t.Run("func-value-local", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-8")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`fn := statProbe
	fn(path)
	f, err := openPlanFile(path)`, "step-9")
			src += "\nfunc statProbe(p string) { _, _ = os.Stat(p) }\n"
			return src
		})
		assertViolations(t, result, "os.Stat() in statProbe")
	})

	// D: package-level var fn = helper
	t.Run("func-value-pkg", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-10")
			src = mustReplaceExactlyOnce(t, src,
				`const maxFileSize`,
				"var fn = statProbe\n\nconst maxFileSize", "step-11")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`fn(path)
	f, err := openPlanFile(path)`, "step-12")
			src += "\nfunc statProbe(p string) { _, _ = os.Stat(p) }\n"
			return src
		})
		assertViolations(t, result, "os.Stat() in statProbe")
	})

	// E: local alias of os.Stat
	t.Run("alias-local", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-13")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`statFn := os.Stat
	statFn(path)
	f, err := openPlanFile(path)`, "step-14")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// F: package-level alias of os.Stat
	t.Run("alias-pkg", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-15")
			src = mustReplaceExactlyOnce(t, src,
				`const maxFileSize`,
				"var statFn = os.Stat\n\nconst maxFileSize", "step-16")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`statFn(path)
	f, err := openPlanFile(path)`, "step-17")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// G: dot-import
	t.Run("dot-import", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"path/filepath\"\n\t. \"syscall\"", "step-18")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var st Stat_t
	_ = Stat(path, &st)
	f, err := openPlanFile(path)`, "step-19")
			return src
		})
		assertViolations(t, result, "syscall.Stat() in readPlanFileBytes")
	})

	// Negative controls
	t.Run("neg-nonreachable-func", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-20")
			src += "\nfunc foo(p string) { _, _ = os.Stat(p) }\n"
			return src
		})
		assertViolations(t, result)
	})

	t.Run("neg-nonreachable-method", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-21")
			src += "\ntype negType int\nfunc (negType) statProbe(p string) { _, _ = os.Stat(p) }\n"
			return src
		})
		assertViolations(t, result)
	})

	t.Run("neg-nonreachable-var", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-22")
			src = mustReplaceExactlyOnce(t, src,
				`const maxFileSize`,
				"var unusedVar = os.Stat\n\nconst maxFileSize", "step-23")
			return src
		})
		assertViolations(t, result)
	})

	t.Run("neg-file-stat-method", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string { return src })
		assertViolations(t, result)
	})

	// A: parenthesized local alias
	t.Run("paren-alias-local", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-24")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`statFn := (os.Stat)
	statFn(path)
	f, err := openPlanFile(path)`, "step-25")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// B: parenthesized package-level alias
	t.Run("paren-alias-pkg", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-26")
			src = mustReplaceExactlyOnce(t, src,
				`const maxFileSize`,
				"var statFn = (os.Stat)\n\nconst maxFileSize", "step-27")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`statFn(path)
	f, err := openPlanFile(path)`, "step-28")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// C: parenthesized direct call
	t.Run("paren-direct-call", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-29")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`(os.Stat)(path)
	f, err := openPlanFile(path)`, "step-30")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// D: nested parentheses
	t.Run("paren-nested", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-31")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`((os.Stat))(path)
	f, err := openPlanFile(path)`, "step-32")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// E: declared without initializer, later assigned with os.Stat
	t.Run("assigned-after-decl", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-33")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var statFn func(string) (os.FileInfo, error)
	statFn = os.Stat
	statFn(path)
	f, err := openPlanFile(path)`, "step-34")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// F: declared without initializer, later assigned with parenthesized os.Stat
	t.Run("assigned-after-decl-paren", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-35")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var statFn func(string) (os.FileInfo, error)
	statFn = (os.Stat)
	statFn(path)
	f, err := openPlanFile(path)`, "step-36")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// G: local assignment chain
	t.Run("local-chain", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-37")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var a func(string) (os.FileInfo, error)
	var b func(string) (os.FileInfo, error)
	a = os.Stat
	b = a
	b(path)
	f, err := openPlanFile(path)`, "step-38")
			return src
		})
		assertViolations(t, result, "os.Stat() in readPlanFileBytes")
	})

	// H: multiple possible targets, one forbidden
	t.Run("multiple-targets", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-39")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var statFn func(string) (os.FileInfo, error)
	statFn = os.Stat
	statFn = os.Lstat
	statFn(path)
	f, err := openPlanFile(path)`, "step-40")
			return src
		})
		assertViolations(t, result, "os.Lstat() in readPlanFileBytes", "os.Stat() in readPlanFileBytes")
	})

	// I: local shadowing with a different *types.Var and a safe target
	t.Run("shadow-safe", func(t *testing.T) {
		data, err := os.ReadFile("load_file.go")
		if err != nil {
			t.Fatal(err)
		}
		result, err := executeMutationCase(string(data), transformShadowSafe, func(src string) ([]string, error) {
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertViolations(t, result)
	})

	// J: alias assigned but never called
	t.Run("alias-unused", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-42")
			src = mustReplaceExactlyOnce(t, src,
				`const maxFileSize`,
				"var statFn = os.Stat\n\nconst maxFileSize", "step-43")
			return src
		})
		assertViolations(t, result)
	})

	// K: local variable cycle (must not hang)
	t.Run("local-cycle", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-44")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var a func(string) (os.FileInfo, error)
	var b func(string) (os.FileInfo, error)
	a = b
	b = a
	a(path)
	f, err := openPlanFile(path)`, "step-45")
			return src
		})
		assertViolations(t, result)
	})

	// L: parenthesized (*os.File).Stat() method call must not be flagged
	t.Run("file-stat-method-paren", func(t *testing.T) {
		data, err := os.ReadFile("load_file.go")
		if err != nil {
			t.Fatal(err)
		}
		result, err := executeMutationCase(string(data), transformFileStatMethodParen, func(src string) ([]string, error) {
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertViolations(t, result)
	})

	// Preserve previous matrix: os.Lstat
	t.Run("os-lstat", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"os\"\n\t\"path/filepath\"", "step-47")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`if _, err := os.Lstat(path); err != nil { return nil, newLoadError(loadErrorOpen) }
	f, err := openPlanFile(path)`, "step-48")
			return src
		})
		assertViolations(t, result, "os.Lstat() in readPlanFileBytes")
	})

	// Preserve previous matrix: syscall.Lstat
	t.Run("syscall-lstat", func(t *testing.T) {
		result := runGateOnTransform(t, func(src string) string {
			src = mustReplaceExactlyOnce(t, src,
				`"path/filepath"`, "\"path/filepath\"\n\t. \"syscall\"", "step-49")
			src = mustReplaceExactlyOnce(t, src,
				`f, err := openPlanFile(path)`,
				`var st Stat_t
	if err := Lstat(path, &st); err != nil { return nil, newLoadError(loadErrorOpen) }
	f, err := openPlanFile(path)`, "step-50")
			return src
		})
		assertViolations(t, result, "syscall.Lstat() in readPlanFileBytes")
	})
}

func TestArchitectureReplaceExactlyOnce(t *testing.T) {
	// A: target present exactly once
	t.Run("present-once", func(t *testing.T) {
		result, err := replaceExactlyOnce("abc def ghi", "def", "xyz")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "abc xyz ghi" {
			t.Fatalf("unexpected result: %q", result)
		}
	})

	// B: target absent
	t.Run("absent", func(t *testing.T) {
		_, err := replaceExactlyOnce("abc def ghi", "notfound", "xyz")
		if err == nil {
			t.Fatal("expected error for absent target")
		}
	})

	// C: target present twice
	t.Run("present-twice", func(t *testing.T) {
		_, err := replaceExactlyOnce("abc abc def", "abc", "xyz")
		if err == nil {
			t.Fatal("expected error for twice-occurring target")
		}
	})

	// D: replacement identical to target
	t.Run("identical-replacement", func(t *testing.T) {
		_, err := replaceExactlyOnce("abc def", "def", "def")
		if err == nil {
			t.Fatal("expected error for identical replacement")
		}
	})

	// E: compound transform, first step valid, second absent
	t.Run("compound-second-absent", func(t *testing.T) {
		src := "first: alpha; second: beta"
		src, err := replaceExactlyOnce(src, "alpha", "omega")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(src, "omega") {
			t.Fatal("first step did not apply")
		}
		_, err = replaceExactlyOnce(src, "gamma", "delta")
		if err == nil {
			t.Fatal("expected error: second step target gamma not present")
		}
	})

	// F: secondary change present, critical mutation absent
	t.Run("secondary-present-critical-absent", func(t *testing.T) {
		src := "first: alpha; second: beta"
		src, err := replaceExactlyOnce(src, "alpha", "omega")
		if err != nil {
			t.Fatal(err)
		}
		// secondary mutation present and valid
		src, err = replaceExactlyOnce(src, "beta", "epsilon")
		if err != nil {
			t.Fatal(err)
		}
		// critical mutation absent — must fail despite secondary success
		_, err = replaceExactlyOnce(src, "critical_absent", "replacement")
		if err == nil {
			t.Fatal("expected error: critical target absent, must not be masked by secondary success")
		}
	})
}

func TestArchitectureReplaceAdversarial(t *testing.T) {
	data, err := os.ReadFile("load_file.go")
	if err != nil {
		t.Fatal(err)
	}
	base := string(data)

	// Simulate production code changing openPlanFile(path) -> (openPlanFile)(path)
	adversarial, err := replaceExactlyOnce(base, `openPlanFile(path)`, `(openPlanFile)(path)`)
	if err != nil {
		t.Fatal(err)
	}

	// Positive: original base, transforms succeed, gate called
	t.Run("shadow-safe-positive", func(t *testing.T) {
		gateCalled := false
		result, err := executeMutationCase(base, transformShadowSafe, func(src string) ([]string, error) {
			gateCalled = true
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !gateCalled {
			t.Fatal("gate was not called on valid transform")
		}
		assertViolations(t, result)
	})

	t.Run("file-stat-method-paren-positive", func(t *testing.T) {
		gateCalled := false
		result, err := executeMutationCase(base, transformFileStatMethodParen, func(src string) ([]string, error) {
			gateCalled = true
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !gateCalled {
			t.Fatal("gate was not called on valid transform")
		}
		assertViolations(t, result)
	})

	// Adversarial: mutated base, transforms fail, gate not called
	t.Run("shadow-safe-adversarial", func(t *testing.T) {
		gateCalled := false
		_, err := executeMutationCase(adversarial, transformShadowSafe, func(src string) ([]string, error) {
			gateCalled = true
			return nil, nil
		})
		if err == nil {
			t.Fatal("expected error: shadow-safe target not found in mutated base")
		}
		if gateCalled {
			t.Fatal("gate executed after failed transform")
		}
	})

	t.Run("file-stat-method-paren-adversarial", func(t *testing.T) {
		gateCalled := false
		_, err := executeMutationCase(adversarial, transformFileStatMethodParen, func(src string) ([]string, error) {
			gateCalled = true
			return nil, nil
		})
		if err == nil {
			t.Fatal("expected error: file-stat-method-paren target not found in mutated base")
		}
	if gateCalled {
		t.Fatal("gate executed after failed transform")
	}
})
}

func TestArchitectureMutationCase(t *testing.T) {
	// A: valid transform, gate called exactly once
	t.Run("valid-transform-gate-called", func(t *testing.T) {
		src := "hello world"
		transform := func(s string) (string, error) {
			return replaceExactlyOnce(s, "world", "universe")
		}
		gateCalled := false
		result, err := executeMutationCase(src, transform, func(s string) ([]string, error) {
			gateCalled = true
			return []string{"violation"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !gateCalled {
			t.Fatal("gate was not called")
		}
		if len(result) != 1 || result[0] != "violation" {
			t.Fatalf("unexpected result: %v", result)
		}
	})

	// B: transform error, gate not called
	t.Run("transform-error-gate-not-called", func(t *testing.T) {
		transform := func(s string) (string, error) {
			return s, fmt.Errorf("test transform error")
		}
		gateCalled := false
		_, err := executeMutationCase("src", transform, func(s string) ([]string, error) {
			gateCalled = true
			return nil, nil
		})
		if err == nil {
			t.Fatal("expected transform error")
		}
		if gateCalled {
			t.Fatal("gate called after transform error")
		}
	})

	// C: gate returns error, error propagated
	t.Run("gate-error-propagated", func(t *testing.T) {
		gateErr := fmt.Errorf("gate failure")
		transform := func(s string) (string, error) { return s, nil }
		_, err := executeMutationCase("src", transform, func(s string) ([]string, error) {
			return nil, gateErr
		})
		if err == nil || err != gateErr {
			t.Fatalf("expected gate error %v, got %v", gateErr, err)
		}
	})

	// D: compound transform fails second step, gate not called, error identifies step
	t.Run("compound-second-step-fails", func(t *testing.T) {
		transform := func(s string) (string, error) {
			s, err := replaceExactlyOnce(s, "hello", "hi")
			if err != nil {
				return s, fmt.Errorf("step1: %w", err)
			}
			s, err = replaceExactlyOnce(s, "absent", "present")
			if err != nil {
				return s, fmt.Errorf("step2: %w", err)
			}
			return s, nil
		}
		gateCalled := false
		_, err := executeMutationCase("hello world", transform, func(s string) ([]string, error) {
			gateCalled = true
			return nil, nil
		})
		if err == nil {
			t.Fatal("expected error on second step")
		}
		if !strings.Contains(err.Error(), "step2") {
			t.Fatal("error should identify step2")
		}
		if gateCalled {
			t.Fatal("gate called after compound transform failure")
		}
	})

	// E: positive shadow-safe — real transform, real gate
	t.Run("shadow-safe-positive", func(t *testing.T) {
		data, err := os.ReadFile("load_file.go")
		if err != nil {
			t.Fatal(err)
		}
		gateCalled := false
		result, err := executeMutationCase(string(data), transformShadowSafe, func(src string) ([]string, error) {
			gateCalled = true
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !gateCalled {
			t.Fatal("gate was not called")
		}
		assertViolations(t, result)
	})

	// F: positive file-stat-method-paren — real transform, real gate
	t.Run("file-stat-method-paren-positive", func(t *testing.T) {
		data, err := os.ReadFile("load_file.go")
		if err != nil {
			t.Fatal(err)
		}
		gateCalled := false
		result, err := executeMutationCase(string(data), transformFileStatMethodParen, func(src string) ([]string, error) {
			gateCalled = true
			return runGateOnSource(t, src), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !gateCalled {
			t.Fatal("gate was not called")
		}
		assertViolations(t, result)
	})
}

func TestArchitectureAuthorizedLoaderPipeline(t *testing.T) {
	sources, err := loadProductionFiles()
	if err != nil {
		t.Fatal(err)
	}
	_, astFiles, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		t.Fatal(err)
	}

	// Find LoadAuthorizedRepairPlan
	arpObj := pkg.Scope().Lookup("LoadAuthorizedRepairPlan")
	if arpObj == nil {
		t.Fatal("LoadAuthorizedRepairPlan not found in package scope")
	}
	arpFn, ok := arpObj.(*types.Func)
	if !ok {
		t.Fatalf("LoadAuthorizedRepairPlan is not a function: %T", arpObj)
	}
	if !arpFn.Exported() {
		t.Fatal("LoadAuthorizedRepairPlan must be exported")
	}

	// Check signature returns *AuthorizedRepairPlan
	sig, ok := arpFn.Type().(*types.Signature)
	if !ok {
		t.Fatal("LoadAuthorizedRepairPlan signature not found")
	}
	if sig.Results().Len() != 2 {
		t.Fatal("LoadAuthorizedRepairPlan must return 2 values")
	}
	retType := sig.Results().At(0).Type()
	if ptr, ok := retType.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok {
			if named.Obj().Name() != "AuthorizedRepairPlan" {
				t.Fatalf("unexpected return type: %s", named.Obj().Name())
			}
		}
	} else {
		t.Fatal("LoadAuthorizedRepairPlan must return *AuthorizedRepairPlan")
	}

	// Verify no other exported function returns *AuthorizedRepairPlan
	for _, name := range pkg.Scope().Names() {
		if name == "LoadAuthorizedRepairPlan" {
			continue
		}
		obj := pkg.Scope().Lookup(name)
		fn, isFunc := obj.(*types.Func)
		if !isFunc || !fn.Exported() {
			continue
		}
		fsig, ok := fn.Type().(*types.Signature)
		if !ok || fsig.Results().Len() == 0 {
			continue
		}
		rType := fsig.Results().At(0).Type()
		if ptr, ok := rType.(*types.Pointer); ok {
			if named, ok := ptr.Elem().(*types.Named); ok {
				if named.Obj().Name() == "AuthorizedRepairPlan" {
					t.Fatalf("%s also returns *AuthorizedRepairPlan", name)
				}
			}
		}
	}

	// Walk AST to find LoadAuthorizedRepairPlan body and verify required calls
	var arpDecl *ast.FuncDecl
	for _, f := range astFiles {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				if info.Defs[fd.Name] == arpFn {
					arpDecl = fd
					break
				}
			}
		}
	}
	if arpDecl == nil || arpDecl.Body == nil {
		t.Fatal("LoadAuthorizedRepairPlan body not found")
	}

	// Count critical calls
	readPlanFileBytesCount := 0
	preparePlanBytesCount := 0
	parseApprovedDigestCount := 0
	arpCompositeLitCount := 0
	var arpCompositeLit *ast.CompositeLit
	hasNilReturnOnError := false

	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok {
					if info.Uses[ident] == pkg.Scope().Lookup(sel.Sel.Name) {
						// package-level call
						if info.Uses[ident] == pkg.Scope().Lookup(sel.Sel.Name) {
							// This is not quite right — let me check the selector differently
						}
					}
				}
			}
			if ident, ok := node.Fun.(*ast.Ident); ok {
				obj := info.Uses[ident]
				if obj == nil {
					obj = info.Defs[ident]
				}
				if obj != nil && obj.Pkg() == pkg {
					switch obj.Name() {
					case "readPlanFileBytes":
						readPlanFileBytesCount++
					case "preparePlanBytes":
						preparePlanBytesCount++
					case "parseApprovedPlanDigestStrict":
						parseApprovedDigestCount++
					}
				}
			}
		case *ast.CompositeLit:
			clType := info.TypeOf(node)
			if clType == nil {
				return true
			}
			if named, ok := derefNamed(clType); ok {
				if named.Obj().Name() == "AuthorizedRepairPlan" {
					arpCompositeLitCount++
					arpCompositeLit = node
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range node.Results {
				if ident, ok := expr.(*ast.Ident); ok && isPredeclaredNil(info, ident) {
					hasNilReturnOnError = true
				}
			}
		}
		return true
	})

	if readPlanFileBytesCount != 1 {
		t.Fatalf("expected 1 call to readPlanFileBytes, got %d", readPlanFileBytesCount)
	}
	if preparePlanBytesCount != 1 {
		t.Fatalf("expected 1 call to preparePlanBytes, got %d", preparePlanBytesCount)
	}
	if parseApprovedDigestCount != 1 {
		t.Fatalf("expected 1 call to parseApprovedPlanDigestStrict, got %d", parseApprovedDigestCount)
	}
	if arpCompositeLitCount != 1 {
		t.Fatalf("expected 1 AuthorizedRepairPlan composite literal, got %d", arpCompositeLitCount)
	}
	if arpCompositeLit == nil {
		t.Fatal("AuthorizedRepairPlan composite literal not found")
	}
	if !hasNilReturnOnError {
		t.Fatal("must return nil on error")
	}

	// ---------- semantic variable tracking ----------

	// Find variables from short declarations: state, calculated, approved
	var (
		stateVar      *types.Var
		calculatedVar *types.Var
		approvedVar   *types.Var
		prepareErrVar *types.Var
		digestErrVar  *types.Var
	)
	// matchedIf is the if-stmt containing the NEQ comparison.
	var mismatchIf *ast.IfStmt

	// Track AST nodes of the mismatch return inside mismatchIf.
	var mismatchReturn *ast.ReturnStmt

	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE && node.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range node.Rhs {
				call, ok := unparenExpr(rhs).(*ast.CallExpr)
				if !ok {
					continue
				}
				callIdent, ok := call.Fun.(*ast.Ident)
				if !ok {
					continue
				}
				callObj := info.Uses[callIdent]
				if callObj == nil {
					callObj = info.Defs[callIdent]
				}
				if callObj == nil || callObj.Pkg() != pkg {
					continue
				}
				switch callObj.Name() {
				case "preparePlanBytes":
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && stateVar == nil {
							stateVar = v
						}
						if i == 1 && isVar && calculatedVar == nil {
							calculatedVar = v
						}
						if i == 2 && isVar && prepareErrVar == nil {
							prepareErrVar = v
						}
					}
				case "parseApprovedPlanDigestStrict":
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && approvedVar == nil {
							approvedVar = v
						}
						if i == 1 && isVar && digestErrVar == nil {
							digestErrVar = v
						}
					}
				}
			}
		case *ast.IfStmt:
			if bin, ok := node.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				mismatchIf = node
			}
		}
		return true
	})

	if stateVar == nil {
		t.Fatal("cannot resolve state variable from preparePlanBytes")
	}
	if calculatedVar == nil {
		t.Fatal("cannot resolve calculated variable from preparePlanBytes")
	}
	if approvedVar == nil {
		t.Fatal("cannot resolve approved variable from parseApprovedPlanDigestStrict")
	}

	if mismatchIf == nil {
		t.Fatal("digest mismatch if-stmt not found")
	}

	// Find the return statement inside mismatchIf's body
	ast.Inspect(mismatchIf.Body, func(ifN ast.Node) bool {
		if ret, ok := ifN.(*ast.ReturnStmt); ok && mismatchReturn == nil {
			mismatchReturn = ret
			return false
		}
		return true
	})

	// ---------- 2. order: composite after mismatch ----------

	if arpCompositeLit.Pos() <= mismatchIf.Pos() {
		t.Fatal("composite literal must appear after the digest comparison")
	}

	// ---------- 3. comparison operands ----------

	bin, ok := mismatchIf.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		t.Fatal("mismatch condition must be a != comparison")
	}

	// LHS must resolve to calculatedVar (from preparePlanBytes)
	lhsIdent, ok := unparenExpr(bin.X).(*ast.Ident)
	if !ok {
		t.Fatalf("LHS of mismatch must be an identifier, got %T", bin.X)
	}
	lhsObj := info.Uses[lhsIdent]
	if lhsObj == nil {
		lhsObj = info.Defs[lhsIdent]
	}
	if lhsObj != calculatedVar {
		t.Fatalf("LHS of mismatch must be the calculated variable from preparePlanBytes, got %v", lhsObj)
	}

	// RHS must be [32]byte(approved) — a call expression with type conversion
	rhsCall, ok := unparenExpr(bin.Y).(*ast.CallExpr)
	if !ok {
		t.Fatal("RHS of mismatch must be [32]byte conversion")
	}
	// Check conversion: [32]byte(approved)
	arrType, ok := rhsCall.Fun.(*ast.ArrayType)
	if !ok || arrType.Len == nil {
		t.Fatal("RHS must be [32]byte conversion")
	}
	if len(rhsCall.Args) != 1 {
		t.Fatal("RHS conversion must have 1 argument")
	}
	rhsArg, ok := unparenExpr(rhsCall.Args[0]).(*ast.Ident)
	if !ok {
		t.Fatal("RHS argument must be approved identifier")
	}
	rhsArgObj := info.Uses[rhsArg]
	if rhsArgObj == nil {
		rhsArgObj = info.Defs[rhsArg]
	}
	if rhsArgObj != approvedVar {
		t.Fatalf("RHS of mismatch must be the approved variable from parseApprovedPlanDigestStrict, got %v", rhsArgObj)
	}

	// ---------- 3a. mismatch return must be nil + mismatch error ----------

	if mismatchReturn == nil {
		t.Fatal("mismatch branch must contain a return statement")
	}
	if len(mismatchReturn.Results) != 2 {
		t.Fatal("mismatch return must have 2 values")
	}
	// First return value must be nil
	if id, ok := unparenExpr(mismatchReturn.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
		t.Fatal("mismatch return first value must be nil")
	}
	// Second return value must be newLoadError(loadErrorApprovedDigestMismatch)
	if call, ok := unparenExpr(mismatchReturn.Results[1]).(*ast.CallExpr); ok {
		if callIdent, ok := call.Fun.(*ast.Ident); ok {
			if obj := info.Uses[callIdent]; obj == nil || obj.Name() != "newLoadError" {
				t.Fatal("mismatch return must call newLoadError")
			}
		}
	} else {
		t.Fatal("mismatch return second value must be a call")
	}

	// ---------- 4. provenance of composite literal ----------

	var compositeFieldsByKey = make(map[string]*ast.KeyValueExpr)
	for _, elt := range arpCompositeLit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok {
				compositeFieldsByKey[key.Name] = kv
			}
		}
	}

	// seal must receive authorizedPlanSeal
	sealKV, hasSeal := compositeFieldsByKey["seal"]
	if !hasSeal {
		t.Fatal("composite literal missing seal field")
	}
	sealVal, ok := unparenExpr(sealKV.Value).(*ast.Ident)
	if !ok || info.Uses[sealVal] != pkg.Scope().Lookup("authorizedPlanSeal") {
		t.Fatal("seal field must receive the package-level authorizedPlanSeal singleton")
	}

	// state must receive &stateVar
	stateKV, hasState := compositeFieldsByKey["state"]
	if !hasState {
		t.Fatal("composite literal missing state field")
	}
	stateUnary, ok := unparenExpr(stateKV.Value).(*ast.UnaryExpr)
	if !ok || stateUnary.Op != token.AND {
		t.Fatal("state field must be &state, an address-of expression")
	}
	stateIdent, ok := unparenExpr(stateUnary.X).(*ast.Ident)
	if !ok {
		t.Fatal("state address-of operand must be an identifier")
	}
	stateIdentObj := info.Uses[stateIdent]
	if stateIdentObj == nil {
		stateIdentObj = info.Defs[stateIdent]
	}
	if stateIdentObj != stateVar {
		t.Fatalf("state field must be &state where state comes from preparePlanBytes, got %v", stateIdentObj)
	}

	// digest must receive calculatedVar
	digestKV, hasDigest := compositeFieldsByKey["digest"]
	if !hasDigest {
		t.Fatal("composite literal missing digest field")
	}
	digestIdent, ok := unparenExpr(digestKV.Value).(*ast.Ident)
	if !ok {
		t.Fatal("digest field must be an identifier")
	}
	digestIdentObj := info.Uses[digestIdent]
	if digestIdentObj == nil {
		digestIdentObj = info.Defs[digestIdent]
	}
	if digestIdentObj != calculatedVar {
		t.Fatalf("digest field must receive calculated variable from preparePlanBytes, got %v", digestIdentObj)
	}

	// ---------- 5. digest parser error must be checked ----------

	// The error from parseApprovedPlanDigestStrict must be tested before the comparison.
	// Find the error check if-stmt that tests digestErrVar and verify it appears
	// before mismatchIf.
	if digestErrVar == nil {
		t.Fatal("cannot resolve error variable from parseApprovedPlanDigestStrict")
	}
	var digestErrCheckPos token.Pos
	var digestErrCheckFound bool
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok && ifStmt.Pos() < mismatchIf.Pos() {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == digestErrVar {
						digestErrCheckPos = ifStmt.Pos()
						digestErrCheckFound = true
					}
				}
			}
		}
		return true
	})
	if !digestErrCheckFound {
		t.Fatal("error from parseApprovedPlanDigestStrict must be checked before the digest comparison")
	}
	_ = digestErrCheckPos

	// ---------- 6. no globals of approvedPlanDigest type ----------

	digestTypeObj := pkg.Scope().Lookup("approvedPlanDigest")
	if digestTypeObj != nil {
		digestType, ok := digestTypeObj.Type().(*types.Named)
		if !ok {
			t.Fatal("approvedPlanDigest is not a named type")
		}
		for _, f := range astFiles {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						obj := info.Defs[name]
						if obj == nil {
							continue
						}
						objType := obj.Type()
						if objType != nil && types.Identical(objType, digestType) {
							t.Errorf("global variable %s has forbidden type approvedPlanDigest", name.Name)
						}
						if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), digestType) {
							t.Errorf("global variable %s has forbidden type *approvedPlanDigest", name.Name)
						}
					}
				}
			}
		}
	}

	// ---------- 7. no extra composite literals in other production functions ----------

	for _, f := range astFiles {
		ast.Inspect(f, func(n ast.Node) bool {
			if node, ok := n.(*ast.FuncDecl); ok && node != arpDecl && node.Body != nil {
				ast.Inspect(node.Body, func(m ast.Node) bool {
					if cl, ok := m.(*ast.CompositeLit); ok {
						clType := info.TypeOf(cl)
						if named, ok := derefNamed(clType); ok {
							if named.Obj().Name() == "AuthorizedRepairPlan" {
								t.Errorf("AuthorizedRepairPlan composite literal in unexpected function: %s", node.Name.Name)
							}
						}
					}
					return true
				})
			}
			return true
		})
	}

	// ---------- 8. mismatch error identity: loadErrorApprovedDigestMismatch ----------

	{
		mismatchErrCall, ok := unparenExpr(mismatchReturn.Results[1]).(*ast.CallExpr)
		if !ok {
			t.Fatal("mismatch return second value must be a call")
		}
		if len(mismatchErrCall.Args) != 1 {
			t.Fatal("mismatch error call must have 1 argument")
		}
		mismatchErrArg, ok := unparenExpr(mismatchErrCall.Args[0]).(*ast.Ident)
		if !ok {
			t.Fatal("mismatch error argument must be an identifier")
		}
		mismatchErrArgObj := info.Uses[mismatchErrArg]
		if mismatchErrArgObj == nil {
			mismatchErrArgObj = info.Defs[mismatchErrArg]
		}
		if mismatchErrArgObj != pkg.Scope().Lookup("loadErrorApprovedDigestMismatch") {
			t.Fatal("mismatch error must be loadErrorApprovedDigestMismatch")
		}
	}

	// ---------- 9. readPlanFileBytes error gate ----------

	var (
		readDataVar *types.Var
		readErrVar  *types.Var
	)
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range assign.Rhs {
				call, ok := unparenExpr(rhs).(*ast.CallExpr)
				if !ok {
					continue
				}
				callIdent, ok := call.Fun.(*ast.Ident)
				if !ok {
					continue
				}
				callObj := info.Uses[callIdent]
				if callObj == nil {
					callObj = info.Defs[callIdent]
				}
				if callObj == nil || callObj.Pkg() != pkg {
					continue
				}
				if callObj.Name() == "readPlanFileBytes" {
					for i, lhs := range assign.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && readDataVar == nil {
							readDataVar = v
						}
						if i == 1 && isVar && readErrVar == nil {
							readErrVar = v
						}
					}
				}
			}
		}
		return true
	})
	if readErrVar == nil {
		t.Fatal("cannot resolve error variable from readPlanFileBytes")
	}
	if readDataVar == nil {
		t.Fatal("cannot resolve data variable from readPlanFileBytes")
	}

	readErrCheckFound := false
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == readErrVar {
						readErrCheckFound = true
						ast.Inspect(ifStmt.Body, func(bodyN ast.Node) bool {
							if ret, ok := bodyN.(*ast.ReturnStmt); ok {
								if len(ret.Results) != 2 {
									return false
								}
								if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
									t.Fatal("readPlanFileBytes error branch must return nil as first value")
								}
								if retID, ok := unparenExpr(ret.Results[1]).(*ast.Ident); ok {
									if obj := info.Uses[retID]; obj == nil || obj != readErrVar {
										t.Fatal("readPlanFileBytes error branch must return the same error object")
									}
								}
							}
							return true
						})
						return false
					}
				}
			}
		}
		return true
	})
	if !readErrCheckFound {
		t.Fatal("error from readPlanFileBytes must be checked immediately")
	}

	// ---------- 10. preparePlanBytes error gate ----------

	if prepareErrVar == nil {
		t.Fatal("cannot resolve error variable from preparePlanBytes")
	}
	prepareErrCheckFound := false
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == prepareErrVar {
						prepareErrCheckFound = true
						ast.Inspect(ifStmt.Body, func(bodyN ast.Node) bool {
							if ret, ok := bodyN.(*ast.ReturnStmt); ok {
								if len(ret.Results) != 2 {
									return false
								}
								if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
									t.Fatal("preparePlanBytes error branch must return nil as first value")
								}
								if retID, ok := unparenExpr(ret.Results[1]).(*ast.Ident); !ok {
									t.Fatal("preparePlanBytes error branch must return the error variable, not a call expression")
								} else if obj := info.Uses[retID]; obj == nil || obj != prepareErrVar {
									t.Fatal("preparePlanBytes error branch must return the same error object")
								}
							}
							return true
						})
						return false
					}
				}
			}
		}
		return true
	})
	if !prepareErrCheckFound {
		t.Fatal("error from preparePlanBytes must be checked immediately")
	}

	// ---------- 11. IsAuthorized mandatory ----------

	var isAuthorizedBlock *ast.IfStmt
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok && ifStmt.Pos() > arpCompositeLit.Pos() {
			if unary, ok := ifStmt.Cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
				if call, ok := unparenExpr(unary.X).(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if sel.Sel.Name == "IsAuthorized" {
							isAuthorizedBlock = ifStmt
							return false
						}
					}
				}
			}
		}
		return true
	})
	if isAuthorizedBlock == nil {
		t.Fatal("IsAuthorized call not found after composite literal")
	}

	var isAuthReturnFound bool
	ast.Inspect(isAuthorizedBlock.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			isAuthReturnFound = true
			if len(ret.Results) != 2 {
				t.Fatal("IsAuthorized error branch must return 2 values")
			}
			if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
				t.Fatal("IsAuthorized error branch must return nil as first value")
			}
			if call, ok := unparenExpr(ret.Results[1]).(*ast.CallExpr); ok {
				if callFn, ok := call.Fun.(*ast.Ident); ok {
					if obj := info.Uses[callFn]; obj == nil || obj.Name() != "newLoadError" {
						t.Fatal("IsAuthorized error branch must call newLoadError")
					}
				}
				if len(call.Args) == 1 {
					if argIdent, ok := unparenExpr(call.Args[0]).(*ast.Ident); ok {
						if argObj := info.Uses[argIdent]; argObj != nil {
							if argObj != pkg.Scope().Lookup("loadErrorInternalInvariant") {
								t.Fatal("IsAuthorized error branch must use loadErrorInternalInvariant")
							}
						}
					}
				}
			}
			return false
		}
		return true
	})
	if !isAuthReturnFound {
		t.Fatal("IsAuthorized branch must contain a return statement")
	}

	// ---------- 12. success return gate ----------

	{
		var lastReturn *ast.ReturnStmt
		ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok {
				lastReturn = ret
			}
			return true
		})
		if lastReturn == nil {
			t.Fatal("final return statement not found")
		}
		if len(lastReturn.Results) != 2 {
			t.Fatal("final return must have 2 values")
		}
		if id, ok := unparenExpr(lastReturn.Results[1]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
			t.Fatal("final return must have nil as second value")
		}
	}

	// ---------- 13. nil identity strict: reject typed nil ----------

	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			if len(ret.Results) == 2 {
				expr := unparenExpr(ret.Results[0])
				if call, ok := expr.(*ast.CallExpr); ok {
					t.Errorf("error-path return must use bare nil, not a type conversion or constructor (%T at pos %d)", call.Fun, call.Pos())
				}
			}
		}
		return true
	})

	// ---------- 14. no capability globals (*AuthorizedRepairPlan / AuthorizedRepairPlan) ----------

	arpTypeObj := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if arpTypeObj != nil {
		arpType, ok := arpTypeObj.Type().(*types.Named)
		if ok {
			for _, f := range astFiles {
				for _, decl := range f.Decls {
					gd, ok := decl.(*ast.GenDecl)
					if !ok || gd.Tok != token.VAR {
						continue
					}
					for _, spec := range gd.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, name := range vs.Names {
							obj := info.Defs[name]
							if obj == nil {
								continue
							}
							objType := obj.Type()
							if objType != nil {
								if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), arpType) {
									t.Errorf("global variable %s has forbidden type *AuthorizedRepairPlan", name.Name)
								}
								if types.Identical(objType, arpType) {
									t.Errorf("global variable %s has forbidden type AuthorizedRepairPlan", name.Name)
								}
							}
						}
					}
				}
			}
		}
	}

	// Verify no assignment to capability globals from within the loader
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil {
						if v, ok := obj.(*types.Var); ok {
							if v.Pkg() == pkg && v.Parent() == pkg.Scope() {
								if arpTypeObj != nil {
									arpType, ok := arpTypeObj.Type().(*types.Named)
									if ok {
										objType := v.Type()
										if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), arpType) {
											t.Errorf("loader assigns to global capability variable %s", v.Name())
										}
										if types.Identical(objType, arpType) {
											t.Errorf("loader assigns to global capability variable %s", v.Name())
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	// ---------- 15. error RHS must be predeclared nil ----------
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if rhsIdent, ok := unparenExpr(bin.Y).(*ast.Ident); ok {
					if lhsIdent, ok := unparenExpr(bin.X).(*ast.Ident); ok {
						lhsObj := info.Uses[lhsIdent]
						if lhsObj == nil {
							lhsObj = info.Defs[lhsIdent]
						}
						if lhsObj == readErrVar || lhsObj == prepareErrVar || lhsObj == digestErrVar {
							if !isPredeclaredNil(info, rhsIdent) {
								t.Fatalf("RHS of error check must be predeclared nil (error variable check)")
							}
						}
					}
				}
			}
		}
		return true
	})

	// ---------- 16. immediate adjacency for error checks ----------
	for idx, stmt := range arpDecl.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		var callName string
		for _, rhs := range assign.Rhs {
			call, ok := unparenExpr(rhs).(*ast.CallExpr)
			if !ok {
				continue
			}
			callIdent, ok := call.Fun.(*ast.Ident)
			if !ok {
				continue
			}
			callObj := info.Uses[callIdent]
			if callObj == nil {
				callObj = info.Defs[callIdent]
			}
			if callObj != nil && callObj.Pkg() == pkg {
				callName = callObj.Name()
				break
			}
		}
		if callName != "readPlanFileBytes" && callName != "preparePlanBytes" && callName != "parseApprovedPlanDigestStrict" {
			continue
		}
		if idx+1 >= len(arpDecl.Body.List) {
			t.Fatalf("call to %s must be immediately followed by error check", callName)
		}
		nextStmt := arpDecl.Body.List[idx+1]
		nextIf, ok := nextStmt.(*ast.IfStmt)
		if !ok {
			t.Fatalf("call to %s must be immediately followed by error check if-statement (got %T)", callName, nextStmt)
		}
		if bin, ok := nextIf.Cond.(*ast.BinaryExpr); !ok || bin.Op != token.NEQ {
			t.Fatalf("call to %s must be followed by != nil check", callName)
		}
	}

	// ---------- 17. immediate adjacency confirmed (no intervening operations possible) ----------
	// Section 16 already verifies the next statement after each call is its error check.
	// No operations can intervene between the call and its immediate error check.

	// ---------- 18. IsAuthorized method identity (*types.Func) ----------
	arpTypeObj2 := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if arpTypeObj2 == nil {
		t.Fatal("AuthorizedRepairPlan type not found")
	}
	arpType2, ok := arpTypeObj2.Type().(*types.Named)
	if !ok {
		t.Fatal("AuthorizedRepairPlan is not a named type")
	}
	ptrType2 := types.NewPointer(arpType2)
	isAuthMethodSet := types.NewMethodSet(ptrType2)
	isAuthSel := isAuthMethodSet.Lookup(pkg, "IsAuthorized")
	if isAuthSel == nil {
		t.Fatal("(*AuthorizedRepairPlan).IsAuthorized method not found")
	}
	isAuthFunc := isAuthSel.Obj().(*types.Func)

	var isAuthCallCount int
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "IsAuthorized" {
			calledObj := info.Uses[sel.Sel]
			if calledObj != nil && calledObj == isAuthFunc {
				isAuthCallCount++
			}
		}
		return true
	})
	if isAuthCallCount != 1 {
		t.Fatalf("IsAuthorized must be called exactly once (got %d)", isAuthCallCount)
	}

	// Track the plan variable from the composite literal assignment.
	var planVar *types.Var
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range assign.Rhs {
				rhs = unparenExpr(rhs)
				// Handle &AuthorizedRepairPlan{...}
				if unary, ok := rhs.(*ast.UnaryExpr); ok && unary.Op == token.AND {
					rhs = unparenExpr(unary.X)
				}
				cl, ok := rhs.(*ast.CompositeLit)
				if !ok {
					continue
				}
				clType := info.TypeOf(cl)
				if named, ok := derefNamed(clType); ok && named.Obj().Name() == "AuthorizedRepairPlan" {
					for _, lhs := range assign.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							if v, isVar := info.Defs[id].(*types.Var); isVar {
								planVar = v
								return false
							}
							if v, isVar := info.Uses[id].(*types.Var); isVar {
								planVar = v
								return false
							}
						}
					}
				}
			}
		}
		return true
	})
	if planVar == nil {
		t.Fatal("cannot resolve plan variable from composite literal assignment")
	}

	// Verify IsAuthorized receiver is the plan variable.
	var isAuthReceiverIdent *ast.Ident
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "IsAuthorized" {
			calledObj := info.Uses[sel.Sel]
			if calledObj != nil && calledObj == isAuthFunc {
				if id, ok := sel.X.(*ast.Ident); ok {
					isAuthReceiverIdent = id
				}
			}
		}
		return true
	})
	if isAuthReceiverIdent == nil {
		t.Fatal("IsAuthorized receiver not found")
	}
	receiverObj := info.Uses[isAuthReceiverIdent]
	if receiverObj == nil {
		receiverObj = info.Defs[isAuthReceiverIdent]
	}
	if receiverObj != planVar {
		t.Fatalf("IsAuthorized must be called on the plan variable from the composite literal, got %v", receiverObj)
	}

	// ---------- 19. final return provenance ----------
	{
		var lastRet *ast.ReturnStmt
		ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok {
				lastRet = ret
			}
			return true
		})
		if lastRet == nil || len(lastRet.Results) != 2 {
			t.Fatal("final return must have 2 values")
		}
		if retID, ok := unparenExpr(lastRet.Results[0]).(*ast.Ident); ok {
			retObj := info.Uses[retID]
			if retObj == nil {
				retObj = info.Defs[retID]
			}
			if retObj != planVar {
				t.Fatalf("final return must use the plan variable from the composite literal, got %v", retObj)
			}
		}
		if retNil, ok := unparenExpr(lastRet.Results[1]).(*ast.Ident); !ok || !isPredeclaredNil(info, retNil) {
			t.Fatal("final return error must be predeclared nil")
		}
	}

	// ---------- 20. global capability type equivalents ----------
	{
		arpType3 := arpType2
		arpUnderlying := arpType3.Underlying()
		for _, f := range astFiles {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						obj := info.Defs[name]
						if obj == nil {
							continue
						}
						objType := obj.Type()
						if objType == nil {
							continue
						}
						unalised := types.Unalias(objType)
						if ptr, ok := unalised.(*types.Pointer); ok {
							if types.Identical(ptr.Elem(), arpType3) ||
								(ptr.Elem().Underlying() != nil && types.Identical(ptr.Elem().Underlying(), arpUnderlying)) {
								t.Errorf("global variable %s has forbidden type *AuthorizedRepairPlan (or equivalent)", name.Name)
							}
						}
						if named, ok := unalised.(*types.Named); ok && named.Obj() != arpType3.Obj() {
							if named.Underlying() != nil && types.Identical(named.Underlying(), arpUnderlying) {
								t.Errorf("global variable %s has forbidden type equivalent to AuthorizedRepairPlan", name.Name)
							}
						}
						if types.Identical(unalised, arpType3) {
							t.Errorf("global variable %s has forbidden type AuthorizedRepairPlan", name.Name)
						}
					}
				}
			}
		}
	}
}

// ---------- authorized loader pipeline gate helper (used by mutation tests) ----------

func checkAuthorizedLoaderPipelineGate(sources []typedSource) error {
	_, astFiles, pkg, info, err := typeCheckSources(sources)
	if err != nil {
		return fmt.Errorf("type-check: %w", err)
	}

	arpObj := pkg.Scope().Lookup("LoadAuthorizedRepairPlan")
	if arpObj == nil {
		return fmt.Errorf("LoadAuthorizedRepairPlan not found")
	}
	arpFn, ok := arpObj.(*types.Func)
	if !ok {
		return fmt.Errorf("LoadAuthorizedRepairPlan is not a function")
	}
	if !arpFn.Exported() {
		return fmt.Errorf("LoadAuthorizedRepairPlan must be exported")
	}

	sig, ok := arpFn.Type().(*types.Signature)
	if !ok {
		return fmt.Errorf("signature not found")
	}
	if sig.Results().Len() != 2 {
		return fmt.Errorf("must return 2 values")
	}
	retType := sig.Results().At(0).Type()
	if ptr, ok := retType.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok {
			if named.Obj().Name() != "AuthorizedRepairPlan" {
				return fmt.Errorf("unexpected return type")
			}
		}
	} else {
		return fmt.Errorf("must return *AuthorizedRepairPlan")
	}

	// No other exported function returns *AuthorizedRepairPlan
	for _, name := range pkg.Scope().Names() {
		if name == "LoadAuthorizedRepairPlan" {
			continue
		}
		obj := pkg.Scope().Lookup(name)
		fn, isFunc := obj.(*types.Func)
		if !isFunc || !fn.Exported() {
			continue
		}
		fsig, ok := fn.Type().(*types.Signature)
		if !ok || fsig.Results().Len() == 0 {
			continue
		}
		rType := fsig.Results().At(0).Type()
		if ptr, ok := rType.(*types.Pointer); ok {
			if named, ok := ptr.Elem().(*types.Named); ok {
				if named.Obj().Name() == "AuthorizedRepairPlan" {
					return fmt.Errorf("%s also returns *AuthorizedRepairPlan", name)
				}
			}
		}
	}

	var arpDecl *ast.FuncDecl
	for _, f := range astFiles {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				if info.Defs[fd.Name] == arpFn {
					arpDecl = fd
					break
				}
			}
		}
	}
	if arpDecl == nil || arpDecl.Body == nil {
		return fmt.Errorf("LoadAuthorizedRepairPlan body not found")
	}

	readPlanFileBytesCount := 0
	preparePlanBytesCount := 0
	parseApprovedDigestCount := 0
	arpCompositeLitCount := 0
	var arpCompositeLit *ast.CompositeLit
	hasNilReturnOnError := false

	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if ident, ok := node.Fun.(*ast.Ident); ok {
				obj := info.Uses[ident]
				if obj == nil {
					obj = info.Defs[ident]
				}
				if obj != nil && obj.Pkg() == pkg {
					switch obj.Name() {
					case "readPlanFileBytes":
						readPlanFileBytesCount++
					case "preparePlanBytes":
						preparePlanBytesCount++
					case "parseApprovedPlanDigestStrict":
						parseApprovedDigestCount++
					}
				}
			}
		case *ast.CompositeLit:
			clType := info.TypeOf(node)
			if clType == nil {
				return true
			}
			if named, ok := derefNamed(clType); ok {
				if named.Obj().Name() == "AuthorizedRepairPlan" {
					arpCompositeLitCount++
					arpCompositeLit = node
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range node.Results {
				if ident, ok := expr.(*ast.Ident); ok && isPredeclaredNil(info, ident) {
					hasNilReturnOnError = true
				}
			}
		}
		return true
	})

	if readPlanFileBytesCount != 1 {
		return fmt.Errorf("expected 1 call to readPlanFileBytes, got %d", readPlanFileBytesCount)
	}
	if preparePlanBytesCount != 1 {
		return fmt.Errorf("expected 1 call to preparePlanBytes, got %d", preparePlanBytesCount)
	}
	if parseApprovedDigestCount != 1 {
		return fmt.Errorf("expected 1 call to parseApprovedPlanDigestStrict, got %d", parseApprovedDigestCount)
	}
	if arpCompositeLitCount != 1 {
		return fmt.Errorf("expected 1 AuthorizedRepairPlan composite literal, got %d", arpCompositeLitCount)
	}
	if arpCompositeLit == nil {
		return fmt.Errorf("AuthorizedRepairPlan composite literal not found")
	}
	if !hasNilReturnOnError {
		return fmt.Errorf("must return nil on error")
	}

	// semantic variable tracking
	var (
		stateVar      *types.Var
		calculatedVar *types.Var
		approvedVar   *types.Var
		prepareErrVar *types.Var
		digestErrVar  *types.Var
	)
	var mismatchIf *ast.IfStmt
	var mismatchReturn *ast.ReturnStmt

	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE && node.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range node.Rhs {
				call, ok := unparenExpr(rhs).(*ast.CallExpr)
				if !ok {
					continue
				}
				callIdent, ok := call.Fun.(*ast.Ident)
				if !ok {
					continue
				}
				callObj := info.Uses[callIdent]
				if callObj == nil {
					callObj = info.Defs[callIdent]
				}
				if callObj == nil || callObj.Pkg() != pkg {
					continue
				}
				switch callObj.Name() {
				case "preparePlanBytes":
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && stateVar == nil {
							stateVar = v
						}
						if i == 1 && isVar && calculatedVar == nil {
							calculatedVar = v
						}
						if i == 2 && isVar && prepareErrVar == nil {
							prepareErrVar = v
						}
					}
				case "parseApprovedPlanDigestStrict":
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && approvedVar == nil {
							approvedVar = v
						}
						if i == 1 && isVar && digestErrVar == nil {
							digestErrVar = v
						}
					}
				}
			}
		case *ast.IfStmt:
			if bin, ok := node.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				mismatchIf = node
			}
		}
		return true
	})

	if stateVar == nil {
		return fmt.Errorf("cannot resolve state variable from preparePlanBytes")
	}
	if calculatedVar == nil {
		return fmt.Errorf("cannot resolve calculated variable from preparePlanBytes")
	}
	if approvedVar == nil {
		return fmt.Errorf("cannot resolve approved variable from parseApprovedPlanDigestStrict")
	}
	if mismatchIf == nil {
		return fmt.Errorf("digest mismatch if-stmt not found")
	}

	// Find the return statement inside mismatchIf's body
	ast.Inspect(mismatchIf.Body, func(ifN ast.Node) bool {
		if ret, ok := ifN.(*ast.ReturnStmt); ok && mismatchReturn == nil {
			mismatchReturn = ret
			return false
		}
		return true
	})

	// order
	if arpCompositeLit.Pos() <= mismatchIf.Pos() {
		return fmt.Errorf("composite literal must appear after the digest comparison")
	}

	// comparison operands
	bin, ok := mismatchIf.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return fmt.Errorf("mismatch condition must be a != comparison")
	}
	lhsIdent, ok := unparenExpr(bin.X).(*ast.Ident)
	if !ok {
		return fmt.Errorf("LHS of mismatch must be an identifier")
	}
	lhsObj := info.Uses[lhsIdent]
	if lhsObj == nil {
		lhsObj = info.Defs[lhsIdent]
	}
	if lhsObj != calculatedVar {
		return fmt.Errorf("LHS of mismatch must be calculated from preparePlanBytes")
	}
	rhsCall, ok := unparenExpr(bin.Y).(*ast.CallExpr)
	if !ok {
		return fmt.Errorf("RHS of mismatch must be [32]byte conversion")
	}
	if len(rhsCall.Args) != 1 {
		return fmt.Errorf("RHS conversion must have 1 argument")
	}
	rhsArg, ok := unparenExpr(rhsCall.Args[0]).(*ast.Ident)
	if !ok {
		return fmt.Errorf("RHS argument must be approved identifier")
	}
	rhsArgObj := info.Uses[rhsArg]
	if rhsArgObj == nil {
		rhsArgObj = info.Defs[rhsArg]
	}
	if rhsArgObj != approvedVar {
		return fmt.Errorf("RHS of mismatch must be approved from parseApprovedPlanDigestStrict")
	}

	// mismatch return nil + mismatch error
	if mismatchReturn == nil {
		return fmt.Errorf("mismatch branch must return")
	}
	if len(mismatchReturn.Results) != 2 {
		return fmt.Errorf("mismatch return must have 2 values")
	}
	if id, ok := unparenExpr(mismatchReturn.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
		return fmt.Errorf("mismatch return first value must be nil")
	}

	// provenance of composite literal
	var compositeFieldsByKey = make(map[string]*ast.KeyValueExpr)
	for _, elt := range arpCompositeLit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok {
				compositeFieldsByKey[key.Name] = kv
			}
		}
	}
	// seal
	sealKV, hasSeal := compositeFieldsByKey["seal"]
	if !hasSeal {
		return fmt.Errorf("composite missing seal")
	}
	sealVal, ok := unparenExpr(sealKV.Value).(*ast.Ident)
	if !ok || info.Uses[sealVal] != pkg.Scope().Lookup("authorizedPlanSeal") {
		return fmt.Errorf("seal must be authorizedPlanSeal singleton")
	}
	// state
	stateKV, hasState := compositeFieldsByKey["state"]
	if !hasState {
		return fmt.Errorf("composite missing state")
	}
	stateUnary, ok := unparenExpr(stateKV.Value).(*ast.UnaryExpr)
	if !ok || stateUnary.Op != token.AND {
		return fmt.Errorf("state must be &state address-of")
	}
	stateIdent, ok := unparenExpr(stateUnary.X).(*ast.Ident)
	if !ok {
		return fmt.Errorf("state address-of operand must be ident")
	}
	stateIdentObj := info.Uses[stateIdent]
	if stateIdentObj == nil {
		stateIdentObj = info.Defs[stateIdent]
	}
	if stateIdentObj != stateVar {
		return fmt.Errorf("state field must be &state from preparePlanBytes")
	}
	// digest
	digestKV, hasDigest := compositeFieldsByKey["digest"]
	if !hasDigest {
		return fmt.Errorf("composite missing digest")
	}
	digestIdent, ok := unparenExpr(digestKV.Value).(*ast.Ident)
	if !ok {
		return fmt.Errorf("digest field must be ident")
	}
	digestIdentObj := info.Uses[digestIdent]
	if digestIdentObj == nil {
		digestIdentObj = info.Defs[digestIdent]
	}
	if digestIdentObj != calculatedVar {
		return fmt.Errorf("digest field must be calculated from preparePlanBytes")
	}

	// digest parser error must be checked
	if digestErrVar == nil {
		return fmt.Errorf("cannot resolve error var from parseApprovedPlanDigestStrict")
	}
	digestErrCheckFound := false
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok && ifStmt.Pos() < mismatchIf.Pos() {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == digestErrVar {
						digestErrCheckFound = true
					}
				}
			}
		}
		return true
	})
	if !digestErrCheckFound {
		return fmt.Errorf("error from parseApprovedPlanDigestStrict must be checked")
	}

	// no globals of approvedPlanDigest type
	digestTypeObj := pkg.Scope().Lookup("approvedPlanDigest")
	if digestTypeObj != nil {
		digestType, ok := digestTypeObj.Type().(*types.Named)
		if !ok {
			return fmt.Errorf("approvedPlanDigest is not a named type")
		}
		for _, f := range astFiles {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						obj := info.Defs[name]
						if obj == nil {
							continue
						}
						objType := obj.Type()
						if objType != nil && types.Identical(objType, digestType) {
							return fmt.Errorf("global %s has forbidden type approvedPlanDigest", name.Name)
						}
						if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), digestType) {
							return fmt.Errorf("global %s has forbidden type *approvedPlanDigest", name.Name)
						}
					}
				}
			}
		}
	}

	// no extra composite literals in other production functions
	for _, f := range astFiles {
		ast.Inspect(f, func(n ast.Node) bool {
			if node, ok := n.(*ast.FuncDecl); ok && node != arpDecl && node.Body != nil {
				ast.Inspect(node.Body, func(m ast.Node) bool {
					if cl, ok := m.(*ast.CompositeLit); ok {
						clType := info.TypeOf(cl)
						if named, ok := derefNamed(clType); ok {
							if named.Obj().Name() == "AuthorizedRepairPlan" {
								return false // signal to stop inspection
							}
						}
					}
					return true
				})
			}
			return true
		})
	}
	// Verify exact count after inspection
	extraLitCount := 0
	for _, f := range astFiles {
		ast.Inspect(f, func(n ast.Node) bool {
			if node, ok := n.(*ast.FuncDecl); ok && node != arpDecl && node.Body != nil {
				ast.Inspect(node.Body, func(m ast.Node) bool {
					if cl, ok := m.(*ast.CompositeLit); ok {
						clType := info.TypeOf(cl)
						if named, ok := derefNamed(clType); ok {
							if named.Obj().Name() == "AuthorizedRepairPlan" {
								extraLitCount++
							}
						}
					}
					return true
				})
			}
			return true
		})
	}
	if extraLitCount > 0 {
		return fmt.Errorf("AuthorizedRepairPlan composite literal in unexpected function (count=%d)", extraLitCount)
	}

	// ---------- mismatch error identity: loadErrorApprovedDigestMismatch ----------
	{
		mismatchErrCall, ok := unparenExpr(mismatchReturn.Results[1]).(*ast.CallExpr)
		if !ok {
			return fmt.Errorf("mismatch return second value must be a call")
		}
		if len(mismatchErrCall.Args) != 1 {
			return fmt.Errorf("mismatch error call must have 1 argument")
		}
		mismatchErrArg, ok := unparenExpr(mismatchErrCall.Args[0]).(*ast.Ident)
		if !ok {
			return fmt.Errorf("mismatch error argument must be an identifier")
		}
		mismatchErrArgObj := info.Uses[mismatchErrArg]
		if mismatchErrArgObj == nil {
			mismatchErrArgObj = info.Defs[mismatchErrArg]
		}
		if mismatchErrArgObj != pkg.Scope().Lookup("loadErrorApprovedDigestMismatch") {
			return fmt.Errorf("mismatch error must be loadErrorApprovedDigestMismatch")
		}
	}

	// ---------- readPlanFileBytes error gate ----------
	var (
		readDataVar *types.Var
		readErrVar  *types.Var
	)
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range assign.Rhs {
				call, ok := unparenExpr(rhs).(*ast.CallExpr)
				if !ok {
					continue
				}
				callIdent, ok := call.Fun.(*ast.Ident)
				if !ok {
					continue
				}
				callObj := info.Uses[callIdent]
				if callObj == nil {
					callObj = info.Defs[callIdent]
				}
				if callObj == nil || callObj.Pkg() != pkg {
					continue
				}
				if callObj.Name() == "readPlanFileBytes" {
					for i, lhs := range assign.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, isVar := info.Defs[id].(*types.Var)
						if !isVar {
							v, isVar = info.Uses[id].(*types.Var)
						}
						if i == 0 && isVar && readDataVar == nil {
							readDataVar = v
						}
						if i == 1 && isVar && readErrVar == nil {
							readErrVar = v
						}
					}
				}
			}
		}
		return true
	})
	if readErrVar == nil {
		return fmt.Errorf("cannot resolve error variable from readPlanFileBytes")
	}
	if readDataVar == nil {
		return fmt.Errorf("cannot resolve data variable from readPlanFileBytes")
	}

	readErrCheckFound := false
	var readErrGateErr error
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == readErrVar {
						readErrCheckFound = true
						ast.Inspect(ifStmt.Body, func(bodyN ast.Node) bool {
							if ret, ok := bodyN.(*ast.ReturnStmt); ok {
								if len(ret.Results) != 2 {
									readErrGateErr = fmt.Errorf("readPlanFileBytes error branch must return 2 values")
									return false
								}
								if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
									readErrGateErr = fmt.Errorf("readPlanFileBytes error branch must return nil as first value")
									return false
								}
								if retID, ok := unparenExpr(ret.Results[1]).(*ast.Ident); ok {
									if obj := info.Uses[retID]; obj == nil || obj != readErrVar {
										readErrGateErr = fmt.Errorf("readPlanFileBytes error branch must return the same error object")
										return false
									}
								}
							}
							return true
						})
						return false
					}
				}
			}
		}
		return true
	})
	if readErrGateErr != nil {
		return readErrGateErr
	}
	if !readErrCheckFound {
		return fmt.Errorf("error from readPlanFileBytes must be checked immediately")
	}

	// ---------- preparePlanBytes error gate ----------
	if prepareErrVar == nil {
		return fmt.Errorf("cannot resolve error variable from preparePlanBytes")
	}
	prepareErrCheckFound := false
	var prepareErrGateErr error
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := unparenExpr(bin.X).(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil && obj == prepareErrVar {
						prepareErrCheckFound = true
						ast.Inspect(ifStmt.Body, func(bodyN ast.Node) bool {
							if ret, ok := bodyN.(*ast.ReturnStmt); ok {
								if len(ret.Results) != 2 {
									prepareErrGateErr = fmt.Errorf("preparePlanBytes error branch must return 2 values")
									return false
								}
								if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
									prepareErrGateErr = fmt.Errorf("preparePlanBytes error branch must return nil as first value")
									return false
								}
								if retID, ok := unparenExpr(ret.Results[1]).(*ast.Ident); !ok {
									prepareErrGateErr = fmt.Errorf("preparePlanBytes error branch must return the error variable, not a call expression")
									return false
								} else if obj := info.Uses[retID]; obj == nil || obj != prepareErrVar {
									prepareErrGateErr = fmt.Errorf("preparePlanBytes error branch must return the same error object")
									return false
								}
							}
							return true
						})
						return false
					}
				}
			}
		}
		return true
	})
	if prepareErrGateErr != nil {
		return prepareErrGateErr
	}
	if !prepareErrCheckFound {
		return fmt.Errorf("error from preparePlanBytes must be checked immediately")
	}

	// ---------- IsAuthorized mandatory ----------
	var isAuthorizedBlock *ast.IfStmt
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok && ifStmt.Pos() > arpCompositeLit.Pos() {
			if unary, ok := ifStmt.Cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
				if call, ok := unparenExpr(unary.X).(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if sel.Sel.Name == "IsAuthorized" {
							isAuthorizedBlock = ifStmt
							return false
						}
					}
				}
			}
		}
		return true
	})
	if isAuthorizedBlock == nil {
		return fmt.Errorf("IsAuthorized call not found after composite literal")
	}

	var isAuthReturnFound bool
	ast.Inspect(isAuthorizedBlock.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			isAuthReturnFound = true
			if len(ret.Results) != 2 {
				return false
			}
			if id, ok := unparenExpr(ret.Results[0]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
				return false
			}
			if call, ok := unparenExpr(ret.Results[1]).(*ast.CallExpr); ok {
				if callFn, ok := call.Fun.(*ast.Ident); ok {
					if obj := info.Uses[callFn]; obj == nil || obj.Name() != "newLoadError" {
						return false
					}
				}
				if len(call.Args) == 1 {
					if argIdent, ok := unparenExpr(call.Args[0]).(*ast.Ident); ok {
						if argObj := info.Uses[argIdent]; argObj != nil {
							if argObj != pkg.Scope().Lookup("loadErrorInternalInvariant") {
								return false
							}
						}
					}
				}
			}
			return false
		}
		return true
	})
	if !isAuthReturnFound {
		return fmt.Errorf("IsAuthorized branch must return nil + newLoadError(loadErrorInternalInvariant)")
	}

	// ---------- success return gate ----------
	{
		var lastReturn *ast.ReturnStmt
		ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok {
				lastReturn = ret
			}
			return true
		})
		if lastReturn == nil {
			return fmt.Errorf("final return statement not found")
		}
		if len(lastReturn.Results) != 2 {
			return fmt.Errorf("final return must have 2 values")
		}
		if id, ok := unparenExpr(lastReturn.Results[1]).(*ast.Ident); !ok || !isPredeclaredNil(info, id) {
			return fmt.Errorf("final return must have nil as second value")
		}
	}

	// ---------- nil identity strict: reject typed nil ----------
	typedNilFound := false
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			if len(ret.Results) == 2 {
				expr := unparenExpr(ret.Results[0])
				if _, ok := expr.(*ast.CallExpr); ok {
					typedNilFound = true
					return false
				}
			}
		}
		return true
	})
	if typedNilFound {
		return fmt.Errorf("error-path return must use bare nil, not a type conversion or constructor")
	}

	// ---------- no capability globals (*AuthorizedRepairPlan / AuthorizedRepairPlan) ----------
	arpTypeObj := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if arpTypeObj != nil {
		arpType, ok := arpTypeObj.Type().(*types.Named)
		if ok {
			for _, f := range astFiles {
				for _, decl := range f.Decls {
					gd, ok := decl.(*ast.GenDecl)
					if !ok || gd.Tok != token.VAR {
						continue
					}
					for _, spec := range gd.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, name := range vs.Names {
							obj := info.Defs[name]
							if obj == nil {
								continue
							}
							objType := obj.Type()
							if objType != nil {
								if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), arpType) {
									return fmt.Errorf("global %s has forbidden type *AuthorizedRepairPlan", name.Name)
								}
								if types.Identical(objType, arpType) {
									return fmt.Errorf("global %s has forbidden type AuthorizedRepairPlan", name.Name)
								}
							}
						}
					}
				}
			}
		}
	}

	// Verify no assignment to capability globals from within the loader
	var globalAssignErr error
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					if obj := info.Uses[id]; obj != nil {
						if v, ok := obj.(*types.Var); ok {
							if v.Pkg() == pkg && v.Parent() == pkg.Scope() {
								if arpTypeObj != nil {
									arpType, ok := arpTypeObj.Type().(*types.Named)
									if ok {
										objType := v.Type()
										if ptr, ok := objType.(*types.Pointer); ok && types.Identical(ptr.Elem(), arpType) {
											globalAssignErr = fmt.Errorf("loader assigns to global capability variable %s", v.Name())
											return false
										}
										if types.Identical(objType, arpType) {
											globalAssignErr = fmt.Errorf("loader assigns to global capability variable %s", v.Name())
											return false
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return true
	})
	if globalAssignErr != nil {
		return globalAssignErr
	}

	// ---------- error RHS must be predeclared nil ----------
	var rhsNilErr error
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if rhsIdent, ok := unparenExpr(bin.Y).(*ast.Ident); ok {
					if lhsIdent, ok := unparenExpr(bin.X).(*ast.Ident); ok {
						lhsObj := info.Uses[lhsIdent]
						if lhsObj == nil {
							lhsObj = info.Defs[lhsIdent]
						}
						if lhsObj == readErrVar || lhsObj == prepareErrVar || lhsObj == digestErrVar {
							if !isPredeclaredNil(info, rhsIdent) {
								rhsNilErr = fmt.Errorf("RHS of error check must be predeclared nil")
								return false
							}
						}
					}
				}
			}
		}
		return true
	})
	if rhsNilErr != nil {
		return rhsNilErr
	}

	// ---------- immediate adjacency for error checks ----------
	for idx, stmt := range arpDecl.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		var callName string
		for _, rhs := range assign.Rhs {
			call, ok := unparenExpr(rhs).(*ast.CallExpr)
			if !ok {
				continue
			}
			callIdent, ok := call.Fun.(*ast.Ident)
			if !ok {
				continue
			}
			callObj := info.Uses[callIdent]
			if callObj == nil {
				callObj = info.Defs[callIdent]
			}
			if callObj != nil && callObj.Pkg() == pkg {
				callName = callObj.Name()
				break
			}
		}
		if callName != "readPlanFileBytes" && callName != "preparePlanBytes" && callName != "parseApprovedPlanDigestStrict" {
			continue
		}
		if idx+1 >= len(arpDecl.Body.List) {
			return fmt.Errorf("call to %s must be immediately followed by error check", callName)
		}
		nextStmt := arpDecl.Body.List[idx+1]
		nextIf, ok := nextStmt.(*ast.IfStmt)
		if !ok {
			return fmt.Errorf("call to %s must be immediately followed by error check if-statement", callName)
		}
		if bin, ok := nextIf.Cond.(*ast.BinaryExpr); !ok || bin.Op != token.NEQ {
			return fmt.Errorf("call to %s must be followed by != nil check", callName)
		}
	}

	// ---------- IsAuthorized method identity (*types.Func) ----------
	arpTypeObjH := pkg.Scope().Lookup("AuthorizedRepairPlan")
	if arpTypeObjH == nil {
		return fmt.Errorf("AuthorizedRepairPlan type not found")
	}
	arpTypeH, ok := arpTypeObjH.Type().(*types.Named)
	if !ok {
		return fmt.Errorf("AuthorizedRepairPlan is not a named type")
	}
	ptrTypeH := types.NewPointer(arpTypeH)
	isAuthMethodSetH := types.NewMethodSet(ptrTypeH)
	isAuthSelH := isAuthMethodSetH.Lookup(pkg, "IsAuthorized")
	if isAuthSelH == nil {
		return fmt.Errorf("(*AuthorizedRepairPlan).IsAuthorized method not found")
	}
	isAuthFuncH := isAuthSelH.Obj().(*types.Func)

	var isAuthCallCountH int
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "IsAuthorized" {
			calledObj := info.Uses[sel.Sel]
			if calledObj != nil && calledObj == isAuthFuncH {
				isAuthCallCountH++
			}
		}
		return true
	})
	if isAuthCallCountH != 1 {
		return fmt.Errorf("IsAuthorized must be called exactly once (got %d)", isAuthCallCountH)
	}

	// Track the plan variable from the composite literal assignment.
	var planVarH *types.Var
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
				return true
			}
			for _, rhs := range assign.Rhs {
				rhs = unparenExpr(rhs)
				if unary, ok := rhs.(*ast.UnaryExpr); ok && unary.Op == token.AND {
					rhs = unparenExpr(unary.X)
				}
				cl, ok := rhs.(*ast.CompositeLit)
				if !ok {
					continue
				}
				clType := info.TypeOf(cl)
				if named, ok := derefNamed(clType); ok && named.Obj().Name() == "AuthorizedRepairPlan" {
					for _, lhs := range assign.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							if v, isVar := info.Defs[id].(*types.Var); isVar {
								planVarH = v
								return false
							}
							if v, isVar := info.Uses[id].(*types.Var); isVar {
								planVarH = v
								return false
							}
						}
					}
				}
			}
		}
		return true
	})
	if planVarH == nil {
		return fmt.Errorf("cannot resolve plan variable from composite literal assignment")
	}

	// Verify IsAuthorized receiver is the plan variable.
	var receiverErr error
	ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "IsAuthorized" {
			calledObj := info.Uses[sel.Sel]
			if calledObj != nil && calledObj == isAuthFuncH {
				if id, ok := sel.X.(*ast.Ident); ok {
					receiverObj := info.Uses[id]
					if receiverObj == nil {
						receiverObj = info.Defs[id]
					}
					if receiverObj != planVarH {
						receiverErr = fmt.Errorf("IsAuthorized must be called on the plan variable from the composite literal, got %v", receiverObj)
						return false
					}
				}
			}
		}
		return true
	})
	if receiverErr != nil {
		return receiverErr
	}

	// ---------- final return provenance ----------
	{
		var lastRet *ast.ReturnStmt
		ast.Inspect(arpDecl.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok {
				lastRet = ret
			}
			return true
		})
		if lastRet == nil || len(lastRet.Results) != 2 {
			return fmt.Errorf("final return must have 2 values")
		}
		if retID, ok := unparenExpr(lastRet.Results[0]).(*ast.Ident); ok {
			retObj := info.Uses[retID]
			if retObj == nil {
				retObj = info.Defs[retID]
			}
			if retObj != planVarH {
				return fmt.Errorf("final return must use the plan variable from the composite literal, got %v", retObj)
			}
		}
		if retNil, ok := unparenExpr(lastRet.Results[1]).(*ast.Ident); !ok || !isPredeclaredNil(info, retNil) {
			return fmt.Errorf("final return error must be predeclared nil")
		}
	}

	// ---------- global capability type equivalents ----------
	{
		arpUnderlyingH := arpTypeH.Underlying()
		for _, f := range astFiles {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						obj := info.Defs[name]
						if obj == nil {
							continue
						}
						objType := obj.Type()
						if objType == nil {
							continue
						}
						unalised := types.Unalias(objType)
						if ptr, ok := unalised.(*types.Pointer); ok {
							if types.Identical(ptr.Elem(), arpTypeH) ||
								(ptr.Elem().Underlying() != nil && types.Identical(ptr.Elem().Underlying(), arpUnderlyingH)) {
								return fmt.Errorf("global %s has forbidden type *AuthorizedRepairPlan (or equivalent)", name.Name)
							}
						}
						if named, ok := unalised.(*types.Named); ok && named.Obj() != arpTypeH.Obj() {
							if named.Underlying() != nil && types.Identical(named.Underlying(), arpUnderlyingH) {
								return fmt.Errorf("global %s has forbidden type equivalent to AuthorizedRepairPlan", name.Name)
							}
						}
						if types.Identical(unalised, arpTypeH) {
							return fmt.Errorf("global %s has forbidden type AuthorizedRepairPlan", name.Name)
						}
					}
				}
			}
		}
	}

	return nil
}

func derefNamed(t types.Type) (*types.Named, bool) {
	u := types.Unalias(t)
	if p, ok := u.(*types.Pointer); ok {
		u = types.Unalias(p.Elem())
	}
	n, ok := u.(*types.Named)
	return n, ok
}

// ---------- mutation matrix helpers ----------

func mutateProductionSource(transform func(string) string) ([]typedSource, error) {
	prodFiles, err := loadProductionFiles()
	if err != nil {
		return nil, err
	}
	for i, f := range prodFiles {
		if f.name == "load_authorized.go" {
			data, err := os.ReadFile(f.name)
			if err != nil {
				return nil, err
			}
			mutated := transform(string(data))
			if mutated == string(data) {
				return nil, fmt.Errorf("transform did not change the source")
			}
			prodFiles[i].src = mutated
			return prodFiles, nil
		}
	}
	return nil, fmt.Errorf("load_authorized.go not found in production files")
}

func assertLoaderGateFailsWith(t *testing.T, transform func(string) string, wantSubstr string) {
	t.Helper()
	sources, err := mutateProductionSource(transform)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	err = checkAuthorizedLoaderPipelineGate(sources)
	if err == nil {
		t.Fatalf("expected gate to fail, but it passed (expected %q)", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("wrong gate failure:\n  want contains: %q\n  got: %q", wantSubstr, err.Error())
	}
}

// ---------- mutation A: literal before comparison ----------

func TestArchitectureAuthorizedLoaderMutationA(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	if calculated != [32]byte(approved) {
		return nil, newLoadError(loadErrorApprovedDigestMismatch)
	}

	plan := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &state,
		digest: calculated,
	}`
		new := `	plan := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &state,
		digest: calculated,
	}

	if calculated != [32]byte(approved) {
		return nil, newLoadError(loadErrorApprovedDigestMismatch)
	}`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "composite literal must appear after")
}

// ---------- mutation B: != swapped to == ----------

func TestArchitectureAuthorizedLoaderMutationB(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		return mustReplaceExactlyOnce(t, src, "calculated !=", "calculated ==","replace")
	}, "LHS of mismatch must be calculated")
}

// ---------- mutation C: approved digest stored in field ----------

func TestArchitectureAuthorizedLoaderMutationC(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `digest: calculated,`
		new := `digest: [32]byte(approved),`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "digest field must be")
}

// ---------- mutation D: other state in field ----------

func TestArchitectureAuthorizedLoaderMutationD(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, err
	}

	approved, err := parseApprovedPlanDigestStrict(approvedDigest)`
		new := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, err
	}
	_ = state

	approved, err := parseApprovedPlanDigestStrict(approvedDigest)`
		src = mustReplaceExactlyOnce(t, src, old, new,"replace")

		oldLit := `state:  &state,`
		newLit := `state:  &authorizedPlanState{},`
		return mustReplaceExactlyOnce(t, src, oldLit, newLit,"replace")
	}, "state address-of operand must be")
}

// ---------- mutation E: return capability on mismatch ----------

func TestArchitectureAuthorizedLoaderMutationE(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		oldRet := `return nil, newLoadError(loadErrorApprovedDigestMismatch)`
		newRet := `return (*AuthorizedRepairPlan)(nil), newLoadError(loadErrorApprovedDigestMismatch)`
		return mustReplaceExactlyOnce(t, src, oldRet, newRet,"replace")
	}, "mismatch return first value must be nil")
}

// ---------- mutation F: helper that builds capability ----------

func TestArchitectureAuthorizedLoaderMutationF(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		return src + `

func forgedBuilder(path, approvedDigest string) *AuthorizedRepairPlan {
	return &AuthorizedRepairPlan{seal: authorizedPlanSeal, digest: [32]byte{}}
}
`
	}, "AuthorizedRepairPlan composite literal in unexpected function")
}

// ---------- mutation G: raw SHA-256 comparison ----------

func TestArchitectureAuthorizedLoaderMutationG(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		oldImport := `import (
	"encoding/hex"
	"strings"
)`
		newImport := `import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)`
		src = mustReplaceExactlyOnce(t, src, oldImport, newImport,"replace")

		oldBody := `	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {
		return nil, err
	}

	if calculated != [32]byte(approved) {`
		newBody := `	h := sha256.Sum256(data)
	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {
		return nil, err
	}

	if h != [32]byte(approved) {`
		return mustReplaceExactlyOnce(t, src, oldBody, newBody,"replace")
	}, "LHS of mismatch must be calculated")
}

// ---------- mutation H: ignore parser error ----------

func TestArchitectureAuthorizedLoaderMutationH(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {
		return nil, err
	}

	if calculated !=`
		new := `	approved, _ := parseApprovedPlanDigestStrict(approvedDigest)

	if calculated !=`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "error from parseApprovedPlanDigestStrict must be checked")
}

// ---------- mutation I: global approvedPlanDigest ----------

func TestArchitectureAuthorizedLoaderMutationI(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `type approvedPlanDigest [32]byte`
		new := `var forbiddenGlobal approvedPlanDigest

type approvedPlanDigest [32]byte`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "global forbiddenGlobal has forbidden type approvedPlanDigest")
}

// ---------- mutation J: second composite literal in other function ----------

func TestArchitectureAuthorizedLoaderMutationJ(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		return src + `

func planForPath(path string) *AuthorizedRepairPlan {
	p := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &authorizedPlanState{},
		digest: [32]byte{},
	}
	return p
}
`
	}, "AuthorizedRepairPlan composite literal in unexpected function")
}

// ---------- mutation K: mismatch uses loadErrorInternalInvariant ----------

func TestArchitectureAuthorizedLoaderMutationK(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `return nil, newLoadError(loadErrorApprovedDigestMismatch)`
		new := `return nil, newLoadError(loadErrorInternalInvariant)`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "mismatch error must be loadErrorApprovedDigestMismatch")
}

// ---------- mutation L: remove IsAuthorized block ----------

func TestArchitectureAuthorizedLoaderMutationL(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := "	if !plan.IsAuthorized() {\n		return nil, newLoadError(loadErrorInternalInvariant)\n	}\n"
		return mustReplaceExactlyOnce(t, src, old, "","replace")
	}, "IsAuthorized")
}

// ---------- mutation M: ignore error from readPlanFileBytes ----------

func TestArchitectureAuthorizedLoaderMutationM(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	data, err := readPlanFileBytes(path)
	if err != nil {
		return nil, err
	}`
		new := `	data, _ := readPlanFileBytes(path)`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "readPlanFileBytes")
}

// ---------- mutation N: ignore error from preparePlanBytes ----------

func TestArchitectureAuthorizedLoaderMutationN(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, err
	}`
		new := `	state, calculated, _ := preparePlanBytes(data)`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "preparePlanBytes")
}

// ---------- mutation O: global *AuthorizedRepairPlan assigned by loader ----------

func TestArchitectureAuthorizedLoaderMutationO(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		oldImport := `import (
	"encoding/hex"
	"strings"
)`
		newImport := `import (
	"encoding/hex"
	"strings"
)

var leakedAuthorizedPlan *AuthorizedRepairPlan
`
		src = mustReplaceExactlyOnce(t, src, oldImport, newImport,"replace")

		oldRet := `	return plan, nil`
		newRet := `	leakedAuthorizedPlan = plan
	return plan, nil`
		return mustReplaceExactlyOnce(t, src, oldRet, newRet,"replace")
	}, "AuthorizedRepairPlan")
}

// ---------- mutation P: mismatch returns new(AuthorizedRepairPlan) ----------

func TestArchitectureAuthorizedLoaderMutationP(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `return nil, newLoadError(loadErrorApprovedDigestMismatch)`
		new := `return new(AuthorizedRepairPlan), newLoadError(loadErrorApprovedDigestMismatch)`
		return mustReplaceExactlyOnce(t, src, old, new,"replace")
	}, "mismatch return first value must be nil")
}

// ---------- mutation Q: use data before readFile error check ----------

func TestArchitectureAuthorizedLoaderMutationQ(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	data, err := readPlanFileBytes(path)
	if err != nil {`
		new := `	data, err := readPlanFileBytes(path)
	_ = data
	if err != nil {`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "readPlanFileBytes")
}

// ---------- mutation R: use state before preparePlanBytes error check ----------

func TestArchitectureAuthorizedLoaderMutationR(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {`
		new := `	state, calculated, err := preparePlanBytes(data)
	_ = state
	if err != nil {`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "preparePlanBytes")
}

// ---------- mutation S: preparePlanBytes error branch returns wrong error ----------

func TestArchitectureAuthorizedLoaderMutationS(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, err
	}`
		new := `	state, calculated, err := preparePlanBytes(data)
	if err != nil {
		return nil, newLoadError(loadErrorInternalInvariant)
	}`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "preparePlanBytes error branch must return the error variable, not a call expression")
}

// ---------- mutation T: use approved before digest parser error check ----------

func TestArchitectureAuthorizedLoaderMutationT(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {`
		new := `	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	_ = approved
	if err != nil {`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "parseApprovedPlanDigestStrict")
}

// ---------- mutation U: IsAuthorized called on another object ----------

func TestArchitectureAuthorizedLoaderMutationU(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	plan := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &state,
		digest: calculated,
	}

	if !plan.IsAuthorized() {`
		new := `	plan := &AuthorizedRepairPlan{
		seal:   authorizedPlanSeal,
		state:  &state,
		digest: calculated,
	}
	other := plan

	if !other.IsAuthorized() {`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "IsAuthorized must be called on the plan variable")
}

// ---------- mutation V: IsAuthorized called twice ----------

func TestArchitectureAuthorizedLoaderMutationV(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	if !plan.IsAuthorized() {
		return nil, newLoadError(loadErrorInternalInvariant)
	}

	return plan, nil`
		new := `	if !plan.IsAuthorized() {
		return nil, newLoadError(loadErrorInternalInvariant)
	}
	if !plan.IsAuthorized() {
		return nil, newLoadError(loadErrorInternalInvariant)
	}

	return plan, nil`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "IsAuthorized must be called exactly once")
}

// ---------- mutation W: final return uses different pointer ----------

func TestArchitectureAuthorizedLoaderMutationW(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	return plan, nil`
		new := `	return &AuthorizedRepairPlan{seal: authorizedPlanSeal, state: &state, digest: calculated}, nil`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "AuthorizedRepairPlan")
}

// ---------- mutation X: local variable named nil shadows predeclared nil ----------

func TestArchitectureAuthorizedLoaderMutationX(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {`
		new := `	nil := "shadow"
	_ = nil
	approved, err := parseApprovedPlanDigestStrict(approvedDigest)
	if err != nil {`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "nil")
}

// ---------- mutation Y: global with type equivalent to AuthorizedRepairPlan ----------

func TestArchitectureAuthorizedLoaderMutationY(t *testing.T) {
	assertLoaderGateFailsWith(t, func(src string) string {
		old := `type approvedPlanDigest [32]byte`
		new := `type aliasAuthorizedRepairPlan struct {
	seal   *authorizationSeal
	state  *authorizedPlanState
	digest [32]byte
}

var forbiddenAlias aliasAuthorizedRepairPlan

type approvedPlanDigest [32]byte`
		return mustReplaceExactlyOnce(t, src, old, new, "replace")
	}, "equivalent")
}
