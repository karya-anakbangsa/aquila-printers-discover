//go:build windows

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	winspool             = syscall.NewLazyDLL("winspool.drv")
	procOpenPrinterW     = winspool.NewProc("OpenPrinterW")
	procStartDocPrinterW = winspool.NewProc("StartDocPrinterW")
	procStartPagePrinter = winspool.NewProc("StartPagePrinter")
	procWritePrinter     = winspool.NewProc("WritePrinter")
	procEndPagePrinter   = winspool.NewProc("EndPagePrinter")
	procEndDocPrinter    = winspool.NewProc("EndDocPrinter")
	procClosePrinter     = winspool.NewProc("ClosePrinter")
)

type docInfo1 struct {
	DocName    *uint16
	OutputFile *uint16
	Datatype   *uint16
}

// PrintRawWin32 sends raw bytes (ESC/POS, TSPL, ZPL, or raw text) directly to a Windows printer queue
// via native Win32 Spooler API (winspool.drv) in under 1 millisecond.
func PrintRawWin32(printerName string, data []byte, docTitle string) error {
	if len(data) == 0 {
		return fmt.Errorf("data cetak kosong")
	}
	pName, err := syscall.UTF16PtrFromString(printerName)
	if err != nil {
		return err
	}

	var hPrinter syscall.Handle
	r1, _, err := procOpenPrinterW.Call(
		uintptr(unsafe.Pointer(pName)),
		uintptr(unsafe.Pointer(&hPrinter)),
		0,
	)
	if r1 == 0 {
		return fmt.Errorf("OpenPrinterW gagal untuk '%s': %w", printerName, err)
	}
	defer procClosePrinter.Call(uintptr(hPrinter))

	if docTitle == "" {
		docTitle = "RSISA Printer Server USB Ticket"
	}
	docName, _ := syscall.UTF16PtrFromString(docTitle)
	dataType, _ := syscall.UTF16PtrFromString("RAW")
	docInfo := docInfo1{
		DocName:  docName,
		Datatype: dataType,
	}

	jobID, _, err := procStartDocPrinterW.Call(
		uintptr(hPrinter),
		1,
		uintptr(unsafe.Pointer(&docInfo)),
	)
	if jobID == 0 {
		return fmt.Errorf("StartDocPrinterW gagal: %w", err)
	}
	defer procEndDocPrinter.Call(uintptr(hPrinter))

	r1, _, err = procStartPagePrinter.Call(uintptr(hPrinter))
	if r1 == 0 {
		return fmt.Errorf("StartPagePrinter gagal: %w", err)
	}
	defer procEndPagePrinter.Call(uintptr(hPrinter))

	var written uint32
	r1, _, err = procWritePrinter.Call(
		uintptr(hPrinter),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&written)),
	)
	if r1 == 0 {
		return fmt.Errorf("WritePrinter gagal: %w", err)
	}

	return nil
}

