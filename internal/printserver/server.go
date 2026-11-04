package printserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RSISA-Semarang/rsisa-print-server/internal/utils"

	"github.com/gofiber/fiber/v2"
)

const maxPDFBytes = 20 << 20

// EventLog menerima catatan peristiwa layanan untuk ditampilkan di UI desktop.
type EventLog func(level, message string)

// Server membungkus aplikasi Fiber print server beserta konfigurasinya.
// Konfigurasi dapat diperbarui saat berjalan (misalnya daftar origin) tanpa
// perlu me-restart listener.
type Server struct {
	mu     sync.RWMutex
	cfg    Config
	app    *fiber.App
	events EventLog
}

// New membuat server print dengan konfigurasi awal. events boleh nil.
func New(cfg Config, events EventLog) *Server {
	if events == nil {
		events = func(level, message string) {
			log.Printf("%s %s", level, message)
		}
	}
	server := &Server{cfg: cfg, events: events}
	app := fiber.New(fiber.Config{
		BodyLimit:     maxPDFBytes + 1024,
		ReadTimeout:   30 * time.Second,
		WriteTimeout:  60 * time.Second,
		Concurrency:   256 * 1024,
		StrictRouting: true,
	})
	app.Use(server.corsMiddleware)
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "service": "rsisa-print-server"})
	})
	app.Get("/printers", listLocalPrinters)
	app.Get("/printers/discover", discoverNetworkPrinters)
	app.Get("/printers/usb", listUSBPrinters)
	app.Get("/printers/ports", listActivePorts)
	app.Post("/printers/set-port", server.setPrinterPortEndpoint)
	app.Post("/printers/clear-queue", server.clearPrinterQueueEndpoint)
	app.Post("/printers/test", server.testLocalPrinter)
	app.Post("/printers/raw", server.submitRaw)
	app.Post("/print-raw", server.submitRaw)
	app.Post("/print/:documentType", server.submitPDF)
	server.app = app
	return server
}

// App mengembalikan aplikasi Fiber yang dipakai (untuk pengujian).
func (s *Server) App() *fiber.App { return s.app }

// Listen menjalankan server pada alamat yang diberikan dan memblokir.
func (s *Server) Listen(addr string) error { return s.app.Listen(addr) }

// Shutdown menghentikan server secara graceful.
func (s *Server) Shutdown(ctx context.Context) error { return s.app.ShutdownWithContext(ctx) }

// Config mengembalikan salinan konfigurasi saat ini.
func (s *Server) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	cfg.AllowedOrigin = append([]string(nil), s.cfg.AllowedOrigin...)
	cfg.Printers = append([]PrinterConfig(nil), s.cfg.Printers...)
	cfg.Routes = append([]RouteConfig(nil), s.cfg.Routes...)
	return cfg
}

