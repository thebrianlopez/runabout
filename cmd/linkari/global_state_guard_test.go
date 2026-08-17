package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EPIC-258 guard: package-level function seams and test writes to package globals
// must not grow while the remaining legacy seams are threaded through explicit
// dependencies. This is intentionally AST-based so a renamed or newly-added seam
// fails by property, not by a grep for historical symbol names.
func TestEPIC258GlobalStateGuard(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	packageVars := map[string]bool{}
	funcVars := map[string]string{}
	var testFiles, srcFiles []*ast.File
	for _, path := range files {
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, parsed)
			continue
		}
		srcFiles = append(srcFiles, parsed)
	}

	// Pass 1: collect names that make a var func-typed by reference rather than
	// by syntax - named func types (`type tsnetStartFunc func(...)`) and
	// top-level funcs usable as an initializer (`var f = realTsnetStart`).
	// Without this pass both forms are invisible to the classifier below, which
	// is how the tsnetStart seam went uncounted until EPIC-258 M2.
	funcTypeNames := map[string]bool{}
	funcDeclNames := map[string]bool{}
	for _, file := range srcFiles {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcDeclNames[d.Name.Name] = true
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, isFunc := ts.Type.(*ast.FuncType); isFunc {
						funcTypeNames[ts.Name.Name] = true
					}
				}
			}
		}
	}

	// Pass 2: classify package vars.
	isFuncTyped := func(expr ast.Expr) bool {
		switch t := expr.(type) {
		case *ast.FuncType:
			return true
		case *ast.Ident:
			return funcTypeNames[t.Name]
		}
		return false
	}
	isFuncValued := func(expr ast.Expr) bool {
		switch v := expr.(type) {
		case *ast.FuncLit:
			return true
		case *ast.Ident:
			return funcDeclNames[v.Name]
		}
		return false
	}
	for _, file := range srcFiles {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					packageVars[name.Name] = true
					if vs.Type != nil && isFuncTyped(vs.Type) {
						funcVars[name.Name] = fset.Position(name.Pos()).String()
						continue
					}
					if i < len(vs.Values) && isFuncValued(vs.Values[i]) {
						funcVars[name.Name] = fset.Position(name.Pos()).String()
					}
				}
			}
		}
	}

	if got := unexpectedKeys(funcVars, legacyFuncVarAllowlist); len(got) > 0 {
		t.Fatalf("package-level function vars are forbidden; thread dependencies instead:\n%s", strings.Join(got, "\n"))
	}

	writes := map[string]string{}
	for _, file := range testFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				if x.Tok == token.DEFINE {
					// := declares new locals; a name matching a package var is a
					// shadow, not a write.
					break
				}
				for _, lhs := range x.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" && packageVars[ident.Name] {
						writes[ident.Name] = fset.Position(ident.Pos()).String()
					}
				}
			case *ast.IncDecStmt:
				if ident, ok := x.X.(*ast.Ident); ok && ident.Name != "_" && packageVars[ident.Name] {
					writes[ident.Name] = fset.Position(ident.Pos()).String()
				}
			}
			return true
		})
	}
	if got := unexpectedKeys(writes, legacyTestGlobalWriteAllowlist); len(got) > 0 {
		t.Fatalf("tests must not write package globals; inject dependencies or add explicit EPIC-258 debt:\n%s", strings.Join(got, "\n"))
	}
}

func unexpectedKeys(found map[string]string, allowed map[string]bool) []string {
	var out []string
	for name, pos := range found {
		if !allowed[name] {
			out = append(out, pos+": "+name)
		}
	}
	sort.Strings(out)
	return out
}

var legacyFuncVarAllowlist = map[string]bool{}

var legacyTestGlobalWriteAllowlist = map[string]bool{
	// PERMANENT, by design - not debt. archiveThresholdCfg is a genuine
	// process-wide singleton: every access (production and test) is guarded by
	// archiveThresholdMu, and it backs the SIGHUP hot-reload contract added in
	// EPIC-051 M6 (ReloadArchiveThresholdConfig, wired at main.go). It is not a
	// race, and it has no reachable owner - archiveThreshold() is a free
	// function called from 8 sites including cobra RunE loops in cmd_score.go
	// and cmd_triage.go that have neither a Router nor scoringDeps in scope.
	// EPIC-258 M1 explicitly carves out this case: inject for the scoring path,
	// keep mutex-guarded globals "only where a genuine process-wide singleton
	// exists". It trips this guard only because the guard bans any test write to
	// a package var, synchronized or not. Decision recorded 20260817.
	"archiveThresholdCfg":             true,
	"imageShortCircuitBypassMinChars": true, // image_transcription_bt_test.go F3-BT-2 tests the startup config writer itself
	"likedVideosSyncing":              true,
	"watchLaterSyncing":               true,
	"ytFallbackToAudio":               true,
}
