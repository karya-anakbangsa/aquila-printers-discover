package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrintRawEmpty(t *testing.T) {
	if err := PrintRawWin32("DummyPrinter", nil, ""); err == nil {
		t.Error("expected error for empty data in PrintRawWin32, got nil")
	}
	if err := PrintRawPowerShellFallback("DummyPrinter", []byte{}, ""); err == nil {
		t.Error("expected error for empty data in PrintRawPowerShellFallback, got nil")
	}
	if err := PrintRawToWindowsPrinter("DummyPrinter", []byte{}, ""); err == nil {
		t.Error("expected error for empty data in PrintRawToWindowsPrinter, got nil")
	}
}

func TestSerialPortHelpers(t *testing.T) {
	// Test TryOpenSerial on non-existent port returns error
	_, err := TryOpenSerial("COM99999")
	if err == nil {
		t.Error("expected error opening non-existent serial port, got nil")
	}

	// Test TryOpenSerial with a temporary file to verify write functionality
	tmpDir := t.TempDir()
	dummyFile := filepath.Join(tmpDir, "dummy_serial")
	if err := os.WriteFile(dummyFile, []byte{}, 0666); err != nil {
		t.Fatal(err)
	}

	if err := PrintRawToSerialPort(dummyFile, []byte("ESC/POS test data")); err != nil {
		t.Fatalf("unexpected error writing to dummy file: %v", err)
	}

	content, err := os.ReadFile(dummyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "ESC/POS test data" {
		t.Errorf("got %q, want 'ESC/POS test data'", string(content))
	}
}