// UpdateConfig mengganti konfigurasi yang dipakai saat berjalan; efektif untuk
// request berikutnya tanpa restart.
func (s *Server) UpdateConfig(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

func (s *Server) logf(level, format string, args ...any) {
	s.events(level, fmt.Sprintf(format, args...))
}

func (s *Server) submitPDF(c *fiber.Ctx) error {
	cfg := s.Config()
	if len(c.Body()) > maxPDFBytes {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "ukuran PDF maksimal 20 MB")
	}
	pdf := c.Body()
	isRaw := strings.EqualFold(c.Get("X-RSISA-Raw"), "true") ||
		strings.HasPrefix(c.Get(fiber.HeaderContentType), "text/") ||
		strings.HasPrefix(c.Get(fiber.HeaderContentType), "application/octet-stream")
	if !isRaw && (len(pdf) < 5 || string(pdf[:5]) != "%PDF-") {
		return fiber.NewError(fiber.StatusUnsupportedMediaType, "kirim isi file PDF dengan Content-Type application/pdf")
	}
	module := strings.TrimSpace(c.Query("module_code"))
	if module == "" {
		module = "umum"
	}
	docType := strings.TrimSpace(c.Params("documentType"))
	var route RouteConfig
	var printer PrinterConfig
	if rawURI := strings.TrimSpace(c.Get("X-RSISA-Printer-URI")); rawURI != "" {
		uri, err := utils.NormalizePrinterURI(rawURI)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		copies := 1
		if rawCopies := strings.TrimSpace(c.Get("X-RSISA-Copies")); rawCopies != "" {
			copies, err = strconv.Atoi(rawCopies)
			if err != nil || copies < 1 || copies > 20 {
				return fiber.NewError(fiber.StatusBadRequest, "jumlah salinan harus antara 1 dan 20")
			}
		}
		printer = PrinterConfig{Name: strings.TrimSpace(c.Get("X-RSISA-Printer-Name")), URI: uri}
		if printer.Name == "" {
			printer.Name = uri
		}
		route = RouteConfig{Copies: copies, Media: strings.TrimSpace(c.Get("X-RSISA-Media"))}
	} else {
		var found bool
		route, found = findRoute(cfg, module, docType)
		if !found && module != "umum" {
			route, found = findRoute(cfg, "umum", docType)
		}
		if !found {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "printer untuk cetakan ini belum diatur di aplikasi")
		}
		printer, found = findPrinter(cfg, route.Printer)
		if !found {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "printer tujuan tidak ditemukan di konfigurasi print server")
		}
	}
	if isRaw {
		queueName, windowsQueueErr := utils.WindowsQueueName(printer.URI)
		if windowsQueueErr == nil {
			if err := utils.PrintRawToWindowsPrinter(queueName, pdf, docType); err != nil {
				s.logf("ERROR", "POST /print/%s gagal (raw) ke %s: %v", docType, printer.Name, err)
				return fiber.NewError(fiber.StatusBadGateway, err.Error())
			}
			s.logf("INFO", "POST /print/%s -> %s 202 (Windows USB/RAW)", docType, printer.Name)
			return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
				"message": "Dokumen RAW sudah dikirim ke antrean printer USB. Periksa hasil cetaknya.",
				"printer": printer.Name,
			})
		}
	}
	if utils.IsTCPPrinter(printer.URI) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "TCP 9100 mengirim PDF mentah dan dapat mencetak karakter acak. Pilih antrean printer PC ini melalui IPP (127.0.0.1:631).")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 50*time.Second)
	defer cancel()
	ippJobID, err := submitPrinterPDF(ctx, printer.URI, pdf, docType, "farmasi", route.Copies, route.Media)
	if err != nil {
		s.logf("ERROR", "POST /print/%s gagal ke %s: %v", docType, printer.Name, err)
		var ippErr *utils.IPPSubmissionError
		if errors.As(err, &ippErr) {
			return fiber.NewError(fiber.StatusBadGateway, "status cetak belum pasti; periksa printer sebelum mencoba lagi agar tidak tercetak ganda. "+err.Error())
		}
		return fiber.NewError(fiber.StatusBadGateway, err.Error())
	}
	_, windowsQueueErr := utils.WindowsQueueName(printer.URI)
	windowsQueue := windowsQueueErr == nil
	if ippJobID > 0 {
		s.logf("INFO", "POST /print/%s -> %s 202 (IPP job %d)", docType, printer.Name, ippJobID)
	} else if windowsQueue {
		s.logf("INFO", "POST /print/%s -> %s 202 (Windows driver)", docType, printer.Name)
	} else {
		s.logf("INFO", "POST /print/%s -> %s 202 (local print queue)", docType, printer.Name)
	}
	response := fiber.Map{
		"message": "Dokumen sudah dikirim ke printer. Periksa hasil cetaknya.",
		"printer": printer.Name,
	}
	if !windowsQueue {
		response["ipp_job_id"] = ippJobID
	}
	return c.Status(fiber.StatusAccepted).JSON(response)
}

func listLocalPrinters(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	var printers []LocalPrinter
	var err error
	switch runtime.GOOS {
	case "windows":
		printers, err = listWindowsPrinters(ctx)
	case "darwin", "linux":
		printers, err = listCUPSPrinters(ctx)
	default:
		return fiber.NewError(fiber.StatusNotImplemented, "deteksi printer belum didukung pada sistem operasi ini")
	}
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "tidak dapat membaca antrean printer pada PC ini: "+err.Error())
	}
	if printers == nil {
		printers = []LocalPrinter{}
	}
	return c.JSON(fiber.Map{"data": printers, "platform": runtime.GOOS})
}

func discoverNetworkPrinters(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 3*time.Second)
	defer cancel()
	printers, err := discoverIPPPrinters(ctx)
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "pencarian Bonjour/mDNS gagal: "+err.Error())
	}
	if printers == nil {
		printers = []discoveredPrinter{}
	}
	return c.JSON(fiber.Map{"data": printers, "platform": runtime.GOOS})
}

