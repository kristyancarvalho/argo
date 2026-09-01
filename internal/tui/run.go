package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/console"
)

func Run(ctx context.Context, client Client, input io.Reader, output io.Writer) error {
	model, err := NewModelWithOptions(ctx, client, Options{Color: console.Enabled(output)})
	if err != nil {
		return err
	}
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}

	return nil
}
