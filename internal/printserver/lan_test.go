package printserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLANConfiguration(t *testing.T) {
	for _, tc := range []struct {
		listen string
		token  string
		valid  bool
	}{
		{"127.0.0.1:17891", "", true},
		{"192.168.1.2:17891", "short-token", false},
		{"192.168.1.2:17891", "test-token-with-24-characters", true},
		{"0.0.0.0:17891", "short-token", false},
		{"0.0.0.0:17891", "test-token-with-24-characters", true},
		{"192.168.1.2:99999", "test-token-with-24-characters", false},
	} {
		cfg := DefaultConfig()
		cfg.Listen, cfg.LANAccessToken = tc.listen, tc.token
		if err := cfg.Validate(); (err == nil) != tc.valid {
			t.Errorf("listen %q valid = %t, error = %v", tc.listen, tc.valid, err)
		}
	}
}

func TestRemoteListAndPDFThroughLocalServer(t *testing.T) {
	token := "test-token-with-24-characters"
	old := hostHTTPClient
	t.Cleanup(func() { hostHTTPClient = old })
	var received bool
	hostHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("X-RSISA-LAN-Token") != token {
				t.Error("token tidak diteruskan ke PC pemilik")
			}
			if r.Method == http.MethodGet {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"name":"Printer PC2","state":"ready","owner_pc":"PC2"}],"platform":"windows"}`)), Header: make(http.Header)}, nil
			}
			body, _ := io.ReadAll(r.Body)
			received = r.Header.Get("X-RSISA-Queue") == "Printer PC2" && string(body) == "%PDF-1.4\n%%EOF"
			return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
		})}
	}
	s := New(Config{LANAccessToken: token}, nil)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.App().Listener(listener) }()
	t.Cleanup(func() { _ = s.App().Shutdown() })
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + listener.Addr().String()
	response, err := client.Get(base + "/hosts/192.168.1.20/printers")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data []LocalPrinter `json:"data"`
	}
	err = json.NewDecoder(response.Body).Decode(&result)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(result.Data) != 1 || result.Data[0].OwnerPC != "PC2" || result.Data[0].OwnerIP != "192.168.1.20" {
		t.Fatalf("daftar PC2 tidak sesuai: %+v, %v", result, err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/print/etiket", bytes.NewBufferString("%PDF-1.4\n%%EOF"))
	req.Header.Set("X-RSISA-Printer-URI", result.Data[0].URI)
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !received {
		t.Fatalf("PDF tidak diteruskan ke antrean PC2: status %d", response.StatusCode)
	}
}

func TestLANAuthorization(t *testing.T) {
	token := "test-token-with-24-characters"
	if !lanAccessAllowed(net.ParseIP("192.168.1.4"), token, token) {
		t.Fatal("klien LAN dengan token valid ditolak")
	}
	if lanAccessAllowed(net.ParseIP("192.168.1.4"), token, "wrong") || lanAccessAllowed(net.ParseIP("8.8.8.8"), token, token) {
		t.Fatal("klien tanpa token valid atau dari IP publik diterima")
	}
}

func TestRemoteQueueForwarding(t *testing.T) {
	queue := `\\PC2\Printer Farmasi`
	uri := "ipp://192.168.1.20:17891/printers/" + base64.RawURLEncoding.EncodeToString([]byte(queue))
	host, gotQueue, ok := remoteQueue(uri)
	if !ok || host != "192.168.1.20" || gotQueue != queue {
		t.Fatalf("remote queue = %q, %q, %v", host, gotQueue, ok)
	}
	if _, _, ok := remoteQueue("ipp://8.8.8.8:17891/printers/cHJpbnRlcg"); ok {
		t.Fatal("alamat publik tidak boleh digunakan sebagai PC printer")
	}
	token := "test-token-with-24-characters"
	old := hostHTTPClient
	t.Cleanup(func() { hostHTTPClient = old })
	hostHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Host != "192.168.1.20:17891" || r.Header.Get("X-RSISA-Queue") != queue || r.Header.Get("X-RSISA-LAN-Token") != token || string(body) != "%PDF-test" {
				t.Errorf("PDF atau tujuan PC printer tidak sesuai")
			}
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
		})}
	}
	s := New(Config{LANAccessToken: token}, nil)
	if err := s.forwardHostPDF(context.Background(), host, queue, []byte("%PDF-test"), "etiket", 1, ""); err != nil {
		t.Fatal(err)
	}
}
