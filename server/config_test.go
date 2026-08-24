package main

import "testing"

func TestLoadConfigBuildsScannerAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		port string
		want string
	}{
		{name: "defaults", want: ":8080"},
		{name: "custom port", port: "9000", want: ":9000"},
		{name: "IPv4", addr: "127.0.0.1", port: "9000", want: "127.0.0.1:9000"},
		{name: "IPv6", addr: "::1", port: "9000", want: "[::1]:9000"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SCANNER_ADDR", test.addr)
			t.Setenv("SCANNER_PORT", test.port)
			t.Setenv("IS_TIMEDOCK", "N")

			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Addr != test.want {
				t.Fatalf("unexpected scanner address: got %q, want %q", cfg.Addr, test.want)
			}
		})
	}
}
