package console

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

type Code string

const (
	Reset   Code = "0"
	Bold    Code = "1"
	Dim     Code = "2"
	Red     Code = "31"
	Green   Code = "32"
	Yellow  Code = "33"
	Blue    Code = "34"
	Magenta Code = "35"
	Cyan    Code = "36"
)

type descriptor interface {
	Fd() uintptr
}

func Enabled(output io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	file, valid := output.(descriptor)

	return valid && term.IsTerminal(file.Fd())
}

func Paint(enabled bool, code Code, value string) string {
	if !enabled || code == "" || value == "" {
		return value
	}

	return "\x1b[" + string(code) + "m" + value + "\x1b[0m"
}
