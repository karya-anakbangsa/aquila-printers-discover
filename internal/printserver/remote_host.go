package printserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

func privateHost(raw string) bool {
	ip := net.ParseIP(raw)
	return ip != nil && ip.To4() != nil && (ip.IsPrivate() || ip.IsLoopback())
}

func remoteQueue(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ipp" || u.Port() != "17891" || !privateHost(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	encoded, ok := strings.CutPrefix(u.Path, "/printers/")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	queue := string(decoded)
	if !ok || err != nil || queue == "" || strings.ContainsAny(queue, "\r\n\x00") || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", "", false
	}
	return u.Hostname(), queue, true
}

func (s *Server) hostRequest(ctx context.Context, method, host, path string, body io.Reader) (*http.Request, error) {
	if !privateHost(host) {
		return nil, fmt.Errorf("IP PC harus berupa IPv4 private")
	}
	token := s.Config().LANAccessToken
	req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort(host, "17891")+path, body)
	if err == nil && len(token) >= 24 {
		req.Header.Set("X-RSISA-LAN-Token", token)
	}
	return req, err
}

var hostHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 55 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Server) listHostPrinters(c *fiber.Ctx) error {
	host := c.Params("ip")
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	req, err := s.hostRequest(ctx, http.MethodGet, host, "/printers", nil)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	client := hostHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, "PC printer tidak dapat dihubungi: "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fiber.NewError(fiber.StatusBadGateway, "PC printer menolak permintaan (HTTP "+strconv.Itoa(resp.StatusCode)+")")
	}
	var result struct {
		Data     []LocalPrinter `json:"data"`
		Platform string         `json:"platform"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return fiber.NewError(fiber.StatusBadGateway, "daftar printer PC tidak valid")
	}
	for i := range result.Data {
		p := &result.Data[i]
		p.OwnerIP = host
		p.URI = "ipp://" + net.JoinHostPort(host, "17891") + "/printers/" + base64.RawURLEncoding.EncodeToString([]byte(p.Name))
	}
	return c.JSON(fiber.Map{"data": result.Data, "platform": result.Platform})
}

func (s *Server) forwardHostPDF(ctx context.Context, host, queue string, pdf []byte, documentType string, copies int, media string) error {
	req, err := s.hostRequest(ctx, http.MethodPost, host, "/print/"+url.PathEscape(documentType), bytes.NewReader(pdf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("X-RSISA-Queue", queue)
	req.Header.Set("X-RSISA-Copies", strconv.Itoa(copies))
	req.Header.Set("X-RSISA-Media", media)
	client := hostHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("status cetak belum pasti; periksa PC printer sebelum mencoba lagi: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("PC printer menolak pekerjaan (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	return nil
}
