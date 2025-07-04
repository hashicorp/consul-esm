// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/armon/go-metrics/prometheus"
	"github.com/hashicorp/consul/lib"
	"github.com/stretchr/testify/assert"
)

func TestDecodeMergeConfig(t *testing.T) {
	raw := bytes.NewBufferString(`
log_level = "INFO"
enable_debug = true
enable_syslog = true
instance_id = "test-instance-id"
consul_service = "service"
consul_service_tag = "asdf"
consul_kv_path = "custom-esm/"
node_reconnect_timeout = "22s"
node_health_refresh_interval = "23s"
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
https_ca_file = "CA-cert.pem"
https_ca_path = "CAPath/"
https_cert_file = "server-cert.pem"
https_key_file = "server-key.pem"
disable_coordinate_updates = true
client_address = "127.0.0.1:8080"
ping_type = "socket"
telemetry {
	circonus_api_app = "circonus_api_app"
 	circonus_api_token = "circonus_api_token"
 	circonus_api_url = "www.circonus.com"
 	circonus_broker_id = "1234"
 	circonus_broker_select_tag = "circonus_broker_select_tag"
 	circonus_check_display_name = "circonus_check_display_name"
 	circonus_check_force_metric_activation = "circonus_check_force_metric_activation"
 	circonus_check_id = "5678"
 	circonus_check_instance_id = "abcd"
 	circonus_check_search_tag = "circonus_check_search_tag"
 	circonus_check_tags = "circonus_check_tags"
 	circonus_submission_interval = "5s"
 	circonus_submission_url = "www.example.com"
 	disable_hostname = true
 	dogstatsd_addr = "1.2.3.4"
 	dogstatsd_tags = ["dogstatsd"]
 	filter_default = true
 	prefix_filter = ["+good", "-bad", "+better", "-worse", "wrong", ""]
 	metrics_prefix = "test"
 	prometheus_retention_time = "5h"
 	statsd_address = "example.io:8888"
 	statsite_address = "5.6.7.8"
}
passing_threshold = 3
critical_threshold = 2
log_json = true
`)

	expected := &Config{
		LogLevel:                  "INFO",
		EnableDebug:               true,
		InstanceID:                "test-instance-id",
		Service:                   "service",
		Tag:                       "asdf",
		KVPath:                    "custom-esm/",
		NodeReconnectTimeout:      22 * time.Second,
		NodeHealthRefreshInterval: 23 * time.Second,
		CoordinateUpdateInterval:  12 * time.Second,
		NodeMeta: map[string]string{
			"a": "1",
			"b": "2",
		},
		HTTPAddr:                 "localhost:4949",
		Token:                    "qwerasdf",
		Datacenter:               "dc3",
		CAFile:                   "ca.pem",
		CAPath:                   "ca/",
		CertFile:                 "cert.pem",
		KeyFile:                  "key.pem",
		TLSServerName:            "example.io",
		HTTPSCAFile:              "CA-cert.pem",
		HTTPSCAPath:              "CAPath/",
		HTTPSCertFile:            "server-cert.pem",
		HTTPSKeyFile:             "server-key.pem",
		DisableCoordinateUpdates: true,
		ClientAddress:            "127.0.0.1:8080",
		PingType:                 PingTypeSocket,
		Telemetry: lib.TelemetryConfig{
			CirconusAPIApp:                     "circonus_api_app",
			CirconusAPIToken:                   "circonus_api_token",
			CirconusAPIURL:                     "www.circonus.com",
			CirconusBrokerID:                   "1234",
			CirconusBrokerSelectTag:            "circonus_broker_select_tag",
			CirconusCheckDisplayName:           "circonus_check_display_name",
			CirconusCheckForceMetricActivation: "circonus_check_force_metric_activation",
			CirconusCheckID:                    "5678",
			CirconusCheckInstanceID:            "abcd",
			CirconusCheckSearchTag:             "circonus_check_search_tag",
			CirconusCheckTags:                  "circonus_check_tags",
			CirconusSubmissionInterval:         "5s",
			CirconusSubmissionURL:              "www.example.com",
			DisableHostname:                    true,
			DogstatsdAddr:                      "1.2.3.4",
			DogstatsdTags:                      []string{"dogstatsd"},
			FilterDefault:                      true,
			AllowedPrefixes:                    []string{"good", "better"},
			BlockedPrefixes:                    []string{"bad", "worse"},
			MetricsPrefix:                      "test",
			PrometheusOpts: prometheus.PrometheusOpts{
				Expiration: 5 * time.Hour,
			},
			StatsdAddr:   "example.io:8888",
			StatsiteAddr: "5.6.7.8",
		},
		PassingThreshold:  3,
		CriticalThreshold: 2,
		LogJSON:           true,
		EnableSyslog:      true,
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

		result, err := DefaultConfig()
		if err != nil {
			t.Fatal(err)
		}

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

func TestDecodeConfig(t *testing.T) {
	cases := []struct {
		name        string
		config      string
		expectError bool
	}{
		{
			"hcl parsing error",
			"{config}",
			true,
		},
		{
			"map decode error",
			`stuff = "stuff"`,
			true,
		},
		{
			"multiple telemetry blocks error",
			`telemetry {
				 metrics_prefix = "test"
			}
			telemetry {
				metrics_prefix = "test2"
		    }`,
			true,
		},
		{
			"multiple node-meta blocks error",
			`external_node_meta {
				a = "1"
			}
			external_node_meta {
				b = "2"
		    }`,
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.NewBufferString(tc.config)
			_, err := DecodeConfig(raw)
			if tc.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestConvertTelemetry(t *testing.T) {
	cases := []struct {
		name        string
		telem       Telemetry
		expectError bool
		expected    lib.TelemetryConfig
	}{
		{
			"happy path",
			Telemetry{
				PrefixFilter:            []string{"+allow", "-deny"},
				PrometheusRetentionTime: stringPointer("1m"),
			},
			false,
			lib.TelemetryConfig{
				AllowedPrefixes: []string{"allow"},
				BlockedPrefixes: []string{"deny"},
				PrometheusOpts: prometheus.PrometheusOpts{
					Expiration: 1 * time.Minute,
				},
			},
		},
		{
			"empty prefix rule gets ignored",
			Telemetry{
				PrefixFilter: []string{""},
			},
			false,
			lib.TelemetryConfig{},
		},
		{
			"invalid prefix rule gets ignored",
			Telemetry{
				PrefixFilter: []string{"*test"},
			},
			false,
			lib.TelemetryConfig{},
		},
		{
			"invalid prometheus retention error",
			Telemetry{
				PrometheusRetentionTime: stringPointer("invalid"),
			},
			true,
			lib.TelemetryConfig{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := convertTelemetry(tc.telem)
			if tc.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestPartition(t *testing.T) {
	cases := []struct {
		name           string
		config         string
		expectedConfig Config
		expectError    bool
	}{
		{
			"Partition not defined",
			"",
			Config{
				Partition: "",
			},
			false,
		},
		{
			"Partition is empty",
			"partition = \"\"",
			Config{
				Partition: "",
			},
			false,
		},
		{
			"Partition is default",
			"partition = \"default\"",
			Config{
				Partition: "default",
			},
			false,
		},
		{
			"Partition is non-default",
			"partition = \"admin\"",
			Config{
				Partition: "admin",
			},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.NewBufferString(tc.config)
			humanConfig, err := DecodeConfig(raw)
			if tc.expectError {
				assert.Error(t, err)
				return
			}

			result := &Config{}
			MergeConfig(result, humanConfig)

			// comparing partition only string
			assert.Equal(t, tc.expectedConfig.Partition, result.Partition)
			assert.NoError(t, err)
		})
	}
}

func stringPointer(s string) *string {
	if len(s) == 0 {
		return nil
	}
	return &s
}
