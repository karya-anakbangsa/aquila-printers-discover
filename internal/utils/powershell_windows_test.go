//go:build windows

package utils

import (
	"context"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPowerShellCommandHidesConsole(t *testing.T) {
	command := PowerShellCommand(context.Background(), "-Command", "exit 0")
	settings := command.SysProcAttr
	if settings == nil || !settings.HideWindow || settings.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("PowerShell console not hidden: %+v", command.SysProcAttr)
	}
}

func TestHiddenCommandsDisableConsoleWindow(t *testing.T) {
	cases := []struct {
		name string
		cmd  *exec.Cmd
	}{
		{name: "HiddenCommand arp", cmd: HiddenCommand(context.Background(), "arp", "-a")},
		{name: "HiddenCommand netsh", cmd: HiddenCommand(context.Background(), "netsh", "advfirewall", "firewall", "show", "rule")},
		{name: "serialPortsRegistryCommand", cmd: serialPortsRegistryCommand(context.Background())},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := tc.cmd.SysProcAttr
			if settings == nil || !settings.HideWindow || settings.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
				t.Fatalf("%s console not hidden: %+v", tc.name, tc.cmd.SysProcAttr)
			}
		})
	}
}
