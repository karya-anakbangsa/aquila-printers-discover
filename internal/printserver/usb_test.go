package printserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetermineConnectionType(t *testing.T) {
	tests := []struct {
		port     string
		driver   string
		ip       string
		expected string
	}{
		{"USB001", "POS-58", "", "usb"},
		{"USB002", "Generic / Text Only", "", "usb"},
		{"DOT4_001", "HP LaserJet", "", "usb"},
		{"COM1", "Serial Printer", "", "serial"},
		{"COM3", "Thermal Receipt", "", "serial"},
		{"IP_192.168.1.100", "Epson L3150", "192.168.1.100", "network"},
		{"9100", "Zebra ZPL", "10.0.0.5", "network"},
		{"WSD-xyz", "Canon TS5300", "", "network"},
		{"FILE:", "Microsoft Print to PDF", "", "virtual"},
		{"PORTPROMPT:", "Microsoft XPS Document Writer", "", "virtual"},
		{`\\.\pipe\PDFPrint`, "PDF24", "", "virtual"},
		{"LPT1", "Old DotMatrix", "", "serial"},
	}

	for _, tt := range tests {
		got := DetermineConnectionType(tt.port, tt.driver, tt.ip)
		if got != tt.expected {
			t.Errorf("DetermineConnectionType(%q, %q, %q) = %q; want %q", tt.port, tt.driver, tt.ip, got, tt.expected)
		}
	}
}

func TestParseWindowsPrintersSetsUSBConnectionType(t *testing.T) {
	data := []byte(`[
		{"name":"Thermal POS 58","state":"ready","driver":"POS-58","port":"USB001","ip":null},
		{"name":"Office Network Printer","state":"ready","driver":"HP LaserJet","port":"IP_192.168.1.50","ip":"192.168.1.50"},
		{"name":"Serial Barcode","state":"ready","driver":"Zebra","port":"COM2","ip":null}
	]`)

	printers, err := parseWindowsPrinters(data)
	if err != nil {
		t.Fatalf("parseWindowsPrinters gagal: %v", err)
	}
	if len(printers) != 3 {
		t.Fatalf("panjang printers = %d, want 3", len(printers))
	}

	if printers[0].ConnectionType != "usb" {
		t.Errorf("printers[0].ConnectionType = %q, want 'usb'", printers[0].ConnectionType)
	}
	if printers[1].ConnectionType != "network" {
		t.Errorf("printers[1].ConnectionType = %q, want 'network'", printers[1].ConnectionType)
	}
	if printers[2].ConnectionType != "serial" {
		t.Errorf("printers[2].ConnectionType = %q, want 'serial'", printers[2].ConnectionType)
	}
}

func TestUSBEndpoints(t *testing.T) {
	cfg := Config{
		Listen:        "127.0.0.1:17891",
		AllowedOrigin: []string{"http://localhost:4200"},
	}
	server := New(cfg, nil)

	// Test GET /printers/usb
	reqUSB := httptest.NewRequest("GET", "/printers/usb", nil)
	respUSB, err := server.App().Test(reqUSB, -1)
	if err != nil {
		t.Fatalf("GET /printers/usb failed: %v", err)
	}
	if respUSB.StatusCode != 200 {
		t.Errorf("GET /printers/usb status = %d, want 200", respUSB.StatusCode)
	}

	// Test GET /printers/ports
	reqPorts := httptest.NewRequest("GET", "/printers/ports", nil)
	respPorts, err := server.App().Test(reqPorts, -1)
	if err != nil {
		t.Fatalf("GET /printers/ports failed: %v", err)
	}
	if respPorts.StatusCode != 200 {
		t.Errorf("GET /printers/ports status = %d, want 200", respPorts.StatusCode)
	}

	// Test POST /printers/raw empty body
	reqRawEmpty := httptest.NewRequest("POST", "/printers/raw", bytes.NewReader(nil))
	respRawEmpty, err := server.App().Test(reqRawEmpty, -1)
	if err != nil {
		t.Fatalf("POST /printers/raw empty test failed: %v", err)
	}
	if respRawEmpty.StatusCode != 400 {
		t.Errorf("POST /printers/raw empty status = %d, want 400", respRawEmpty.StatusCode)
	}

	// Test POST /printers/raw with an unavailable target so the test cannot print to a real queue.
	rawJSON := `{"printer":"__rsisa_test_missing_printer__","data":"test data"}`
	reqRawNoTarget := httptest.NewRequest("POST", "/printers/raw", strings.NewReader(rawJSON))
	reqRawNoTarget.Header.Set("Content-Type", "application/json")
	respRawNoTarget, err := server.App().Test(reqRawNoTarget, -1)
	if err != nil {
		t.Fatalf("POST /printers/raw no target failed: %v", err)
	}
	if respRawNoTarget.StatusCode != 502 {
		t.Errorf("POST /printers/raw with unavailable printer status = %d, want 502", respRawNoTarget.StatusCode)
	}

	// Test POST /printers/raw invalid base64
	invalidB64 := `{"printer":"POS58","is_base64":true,"data":"not_valid_base64!!!"}`
	reqInvalidB64 := httptest.NewRequest("POST", "/printers/raw", strings.NewReader(invalidB64))
	reqInvalidB64.Header.Set("Content-Type", "application/json")
	respInvalidB64, err := server.App().Test(reqInvalidB64, -1)
	if err != nil {
		t.Fatalf("POST /printers/raw invalid b64 failed: %v", err)
	}
	if respInvalidB64.StatusCode != 400 {
		t.Errorf("POST /printers/raw invalid b64 status = %d, want 400", respInvalidB64.StatusCode)
	}

	// Test generateRawTestBytes output
	testBytes := generateRawTestBytes("Epson USB Thermal")
	if len(testBytes) == 0 {
		t.Fatal("generateRawTestBytes returned empty slice")
	}
	if !bytes.Contains(testBytes, []byte("RSISA PRINT SERVER")) {
		t.Error("generateRawTestBytes missing title")
	}
	if !bytes.Contains(testBytes, []byte("Epson USB Thermal")) {
		t.Error("generateRawTestBytes missing printer name")
	}
}

func TestCORSIncludesRawHeaders(t *testing.T) {
	cfg := Config{
		Listen:        "127.0.0.1:17891",
		AllowedOrigin: []string{"http://localhost:4200"},
	}
	server := New(cfg, nil)

	req := httptest.NewRequest("OPTIONS", "/printers/raw", nil)
	req.Header.Set("Origin", "http://localhost:4200")
	resp, err := server.App().Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("OPTIONS status = %d, want 204", resp.StatusCode)
	}

	allowedHeaders := resp.Header.Get("Access-Control-Allow-Headers")
	for _, h := range []string{"X-RSISA-Printer-Port", "X-RSISA-Doc-Title", "X-RSISA-Raw"} {
		if !strings.Contains(allowedHeaders, h) {
			t.Errorf("Access-Control-Allow-Headers %q missing %q", allowedHeaders, h)
		}
	}
}

func TestRawPrintBase64PayloadStructure(t *testing.T) {
	orig := "ESC/POS Data 12345"
	encoded := base64.StdEncoding.EncodeToString([]byte(orig))

	payload := rawPrintRequest{
		Printer:  "POS58",
		Port:     "COM3",
		Data:     encoded,
		IsBase64: true,
		Title:    "Test Ticket",
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var parsed rawPrintRequest
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatal(err)
	}

	decoded, err := base64.StdEncoding.DecodeString(parsed.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != orig {
		t.Errorf("decoded = %q, want %q", string(decoded), orig)
	}
}
