package printserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/RSISA-Semarang/rsisa-print-server/internal/utils"
)

type CloudPrintJob struct {
	ID             string `json:"id"`
	PrinterID      string `json:"printer_id"`
	QueueName      string `json:"queue_name"`
	JobName        string `json:"job_name"`
	DocumentFormat string `json:"document_format"`
	ContentType    string `json:"content_type"`
	Document       string `json:"document"` // base64
	Copies         int    `json:"copies"`
	Media          string `json:"media"`
}

type CloudJobResponse struct {
	Data CloudPrintJob `json:"data"`
}

type AgentRegisterResponse struct {
	AgentID  string `json:"agent_id"`
	APIToken string `json:"api_token"`
	Message  string `json:"message"`
}

// StartCloudAgent menjalankan worker background untuk polling tugas cetak dari website pusat.
func (s *Server) StartCloudAgent(ctx context.Context) {
	cfg := s.Config()
	if !cfg.CloudAgentEnabled || strings.TrimSpace(cfg.CloudServerURL) == "" {
		s.logf("INFO", "[Cloud Agent] Dinonaktifkan (cloud_agent_enabled=false)")
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.CloudServerURL), "/")
	intervalSec := cfg.CloudPollInterval
	if intervalSec <= 0 {
		intervalSec = 2
	}
	pollTicker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer pollTicker.Stop()

	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Pastikan Agent terdaftar di server
	agentID, token, err := s.ensureAgentRegistered(ctx, client, baseURL)
	if err != nil {
		s.logf("WARN", "[Cloud Agent] Registrasi agent ke website gagal: %v (akan dicoba lagi berkala)", err)
	} else {
		s.logf("INFO", "[Cloud Agent] Terhubung ke website SIMRS! Agent ID: %s", agentID)
		// 2. Daftarkan seluruh printer lokal ke server web
		s.syncLocalPrintersToCloud(ctx, client, baseURL, agentID, token)
	}

	s.logf("INFO", "[Cloud Agent] Background worker aktif mendengarkan antrean (Interval: %ds)", intervalSec)

	for {
		select {
		case <-ctx.Done():
			s.logf("INFO", "[Cloud Agent] Berhenti.")
			return
		case <-pollTicker.C:
			currentCfg := s.Config()
			if !currentCfg.CloudAgentEnabled {
				continue
			}
			currBaseURL := strings.TrimRight(strings.TrimSpace(currentCfg.CloudServerURL), "/")
			if currBaseURL == "" {
				continue
			}

			if currentCfg.CloudAgentID == "" || currentCfg.CloudAgentToken == "" {
				agID, agTok, err := s.ensureAgentRegistered(ctx, client, currBaseURL)
				if err != nil {
					continue
				}
				agentID = agID
				token = agTok
				s.syncLocalPrintersToCloud(ctx, client, currBaseURL, agentID, token)
			} else {
				agentID = currentCfg.CloudAgentID
				token = currentCfg.CloudAgentToken
			}

			s.pollAndExecuteJobs(ctx, client, currBaseURL, agentID, token)
		}
	}
}

// ensureAgentRegistered memastikan agent memiliki ID dan token dari server
func (s *Server) ensureAgentRegistered(ctx context.Context, client *http.Client, baseURL string) (string, string, error) {
	s.mu.RLock()
	existingID := s.cfg.CloudAgentID
	existingToken := s.cfg.CloudAgentToken
	s.mu.RUnlock()

	if existingID != "" && existingToken != "" {
		return existingID, existingToken, nil
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "PC-A"
	}

	bodyBytes, _ := json.Marshal(map[string]string{
		"name":         fmt.Sprintf("PC A (%s)", hostname),
		"machine_name": hostname,
	})

	urls := []string{
		fmt.Sprintf("%s/sys/printing/agent/register", baseURL),
		fmt.Sprintf("%s/api/sys/printing/agent/register", baseURL),
		fmt.Sprintf("%s/agent/printing/register", baseURL),
	}

	var lastErr error
	for _, regURL := range urls {
		req, err := http.NewRequestWithContext(ctx, "POST", regURL, bytes.NewReader(bodyBytes))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			var regRes AgentRegisterResponse
			if err := json.Unmarshal(respBytes, &regRes); err == nil && regRes.AgentID != "" && regRes.APIToken != "" {
				s.mu.Lock()
				s.cfg.CloudAgentID = regRes.AgentID
				s.cfg.CloudAgentToken = regRes.APIToken
				s.mu.Unlock()
				return regRes.AgentID, regRes.APIToken, nil
			}
		}
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(respBytes))
	}
	return "", "", lastErr
}

