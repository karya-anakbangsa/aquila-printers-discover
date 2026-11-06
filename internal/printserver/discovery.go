package printserver

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mdnsPort        = 5353
	mdnsPTR  uint16 = 12
	mdnsTXT  uint16 = 16
	mdnsSRV  uint16 = 33
	mdnsA    uint16 = 1
	mdnsAAAA uint16 = 28
)

var mdnsGroup = &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: mdnsPort}

type DiscoveredPrinter struct {
	Name  string   `json:"name"`
	Model string   `json:"model,omitempty"`
	Host  string   `json:"host"`
	IP    string   `json:"ip,omitempty"`
	Port  int      `json:"port"`
	URI   string   `json:"uri"`
	PDL   []string `json:"pdl,omitempty"`
	State string   `json:"state"`
}
type discoveredPrinter = DiscoveredPrinter

// DiscoverIPPPrinters mencari printer jaringan baik melalui Bonjour/mDNS maupun
// pemindaian aktif subnet LAN (ARP & port IPP/RAW) secara hibrida.
func DiscoverIPPPrinters(ctx context.Context) ([]DiscoveredPrinter, error) {
	return discoverIPPPrinters(ctx)
}

type mdnsRecord struct {
	Name   string
	Type   uint16
	Data   []byte
	Value  string
	Target string
	Port   int
}

type mdnsService struct {
	Name      string
	Secure    bool
	Target    string
	Port      int
	Text      map[string]string
	Addresses []net.IP
}

func discoverIPPPrinters(ctx context.Context) ([]discoveredPrinter, error) {
	merged := make(map[string]discoveredPrinter)
	var mu sync.Mutex

	var wg sync.WaitGroup

	// Engine 1: mDNS / Bonjour Paralel Multi-Interface
	wg.Add(1)
	go func() {
		defer wg.Done()
		mdnsResults, err := discoverMDNSPrinters(ctx)
		if err == nil {
			mu.Lock()
			for k, p := range mdnsResults {
				merged[k] = p
			}
			mu.Unlock()
		}
	}()

	// Engine 2: Active LAN Subnet & ARP Probe
	wg.Add(1)
	go func() {
		defer wg.Done()
		subnetResults := discoverSubnetPrinters(ctx)
		mu.Lock()
		for k, p := range subnetResults {
			existing, found := merged[k]
			if !found {
				merged[k] = p
			} else {
				// Bila sudah ditemukan lewat mDNS, perkaya informasi yang belum ada
				if existing.Model == "" && p.Model != "" {
					existing.Model = p.Model
				}
				if existing.IP == "" && p.IP != "" {
					existing.IP = p.IP
				}
				if len(existing.PDL) == 0 && len(p.PDL) > 0 {
					existing.PDL = p.PDL
				}
				merged[k] = existing
			}
		}
		mu.Unlock()
	}()

	wg.Wait()

	printers := make([]discoveredPrinter, 0, len(merged))
	for _, p := range merged {
		printers = append(printers, p)
	}

	sort.Slice(printers, func(i, j int) bool {
		return strings.ToLower(printers[i].Name) < strings.ToLower(printers[j].Name)
	})

	return printers, nil
}

