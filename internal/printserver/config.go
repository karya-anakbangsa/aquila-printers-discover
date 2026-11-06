package printserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RSISA-Semarang/rsisa-print-server/internal/utils"
)

// DefaultListen adalah alamat dengarkan bawaan; hanya loopback agar tidak
// bisa diakses dari jaringan lain.
const DefaultListen = "127.0.0.1:17891"

// DefaultOrigins adalah daftar origin bawaan saat print-server.json dibuat
// pertama kali oleh aplikasi desktop.
var DefaultOrigins = []string{"https://farmasi.example.test", "http://localhost:4200"}

type PrinterConfig struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

type RouteConfig struct {
	ModuleCode   string `json:"module_code"`
	DocumentType string `json:"document_type"`
	Printer      string `json:"printer"`
	Copies       int    `json:"copies"`
	Media        string `json:"media"`
}

type LocalPrinter struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	IsDefault      bool   `json:"is_default"`
	OwnerPC        string `json:"owner_pc,omitempty"`
	OwnerIP        string `json:"owner_ip,omitempty"`
	Driver         string `json:"driver,omitempty"`
	Port           string `json:"port,omitempty"`
	IP             string `json:"ip,omitempty"`
	URI            string `json:"uri,omitempty"`
	ConnectionType string `json:"connection_type,omitempty"`
	PrinterType    string `json:"printer_type,omitempty"` // "thermal" atau "document"
}

// IsThermalOrPOS checks if a printer name, driver, or port indicates a thermal receipt / label printer.
func IsThermalOrPOS(name, driver, port string) bool {
	combined := strings.ToUpper(name + " " + driver + " " + port)
	keywords := []string{
		"POS", "THERMAL", "RECEIPT", "STRUK", "KASIR",
		"TM-T", "TM-U", "TM-P", "EPSON TM",
		"RP80", "XP-", "XP58", "XP80", "XPRINTER",
		"58MM", "80MM", "58 MM", "80 MM",
		"MINIPRINTER", "MINI PRINTER",
		"LABEL", "BARCODE", "ZEBRA", "TSC", "ARGOX", "POSTEK",
		"GENERIC / TEXT ONLY", "GENERIC/TEXT", "RAW",
		"BT-", "MPT-", "ZJ-", "GOOJPRT", "PANDA", "IWARE", "EPX", "BLUEBAMBOO",
	}
	for _, kw := range keywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}

// DetermineConnectionType detects whether a printer is USB, serial, network, or virtual.
func DetermineConnectionType(port, driver, ip string) string {
	portUpper := strings.ToUpper(strings.TrimSpace(port))
	driverUpper := strings.ToUpper(strings.TrimSpace(driver))
	if strings.HasPrefix(portUpper, "USB") || strings.HasPrefix(portUpper, "DOT4") || strings.Contains(portUpper, "USB") || strings.Contains(driverUpper, "USB") {
		return "usb"
	}
	if strings.HasPrefix(portUpper, "COM") || strings.HasPrefix(portUpper, "LPT") {
		return "serial"
	}
	if ip != "" || strings.HasPrefix(portUpper, "IP_") || strings.HasPrefix(portUpper, "TCP") || strings.HasPrefix(portUpper, "WSD-") || strings.Contains(portUpper, ".") {
		return "network"
	}
	if strings.HasPrefix(portUpper, "FILE:") || strings.HasPrefix(portUpper, "PORTPROMPT:") || strings.Contains(driverUpper, "PDF") || strings.Contains(driverUpper, "XPS") {
		return "virtual"
	}
	if portUpper != "" {
		return "local"
	}
	return ""
}

type Config struct {
	Listen         string          `json:"listen"`
	LANAccessToken string          `json:"lan_access_token,omitempty"`
	AllowedOrigin  []string        `json:"allowed_origins"`
	Printers       []PrinterConfig `json:"printers,omitempty"`
	Routes         []RouteConfig   `json:"routes,omitempty"`
}

