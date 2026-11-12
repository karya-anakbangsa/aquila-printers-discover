//go:build windows

package main

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFirewallRuleCommandsHideConsole(t *testing.T) {
	checkCmd, addCmd := firewallRuleCommands()
	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
	}{
		{name: "check", cmd: checkCmd},
		{name: "add", cmd: addCmd},
	} {
		settings := tc.cmd.SysProcAttr
		if settings == nil || !settings.HideWindow || settings.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
			t.Fatalf("netsh %s command console not hidden: %+v", tc.name, tc.cmd.SysProcAttr)
		}
	}
}
