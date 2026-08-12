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
	var testFiles []*ast.File
	for _, path := range files {
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, parsed)
			continue
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					packageVars[name.Name] = true
					if _, ok := vs.Type.(*ast.FuncType); ok {
						funcVars[name.Name] = fset.Position(name.Pos()).String()
						continue
					}
					if i < len(vs.Values) {
						if _, ok := vs.Values[i].(*ast.FuncLit); ok {
							funcVars[name.Name] = fset.Position(name.Pos()).String()
						}
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

var legacyFuncVarAllowlist = map[string]bool{
	"awsDoctorProbeFn":         true,
	"configRefResolverFactory": true,
	"enqueueTranscriptPushFn":  true,
	"execGitShowProfile":       true,
	"execLiteCmd":              true,
	"filenameShortKeywords":    true,
	"monotonicID":              true,
	"probeYouTubeSlotFn":       true,
	"refreshScorerFn":          true,
	"registeredScorerFn":       true,
	"runClaudeHaikuVision":     true,
	"scoreAsyncDoneHook":       true,
	"validProfilesSorted":      true,
}

var legacyTestGlobalWriteAllowlist = map[string]bool{
	"archiveThresholdCfg":             true,
	"awsDoctorProbeFn":                true,
	"configRefResolverFactory":        true,
	"domainRouteOverrideEmitter":      true,
	"enqueueTranscriptPushFn":         true,
	"execConnectAndRead":              true,
	"execGetRecord":                   true,
	"execGitShowProfile":              true,
	"execLiteCmd":                     true,
	"execLiteParse":                   true,
	"execNormalizeURL":                true,
	"execPublishReply":                true,
	"execYouTubeChannelsList":         true,
	"execYouTubePlaylistItems":        true,
	"execYouTubePlaylistItemsList":    true,
	"execYouTubeSubscriptionsList":    true,
	"execYtdlpAudio":                  true,
	"firecrawlClient":                 true,
	"imageNoiseGateMinBytes":          true,
	"imageShortCircuitBypassMinChars": true,
	"imageTextExtractionEnabled":      true,
	"imageTextExtractionSem":          true,
	"isTerminalFn":                    true,
	"likedVideosSyncing":              true,
	"normalizeHTTPClient":             true,
	"pasteReaderFn":                   true,
	"piBinaryPath":                    true,
	"probeYouTubeSlotFn":              true,
	"profilePathOverride":             true,
	"refreshScorerFn":                 true,
	"registeredScorerFn":              true,
	"renameFile":                      true,
	"runYouTubeLoopbackAuthFn":        true,
	"scoreAsyncDoneHook":              true,
	"sensitivePatterns":               true,
	"tsnetStart":                      true,
	"validStatuses":                   true,
	"visionModelName":                 true,
	"watchLaterSyncing":               true,
	"youtubeOAuthEndpoint":            true,
	"ytAudioMaxRetries":               true,
	"ytAudioSem":                      true,
	"ytFallbackToAudio":               true,
	"ytSubtitleMaxRetries":            true,
	"ytSubtitleSem":                   true,
	"ytSubtitleTimeoutSecs":           true,
	"ytWhisperTimeoutSecs":            true,
}
