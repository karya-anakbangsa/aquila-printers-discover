//go:build !windows

package utils

import (
	"context"
	"os/exec"
)

// HiddenCommand starts an external command with context on non-Windows platforms.
func HiddenCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// PowerShellCommand starts a PowerShell command with context on non-Windows platforms.
func PowerShellCommand(ctx context.Context, args ...string) *exec.Cmd {
	return HiddenCommand(ctx, "powershell.exe", args...)
}
