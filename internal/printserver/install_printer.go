package printserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// SanitizeCUPSQueueName membersihkan nama printer agar sesuai standar nama antrean CUPS
// (hanya huruf, angka, underscore, dan tanda minus; tanpa spasi).
func SanitizeCUPSQueueName(name string) string {
	name = strings.TrimSpace(name)
	reg := regexp.MustCompile(`[^a-zA-Z0-9_\-]`)
	cleaned := reg.ReplaceAllString(name, "_")
	regMulti := regexp.MustCompile(`_+`)
	cleaned = regMulti.ReplaceAllString(cleaned, "_")
	cleaned = strings.Trim(cleaned, "_-")
	if cleaned == "" {
		cleaned = "Printer_LAN"
	}
	return cleaned
}

// InstallNetworkPrinter menambahkan antrean printer jaringan (IPP/IPPS) langsung ke
// sistem operasi lokal (CUPS pada macOS/Linux, Windows Spooler pada Windows).
func InstallNetworkPrinter(ctx context.Context, name, uri, model string) error {
	name = strings.TrimSpace(name)
	uri = strings.TrimSpace(uri)
	if name == "" {
		return errors.New("nama printer tidak boleh kosong")
	}
	if uri == "" {
		return errors.New("URI printer tidak boleh kosong")
	}

	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Scheme != "ipp" && parsed.Scheme != "ipps" && parsed.Scheme != "socket" && parsed.Scheme != "tcp") {
		return fmt.Errorf("URI printer tidak valid: %s", uri)
	}

	switch runtime.GOOS {
	case "darwin", "linux":
		return installCUPSPrinter(ctx, name, uri, model)
	case "windows":
		return installWindowsNetworkPrinter(ctx, name, uri, model)
	default:
		return fmt.Errorf("OS %q belum didukung untuk instalasi otomatis", runtime.GOOS)
	}
}

// installCUPSPrinter menggunakan lpadmin untuk mendaftarkan printer IPP Everywhere di CUPS (macOS/Linux).
func installCUPSPrinter(ctx context.Context, name, uri, model string) error {
	queueName := SanitizeCUPSQueueName(name)
	displayName := name
	if model != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(model)) {
		displayName = fmt.Sprintf("%s (%s)", name, model)
	}

	lpadminPath := "lpadmin"
	if _, err := exec.LookPath(lpadminPath); err != nil {
		if _, errSub := exec.LookPath("/usr/sbin/lpadmin"); errSub == nil {
			lpadminPath = "/usr/sbin/lpadmin"
		}
	}

	// Coba pertama kali dengan driverless standard IPP Everywhere:
	// lpadmin -p <queue> -E -v <uri> -m everywhere -D "<displayName>"
	cmd := exec.CommandContext(ctx, lpadminPath, "-p", queueName, "-E", "-v", uri, "-m", "everywhere", "-D", displayName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback jika -m everywhere gagal
		cmdFallback := exec.CommandContext(ctx, lpadminPath, "-p", queueName, "-E", "-v", uri, "-D", displayName)
		outputFallback, errFallback := cmdFallback.CombinedOutput()
		if errFallback != nil {
			return fmt.Errorf("lpadmin gagal: %s (%w)", strings.TrimSpace(string(output)), err)
		}
		_ = outputFallback
	}

	// Pastikan antrean di-enable dan menerima jobs
	enablePath := "cupsenable"
	if _, err := exec.LookPath(enablePath); err != nil {
		if _, errSub := exec.LookPath("/usr/sbin/cupsenable"); errSub == nil {
			enablePath = "/usr/sbin/cupsenable"
		}
	}
	_ = exec.CommandContext(ctx, enablePath, queueName).Run()

	acceptPath := "cupsaccept"
	if _, err := exec.LookPath(acceptPath); err != nil {
		if _, errSub := exec.LookPath("/usr/sbin/cupsaccept"); errSub == nil {
			acceptPath = "/usr/sbin/cupsaccept"
		}
	}
	_ = exec.CommandContext(ctx, acceptPath, queueName).Run()

	return nil
}

