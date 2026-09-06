package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type watchRegionRenderer struct {
	output io.Writer
	size   func() (int, int)
	lines  int
	width  int
	height int
}

type terminalDescriptor interface {
	Fd() uintptr
}

func newWatchRegionRenderer(output io.Writer, size func() (int, int)) *watchRegionRenderer {
	return &watchRegionRenderer{output: output, size: size}
}

func (renderer *watchRegionRenderer) Render(snapshot WatchSnapshot) error {
	var buffer bytes.Buffer
	if err := renderWatchSnapshot(&buffer, snapshot); err != nil {
		return err
	}
	width, height := renderer.size()
	width = max(width, 1)
	height = max(height, 2)
	lines := fitWatchLines(buffer.String(), width, height)
	text := strings.Join(lines, "\n")
	if renderer.lines > 0 && (width != renderer.width || height != renderer.height) {
		if _, err := io.WriteString(renderer.output, "\x1b[2J\x1b[H"); err != nil {
			return err
		}
		renderer.lines = 0
	}
	if renderer.lines == 0 {
		if _, err := io.WriteString(renderer.output, text+"\n"); err != nil {
			return err
		}
		renderer.lines = len(lines)
		renderer.width = width
		renderer.height = height

		return nil
	}
	if _, err := fmt.Fprintf(renderer.output, "\x1b[%dA", renderer.lines); err != nil {
		return err
	}
	rows := max(renderer.lines, len(lines))
	for index := 0; index < rows; index++ {
		if _, err := io.WriteString(renderer.output, "\r\x1b[2K"); err != nil {
			return err
		}
		if index < len(lines) {
			if _, err := io.WriteString(renderer.output, lines[index]); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(renderer.output, "\n"); err != nil {
			return err
		}
	}
	if rows > len(lines) {
		if _, err := fmt.Fprintf(renderer.output, "\x1b[%dA", rows-len(lines)); err != nil {
			return err
		}
	}
	renderer.lines = len(lines)
	renderer.width = width
	renderer.height = height

	return nil
}

func watchTerminalSize(output io.Writer) func() (int, int) {
	descriptor, valid := output.(terminalDescriptor)

	return func() (int, int) {
		if valid {
			width, height, err := term.GetSize(descriptor.Fd())
			if err == nil && width > 0 && height > 1 {
				return width, height
			}
		}

		return 80, 24
	}
}

func fitWatchLines(text string, width, height int) []string {
	logical := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines := make([]string, 0, len(logical))
	for _, line := range logical {
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	maximum := max(height-1, 1)
	if len(lines) <= maximum {
		return lines
	}
	omitted := len(lines) - maximum + 1
	lines = lines[:maximum]
	lines[maximum-1] = ansi.Truncate(fmt.Sprintf("... %d more", omitted), width, "…")

	return lines
}
