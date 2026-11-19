package utils

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrintIPPHighQualityOnlyWhenSupported(t *testing.T) {
	pdf := []byte("%PDF-1.4\n%%EOF")
	for _, tc := range []struct {
		name       string
		advertised byte
		wantHigh   bool
	}{
		{"high", 5, true},
		{"normal only", 4, false},
		{"unknown", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			printer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) < 8 {
					t.Errorf("invalid IPP request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				response := ippRequestHeaderVersion(ippStatusOK, binary.BigEndian.Uint32(body[4:8]), 2, 0)
				switch binary.BigEndian.Uint16(body[2:4]) {
				case ippOperationGetPrinterAttributes:
					if !bytes.Contains(body, []byte("print-quality-supported")) {
						t.Error("missing quality capability request")
					}
					response = append(response, 0x04) // printer-attributes-tag
					response = appendIPPAttribute(response, ippTagMimeMediaType, "document-format-supported", "application/pdf")
					if tc.advertised != 0 {
						response = appendIPPAttribute(response, ippTagEnum, "print-quality-supported", string([]byte{0, 0, 0, tc.advertised}))
					}
				case ippOperationPrintJob:
					high := appendIPPAttribute(nil, ippTagEnum, "print-quality", "\x00\x00\x00\x05")
					if bytes.Contains(body, high) != tc.wantHigh || !bytes.HasSuffix(body, pdf) {
						t.Errorf("unexpected print quality or modified PDF (want High: %v)", tc.wantHigh)
					}
					response = append(response, ippTagJobAttributes)
					response = appendIPPAttribute(response, ippTagInteger, "job-id", "\x00\x00\x00\x2a")
				default:
					t.Error("unexpected IPP operation")
				}
				_, _ = w.Write(append(response, ippTagEnd))
			}))
			defer printer.Close()
			job, err := PrintIPPDocument(context.Background(), "ipp"+strings.TrimPrefix(printer.URL, "http"), pdf, "Test", "rsisa", 1, "")
			if err != nil || job != 42 {
				t.Fatalf("print failed: job=%d, err=%v", job, err)
			}
		})
	}
}
