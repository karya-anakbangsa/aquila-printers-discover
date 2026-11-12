//go:build windows

package printserver

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestArpCacheCommandHidesConsole(t *testing.T) {
	command := arpCacheCommand(context.Background())
	settings := command.SysProcAttr
	if settings == nil || !settings.HideWindow || settings.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("arp cache command console not hidden: %+v", command.SysProcAttr)
	}
}
