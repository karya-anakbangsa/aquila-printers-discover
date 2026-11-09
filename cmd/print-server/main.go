package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/RSISA-Semarang/rsisa-print-server/internal/printserver"
)

func main() {
	var (
		cloudURLFlag string
		listenFlag   string
		pollFlag     int
	)
	flag.StringVar(&cloudURLFlag, "cloud-url", "", "URL Server Web SIMRS untuk Cloud Agent (contoh: http://192.168.1.100:8081)")
	flag.StringVar(&listenFlag, "listen", "", "Alamat listen HTTP lokal (contoh: 127.0.0.1:17891)")
	flag.IntVar(&pollFlag, "poll", 0, "Interval polling antrean cloud dalam detik (contoh: 2)")
	flag.Parse()

	configPath := os.Getenv("RSISA_PRINT_SERVER_CONFIG")
	if configPath == "" {
		configPath = "print-server.json"
		if executable, err := os.Executable(); err == nil {
			configPath = filepath.Join(filepath.Dir(executable), configPath)
		}
	}

	if created, err := printserver.EnsureConfigFile(configPath); err != nil {
		log.Printf("peringatan inisialisasi config: %v", err)
	} else if created {
		log.Printf("file konfigurasi bawaan dibuat di %s", configPath)
	}

	cfg, err := printserver.LoadConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}

	// Override dari flags jika disediakan
	if cloudURLFlag != "" {
		cfg.CloudServerURL = cloudURLFlag
		cfg.CloudAgentEnabled = true
	}
	if listenFlag != "" {
		cfg.Listen = listenFlag
	}
	if pollFlag > 0 {
		cfg.CloudPollInterval = pollFlag
	}

	server := printserver.New(cfg, nil)
	go func() {
		if err := server.Listen(cfg.Listen); err != nil {
			log.Printf("print server berhenti: %v", err)
		}
	}()
	log.Printf("RSISA print server aktif di http://%s (config: %s)", cfg.Listen, configPath)
	if cfg.CloudAgentEnabled {
		log.Printf("[Cloud Agent] Mode aktif -> Relay ke %s (Poll setiap %ds)", cfg.CloudServerURL, cfg.CloudPollInterval)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
