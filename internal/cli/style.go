package cli

import (
	"io"
	"strings"

	"github.com/kristyancarvalho/argo/internal/console"
)

type Options struct {
	Color bool
}

type styledWriter struct {
	output io.Writer
}

func (writer styledWriter) Write(data []byte) (int, error) {
	styled := styleCommandOutput(string(data))
	_, err := io.WriteString(writer.output, styled)
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

func styleCommandOutput(value string) string {
	lines := strings.SplitAfter(value, "\n")
	for index, line := range lines {
		plain := strings.TrimSuffix(line, "\n")
		newline := strings.TrimPrefix(line, plain)
		switch {
		case strings.HasPrefix(plain, "Argo "):
			plain = console.Paint(true, console.Cyan, console.Paint(true, console.Bold, plain))
		case strings.HasSuffix(plain, ":"):
			plain = console.Paint(true, console.Cyan, console.Paint(true, console.Bold, plain))
		case strings.HasPrefix(plain, "  "):
			trimmed := strings.TrimSpace(plain)
			if end := strings.IndexAny(trimmed, " \t"); end > 0 {
				plain = strings.Replace(plain, trimmed[:end], console.Paint(true, console.Cyan, trimmed[:end]), 1)
			}
		case strings.Index(plain, ": ") > 0:
			separator := strings.Index(plain, ": ")
			plain = console.Paint(true, console.Bold, plain[:separator]) + plain[separator:]
		}
		for token, code := range map[string]console.Code{
			"active": console.Green, "running": console.Green, "completed": console.Green,
			"queued": console.Cyan, "downloading": console.Blue, "paused": console.Yellow,
			"inactive": console.Yellow, "failed": console.Red, "canceled": console.Red,
			"high": console.Magenta, "normal": console.Cyan, "low": console.Dim,
		} {
			plain = paintToken(plain, token, code)
		}
		lines[index] = plain + newline
	}

	return strings.Join(lines, "")
}

func paintToken(value, token string, code console.Code) string {
	fields := strings.FieldsFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t' || character == '\n' || character == ':' ||
			character == '(' || character == ')' || character == ','
	})
	for _, field := range fields {
		if field == token {
			return strings.ReplaceAll(value, token, console.Paint(true, code, token))
		}
	}

	return value
}
