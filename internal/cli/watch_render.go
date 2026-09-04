package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

type watchRegionRenderer struct {
	output io.Writer
	lines  int
}

func newWatchRegionRenderer(output io.Writer) *watchRegionRenderer {
	return &watchRegionRenderer{output: output}
}

func (renderer *watchRegionRenderer) Render(snapshot WatchSnapshot) error {
	var buffer bytes.Buffer
	if err := renderWatchSnapshot(&buffer, snapshot); err != nil {
		return err
	}
	text := strings.TrimSuffix(buffer.String(), "\n")
	lines := strings.Split(text, "\n")
	if renderer.lines == 0 {
		if _, err := io.WriteString(renderer.output, text+"\n"); err != nil {
			return err
		}
		renderer.lines = len(lines)

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

	return nil
}
