package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/templates"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/ui"
)

// reportFormat is a validated output format value.
type reportFormat string

const (
	formatMD   reportFormat = "md"
	formatJSON reportFormat = "json"
	formatPDF  reportFormat = "pdf"
)

// parseFormat validates a --format flag value and returns the canonical form.
func parseFormat(s string) (reportFormat, error) {
	switch strings.ToLower(s) {
	case "md", "markdown", "":
		return formatMD, nil
	case "json":
		return formatJSON, nil
	case "pdf":
		return formatPDF, nil
	default:
		return "", fmt.Errorf("unknown format %q — valid values: md, json, pdf", s)
	}
}

// defaultReportPath returns the default output file path for a given base name
// (e.g. "weekly") and format, rooted in the XDG state dir.
func defaultReportPath(base string, rf reportFormat) string {
	return filepath.Join(config.WorkctlStateDir(), base+"."+string(rf))
}

// --------------------------------------------------------------------------
// WriteReport — single format-dispatch entry point for all report types
// --------------------------------------------------------------------------

// WriteReport renders a report to rd.OutputPath in rd.Format.
// rd.ReportType, rd.Format, rd.OutputPath, and the relevant payload fields
// (Signals, Delta, TrackResult, Period) must be populated before calling.
func WriteReport(rd *ReportData) error {
	switch rd.ReportType {
	case "weekly":
		switch rd.Format {
		case formatJSON:
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := export.WriteWeeklyJSON(f, rd.Signals, rd.Period); err != nil {
				return err
			}
		case formatPDF:
			if err := export.ConvertToPDF(rd.OutputPath, func(w io.Writer) error {
				return templates.RenderWeekly(w, rd.Signals, rd.Period)
			}); err != nil {
				return err
			}
		default: // md
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := templates.RenderWeekly(f, rd.Signals, rd.Period); err != nil {
				return err
			}
		}

	case "quarterly":
		switch rd.Format {
		case formatJSON:
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := export.WriteQuarterlyJSON(f, rd.Delta); err != nil {
				return err
			}
		case formatPDF:
			if err := export.ConvertToPDF(rd.OutputPath, func(w io.Writer) error {
				return templates.RenderQuarterly(w, rd.Delta)
			}); err != nil {
				return err
			}
		default: // md
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := templates.RenderQuarterly(f, rd.Delta); err != nil {
				return err
			}
		}

	case "review":
		switch rd.Format {
		case formatJSON:
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := export.WriteReviewJSON(f, rd.Signals, rd.TrackResult, rd.Period); err != nil {
				return err
			}
		case formatPDF:
			if err := export.ConvertToPDF(rd.OutputPath, func(w io.Writer) error {
				return templates.RenderReview(w, rd.Signals, rd.TrackResult, rd.Period)
			}); err != nil {
				return err
			}
		default: // md
			f, err := openOutput(rd.OutputPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := templates.RenderReview(f, rd.Signals, rd.TrackResult, rd.Period); err != nil {
				return err
			}
		}

	default:
		return fmt.Errorf("WriteReport: unknown report type %q", rd.ReportType)
	}

	// Write .signals.json sidecar after primary report succeeds (EPIC-016 M2).
	writeSignalsSidecar(rd)
	return nil
}

// writeSignalsSidecar writes a .signals.json sidecar beside the report file (EPIC-016 M2).
// Errors are non-fatal: logged as warnings so the primary report is never blocked.
func writeSignalsSidecar(rd *ReportData) {
	if rd.OutputPath == "" || rd.Signals == nil {
		return
	}
	sink := export.NewFileSink()
	if err := sink.Write(context.Background(), toPipelineReportData(rd)); err != nil {
		ui.Warnf("signals.json: %v\n", err)
	}
}

// --------------------------------------------------------------------------
// WriteTrendsReport — multi-period trend report
// --------------------------------------------------------------------------

// WriteTrendsReport renders a multi-period trend report to outputPath in the
// requested format (md, json, pdf).
func WriteTrendsReport(ts *TrendSet, rf reportFormat, outputPath string) error {
	tperiods := make([]templates.TrendPeriod, len(ts.Periods))
	eperiods := make([]export.TrendPeriodData, len(ts.Periods))
	for i, p := range ts.Periods {
		tperiods[i] = templates.TrendPeriod{
			Label:        p.Period,
			Signals:      p.Signals,
			TrackResult:  p.TrackResult,
			TrackResults: p.TrackResults,
		}
		eperiods[i] = export.TrendPeriodData{
			Label:        p.Period,
			Signals:      p.Signals,
			TrackResult:  p.TrackResult,
			TrackResults: p.TrackResults,
		}
	}

	switch rf {
	case formatJSON:
		f, err := openOutput(outputPath)
		if err != nil {
			return err
		}
		defer f.Close()
		return export.WriteTrendsJSON(f, eperiods, ts.PeriodSize)
	case formatPDF:
		return export.ConvertToPDF(outputPath, func(w io.Writer) error {
			return templates.RenderTrends(w, tperiods, ts.PeriodSize)
		})
	default: // md
		f, err := openOutput(outputPath)
		if err != nil {
			return err
		}
		defer f.Close()
		return templates.RenderTrends(f, tperiods, ts.PeriodSize)
	}
}

func openOutput(path string) (*os.File, error) {
	// Clean the path to normalize any ".." components before use.
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating output file %s: %w", path, err)
	}
	return f, nil
}
