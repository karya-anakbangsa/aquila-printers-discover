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
if (!(Get-Printer -Name $printerName -ErrorAction SilentlyContinue)) {
    Add-Printer -Name $printerName -DeviceURL $deviceUrl -ErrorAction Stop
}
`, name, uri)

	output, err := hiddenWindowsCommand(ctx, script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gagal menambahkan printer ke Windows: %s (%w)", strings.TrimSpace(string(output)), err)
	}
	return nil
}

