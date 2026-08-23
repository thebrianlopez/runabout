package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression coverage for the 20260823 YouTube pipeline outage.
//
// Two independent defects combined to make every YouTube share fail:
//
//  1. runYtdlpExtract passed -j, an alias for --dump-json, which implies
//     --simulate. yt-dlp therefore resolved metadata and wrote no subtitle
//     files, so extraction reported "no subtitles" for every video. Production
//     logs carried 80 yt_no_subtitles events and zero yt_subtitles_ok.
//  2. The hard-failure message read "yt-dlp extraction failed (no subtitles,
//     exit: ...)", which the classifier's strings.Contains(err, "no subtitles")
//     matched - so real failures (HTTP 403) were reported as the benign signal
//     and diverted into the audio fallback instead of dead-lettering.
//
// A third issue made both undiagnosable: cmd.Output() errors render as bare
// "exit status 1", hiding yt-dlp's actual stderr.

// ─── RG-1: -j must never reappear in the subtitle invocation ─────────────────
//
// AST-based rather than a grep for a flag string, so the guard survives
// refactors of the surrounding call and fails by property.
func TestYTDLPGuard_SubtitleExtractionMustNotUseDumpJSON(t *testing.T) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "youtube.go", nil, 0)
	if err != nil {
		t.Fatalf("parse youtube.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "runYtdlpExtract" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("RG-1: runYtdlpExtract not found in youtube.go")
	}

	var args []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "CommandContext" {
			return true
		}
		for _, arg := range call.Args {
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				args = append(args, strings.Trim(lit.Value, `"`))
			}
		}
		return true
	})

	if len(args) == 0 {
		t.Fatal("RG-1: no exec.CommandContext string literals found in runYtdlpExtract")
	}

	for _, a := range args {
		// -j and --dump-json both imply --simulate: no files are written.
		if a == "-j" || a == "--dump-json" || a == "--simulate" || a == "-s" {
			t.Errorf("RG-1: runYtdlpExtract passes %q, which implies --simulate; "+
				"yt-dlp writes no subtitle files and every extraction reports "+
				"no subtitles. Use --print-json instead.", a)
		}
	}

	var sawPrintJSON bool
	for _, a := range args {
		if a == "--print-json" {
			sawPrintJSON = true
		}
	}
	if !sawPrintJSON {
		t.Error("RG-1: runYtdlpExtract must pass --print-json to emit metadata without simulating")
	}
}

// fakeYtdlp writes an executable stub script and returns its path.
func fakeYtdlp(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "yt-dlp-fake")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake yt-dlp: %v", err)
	}
	return path
}

// outDirShell extracts the directory of the -o template and, critically,
// emulates yt-dlp's real --simulate semantics: -j / --dump-json / -s suppress
// all file output. Without this fidelity the stub would happily write a
// subtitle file under -j and the CT-1 regression could not fail.
const outDirShell = `
outdir=""
prev=""
simulate=0
for a in "$@"; do
  if [ "$prev" = "-o" ]; then outdir=$(dirname "$a"); fi
  case "$a" in
    -j|--dump-json|-s|--simulate) simulate=1 ;;
  esac
  prev="$a"
done
if [ "$simulate" = "1" ]; then
  echo '{"title":"Test Video","id":"vid","duration":120,"channel_id":"UC123"}'
  exit 0
fi
`

// ─── CT-1: a stub that writes an .srt yields a transcript ────────────────────
//
// This is the case the -j bug broke: yt-dlp wrote nothing, so the code fell
// through to the "no subtitles" return even for captioned videos.
func TestYTDLP_CT1_ExtractReturnsTranscriptWhenSubtitleFileWritten(t *testing.T) {
	stub := fakeYtdlp(t, outDirShell+`
printf '1\n00:00:00,000 --> 00:00:02,000\nhello world\n\n' > "$outdir/vid.en.srt"
echo '{"title":"Test Video","id":"vid","duration":120,"channel_id":"UC123"}'
exit 0
`)

	transcript, meta, err := runYtdlpExtract(context.Background(), stub, "https://youtube.com/watch?v=vid")
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if !strings.Contains(transcript, "hello world") {
		t.Errorf("CT-1: transcript = %q, want it to contain %q", transcript, "hello world")
	}
	if meta.Title != "Test Video" {
		t.Errorf("CT-1: meta.Title = %q, want %q", meta.Title, "Test Video")
	}
	if meta.Duration != 120 {
		t.Errorf("CT-1: meta.Duration = %d, want 120", meta.Duration)
	}
}

// ─── CT-2: clean exit with no file is the benign no-subtitles signal ─────────
func TestYTDLP_CT2_CleanExitWithNoFileIsNoSubtitles(t *testing.T) {
	stub := fakeYtdlp(t, `echo '{"title":"Silent","id":"vid"}'
exit 0
`)

	_, _, err := runYtdlpExtract(context.Background(), stub, "https://youtube.com/watch?v=vid")
	if err == nil {
		t.Fatal("CT-2: want an error, got nil")
	}
	if !errors.Is(err, errYTNoSubtitles) {
		t.Errorf("CT-2: err = %v, want errYTNoSubtitles", err)
	}
	if errors.Is(err, errYTExtractFailed) {
		t.Errorf("CT-2: err must not be errYTExtractFailed: %v", err)
	}
}

