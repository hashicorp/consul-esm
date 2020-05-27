package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDecodeMergeConfig(t *testing.T) {
	raw := bytes.NewBufferString(`
log_level = "INFO"
consul_service = "service"
consul_service_tag = "asdf"
consul_kv_path = "custom-esm/"
node_reconnect_timeout = "22s"
node_probe_interval = "12s"
external_node_meta {
	a = "1"
	b = "2"
}
http_addr = "localhost:4949"
token = "qwerasdf"
datacenter = "dc3"
ca_file = "ca.pem"
ca_path = "ca/"
cert_file = "cert.pem"
key_file = "key.pem"
tls_server_name = "example.io"
ping_type = "socket"
`)

	expected := &Config{
		LogLevel:                 "INFO",
		Service:                  "service",
		Tag:                      "asdf",
		KVPath:                   "custom-esm/",
		NodeReconnectTimeout:     22 * time.Second,
		CoordinateUpdateInterval: 12 * time.Second,
		NodeMeta: map[string]string{
			"a": "1",
			"b": "2",
		},
		HTTPAddr:      "localhost:4949",
		Token:         "qwerasdf",
		Datacenter:    "dc3",
		CAFile:        "ca.pem",
		CAPath:        "ca/",
		CertFile:      "cert.pem",
		KeyFile:       "key.pem",
		TLSServerName: "example.io",
		PingType:      PingTypeSocket,
	}

	result := &Config{}
	humanConfig, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	MergeConfig(result, humanConfig)

	if !reflect.DeepEqual(result, expected) {
		t.Fatal("Parsed config doesn't match expected:",
			structDiff(result, expected))
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		raw string
		err string
	}{
		{
			raw: `ping_type = "invalid"`,
			err: `ping_type must be one of either "udp" or "socket"`,
		},
		{
			raw: `ping_type = "socket"`,
			err: "",
		},
		{
			raw: `node_probe_interval = "500ms"`,
			err: "node_probe_interval cannot be lower than 1 second",
		},
	}

	for _, tc := range cases {
		buf := bytes.NewBufferString(tc.raw)

		result := DefaultConfig()
		humanConfig, err := DecodeConfig(buf)
		if err != nil {
			t.Fatal(err)
		}
		MergeConfig(result, humanConfig)

		err = ValidateConfig(result)
		if err == nil && tc.err != "" {
			t.Fatalf("got no error want %q", tc.err)
		}
		if err != nil && tc.err == "" {
			t.Fatalf("got error %s want nil", err)
		}
		if err == nil && tc.err != "" {
			t.Fatalf("got nil want error to contain %q", tc.err)
		}
		if err != nil && tc.err != "" && !strings.Contains(err.Error(), tc.err) {
			t.Fatalf("error %q does not contain %q", err.Error(), tc.err)
		}
	}
}
