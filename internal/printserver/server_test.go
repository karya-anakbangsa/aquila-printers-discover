package printserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRawTCPPDFIsRejectedBeforeSending(t *testing.T) {
	server := New(Config{}, func(level, message string) {})
	app := server.App()

	printRequest := httptest.NewRequest(http.MethodPost, "/print/billing_produksi?module_code=barang_produksi", strings.NewReader("%PDF-1.4\n%%EOF"))
	printRequest.Header.Set("X-RSISA-Printer-URI", "tcp://127.0.0.1:9100")
	testRequest := httptest.NewRequest(http.MethodPost, "/printers/test", strings.NewReader(`{"name":"EPSON","uri":"tcp://127.0.0.1:9100","paper":"A4","mode":"bw"}`))
	testRequest.Header.Set("Content-Type", "application/json")

	for _, request := range []*http.Request{printRequest, testRequest} {
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != fiber.StatusUnprocessableEntity {
			t.Fatalf("%s: got status %d, want 422", request.URL.Path, response.StatusCode)
		}
	}
}

func TestGetPrintersResponseStructure(t *testing.T) {
	server := New(Config{Listen: "0.0.0.0:17891"}, func(level, message string) {})
	app := server.App()

	req := httptest.NewRequest(http.MethodGet, "/printers", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /printers status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	for _, key := range []string{"machine_name", "owner_ip", "ip_addresses", "platform", "data"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("missing required top-level key: %s in %+v", key, result)
		}
	}
	pretty, _ := json.MarshalIndent(result, "", "  ")
	t.Logf("Response JSON:\n%s", string(pretty))
}