func listCUPSPrinters(ctx context.Context) ([]LocalPrinter, error) {
	output, err := exec.CommandContext(ctx, "lpstat", "-p").CombinedOutput()
	if err != nil {
		return nil, commandFailure("lpstat -p", output, err)
	}
	defaultOutput, _ := exec.CommandContext(ctx, "lpstat", "-d").CombinedOutput()
	defaultName := ""
	if index := strings.Index(string(defaultOutput), ":"); index >= 0 {
		defaultName = strings.TrimSpace(string(defaultOutput[index+1:]))
		if defaultName == "none" {
			defaultName = ""
		}
	}
	printers := make([]LocalPrinter, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "printer ") {
			continue
		}
		details := strings.TrimPrefix(line, "printer ")
		separator := strings.Index(details, " is ")
		if separator <= 0 {
			continue
		}
		name := strings.TrimSpace(details[:separator])
		statusText := strings.ToLower(details[separator+4:])
		state := "ready"
		switch {
		case strings.Contains(statusText, "disabled"), strings.Contains(statusText, "paused"):
			state = "disabled"
		case strings.Contains(statusText, "offline"), strings.Contains(statusText, "not responding"):
			state = "offline"
		case strings.Contains(statusText, "printing"):
			state = "printing"
		case !strings.Contains(statusText, "idle") && !strings.Contains(statusText, "enabled"):
			state = "unknown"
		}
		printers = append(printers, LocalPrinter{Name: name, State: state, IsDefault: name == defaultName, ConnectionType: "local"})
	}
	return printers, nil
}

func listWindowsPrinters(ctx context.Context) ([]LocalPrinter, error) {
	script := `$ErrorActionPreference = 'Stop'; $ports = @{}; Get-CimInstance Win32_TCPIPPrinterPort -ErrorAction SilentlyContinue | ForEach-Object { $ports[$_.Name] = $_.HostAddress }; $items = @(Get-CimInstance Win32_Printer | ForEach-Object { $state = if ($_.WorkOffline -or $_.PrinterStatus -eq 7) { 'offline' } elseif ($_.PrinterStatus -eq 8 -or $_.PrinterStatus -eq 6) { 'disabled' } elseif ($_.PrinterStatus -eq 4) { 'printing' } elseif ($_.PrinterStatus -eq 3 -or $_.PrinterStatus -eq 0) { 'ready' } else { 'unknown' }; [PSCustomObject]@{ name = $_.Name; state = $state; is_default = $_.Default; driver = $_.DriverName; port = $_.PortName; ip = $ports[$_.PortName] } }); ConvertTo-Json -InputObject $items -Compress`
	output, err := hiddenWindowsCommand(ctx, script).CombinedOutput()
	if err != nil {
		return nil, commandFailure("Get-CimInstance Win32_Printer", output, err)
	}
	return parseWindowsPrinters(output)
}

func parseWindowsPrinters(output []byte) ([]LocalPrinter, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return []LocalPrinter{}, nil
	}
	var printers []LocalPrinter
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &printers); err != nil {
			return nil, fmt.Errorf("baca daftar printer Windows: %w", err)
		}
	} else {
		var single LocalPrinter
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("baca printer tunggal Windows: %w", err)
		}
		printers = []LocalPrinter{single}
	}
	for index := range printers {
		printers[index].URI = utils.WindowsQueueURI(printers[index].Name)
		printers[index].ConnectionType = DetermineConnectionType(printers[index].Port, printers[index].Driver, printers[index].IP)
		if IsThermalOrPOS(printers[index].Name, printers[index].Driver, printers[index].Port) {
			printers[index].PrinterType = "thermal"
		} else {
			printers[index].PrinterType = "document"
		}
	}
	return printers, nil
}

type testPrinterRequest struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
	utils.PrinterTestOptions
}