// ─── CT-3: non-zero exit with no file is a hard failure, not "no subtitles" ──
//
// The 20260823 outage case: HTTP 403 was reported as yt_no_subtitles.
func TestYTDLP_CT3_NonZeroExitIsHardFailureNotNoSubtitles(t *testing.T) {
	stub := fakeYtdlp(t, `echo 'ERROR: unable to download video data: HTTP Error 403: Forbidden' >&2
exit 1
`)

	_, _, err := runYtdlpExtract(context.Background(), stub, "https://youtube.com/watch?v=vid")
	if err == nil {
		t.Fatal("CT-3: want an error, got nil")
	}
	if !errors.Is(err, errYTExtractFailed) {
		t.Errorf("CT-3: err = %v, want errYTExtractFailed", err)
	}
	if errors.Is(err, errYTNoSubtitles) {
		t.Errorf("CT-3: hard failure must not classify as errYTNoSubtitles: %v", err)
	}
	// The message must not contain the legacy substring, or the compatibility
	// shim in extractYTSubtitles would reclassify it as benign.
	if strings.Contains(err.Error(), "no subtitles") {
		t.Errorf("CT-3: hard-failure message must not contain %q (collides with "+
			"the classifier's compatibility substring match): %v", "no subtitles", err)
	}
	// And the operator must be able to see why.
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("CT-3: stderr detail lost - err = %v, want it to surface the 403", err)
	}
}

// ─── CT-4: extractYTSubtitles classifies a hard failure as yt_dlp_failed ─────
func TestYTDLP_CT4_ClassifierRoutesHardFailureToDeadLetter(t *testing.T) {
	deps := (&ytDeps{
		Ytdlp: func(ctx context.Context, ytPath, url string) (string, ytVideoMeta, error) {
			return "", ytVideoMeta{}, fmt.Errorf("%w: exit status 1: ERROR: HTTP Error 403: Forbidden", errYTExtractFailed)
		},
	}).resolve()

	event, _, _, err := extractYTSubtitles(
		context.Background(), "yt-dlp", "https://youtube.com/watch?v=vid",
		1, nil, nil, deps,
	)

	if event != "yt_dlp_failed" {
		t.Errorf("CT-4: subtitleEvent = %q, want yt_dlp_failed", event)
	}
	if err == nil {
		t.Error("CT-4: want non-nil error so the caller dead-letters the row")
	}
}

// ─── CT-5: legacy plain-string stubs still classify as no-subtitles ──────────
//
// Guards the compatibility shim: existing callers and stubs return a bare
// "no subtitles" error rather than wrapping the sentinel.
func TestYTDLP_CT5_LegacyNoSubtitlesStringStillClassifies(t *testing.T) {
	deps := (&ytDeps{
		Ytdlp: func(ctx context.Context, ytPath, url string) (string, ytVideoMeta, error) {
			return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found for test-url")
		},
	}).resolve()

	event, _, _, err := extractYTSubtitles(
		context.Background(), "yt-dlp", "https://youtube.com/watch?v=vid",
		1, nil, nil, deps,
	)

	if event != "yt_no_subtitles" {
		t.Errorf("CT-5: subtitleEvent = %q, want yt_no_subtitles", event)
	}
	if err != nil {
		t.Errorf("CT-5: err = %v, want nil (benign signal)", err)
	}
}

// ─── CT-6: stderr tail extraction ────────────────────────────────────────────
func TestYTDLP_CT6_StderrTailSurfacesErrorLines(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c",
		`echo 'WARNING: noise' >&2; echo 'ERROR: unable to download video data: HTTP Error 403: Forbidden' >&2; exit 1`)
	_, runErr := cmd.Output()
	if runErr == nil {
		t.Fatal("CT-6: expected the stub command to fail")
	}

	tail := ytdlpStderrTail(runErr)
	if !strings.Contains(tail, "403: Forbidden") {
		t.Errorf("CT-6: tail = %q, want it to contain the 403", tail)
	}
	// ERROR: lines are preferred over WARNING noise.
	if strings.Contains(tail, "WARNING: noise") {
		t.Errorf("CT-6: tail should prefer ERROR: lines, got %q", tail)
	}

	detail := ytdlpExitDetail(runErr)
	if !strings.Contains(detail, "exit status 1") || !strings.Contains(detail, "403") {
		t.Errorf("CT-6: detail = %q, want both exit status and stderr cause", detail)
	}
}

// ─── CT-7: stderr tail is bounded ────────────────────────────────────────────
func TestYTDLP_CT7_StderrTailIsBounded(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c",
		`i=0; while [ $i -lt 500 ]; do echo "ERROR: padding line $i"; i=$((i+1)); done >&2; exit 1`)
	_, runErr := cmd.Output()
	if runErr == nil {
		t.Fatal("CT-7: expected the stub command to fail")
	}

	tail := ytdlpStderrTail(runErr)
	if len(tail) > ytStderrMaxChars {
		t.Errorf("CT-7: tail length = %d, want <= %d", len(tail), ytStderrMaxChars)
	}
}

// ─── CT-8: nil and non-exec errors are handled ───────────────────────────────
func TestYTDLP_CT8_StderrHelpersToleratePlainErrors(t *testing.T) {
	if got := ytdlpStderrTail(nil); got != "" {
		t.Errorf("CT-8: ytdlpStderrTail(nil) = %q, want empty", got)
	}
	if got := ytdlpExitDetail(nil); got != "" {
		t.Errorf("CT-8: ytdlpExitDetail(nil) = %q, want empty", got)
	}
	plain := fmt.Errorf("boom")
	if got := ytdlpStderrTail(plain); got != "" {
		t.Errorf("CT-8: ytdlpStderrTail(plain) = %q, want empty", got)
	}
	if got := ytdlpExitDetail(plain); got != "boom" {
		t.Errorf("CT-8: ytdlpExitDetail(plain) = %q, want %q", got, "boom")
	}
}
