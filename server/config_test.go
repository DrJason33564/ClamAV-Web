package main

import (
	"reflect"
	"testing"
)

func TestLoadConfigBuildsScannerAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		port string
		want []string
	}{
		{name: "defaults", want: []string{":8080"}},
		{name: "custom port", port: "9000", want: []string{":9000"}},
		{name: "IPv4", addr: "127.0.0.1", port: "9000", want: []string{"127.0.0.1:9000"}},
		{name: "IPv6", addr: "::1", port: "9000", want: []string{"[::1]:9000"}},
		{name: "any", addr: " any ", want: []string{":8080"}},
		{name: "multiple", addr: "127.0.0.1, ::1,127.0.0.1", want: []string{"127.0.0.1:8080", "[::1]:8080"}},
		{name: "IPv4 wildcard", addr: "0.0.0.0", want: []string{"0.0.0.0:8080"}},
		{name: "IPv6 wildcard", addr: "::", want: []string{"[::]:8080"}},
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
			if !reflect.DeepEqual(cfg.ListenAddrs, test.want) {
				t.Fatalf("unexpected scanner address: got %q, want %q", cfg.ListenAddrs, test.want)
			}
		})
	}
}

func TestInvalidListenAddresses(t *testing.T) {
	for _, value := range []string{"localhost", "127.0.0.1,", ",::1", "any,::1", "ANY", "0.0.0.0,127.0.0.1", "::,::1", "[::1]", "127.0.0.1:8080", "fe80::1%eth0", "127.0.0.1，::1"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseListenAddresses(value, "8080"); err == nil {
				t.Fatal("expected invalid address error")
			}
		})
	}
}