func (s *Server) testLocalPrinter(c *fiber.Ctx) error {
	var request testPrinterRequest
	if err := c.BodyParser(&request); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "pengaturan tes printer tidak valid")
	}
	request.Name = strings.TrimSpace(request.Name)
	request.URI = strings.TrimSpace(request.URI)
	if request.URI == "" && request.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "pilih printer yang akan dites")
	}
	if _, _, _, err := request.PrinterTestOptions.Dimensions(); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if request.Mode != "" && request.Mode != "bw" && request.Mode != "color" && request.Mode != "raw" {
		return fiber.NewError(fiber.StatusBadRequest, "mode cetak harus hitam putih, warna, atau raw")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 45*time.Second)
	defer cancel()
	if request.URI != "" {
		uri, err := utils.NormalizePrinterURI(request.URI)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		if utils.IsTCPPrinter(uri) {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "TCP 9100 mengirim PDF mentah dan dapat mencetak karakter acak. Pilih antrean printer PC ini melalui IPP (127.0.0.1:631).")
		}
		if request.Name == "" {
			request.Name = uri
		}
		if request.Mode == "raw" {
			if qName, qErr := utils.WindowsQueueName(uri); qErr == nil {
				rawData := generateRawTestBytes(qName)
				if err := utils.PrintRawToWindowsPrinter(qName, rawData, "Tes Printer USB"); err != nil {
					s.logf("ERROR", "tes raw printer %s gagal: %v", qName, err)
					return fiber.NewError(fiber.StatusBadGateway, err.Error())
				}
				s.logf("INFO", "halaman uji raw ESC/POS dikirim ke antrean USB %s", qName)
				return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
					"message": "Halaman uji RAW/ESC-POS dikirim ke printer USB. Periksa hasil cetaknya.",
					"printer": qName,
					"uri":     uri,
					"state":   "sent",
					"mode":    "raw",
				})
			}
		}
		pdf, err := utils.GeneratePrinterTestPDF(request.Name, request.PrinterTestOptions)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "gagal membuat halaman tes: "+err.Error())
		}
		ippJobID, err := submitPrinterPDF(ctx, uri, pdf, "Tes printer", "rsisa", 1, "")
		if err != nil {
			s.logf("ERROR", "tes printer %s gagal: %v", request.Name, err)
			var ippErr *utils.IPPSubmissionError
			if errors.As(err, &ippErr) {
				return fiber.NewError(fiber.StatusBadGateway, "status cetak belum pasti; periksa printer sebelum mencoba lagi agar tidak tercetak ganda. "+err.Error())
			}
			return fiber.NewError(fiber.StatusBadGateway, err.Error())
		}
		s.logf("INFO", "halaman uji dikirim ke %s (job %d)", request.Name, ippJobID)
		response := fiber.Map{
			"message": "Halaman uji kualitas dikirim ke antrean cetak. Periksa hasil cetaknya.",
			"printer": request.Name,
			"uri":     uri,
			"state":   "sent",
		}
		if ippJobID > 0 {
			response["ipp_job_id"] = ippJobID
		}
		return c.Status(fiber.StatusAccepted).JSON(response)
	}
	printers, err := listPrintersForPlatform(ctx)
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "tidak dapat membaca antrean printer pada PC ini: "+err.Error())
	}
	var selected *LocalPrinter
	for index := range printers {
		if printers[index].Name == request.Name {
			selected = &printers[index]
			break
		}
	}
	if selected == nil {
		return fiber.NewError(fiber.StatusNotFound, "printer tidak ditemukan pada PC ini; muat ulang daftar printer")
	}
	if selected.State == "offline" || selected.State == "disabled" {
		return fiber.NewError(fiber.StatusConflict, "printer sedang offline atau dinonaktifkan pada PC ini")
	}
	if request.Mode == "raw" {
		rawData := generateRawTestBytes(selected.Name)
		var err error
		if selected.ConnectionType == "serial" || strings.HasPrefix(strings.ToUpper(selected.Port), "COM") {
			err = utils.PrintRawToSerialPort(selected.Port, rawData)
		} else {
			err = utils.PrintRawToWindowsPrinter(selected.Name, rawData, "Tes Printer USB")
		}
		if err != nil {
			s.logf("ERROR", "tes raw printer %s gagal: %v", selected.Name, err)
			return fiber.NewError(fiber.StatusBadGateway, err.Error())
		}
		s.logf("INFO", "halaman uji raw ESC/POS dikirim ke antrean USB %s", selected.Name)
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message": "Halaman uji RAW/ESC-POS dikirim ke printer USB. Periksa hasil cetaknya.",
			"printer": selected.Name,
			"state":   "sent",
			"mode":    "raw",
		})
	}
	if err := sendQualityTestPage(ctx, selected.Name, request.PrinterTestOptions); err != nil {
		s.logf("ERROR", "tes printer %s gagal: %v", selected.Name, err)
		return fiber.NewError(fiber.StatusBadGateway, err.Error())
	}
	s.logf("INFO", "halaman uji dikirim ke antrean %s", selected.Name)
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"message": "Halaman uji kualitas dikirim ke antrean printer. Periksa hasil cetaknya.",
		"printer": selected.Name,
		"state":   selected.State,
	})
}