// discoverMDNSPrinters menjalankan pencarian mDNS pada seluruh adapter fisik secara paralel.
func discoverMDNSPrinters(ctx context.Context) (map[string]discoveredPrinter, error) {
	var conns []*net.UDPConn

	// Listen pada default socket
	defaultConn, err := net.ListenMulticastUDP("udp4", nil, mdnsGroup)
	if err == nil {
		conns = append(conns, defaultConn)
	}

	// Listen juga pada seluruh interface fisik aktif (Ethernet, Wi-Fi)
	ifaces, ifaceErr := net.Interfaces()
	if ifaceErr == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
				continue
			}
			nameLower := strings.ToLower(iface.Name)
			if strings.Contains(nameLower, "vethernet") || strings.Contains(nameLower, "docker") || strings.Contains(nameLower, "wsl") || strings.Contains(nameLower, "virtual") {
				continue
			}
			c, err := net.ListenMulticastUDP("udp4", &iface, mdnsGroup)
			if err == nil {
				conns = append(conns, c)
			}
		}
	}

	if len(conns) == 0 {
		return nil, errors.New("tidak dapat membuka socket multicast mDNS")
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	deadline := time.Now().Add(2500 * time.Millisecond)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	queryPacket := mdnsQuery(
		"_ipp._tcp.local", mdnsPTR,
		"_ipps._tcp.local", mdnsPTR,
		"_printer._tcp.local", mdnsPTR,
		"_pdl-datastream._tcp.local", mdnsPTR,
	)

	// Kirim paket query multicast keluar dari setiap socket
	for _, c := range conns {
		_ = c.SetDeadline(deadline)
		_, _ = c.WriteToUDP(queryPacket, mdnsGroup)
	}

	var mu sync.Mutex
	services := make(map[string]*mdnsService)
	addresses := make(map[string][]net.IP)

	var readWG sync.WaitGroup
	for _, c := range conns {
		readWG.Add(1)
		go func(conn *net.UDPConn) {
			defer readWG.Done()
			buf := make([]byte, 9000)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				count, _, readErr := conn.ReadFromUDP(buf)
				if readErr != nil {
					return
				}
				records, parseErr := parseMDNSRecords(buf[:count])
				if parseErr != nil {
					continue
				}

				mu.Lock()
				for _, record := range records {
					owner := normalizeDNSName(record.Name)
					switch record.Type {
					case mdnsPTR:
						serviceType := ""
						if strings.HasPrefix(owner, "_ipp._tcp.") {
							serviceType = "_ipp._tcp.local."
						} else if strings.HasPrefix(owner, "_ipps._tcp.") {
							serviceType = "_ipps._tcp.local."
						} else if strings.HasPrefix(owner, "_printer._tcp.") {
							serviceType = "_printer._tcp.local."
						} else if strings.HasPrefix(owner, "_pdl-datastream._tcp.") {
							serviceType = "_pdl-datastream._tcp.local."
						}
						if serviceType == "" || record.Value == "" {
							continue
						}
						instance := normalizeDNSName(record.Value)
						service := getMDNSService(services, instance)
						service.Secure = strings.HasPrefix(serviceType, "_ipps.")
						service.Name = printerInstanceName(record.Value, serviceType)
					case mdnsSRV:
						if len(record.Data) < 7 {
							continue
						}
						service := getMDNSService(services, owner)
						service.Port = record.Port
						service.Target = normalizeDNSName(record.Target)
					case mdnsTXT:
						service := getMDNSService(services, owner)
						service.Text = parseMDNSText(record.Data)
					case mdnsA:
						if len(record.Data) == net.IPv4len {
							addresses[owner] = append(addresses[owner], net.IP(append([]byte(nil), record.Data...)))
						}
					case mdnsAAAA:
						if len(record.Data) == net.IPv6len {
							addresses[owner] = append(addresses[owner], net.IP(append([]byte(nil), record.Data...)))
						}
					}
				}
				mu.Unlock()
			}
		}(c)
	}

	readWG.Wait()

	printersByEndpoint := make(map[string]discoveredPrinter, len(services))
	for _, service := range services {
		if service.Name == "" || service.Port == 0 {
			continue
		}
		service.Addresses = addresses[service.Target]
		host := strings.TrimSuffix(service.Target, ".")
		scheme := "ipp"
		if service.Secure {
			scheme = "ipps"
		} else if service.Port == 9100 {
			scheme = "socket"
		}

		resource := service.Text["rp"]
		if scheme == "socket" {
			resource = ""
		} else {
			if resource == "" {
				resource = "ipp/print"
			}
			resource = "/" + strings.TrimLeft(resource, "/")
		}

		var uri string
		if scheme == "socket" {
			uri = fmt.Sprintf("socket://%s:%d", host, service.Port)
		} else {
			uri = fmt.Sprintf("%s://%s:%d%s", scheme, host, service.Port, resource)
		}

		printer := discoveredPrinter{
			Name:  service.Name,
			Model: service.Text["ty"],
			Host:  host,
			Port:  service.Port,
			URI:   uri,
			State: "ready",
			PDL:   splitTXTValues(service.Text["pdl"]),
		}
		if len(service.Addresses) > 0 {
			printer.IP = service.Addresses[0].String()
		}

		key := strings.ToLower(host)
		if printer.IP != "" {
			key = printer.IP
		}
		printersByEndpoint[key] = printer
	}

	return printersByEndpoint, nil
}

