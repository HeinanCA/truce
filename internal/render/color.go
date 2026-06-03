// Package render formats analyzed workloads for the terminal (table, wide),
// machine consumption (json), and application (diff). It imports no Kubernetes
// packages and makes no cluster calls — it consumes model types and writes to an
// io.Writer.
package render

import "fmt"

// ANSI escape codes used for minimal coloring.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
)

// Palette wraps text in ANSI colors, or returns it unchanged when disabled.
type Palette struct {
	enabled bool
}

// NewPalette returns a Palette. Color is suppressed when noColor is true.
func NewPalette(noColor bool) Palette { return Palette{enabled: !noColor} }

func (p Palette) wrap(code, s string) string {
	if !p.enabled {
		return s
	}
	return code + s + ansiReset
}

func (p Palette) Red(s string) string    { return p.wrap(ansiRed, s) }
func (p Palette) Green(s string) string  { return p.wrap(ansiGreen, s) }
func (p Palette) Yellow(s string) string { return p.wrap(ansiYellow, s) }
func (p Palette) Dim(s string) string    { return p.wrap(ansiDim, s) }
func (p Palette) Bold(s string) string   { return p.wrap(ansiBold, s) }

// Headerf prints a bold header line via fmt-style formatting.
func (p Palette) Headerf(format string, a ...any) string {
	return p.Bold(fmt.Sprintf(format, a...))
}