func listPrintersForPlatform(ctx context.Context) ([]LocalPrinter, error) {
	switch runtime.GOOS {
	case "windows":
		return listWindowsPrinters(ctx)
	case "darwin", "linux":
		return listCUPSPrinters(ctx)
	default:
		return nil, fmt.Errorf("OS %q belum didukung", runtime.GOOS)
	}
}

// ListPrinters membaca antrean printer yang dikenal OS pada PC ini.
func ListPrinters(ctx context.Context) ([]LocalPrinter, error) {
	return listPrintersForPlatform(ctx)
}

// SetPrinterPortFromDesktop updates the port of a printer from the desktop app.
func SetPrinterPortFromDesktop(ctx context.Context, printerName, portName string) error {
	return utils.SetWindowsPrinterPort(ctx, printerName, portName)
}

// ClearPrinterQueueFromDesktop removes all queued jobs for a printer from the desktop app.
func ClearPrinterQueueFromDesktop(ctx context.Context, printerName string) error {
	return utils.ClearWindowsPrintQueue(ctx, printerName)
}

// ListPrinterPortsFromDesktop returns all registered printer ports.
func ListPrinterPortsFromDesktop(ctx context.Context) ([]string, error) {
	ports, _ := utils.ListWindowsPrinterPorts(ctx)
	serialPorts := utils.ListSerialPorts()
	seen := make(map[string]bool)
	var allPorts []string
	for _, p := range ports {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToUpper(p)] {
			seen[strings.ToUpper(p)] = true
			allPorts = append(allPorts, p)
		}
	}
	for _, p := range serialPorts {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToUpper(p)] {
			seen[strings.ToUpper(p)] = true
			allPorts = append(allPorts, p)
		}
	}
	if allPorts == nil {
		allPorts = []string{}
	}
	return allPorts, nil
}

// TestPrinterFromDesktop menguji cetak ke printer tertentu dari aplikasi desktop.
// Jika mode bernilai "auto" atau kosong, secara otomatis mendeteksi apakah printer thermal (RAW ESC/POS) atau printer dokumen (A4).
func TestPrinterFromDesktop(ctx context.Context, name, uri, mode string) error {
	name = strings.TrimSpace(name)
	uri = strings.TrimSpace(uri)
	mode = strings.ToLower(strings.TrimSpace(mode))

	// Jika mode "auto" atau kosong, lakukan deteksi dinamis:
	if mode == "" || mode == "auto" {
		if strings.HasPrefix(strings.ToUpper(uri), "SERIAL://") || strings.HasPrefix(strings.ToUpper(name), "COM") || IsThermalOrPOS(name, "", "") {
			mode = "raw"
		} else {
			mode = "bw"
		}
	}

	if mode == "raw" {
		if qName, err := utils.WindowsQueueName(uri); err == nil && qName != "" {
			name = qName
		}
		if name == "" {
			return errors.New("nama printer tujuan tidak boleh kosong")
		}
		rawData := generateRawTestBytes(name)
		if strings.HasPrefix(strings.ToUpper(uri), "SERIAL://") || strings.HasPrefix(strings.ToUpper(name), "COM") {
			port := strings.TrimPrefix(uri, "serial://")
			if port == "" {
				port = name
			}
			return utils.PrintRawToSerialPort(port, rawData)
		}
		return utils.PrintRawToWindowsPrinter(name, rawData, "Tes Printer USB")
	}

	opts := utils.PrinterTestOptions{
		Paper: "A4",
		Mode:  mode,
	}
	if opts.Mode == "" {
		opts.Mode = "bw"
	}
	if uri != "" {
		if _, qErr := utils.WindowsQueueName(uri); qErr != nil {
			// Printer IPP jaringan Wi-Fi
			pdf, err := utils.GeneratePrinterTestPDF(name, opts)
			if err != nil {
				return err
			}
			_, err = utils.PrintIPPDocument(ctx, uri, pdf, "Tes Printer", "aquila", 1, "")
			return err
		}
	}
	return sendQualityTestPage(ctx, name, opts)
}

func sendQualityTestPage(ctx context.Context, printerName string, options utils.PrinterTestOptions) error {
	switch runtime.GOOS {
	case "windows":
		return sendWindowsQualityTest(ctx, printerName, options)
	case "darwin", "linux":
		pdf, err := utils.GeneratePrinterTestPDF(printerName, options)
		if err != nil {
			return err
		}
		return printPDFToCUPS(ctx, printerName, pdf, "RSISA Printer Test", 1, "")
	default:
		return fmt.Errorf("OS %q belum didukung", runtime.GOOS)
	}
}