// discoverSubnetPrinters memindai host-host aktif di subnet lokal (via ARP cache & probe port IPP/RAW)
// agar printer dapat terdeteksi meski switch/router memblokir paket multicast mDNS antara LAN dan Wi-Fi.
func discoverSubnetPrinters(ctx context.Context) map[string]discoveredPrinter {
	candidates := getCandidateIPs(ctx)
	if len(candidates) == 0 {
		return nil
	}

	results := make(map[string]discoveredPrinter)
	var mu sync.Mutex

	// Batasi konkuren probe maksimal 25 worker paralel
	semaphore := make(chan struct{}, 25)
	var wg sync.WaitGroup

	for _, ip := range candidates {
		select {
		case <-ctx.Done():
			break
		default:
		}

		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			printer := probeSubnetPrinter(ctx, targetIP)
			if printer != nil {
				mu.Lock()
				results[printer.IP] = *printer
				mu.Unlock()
			}
		}(ip)
	}

	wg.Wait()
	return results
}

// getCandidateIPs mengumpulkan IP-IP perangkat aktif di jaringan lokal dari tabel ARP sistem.
func getCandidateIPs(ctx context.Context) []string {
	seen := make(map[string]bool)
	var candidates []string

	// Dapatkan IP lokal komputer ini agar tidak memindai diri sendiri
	localIPs := make(map[string]bool)
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if ip4 := ipNet.IP.To4(); ip4 != nil {
					localIPs[ip4.String()] = true
				}
			}
		}
	}

	// Eksekusi arp -a untuk mengambil cache ARP sistem operasi
	cmd := exec.CommandContext(ctx, "arp", "-a")
	output, err := cmd.Output()
	if err == nil {
		ipRe := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
		matches := ipRe.FindAllString(string(output), -1)
		for _, ipStr := range matches {
			ip := net.ParseIP(ipStr)
			if ip == nil || ip.To4() == nil {
				continue
			}
			ip4 := ip.To4()
			// Hanya periksa private IP range (192.168.x.x, 10.x.x.x, 172.16-31.x.x)
			if !isPrivateIPv4(ip4) {
				continue
			}
			// Hindari alamat broadcast (.255 atau .0) dan IP lokal komputer
			if ip4[3] == 0 || ip4[3] == 255 || localIPs[ipStr] {
				continue
			}
			if !seen[ipStr] {
				seen[ipStr] = true
				candidates = append(candidates, ipStr)
			}
		}
	}

	return candidates
}

func isPrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip[0] == 10 ||
		(ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) ||
		(ip[0] == 192 && ip[1] == 168)
}

// probeSubnetPrinter memeriksa apakah suatu IP mengaktifkan layanan printer (port 631 IPP atau port 9100 RAW).
func probeSubnetPrinter(ctx context.Context, ip string) *discoveredPrinter {
	dialer := net.Dialer{Timeout: 400 * time.Millisecond}

	// Tes port 631 (IPP)
	conn631, err631 := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, "631"))
	has631 := (err631 == nil)
	if has631 {
		_ = conn631.Close()
	}

	// Tes port 9100 (RAW / JetDirect)
	conn9100, err9100 := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, "9100"))
	has9100 := (err9100 == nil)
	if has9100 {
		_ = conn9100.Close()
	}

	// Jika bukan port 631 maupun 9100, bukan printer
	if !has631 && !has9100 {
		return nil
	}

	// Cari nama ramah printer (misal dari judul halaman web management printer atau IPP)
	modelName := fetchPrinterWebModel(ctx, ip)
	if modelName == "" {
		if has631 {
			modelName = fmt.Sprintf("Printer IPP (%s)", ip)
		} else {
			modelName = fmt.Sprintf("Printer RAW (%s)", ip)
		}
	}

	if has631 {
		// Periksa apakah printer IPP membutuhkan HTTPS (IPPS) seperti Epson L565
		scheme := "ipp"
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client := &http.Client{Transport: tr, Timeout: 700 * time.Millisecond}
		testURL := fmt.Sprintf("https://%s:631/ipp/print", ip)
		req, _ := http.NewRequestWithContext(ctx, "POST", testURL, strings.NewReader("\x01\x01\x00\x0b\x00\x00\x00\x01\x03"))
		req.Header.Set("Content-Type", "application/ipp")
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 || resp.StatusCode == 400 || resp.StatusCode == 401 {
				scheme = "ipps"
			}
		}

		return &discoveredPrinter{
			Name:  modelName,
			Model: modelName,
			Host:  ip,
			IP:    ip,
			Port:  631,
			URI:   fmt.Sprintf("%s://%s:631/ipp/print", scheme, ip),
			PDL:   []string{"application/pdf", "application/postscript", "application/octet-stream"},
			State: "ready",
		}
	}

	return &discoveredPrinter{
		Name:  modelName,
		Model: modelName,
		Host:  ip,
		IP:    ip,
		Port:  9100,
		URI:   fmt.Sprintf("socket://%s:9100", ip),
		PDL:   []string{"application/octet-stream", "raw"},
		State: "ready",
	}
}

