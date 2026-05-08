package serving

import (
	"context"
	"fmt"
	"os/exec"
)

// ExecRenderPipeline implements RenderPipeline using render.py + typst CLI.
// render.py produces HTML, JSON, MD, TXT, YAML variants.
// typst compile produces the PDF.
// Output files are named resume-{slug}[.ext] in outputDir.
type ExecRenderPipeline struct {
	RenderPyPath string // path to render.py; defaults to "render.py"
}

func (r *ExecRenderPipeline) Render(ctx context.Context, slug, yamlPath, outputDir string) error {
	renderPy := r.RenderPyPath
	if renderPy == "" {
		renderPy = "render.py"
	}

	cmd := exec.CommandContext(ctx, "python3", renderPy,
		"--input", yamlPath,
		"--output", outputDir,
		"--slug", slug,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("render.py failed: %w\n%s", err, string(out))
	}

	typstCmd := exec.CommandContext(ctx, "typst", "compile", yamlPath,
		fmt.Sprintf("%s/resume-%s.pdf", outputDir, slug),
	)
	if out, err := typstCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("typst compile failed: %w\n%s", err, string(out))
	}

	return nil
}