// syncLocalPrintersToCloud mendaftarkan semua printer USB / lokal ke website pusat
func (s *Server) syncLocalPrintersToCloud(ctx context.Context, client *http.Client, baseURL, agentID, token string) {
	if agentID == "" || token == "" {
		return
	}

	printers, err := listPrintersForPlatform(ctx)
	if err != nil {
		s.logf("WARN", "[Cloud Agent] Gagal mendeteksi printer lokal untuk sinkronisasi: %v", err)
		return
	}

	hostname, _ := os.Hostname()
	printerItems := make([]map[string]string, 0, len(printers))
	for _, p := range printers {
		printerItems = append(printerItems, map[string]string{
			"name":       p.Name,
			"queue_name": p.Name,
		})
	}

	bodyBytes, _ := json.Marshal(map[string]any{
		"machine_name": hostname,
		"printers":     printerItems,
	})

	urls := []string{
		fmt.Sprintf("%s/sys/printing/agent/printers", baseURL),
		fmt.Sprintf("%s/api/sys/printing/agent/printers", baseURL),
		fmt.Sprintf("%s/agent/printing/printers", baseURL),
	}

	for _, syncURL := range urls {
		req, err := http.NewRequestWithContext(ctx, "POST", syncURL, bytes.NewReader(bodyBytes))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Printer-Agent-ID", agentID)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			s.logf("INFO", "[Cloud Agent] %d printer USB/lokal berhasil didaftarkan ke website SIMRS!", len(printerItems))
			return
		}
	}
}

// pollAndExecuteJobs menarik pending jobs dari website dan mengeksekusinya
func (s *Server) pollAndExecuteJobs(ctx context.Context, client *http.Client, baseURL, agentID, token string) {
	if agentID == "" || token == "" {
		return
	}

	urls := []string{
		fmt.Sprintf("%s/sys/printing/agent/jobs", baseURL),
		fmt.Sprintf("%s/api/sys/printing/agent/jobs", baseURL),
		fmt.Sprintf("%s/agent/printing/jobs", baseURL),
	}

	for _, jobsURL := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", jobsURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-Printer-Agent-ID", agentID)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusNoContent {
			_ = resp.Body.Close()
			return
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return
		}

		var res CloudJobResponse
		if err := json.Unmarshal(body, &res); err != nil || res.Data.ID == "" {
			return
		}

		job := res.Data
		s.logf("INFO", "[Cloud Agent] Menerima tugas cetak ID: %s | Printer: %s | Dokumen: %s", job.ID, job.QueueName, job.JobName)
		s.executeSingleCloudJob(ctx, client, baseURL, agentID, token, job)
		return
	}
}

// executeSingleCloudJob mencetak data ke Windows Spooler dan melaporkan hasil kembali ke web
func (s *Server) executeSingleCloudJob(ctx context.Context, client *http.Client, baseURL, agentID, token string, job CloudPrintJob) {
	data, err := base64.StdEncoding.DecodeString(job.Document)
	if err != nil {
		s.reportJobStatus(ctx, client, baseURL, agentID, token, job.ID, "FAILED", "Gagal decode dokumen base64: "+err.Error())
		return
	}

	targetPrinter := strings.TrimSpace(job.QueueName)
	if targetPrinter == "" {
		printers, _ := listPrintersForPlatform(ctx)
		for _, p := range printers {
			if p.State == "ready" && (p.PrinterType == "thermal" || IsThermalOrPOS(p.Name, p.Driver, p.Port) || p.IsDefault) {
				targetPrinter = p.Name
				break
			}
		}
		if targetPrinter == "" && len(printers) > 0 {
			targetPrinter = printers[0].Name
		}
	}

	jobName := job.JobName
	if jobName == "" {
		jobName = "Cloud Relay Print - " + job.ID
	}

	var printErr error
	isPDF := strings.EqualFold(job.DocumentFormat, "pdf") || (len(data) >= 5 && string(data[:5]) == "%PDF-")

	if isPDF {
		copies := job.Copies
		if copies <= 0 {
			copies = 1
		}
		printErr = printPDFToWindowsQueue(ctx, targetPrinter, data, jobName, copies, job.Media)
	} else {
		printErr = utils.PrintRawToWindowsPrinter(targetPrinter, data, jobName)
	}

	if printErr != nil {
		s.logf("ERROR", "[Cloud Agent] Gagal mencetak job %s ke %s: %v", job.ID, targetPrinter, printErr)
		s.reportJobStatus(ctx, client, baseURL, agentID, token, job.ID, "FAILED", printErr.Error())
	} else {
		s.logf("INFO", "[Cloud Agent] SUKSES mencetak job %s ke printer fisik USB (%s)!", job.ID, targetPrinter)
		s.reportJobStatus(ctx, client, baseURL, agentID, token, job.ID, "COMPLETED", "")
	}
}

// reportJobStatus melaporkan status penyelesaian ke server
func (s *Server) reportJobStatus(ctx context.Context, client *http.Client, baseURL, agentID, token, jobID, status, errMsg string) {
	payload := map[string]string{
		"status":        status,
		"error_message": errMsg,
	}
	bodyBytes, _ := json.Marshal(payload)

	urls := []string{
		fmt.Sprintf("%s/sys/printing/agent/jobs/%s/complete", baseURL, jobID),
		fmt.Sprintf("%s/api/sys/printing/agent/jobs/%s/complete", baseURL, jobID),
		fmt.Sprintf("%s/agent/printing/jobs/%s/complete", baseURL, jobID),
	}

	for _, completeURL := range urls {
		req, err := http.NewRequestWithContext(ctx, "PATCH", completeURL, bytes.NewReader(bodyBytes))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Printer-Agent-ID", agentID)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
	}
}