// PrintRawPowerShellFallback sends raw data to a Windows printer via PowerShell inline C# Spooler P/Invoke.
func PrintRawPowerShellFallback(printerName string, data []byte, docTitle string) error {
	if len(data) == 0 {
		return fmt.Errorf("data cetak kosong")
	}
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("aquila_raw_%d_%d.bin", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("tulis file sementara gagal: %w", err)
	}
	defer os.Remove(tempFile)

	if docTitle == "" {
		docTitle = "RSISA Printer Server USB Ticket"
	}

	psScript := fmt.Sprintf(`
$code = @'
using System;
using System.IO;
using System.Runtime.InteropServices;
public class RawPrinterHelper {
    [DllImport("winspool.Drv", EntryPoint="OpenPrinterA", SetLastError=true)]
    public static extern bool OpenPrinter(string pPrinterName, out IntPtr phPrinter, IntPtr pDefault);
    [DllImport("winspool.Drv", EntryPoint="StartDocPrinterA", SetLastError=true)]
    public static extern bool StartDocPrinter(IntPtr hPrinter, int level, [In, MarshalAs(UnmanagedType.LPStruct)] DOCINFOA di);
    [DllImport("winspool.Drv", SetLastError=true)]
    public static extern bool StartPagePrinter(IntPtr hPrinter);
    [DllImport("winspool.Drv", SetLastError=true)]
    public static extern bool WritePrinter(IntPtr hPrinter, IntPtr pBytes, int dwCount, out int dwWritten);
    [DllImport("winspool.Drv", SetLastError=true)]
    public static extern bool EndPagePrinter(IntPtr hPrinter);
    [DllImport("winspool.Drv", SetLastError=true)]
    public static extern bool EndDocPrinter(IntPtr hPrinter);
    [DllImport("winspool.Drv", SetLastError=true)]
    public static extern bool ClosePrinter(IntPtr hPrinter);
    [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Ansi)]
    public class DOCINFOA {
        public string pDocName;
        public string pOutputFile;
        public string pDataType;
    }
    public static void SendFileToPrinter(string printerName, string filePath, string title) {
        byte[] bytes = File.ReadAllBytes(filePath);
        IntPtr hPrinter;
        if (!OpenPrinter(printerName, out hPrinter, IntPtr.Zero)) {
            throw new Exception("OpenPrinter failed for " + printerName);
        }
        try {
            DOCINFOA di = new DOCINFOA { pDocName = title, pDataType = "RAW" };
            if (!StartDocPrinter(hPrinter, 1, di)) throw new Exception("StartDocPrinter failed");
            if (!StartPagePrinter(hPrinter)) throw new Exception("StartPagePrinter failed");
            IntPtr pBytes = Marshal.AllocCoTaskMem(bytes.Length);
            try {
                Marshal.Copy(bytes, 0, pBytes, bytes.Length);
                int written;
                if (!WritePrinter(hPrinter, pBytes, bytes.Length, out written)) throw new Exception("WritePrinter failed");
            } finally {
                Marshal.FreeCoTaskMem(pBytes);
            }
            EndPagePrinter(hPrinter);
            EndDocPrinter(hPrinter);
        } finally {
            ClosePrinter(hPrinter);
        }
    }
}
'@
if (-not ([System.Management.Automation.PSTypeName]'RawPrinterHelper').Type) {
    Add-Type -TypeDefinition $code -ErrorAction Stop
}
[RawPrinterHelper]::SendFileToPrinter('%s', '%s', '%s')
`, strings.ReplaceAll(printerName, "'", "''"), strings.ReplaceAll(tempFile, "'", "''"), strings.ReplaceAll(docTitle, "'", "''"))

	cmd := PowerShellCommand(context.Background(), "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[PRINTER ERROR] PowerShell fallback gagal: %s %v", string(out), err)
		return fmt.Errorf("PowerShell raw print gagal: %s: %w", string(out), err)
	}
	return nil
}

// PrintRawToWindowsPrinter tries ultra-fast native Win32 FFI (<1ms), falling back to PowerShell.
func PrintRawToWindowsPrinter(printerName string, data []byte, docTitle string) error {
	if err := PrintRawWin32(printerName, data, docTitle); err == nil {
		log.Printf("[PRINTER USB/RAW] Berhasil kirim raw ESC/POS via Win32 FFI ke \"%s\" (<1ms)", printerName)
		return nil
	} else {
		log.Printf("[PRINTER WARN] Win32 API raw print gagal, beralih ke PowerShell fallback: %v", err)
	}
	if err := PrintRawPowerShellFallback(printerName, data, docTitle); err != nil {
		return err
	}
	log.Printf("[PRINTER USB/RAW] Berhasil kirim raw ESC/POS ke Windows Spooler \"%s\" via PowerShell", printerName)
	return nil
}

// ListWindowsPrinterPorts returns all printer port names registered in Windows (USB, COM, LPT, WSD, etc.)
func ListWindowsPrinterPorts(ctx context.Context) ([]string, error) {
	script := `$ports = @(); Get-PrinterPort -ErrorAction SilentlyContinue | ForEach-Object { $ports += $_.Name }; $ports | ConvertTo-Json -Compress`
	cmd := PowerShellCommand(ctx, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("baca port printer gagal: %s: %w", string(out), err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return []string{}, nil
	}
	var ports []string
	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &ports)
	} else {
		var single string
		if err := json.Unmarshal([]byte(trimmed), &single); err == nil && single != "" {
			ports = []string{single}
		}
	}
	return ports, nil
}

