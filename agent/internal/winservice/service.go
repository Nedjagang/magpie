// Package winservice provides Windows Service integration for the agent.
// On non-Windows platforms every exported function is a stub that returns
// an informative error so the single-binary build works everywhere.
package winservice

import (
	"context"
	"log/slog"
)

// RunFunc is the agent's main loop. The service handler calls it with a
// context that is cancelled when the SCM sends a Stop signal.
type RunFunc func(ctx context.Context, logger *slog.Logger) error