// fetchPrinterWebModel mengambil judul / model printer dari HTTP interface printer (port 80 / 443).
func fetchPrinterWebModel(ctx context.Context, ip string) string {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   900 * time.Millisecond,
	}

	// Prioritaskan endpoint yang biasa digunakan printer Epson, HP, Canon, Brother
	endpoints := []string{
		fmt.Sprintf("http://%s/PRESENTATION/HTML/TOP/INDEX.HTML", ip), // Epson
		fmt.Sprintf("http://%s/", ip),                                 // HP / Generic
		fmt.Sprintf("https://%s/", ip),
	}

	titleRe := regexp.MustCompile(`(?i)<title>(.*?)</title>`)

	for _, u := range endpoints {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AquilaDiscover/1.0")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		_ = resp.Body.Close()

		if m := titleRe.FindSubmatch(bodyBytes); len(m) > 1 {
			title := strings.TrimSpace(html.UnescapeString(string(m[1])))
			title = strings.ReplaceAll(title, "\u00a0", " ") // &nbsp;
			title = strings.TrimSpace(title)

			// Abaikan judul generic atau error
			titleLower := strings.ToLower(title)
			if title != "" && !strings.Contains(titleLower, "404") &&
				!strings.Contains(titleLower, "403") &&
				!strings.Contains(titleLower, "status") &&
				!strings.Contains(titleLower, "error") &&
				!strings.Contains(titleLower, "index of") {

				// Bila model Epson tidak menyertakan kata "EPSON", tambahkan agar jelas
				if strings.Contains(strings.ToLower(string(bodyBytes)), "epson") && !strings.Contains(titleLower, "epson") {
					return "EPSON " + title
				}
				return title
			}
		}
	}
	return ""
}

func getMDNSService(services map[string]*mdnsService, name string) *mdnsService {
	service, exists := services[name]
	if !exists {
		service = &mdnsService{Name: name, Text: make(map[string]string)}
		services[name] = service
	}
	return service
}

func mdnsQuery(namesAndTypes ...any) []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(namesAndTypes)/2))
	for index := 0; index < len(namesAndTypes); index += 2 {
		name, _ := namesAndTypes[index].(string)
		typeCode, _ := namesAndTypes[index+1].(uint16)
		packet = append(packet, encodeDNSName(name)...)
		question := make([]byte, 4)
		binary.BigEndian.PutUint16(question[0:2], typeCode)
		binary.BigEndian.PutUint16(question[2:4], 1)
		packet = append(packet, question...)
	}
	return packet
}

func encodeDNSName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	var packet []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil
		}
		packet = append(packet, byte(len(label)))
		packet = append(packet, []byte(label)...)
	}
	return append(packet, 0)
}

