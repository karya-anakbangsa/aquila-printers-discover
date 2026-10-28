package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

func TestListenErrorAddressInUse(t *testing.T) {
	for _, cause := range []error{syscall.EADDRINUSE, syscall.Errno(10048)} {
		err := listenError(fmt.Errorf("failed to listen: %w", cause), "127.0.0.1:17891")
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "ubah port di Pengaturan") {
			t.Fatalf("galat port dipakai tidak memberi petunjuk: %v", err)
		}
	}
}
