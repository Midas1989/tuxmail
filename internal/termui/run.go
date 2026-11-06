// SPDX-License-Identifier: AGPL-3.0-or-later

package termui

import (
	"context"
	"errors"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Midas1989/Mailclient/internal/mailcmd"
)

// Run shows the interface until the user quits or ctx ends. The terminal is
// restored by bubbletea in every case, also after a panic.
func Run(ctx context.Context, svc *mailcmd.Service, in io.Reader, out io.Writer) error {
	p := tea.NewProgram(New(ctx, svc), tea.WithAltScreen(), tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil // interrupted from outside: a normal end
	}
	return err
}
