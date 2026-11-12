//go:build windows

package utils

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// HiddenCommand starts an external command without opening a console window on Windows.
func HiddenCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return command
}

// PowerShellCommand starts a PowerShell process without opening a console window.
func PowerShellCommand(ctx context.Context, args ...string) *exec.Cmd {
	return HiddenCommand(ctx, "powershell.exe", args...)
}
