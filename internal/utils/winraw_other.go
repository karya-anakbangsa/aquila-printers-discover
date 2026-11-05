//go:build !windows

package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PrintRawWin32 is only available on Windows.
func PrintRawWin32(printerName string, data []byte, docTitle string) error {
	return fmt.Errorf("Win32 Spooler API hanya tersedia di Windows (OS saat ini: %s)", runtime.GOOS)
}

// PrintRawPowerShellFallback is only available on Windows.
func PrintRawPowerShellFallback(printerName string, data []byte, docTitle string) error {
	return fmt.Errorf("PowerShell Win32 Spooler hanya tersedia di Windows (OS saat ini: %s)", runtime.GOOS)
}

// PrintRawToWindowsPrinter sends raw data to printer; on Unix it uses CUPS raw queue (-o raw).
func PrintRawToWindowsPrinter(printerName string, data []byte, docTitle string) error {
	if len(data) == 0 {
		return fmt.Errorf("data cetak kosong")
	}
	// Fallback to CUPS raw printing on Linux / macOS
	tempFile, err := os.CreateTemp("", "aquila-raw-*.bin")
	if err != nil {
		return fmt.Errorf("buat file sementara gagal: %w", err)
	}
	path := tempFile.Name()
	defer os.Remove(path)

	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("tulis data raw gagal: %w", err)
	}
	_ = tempFile.Close()

	if docTitle == "" {
		docTitle = "RSISA Printer Server RAW Print"
	}
	cmd := exec.CommandContext(context.Background(), "lp", "-d", printerName, "-t", docTitle, "-o", "raw", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cetak raw via antrean printer gagal: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListSerialPorts returns available serial / USB serial ports on Linux/macOS.
func ListSerialPorts() []string {
	var ports []string
	patterns := []string{
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/cu.usbserial*",
		"/dev/cu.usbmodem*",
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil {
			ports = append(ports, matches...)
		}
	}
	return ports
}

// TryOpenSerial opens a serial port device file.
func TryOpenSerial(port string) (*os.File, error) {
	rawPort := strings.TrimSpace(port)
	candidates := []string{rawPort}
	if !strings.HasPrefix(rawPort, "/dev/") {
		candidates = append(candidates, "/dev/"+rawPort)
	}
	var lastErr error
	for _, candidate := range candidates {
		f, err := os.OpenFile(candidate, os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
		f, err = os.OpenFile(candidate, os.O_WRONLY, 0)
		if err == nil {
			return f, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// PrintRawToSerialPort writes raw data to a serial port.
func PrintRawToSerialPort(port string, data []byte) error {
	f, err := TryOpenSerial(port)
	if err != nil {
		return fmt.Errorf("buka port serial %s gagal: %w", port, err)
	}
	defer f.Close()

	n, err := f.Write(data)
	if err != nil {
		return fmt.Errorf("tulis data ke port serial %s gagal: %w", port, err)
	}
	if n < len(data) {
		return fmt.Errorf("hanya %d dari %d byte yang terkirim ke port %s", n, len(data), port)
	}
	return nil
}

// ListWindowsPrinterPorts is a no-op on non-Windows platforms.
func ListWindowsPrinterPorts(ctx context.Context) ([]string, error) {
	return []string{}, nil
}

// SetWindowsPrinterPort is a no-op on non-Windows platforms.
func SetWindowsPrinterPort(ctx context.Context, printerName, portName string) error {
	return fmt.Errorf("ubah port printer hanya didukung di Windows")
}

// ClearWindowsPrintQueue clears CUPS queue jobs on Unix if available.
func ClearWindowsPrintQueue(ctx context.Context, printerName string) error {
	cmd := exec.CommandContext(ctx, "cancel", "-a", printerName)
	_ = cmd.Run()
	return nil
}
