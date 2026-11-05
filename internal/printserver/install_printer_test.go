package printserver

import (
	"testing"
)

func TestSanitizeCUPSQueueName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"EPSON L5190 Series", "EPSON_L5190_Series"},
		{"HP LaserJet Pro M201n (5BD8F2) Bidang 2", "HP_LaserJet_Pro_M201n_5BD8F2_Bidang_2"},
		{"Canon MF240 @ Kasir 1", "Canon_MF240_Kasir_1"},
		{"___Test---Queue___", "Test---Queue"},
		{"", "Printer_LAN"},
		{"   ", "Printer_LAN"},
	}

	for _, c := range cases {
		got := SanitizeCUPSQueueName(c.input)
		if got != c.want {
			t.Errorf("SanitizeCUPSQueueName(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

