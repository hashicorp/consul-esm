package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/pascaldekloe/goe/verify"
)

func TestDecodeMergeConfig(t *testing.T) {
	raw := bytes.NewBufferString(`
log_level = "INFO"
consul_service = "service"
consul_leader_key = "key"
node_reconnect_timeout = "22s"
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
`)

	expected := &Config{
		LogLevel:             "INFO",
		Service:              "service",
		LeaderKey:            "key",
		NodeReconnectTimeout: 22 * time.Second,
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
	}

	result := &Config{}
	humanConfig, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	MergeConfig(result, humanConfig)

	verify.Values(t, "", result, expected)
}