// installWindowsNetworkPrinter menggunakan PowerShell Add-Printer untuk mendaftarkan antrean printer di Windows.
func installWindowsNetworkPrinter(ctx context.Context, name, uri, model string) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$printerName = %q
$deviceUrl = %q
if ($deviceUrl -like "socket://*" -or $deviceUrl -like "tcp://*") {
    $clean = $deviceUrl -replace "^[a-z]+://", ""
    $parts = $clean -split ":"
    $hostAddr = $parts[0]
    $portNum = 9100
    if ($parts.Length -gt 1) { [int]::TryParse($parts[1], [ref]$portNum) }
    $portName = "IP_" + $hostAddr
    if (!(Get-PrinterPort -Name $portName -ErrorAction SilentlyContinue)) {
        Add-PrinterPort -Name $portName -PrinterHostAddress $hostAddr -PortNumber $portNum -ErrorAction SilentlyContinue
    }
    if (!(Get-Printer -Name $printerName -ErrorAction SilentlyContinue)) {
        # Coba pasang dengan Generic / Text Only
        Add-Printer -Name $printerName -PortName $portName -DriverName "Generic / Text Only" -ErrorAction SilentlyContinue
    }
} else {
    if (!(Get-Printer -Name $printerName -ErrorAction SilentlyContinue)) {
        Add-Printer -Name $printerName -DeviceURL $deviceUrl -ErrorAction Stop
    }
}
`, name, uri)

	output, err := hiddenWindowsCommand(ctx, script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gagal menambahkan printer ke Windows: %s (%w)", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// RemoveNetworkPrinter menghapus printer antrean dari sistem operasi (Windows / macOS / Linux).
func RemoveNetworkPrinter(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("nama printer tidak boleh kosong")
	}

	switch runtime.GOOS {
	case "darwin", "linux":
		queueName := SanitizeCUPSQueueName(name)
		lpadminPath := "lpadmin"
		if _, err := exec.LookPath(lpadminPath); err != nil {
			if _, errSub := exec.LookPath("/usr/sbin/lpadmin"); errSub == nil {
				lpadminPath = "/usr/sbin/lpadmin"
			}
		}
		cmd := exec.CommandContext(ctx, lpadminPath, "-x", queueName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("lpadmin -x gagal: %s (%w)", strings.TrimSpace(string(output)), err)
		}
		return nil
	case "windows":
		script := fmt.Sprintf(`
$ErrorActionPreference = 'Continue'
$target = %q
$targets = @()
$exact = Get-Printer -Name $target -ErrorAction SilentlyContinue
if ($exact) {
    $targets += $exact
} else {
    $similar = Get-Printer | Where-Object { 
        $_.Name -eq $target -or 
        $_.Name -like "*$target*" -or 
        $target -like "*$($_.Name)*" 
    }
    if ($similar) {
        $targets += $similar
    }
}

foreach ($p in $targets) {
    $pName = $p.Name
    Get-PrintJob -PrinterName $pName -ErrorAction SilentlyContinue | Remove-PrintJob -ErrorAction SilentlyContinue
    Remove-Printer -Name $pName -ErrorAction SilentlyContinue
    if (Get-Printer -Name $pName -ErrorAction SilentlyContinue) {
        (Get-CimInstance Win32_Printer -Filter "Name='$pName'" -ErrorAction SilentlyContinue) | Remove-CimInstance -ErrorAction SilentlyContinue
    }
    if (Get-Printer -Name $pName -ErrorAction SilentlyContinue) {
        & rundll32.exe printui.dll,PrintUIEntry /dl /n "$pName"
    }
}

if ($targets.Count -eq 0) {
    & rundll32.exe printui.dll,PrintUIEntry /dl /n $target
}
`, name)
		output, err := hiddenWindowsCommand(ctx, script).CombinedOutput()
		if err != nil {
			return fmt.Errorf("gagal menghapus printer dari Windows: %s (%w)", strings.TrimSpace(string(output)), err)
		}
		return nil
	default:
		return fmt.Errorf("OS %q belum didukung untuk penghapusan printer", runtime.GOOS)
	}
}


