package ui

import (
	"fmt"

	"github.com/fatih/color"
)

var (
	success *color.Color
	fail    *color.Color
	warn    *color.Color
	info    *color.Color
	header  *color.Color
	dim     *color.Color
)

func init() {
	success = color.New(color.FgGreen)
	fail = color.New(color.FgRed, color.Bold)
	warn = color.New(color.FgYellow)
	info = color.New(color.FgCyan)
	header = color.New(color.Bold)
	dim = color.New(color.Faint)
}

// Initialize configures color output. Call once from PersistentPreRunE.
// If noColor is true, ANSI escape sequences are unconditionally disabled.
// Otherwise fatih/color handles TTY detection and NO_COLOR automatically.
func Initialize(noColor bool) {
	if noColor {
		color.NoColor = true
	}
}

// Successf prints green text to stdout.
func Successf(format string, a ...interface{}) {
	success.Printf(format, a...)
}

// Errorf prints red bold text to stdout.
func Errorf(format string, a ...interface{}) {
	fail.Printf(format, a...)
}

// Warnf prints yellow text to stdout.
func Warnf(format string, a ...interface{}) {
	warn.Printf(format, a...)
}

// Infof prints cyan text to stdout.
func Infof(format string, a ...interface{}) {
	info.Printf(format, a...)
}

// Headerf prints bold text to stdout.
func Headerf(format string, a ...interface{}) {
	header.Printf(format, a...)
}

// Dimf prints faint text to stdout.
func Dimf(format string, a ...interface{}) {
	dim.Printf(format, a...)
}

// Sprintf variants for when callers need a colored string rather than direct print.

func SuccessSprintf(format string, a ...interface{}) string {
	return success.Sprintf(format, a...)
}

func ErrorSprintf(format string, a ...interface{}) string {
	return fail.Sprintf(format, a...)
}

func WarnSprintf(format string, a ...interface{}) string {
	return warn.Sprintf(format, a...)
}

func InfoSprintf(format string, a ...interface{}) string {
	return info.Sprintf(format, a...)
}

func HeaderSprintf(format string, a ...interface{}) string {
	return header.Sprintf(format, a...)
}

func DimSprintf(format string, a ...interface{}) string {
	return dim.Sprintf(format, a...)
}

// Plain prints uncolored text to stdout (replaces fmt.Printf for consistency).
func Plain(format string, a ...interface{}) {
	fmt.Printf(format, a...)
}