// SetWindowsPrinterPort changes the port of an existing printer queue (e.g. POS-80 -> USB002).
func SetWindowsPrinterPort(ctx context.Context, printerName, portName string) error {
	printerName = strings.TrimSpace(printerName)
	portName = strings.TrimSpace(portName)
	if printerName == "" || portName == "" {
		return fmt.Errorf("nama printer dan port tujuan tidak boleh kosong")
	}
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'; Set-Printer -Name '%s' -PortName '%s'`,
		strings.ReplaceAll(printerName, "'", "''"),
		strings.ReplaceAll(portName, "'", "''"),
	)
	cmd := PowerShellCommand(ctx, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gagal mengubah port printer '%s' ke '%s': %s: %w", printerName, portName, string(out), err)
	}
	log.Printf("[PRINTER CONFIG] Berhasil memindahkan port printer '%s' ke '%s'", printerName, portName)
	return nil
}

// ClearWindowsPrintQueue deletes all pending or stuck print jobs for a specific printer.
func ClearWindowsPrintQueue(ctx context.Context, printerName string) error {
	printerName = strings.TrimSpace(printerName)
	if printerName == "" {
		return fmt.Errorf("nama printer tidak boleh kosong")
	}
	script := fmt.Sprintf(`Get-PrintJob -PrinterName '%s' -ErrorAction SilentlyContinue | Remove-PrintJob -ErrorAction SilentlyContinue`,
		strings.ReplaceAll(printerName, "'", "''"),
	)
	cmd := PowerShellCommand(ctx, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gagal membersihkan antrean cetak '%s': %s: %w", printerName, string(out), err)
	}
	log.Printf("[PRINTER QUEUE] Antrean cetak untuk '%s' berhasil dibersihkan", printerName)
	return nil
}
func ListSerialPorts() []string {
	var ports []string
	seen := make(map[string]bool)

	// Method 1: Check Windows registry for registered COM ports
	cmd := exec.Command("reg", "query", `HKLM\HARDWARE\DEVICEMAP\SERIALCOMM`)
	out, err := cmd.Output()
	if err == nil {
		re := regexp.MustCompile(`(?i)REG_SZ\s+(COM\d+)`)
		matches := re.FindAllStringSubmatch(string(out), -1)
		for _, m := range matches {
			if len(m) > 1 {
				p := strings.ToUpper(strings.TrimSpace(m[1]))
				if !seen[p] {
					seen[p] = true
					ports = append(ports, p)
				}
			}
		}
		if len(ports) > 0 {
			return ports
		}
	}

	// Method 2: Fallback to PowerShell
	psCmd := PowerShellCommand(context.Background(), "-NoProfile", "-Command", "[System.IO.Ports.SerialPort]::GetPortNames()")
	psOut, err := psCmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(psOut), "\n") {
			trimmed := strings.ToUpper(strings.TrimSpace(line))
			if strings.HasPrefix(trimmed, "COM") && !seen[trimmed] {
				seen[trimmed] = true
				ports = append(ports, trimmed)
			}
		}
	}
	return ports
}

// TryOpenSerial opens a serial COM port safely.
func TryOpenSerial(port string) (*os.File, error) {
	rawPort := strings.TrimSpace(port)
	stripped := strings.TrimPrefix(rawPort, `\\.\`)
	winDev := rawPort
	if regexp.MustCompile(`(?i)^com\d+$`).MatchString(stripped) {
		winDev = `\\.\` + strings.ToUpper(stripped)
	}

	candidates := []string{winDev, stripped}
	var lastErr error
	for _, candidate := range candidates {
		f, err := os.OpenFile(candidate, os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
		// Try write-only if RDWR fails
		f, err = os.OpenFile(candidate, os.O_WRONLY, 0)
		if err == nil {
			return f, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// PrintRawToSerialPort writes raw data to a COM port.
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