func submitPrinterPDF(ctx context.Context, uri string, pdf []byte, jobName, userName string, copies int, media string) (int, error) {
	if queueName, err := utils.WindowsQueueName(uri); err == nil {
		if runtime.GOOS != "windows" {
			return 0, fmt.Errorf("antrean Windows hanya dapat dicetak dari Aquila yang berjalan di Windows")
		}
		return 0, printPDFToWindowsQueue(ctx, queueName, pdf, jobName, copies, media)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if queue := localCUPSQueue(uri); queue != "" {
			return 0, printPDFToCUPS(ctx, queue, pdf, jobName, copies, media)
		}
	}
	return utils.PrintIPPDocument(ctx, uri, pdf, jobName, userName, copies, media)
}

func localCUPSQueue(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "ipp" || parsed.Port() != "631" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		return ""
	}
	queue, ok := strings.CutPrefix(parsed.Path, "/printers/")
	if !ok || queue == "" || strings.ContainsAny(queue, "/\x00") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	return queue
}

func printPDFToCUPS(ctx context.Context, printerName string, pdf []byte, jobName string, copies int, media string) error {
	if len(pdf) < 5 || string(pdf[:5]) != "%PDF-" {
		return errors.New("dokumen bukan file PDF yang valid")
	}
	if copies < 1 {
		copies = 1
	}
	if copies > 20 {
		return errors.New("jumlah salinan maksimal 20")
	}
	if strings.ContainsAny(media, " \t\r\n") {
		return errors.New("ukuran kertas tidak valid")
	}
	file, err := os.CreateTemp("", "rsisa-print-*.pdf")
	if err != nil {
		return fmt.Errorf("buat dokumen cetak: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(pdf); err != nil {
		_ = file.Close()
		return fmt.Errorf("tulis dokumen cetak: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("simpan dokumen cetak: %w", err)
	}
	args := []string{"-d", printerName, "-t", jobName, "-n", strconv.Itoa(copies), "-o", "job-sheets=none"}
	if media != "" {
		args = append(args, "-o", "media="+media)
	}
	output, err := exec.CommandContext(ctx, "lp", append(args, path)...).CombinedOutput()
	if err != nil {
		return &utils.IPPSubmissionError{Err: commandFailure("cetak melalui antrean CUPS", output, err)}
	}
	return nil
}

func commandFailure(command string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message != "" {
		return fmt.Errorf("%s gagal: %s: %w", command, message, err)
	}
	return fmt.Errorf("%s gagal: %w", command, err)
}

func findRoute(cfg Config, module, documentType string) (RouteConfig, bool) {
	for _, route := range cfg.Routes {
		if strings.EqualFold(route.ModuleCode, module) && strings.EqualFold(route.DocumentType, documentType) {
			return route, true
		}
	}
	return RouteConfig{}, false
}

func findPrinter(cfg Config, name string) (PrinterConfig, bool) {
	for _, printer := range cfg.Printers {
		if strings.EqualFold(printer.Name, name) {
			return printer, true
		}
	}
	return PrinterConfig{}, false
}

func routeKey(module, documentType string) string {
	return strings.ToLower(module + "/" + documentType)
}

func validCode(value string) bool {
	if len(value) < 1 || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func (s *Server) corsMiddleware(c *fiber.Ctx) error {
	s.mu.RLock()
	allowedOrigins := s.cfg.AllowedOrigin
	s.mu.RUnlock()
	origin := strings.TrimRight(c.Get(fiber.HeaderOrigin), "/")
	allowed := origin == ""
	for _, candidate := range allowedOrigins {
		if origin == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return fiber.NewError(fiber.StatusForbidden, "origin tidak diizinkan")
	}
	if origin != "" {
		c.Set("Access-Control-Allow-Origin", origin)
		c.Set("Vary", "Origin")
	}
	c.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	c.Set("Access-Control-Allow-Headers", "Content-Type, X-RSISA-Printer-Name, X-RSISA-Printer-URI, X-RSISA-Printer-Port, X-RSISA-Doc-Title, X-RSISA-Raw, X-RSISA-Copies, X-RSISA-Media")
	if c.Method() == fiber.MethodOptions {
		if strings.EqualFold(c.Get("Access-Control-Request-Private-Network"), "true") {
			c.Set("Access-Control-Allow-Private-Network", "true")
		}
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Next()
}

func generateRawTestBytes(printerName string) []byte {
	var buf bytes.Buffer
	// Inisialisasi printer ESC/POS
	buf.Write([]byte("\x1b\x40"))       // ESC @: Reset & Inisialisasi
	buf.Write([]byte("\x1b\x74\x00"))   // ESC t 0: Code table standard / CP437
	buf.Write([]byte("\x1b\x61\x01"))   // ESC a 1: Center alignment
	buf.Write([]byte("\x1b\x21\x30"))   // Double height & double width
	buf.WriteString("RSISA PRINT SERVER\n")
	buf.Write([]byte("\x1b\x21\x00"))   // Font normal
	buf.WriteString("Uji Komunikasi USB Plug & Play\n")
	buf.WriteString("================================\n")
	buf.Write([]byte("\x1b\x61\x00"))   // Left alignment
	buf.WriteString("Printer : " + printerName + "\n")
	buf.WriteString("Waktu   : " + time.Now().Format("02/01/2006 15:04:05") + "\n")
	buf.WriteString("Mode    : Direct Raw Spooler (Win32)\n")
	buf.WriteString("--------------------------------\n")
	buf.WriteString("Status  : BERHASIL TERHUBUNG OK!\n")
	buf.WriteString("Angka   : 0123456789\n")
	buf.WriteString("Huruf   : Aa Bb Cc Dd Ee Ff Gg Hh\n")
	buf.WriteString("================================\n")
	buf.Write([]byte("\x1b\x61\x01"))   // Center alignment
	buf.WriteString("CV ANAK BANGSA\n")
	buf.WriteString("*** SELESAI ***\n")
	buf.WriteString("\n\n\n\n")         // Feed line agar tidak terpotong teksnya
	buf.Write([]byte("\x1d\x56\x41\x00")) // GS V 65 0: Paper cut (auto cutter)
	buf.Write([]byte("\x0c"))             // FF: Form Feed (kompatibilitas dot matrix / miniprinter)
	return buf.Bytes()
}

func listUSBPrinters(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	printers, err := listPrintersForPlatform(ctx)
	usbPrinters := make([]LocalPrinter, 0)
	if err == nil {
		for _, p := range printers {
			if p.ConnectionType == "usb" || p.ConnectionType == "serial" {
				usbPrinters = append(usbPrinters, p)
			}
		}
	}
	ports := utils.ListSerialPorts()
	if ports == nil {
		ports = []string{}
	}
	return c.JSON(fiber.Map{
		"data":     usbPrinters,
		"ports":    ports,
		"platform": runtime.GOOS,
	})
}

func listActivePorts(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
	defer cancel()
	ports, _ := utils.ListWindowsPrinterPorts(ctx)
	serialPorts := utils.ListSerialPorts()
	seen := make(map[string]bool)
	var allPorts []string
	for _, p := range ports {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToUpper(p)] {
			seen[strings.ToUpper(p)] = true
			allPorts = append(allPorts, p)
		}
	}
	for _, p := range serialPorts {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToUpper(p)] {
			seen[strings.ToUpper(p)] = true
			allPorts = append(allPorts, p)
		}
	}
	if allPorts == nil {
		allPorts = []string{}
	}
	return c.JSON(fiber.Map{
		"ports":    allPorts,
		"platform": runtime.GOOS,
	})
}

type setPortRequest struct {
	Printer string `json:"printer"`
	Port    string `json:"port"`
}

func (s *Server) setPrinterPortEndpoint(c *fiber.Ctx) error {
	var req setPortRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "parameter ganti port tidak valid")
	}
	req.Printer = strings.TrimSpace(req.Printer)
	req.Port = strings.TrimSpace(req.Port)
	if req.Printer == "" || req.Port == "" {
		return fiber.NewError(fiber.StatusBadRequest, "nama printer dan port tujuan wajib diisi")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	if err := utils.SetWindowsPrinterPort(ctx, req.Printer, req.Port); err != nil {
		s.logf("ERROR", "gagal memindahkan port '%s' ke '%s': %v", req.Printer, req.Port, err)
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	s.logf("INFO", "berhasil memindahkan port printer '%s' ke '%s'", req.Printer, req.Port)
	return c.JSON(fiber.Map{
		"message": fmt.Sprintf("Port printer '%s' berhasil diubah ke '%s'", req.Printer, req.Port),
		"printer": req.Printer,
		"port":    req.Port,
	})
}

type clearQueueRequest struct {
	Printer string `json:"printer"`
}

func (s *Server) clearPrinterQueueEndpoint(c *fiber.Ctx) error {
	var req clearQueueRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "parameter tidak valid")
	}
	req.Printer = strings.TrimSpace(req.Printer)
	if req.Printer == "" {
		return fiber.NewError(fiber.StatusBadRequest, "nama printer wajib diisi")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
	defer cancel()
	if err := utils.ClearWindowsPrintQueue(ctx, req.Printer); err != nil {
		s.logf("ERROR", "gagal membersihkan antrean '%s': %v", req.Printer, err)
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	s.logf("INFO", "antrean printer '%s' berhasil dibersihkan", req.Printer)
	return c.JSON(fiber.Map{
		"message": fmt.Sprintf("Antrean cetak untuk '%s' berhasil dibersihkan", req.Printer),
		"printer": req.Printer,
	})
}

type rawPrintRequest struct {
	Printer  string `json:"printer"`
	Port     string `json:"port"`
	URI      string `json:"uri"`
	Data     string `json:"data"`
	IsBase64 bool   `json:"is_base64"`
	Title    string `json:"title"`
}

func (s *Server) submitRaw(c *fiber.Ctx) error {
	var (
		printerName string
		port        string
		data        []byte
		docTitle    string
	)

	body := c.Body()
	if len(body) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "data cetak tidak boleh kosong")
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var req rawPrintRequest
		if err := json.Unmarshal(trimmed, &req); err == nil {
			printerName = strings.TrimSpace(req.Printer)
			port = strings.TrimSpace(req.Port)
			docTitle = strings.TrimSpace(req.Title)
			if req.URI != "" && printerName == "" {
				if qName, err := utils.WindowsQueueName(req.URI); err == nil {
					printerName = qName
				}
			}
			if req.IsBase64 {
				decoded, err := base64.StdEncoding.DecodeString(req.Data)
				if err != nil {
					return fiber.NewError(fiber.StatusBadRequest, "format base64 tidak valid: "+err.Error())
				}
				data = decoded
			} else {
				data = []byte(req.Data)
			}
		}
	}

	if len(data) == 0 {
		data = body
		if printerName == "" {
			printerName = strings.TrimSpace(c.Get("X-RSISA-Printer-Name"))
		}
		if port == "" {
			port = strings.TrimSpace(c.Get("X-RSISA-Printer-Port"))
		}
		if docTitle == "" {
			docTitle = strings.TrimSpace(c.Get("X-RSISA-Doc-Title"))
		}
		if rawURI := strings.TrimSpace(c.Get("X-RSISA-Printer-URI")); rawURI != "" && printerName == "" {
			if qName, err := utils.WindowsQueueName(rawURI); err == nil {
				printerName = qName
			}
		}
	}

	if docTitle == "" {
		docTitle = "Aquila USB Print"
	}

	if port == "" && printerName == "" {
		// JALAN PINTAS PLUG & PLAY:
		// Jika web klien tidak mengirim nama printer atau port, otomatis cari printer thermal atau default printer yang sedang aktif!
		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()
		printers, _ := listPrintersForPlatform(ctx)
		for _, p := range printers {
			if p.State == "ready" && (p.PrinterType == "thermal" || IsThermalOrPOS(p.Name, p.Driver, p.Port) || p.IsDefault) {
				printerName = p.Name
				break
			}
		}
		if printerName == "" && len(printers) > 0 {
			printerName = printers[0].Name
		}
		if printerName == "" {
			return fiber.NewError(fiber.StatusBadRequest, "tidak ada printer yang terdeteksi pada PC ini. Hubungkan printer USB terlebih dahulu.")
		}
	}

	var err error
	targetDesc := printerName
	// JALAN PINTAS: Abaikan pemaksaan port fisik, selalu arahkan langsung ke Spooler Antrean Windows (PnP Sejati)
	err = utils.PrintRawToWindowsPrinter(printerName, data, docTitle)
	if err != nil && port != "" {
		err = utils.PrintRawToSerialPort(port, data)
	}

	if err != nil {
		s.logf("ERROR", "POST /printers/raw gagal ke %s: %v", targetDesc, err)
		return fiber.NewError(fiber.StatusBadGateway, "gagal mengirim data raw ke printer USB: "+err.Error())
	}

	s.logf("INFO", "POST /printers/raw -> %s 202 (%d bytes)", targetDesc, len(data))
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"message":    "Data RAW/ESC-POS berhasil dikirim ke printer USB.",
		"printer":    printerName,
		"port":       port,
		"bytes_sent": len(data),
	})
}