// DefaultConfig mengembalikan konfigurasi bawaan yang ditulis aplikasi desktop
// ke print-server.json saat pertama kali jalan.
func DefaultConfig() Config {
	return Config{
		Listen:        DefaultListen,
		AllowedOrigin: append([]string(nil), DefaultOrigins...),
	}
}

// LoadConfig membaca konfigurasi dari path; file harus sudah ada.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	contents, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("baca konfigurasi %s: %w (salin print-server.example.json menjadi print-server.json)", path, err)
	}
	if err := json.Unmarshal(contents, &cfg); err != nil {
		return cfg, fmt.Errorf("konfigurasi JSON tidak valid: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate menormalkan dan memeriksa seluruh isi konfigurasi.
func (cfg *Config) Validate() error {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || port == "" {
		return errors.New("alamat listen harus berisi host dan port yang valid")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("port listen harus antara 1 dan 65535")
	}
	if host != "localhost" && host != "127.0.0.1" {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || ip.To4() == nil || (!ip.IsPrivate() && !ip.IsUnspecified()) {
			return errors.New("alamat LAN harus berupa IP private komputer ini atau 0.0.0.0")
		}
		if len(cfg.LANAccessToken) > 0 && (len(cfg.LANAccessToken) < 24 || strings.TrimSpace(cfg.LANAccessToken) != cfg.LANAccessToken) {
			return errors.New("akses LAN memerlukan lan_access_token minimal 24 karakter")
		}
	}
	printerNames := make(map[string]string, len(cfg.Printers))
	for i := range cfg.Printers {
		printer := &cfg.Printers[i]
		printer.Name = strings.TrimSpace(printer.Name)
		if printer.Name == "" {
			return errors.New("nama printer tidak boleh kosong")
		}
		uri, err := utils.NormalizePrinterURI(printer.URI)
		if err != nil {
			return fmt.Errorf("printer %q: %w", printer.Name, err)
		}
		printer.URI = uri
		printerNames[strings.ToLower(printer.Name)] = printer.Name
	}
	seenRoutes := make(map[string]bool, len(cfg.Routes))
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		if route.ModuleCode == "" {
			route.ModuleCode = "umum"
		}
		route.ModuleCode = strings.TrimSpace(route.ModuleCode)
		route.DocumentType = strings.TrimSpace(route.DocumentType)
		key := routeKey(route.ModuleCode, route.DocumentType)
		if !validCode(route.ModuleCode) || !validCode(route.DocumentType) || seenRoutes[key] {
			return fmt.Errorf("module_code/document_type tidak valid atau duplikat: %s", key)
		}
		seenRoutes[key] = true
		if route.Copies == 0 {
			route.Copies = 1
		}
		if route.Copies < 1 || route.Copies > 20 {
			return fmt.Errorf("jumlah salinan pada route %s harus 1 sampai 20", key)
		}
		if _, exists := printerNames[strings.ToLower(route.Printer)]; !exists {
			return fmt.Errorf("printer %q pada route %s tidak terdaftar", route.Printer, key)
		}
	}
	normalized, err := NormalizeOrigins(cfg.AllowedOrigin)
	if err != nil {
		return err
	}
	cfg.AllowedOrigin = normalized
	return nil
}

// SaveConfig menulis konfigurasi secara atomik (tulis ke file sementara lalu
// rename) agar file tidak pernah setengah tertulis.
func SaveConfig(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("tulis konfigurasi: %w", err)
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("buat folder konfigurasi: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".print-server-*.json")
	if err != nil {
		return fmt.Errorf("tulis konfigurasi sementara: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return fmt.Errorf("tulis konfigurasi: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("tutup konfigurasi sementara: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("atur izin konfigurasi: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("simpan konfigurasi %s: %w", path, err)
	}
	return nil
}

// EnsureConfigFile menulis print-server.json berisi konfigurasi bawaan bila
// file belum ada. Mengembalikan true bila file baru dibuat.
func EnsureConfigFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("periksa konfigurasi %s: %w", path, err)
	}
	if err := SaveConfig(path, DefaultConfig()); err != nil {
		return false, err
	}
	return true, nil
}