func parseMDNSRecords(packet []byte) ([]mdnsRecord, error) {
	if len(packet) < 12 {
		return nil, errors.New("paket DNS terlalu pendek")
	}
	questionCount := int(binary.BigEndian.Uint16(packet[4:6]))
	recordCount := int(binary.BigEndian.Uint16(packet[6:8])) + int(binary.BigEndian.Uint16(packet[8:10])) + int(binary.BigEndian.Uint16(packet[10:12]))
	offset := 12
	for range questionCount {
		_, next, err := readDNSName(packet, offset)
		if err != nil || next+4 > len(packet) {
			return nil, errors.New("bagian pertanyaan DNS tidak valid")
		}
		offset = next + 4
	}
	records := make([]mdnsRecord, 0, recordCount)
	for range recordCount {
		name, next, err := readDNSName(packet, offset)
		if err != nil || next+10 > len(packet) {
			return nil, errors.New("record DNS tidak valid")
		}
		typeCode := binary.BigEndian.Uint16(packet[next : next+2])
		dataLength := int(binary.BigEndian.Uint16(packet[next+8 : next+10]))
		dataStart := next + 10
		dataEnd := dataStart + dataLength
		if dataEnd > len(packet) {
			return nil, errors.New("data record DNS tidak lengkap")
		}
		record := mdnsRecord{Name: name, Type: typeCode, Data: append([]byte(nil), packet[dataStart:dataEnd]...)}
		if typeCode == mdnsPTR {
			record.Value, _, err = readDNSName(packet, dataStart)
			if err != nil {
				return nil, err
			}
		} else if typeCode == mdnsSRV {
			if dataLength < 7 {
				return nil, errors.New("record SRV DNS terlalu pendek")
			}
			record.Port = int(binary.BigEndian.Uint16(packet[dataStart+4 : dataStart+6]))
			record.Target, _, err = readDNSName(packet, dataStart+6)
			if err != nil {
				return nil, err
			}
		}
		records = append(records, record)
		offset = dataEnd
	}
	return records, nil
}

func readDNSName(packet []byte, start int) (string, int, error) {
	if start < 0 || start >= len(packet) {
		return "", start, errors.New("offset nama DNS tidak valid")
	}
	labels := make([]string, 0, 4)
	offset, nextOffset, jumped := start, start, false
	visited := make(map[int]bool)
	for steps := 0; steps < 128; steps++ {
		if offset >= len(packet) {
			return "", start, errors.New("nama DNS terpotong")
		}
		length := int(packet[offset])
		if length&0xc0 == 0xc0 {
			if offset+1 >= len(packet) {
				return "", start, errors.New("pointer nama DNS terpotong")
			}
			pointer := ((length & 0x3f) << 8) | int(packet[offset+1])
			if pointer >= len(packet) || visited[pointer] {
				return "", start, errors.New("pointer nama DNS berulang atau tidak valid")
			}
			visited[pointer] = true
			if !jumped {
				nextOffset = offset + 2
			}
			jumped = true
			offset = pointer
			continue
		}
		if length&0xc0 != 0 || length > 63 {
			return "", start, errors.New("label nama DNS tidak valid")
		}
		offset++
		if length == 0 {
			if !jumped {
				nextOffset = offset
			}
			return strings.Join(labels, ".") + ".", nextOffset, nil
		}
		if offset+length > len(packet) {
			return "", start, errors.New("label nama DNS terpotong")
		}
		labels = append(labels, string(packet[offset:offset+length]))
		offset += length
		if !jumped {
			nextOffset = offset
		}
	}
	return "", start, errors.New("nama DNS terlalu dalam")
}

func parseMDNSText(data []byte) map[string]string {
	values := make(map[string]string)
	for offset := 0; offset < len(data); {
		length := int(data[offset])
		offset++
		if offset+length > len(data) {
			break
		}
		value := string(data[offset : offset+length])
		offset += length
		key, item, found := strings.Cut(value, "=")
		if found {
			values[strings.ToLower(key)] = item
		}
	}
	return values
}

func printerInstanceName(instance, serviceType string) string {
	instance = strings.TrimSuffix(instance, ".")
	suffix := strings.TrimSuffix(serviceType, ".")
	if strings.HasSuffix(instance, "."+suffix) {
		instance = strings.TrimSuffix(instance, "."+suffix)
	}
	return strings.TrimSpace(strings.TrimSuffix(instance, "."))
}

func normalizeDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, ".")) + "."
}

func splitTXTValues(value string) []string {
	if value == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return values
}
