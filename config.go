// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/armon/go-metrics/prometheus"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/command/flags"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/hcl"
	"github.com/hashicorp/hcl/hcl/ast"
	"github.com/mitchellh/mapstructure"
)

const (
	PingTypeUDP    = "udp"
	PingTypeSocket = "socket"
)

type Config struct {
	LogLevel          string
	EnableDebug       bool
	EnableSyslog      bool
	EnableAgentless   bool
	SyslogFacility    string
	LogJSON           bool
	LogFile           string
	LogRotateBytes    int
	LogRotateMaxFiles int
	LogRotateDuration time.Duration

	Partition string
	Service   string
	Tag       string
	KVPath    string

	InstanceID                string
	NodeMeta                  map[string]string
	Interval                  time.Duration
	DeregisterAfter           time.Duration
	CheckUpdateInterval       time.Duration
	CoordinateUpdateInterval  time.Duration
	NodeHealthRefreshInterval time.Duration
	NodeReconnectTimeout      time.Duration

	HTTPAddr      string
	Token         string
	Datacenter    string
	CAFile        string
	CAPath        string
	CertFile      string
	KeyFile       string
	TLSServerName string

	HTTPSCAFile   string
	HTTPSCAPath   string
	HTTPSCertFile string
	HTTPSKeyFile  string

	ClientAddress string

	PingType string

	DisableCoordinateUpdates bool

	Telemetry lib.TelemetryConfig

	PassingThreshold  int
	CriticalThreshold int

	ServiceDeregisterHttpHook string
	NodeDeregisterHttpHook    string
}

func (c *Config) ClientConfig() *api.Config {
	conf := api.DefaultConfig()

	if c.HTTPAddr != "" {
		conf.Address = c.HTTPAddr
	}
	if c.Token != "" {
		conf.Token = c.Token
	}
	if c.Datacenter != "" {
		conf.Datacenter = c.Datacenter
	}

	if c.Partition != "" {
		conf.Partition = c.Partition
	}

	if c.CAFile != "" {
		conf.TLSConfig.CAFile = c.CAFile
	}
	if c.CAPath != "" {
		conf.TLSConfig.CAPath = c.CAPath
	}
	if c.CertFile != "" {
		conf.TLSConfig.CertFile = c.CertFile
	}
	if c.KeyFile != "" {
		conf.TLSConfig.KeyFile = c.KeyFile
	}
	if c.TLSServerName != "" {
		conf.TLSConfig.Address = c.TLSServerName
	}

	return conf
}

// DefaultConfig generates esm config with default values
func DefaultConfig() (*Config, error) {
	// if no ID is configured, generate a unique ID for this agent
	instanceID, err := uuid.GenerateUUID()
	if err != nil {
		return nil, err
	}

	return &Config{
		InstanceID: instanceID,
		LogLevel:   "INFO",
		Service:    "consul-esm",
		KVPath:     "consul-esm/",
		NodeMeta: map[string]string{
			"external-node": "true",
		},
		Interval:                  10 * time.Second,
		DeregisterAfter:           72 * time.Hour,
		CheckUpdateInterval:       5 * time.Minute,
		CoordinateUpdateInterval:  10 * time.Second,
		NodeHealthRefreshInterval: 1 * time.Hour,
		NodeReconnectTimeout:      72 * time.Hour,
		PingType:                  PingTypeUDP,
		DisableCoordinateUpdates:  false,
		Partition:                 "",
		LogFile:                   "",
		LogRotateBytes:            0,
		LogRotateMaxFiles:         0,
		LogRotateDuration:         0,

		EnableAgentless: false,
	}, nil
}

// Telemetry is the configuration for to match Consul's go-metrics telemetry config
type Telemetry struct {
	CirconusAPIApp                     *string  `mapstructure:"circonus_api_app"`
	CirconusAPIToken                   *string  `mapstructure:"circonus_api_token"`
	CirconusAPIURL                     *string  `mapstructure:"circonus_api_url"`
	CirconusBrokerID                   *string  `mapstructure:"circonus_broker_id"`
	CirconusBrokerSelectTag            *string  `mapstructure:"circonus_broker_select_tag"`
	CirconusCheckDisplayName           *string  `mapstructure:"circonus_check_display_name"`
	CirconusCheckForceMetricActivation *string  `mapstructure:"circonus_check_force_metric_activation"`
	CirconusCheckID                    *string  `mapstructure:"circonus_check_id"`
	CirconusCheckInstanceID            *string  `mapstructure:"circonus_check_instance_id"`
	CirconusCheckSearchTag             *string  `mapstructure:"circonus_check_search_tag"`
	CirconusCheckTags                  *string  `mapstructure:"circonus_check_tags"`
	CirconusSubmissionInterval         *string  `mapstructure:"circonus_submission_interval"`
	CirconusSubmissionURL              *string  `mapstructure:"circonus_submission_url"`
	DisableHostname                    *bool    `mapstructure:"disable_hostname"`
	DogstatsdAddr                      *string  `mapstructure:"dogstatsd_addr"`
	DogstatsdTags                      []string `mapstructure:"dogstatsd_tags"`
	PrometheusRetentionTime            *string  `mapstructure:"prometheus_retention_time"`
	FilterDefault                      *bool    `mapstructure:"filter_default"`
	PrefixFilter                       []string `mapstructure:"prefix_filter"`
	MetricsPrefix                      *string  `mapstructure:"metrics_prefix"`
	StatsdAddr                         *string  `mapstructure:"statsd_address"`
	StatsiteAddr                       *string  `mapstructure:"statsite_address"`
}

// HumanConfig contains configuration that the practitioner can set
type HumanConfig struct {
	LogLevel          flags.StringValue   `mapstructure:"log_level"`
	EnableDebug       flags.BoolValue     `mapstructure:"enable_debug"`
	EnableSyslog      flags.BoolValue     `mapstructure:"enable_syslog"`
	EnableAgentless   flags.BoolValue     `mapstructure:"enable_agentless"`
	SyslogFacility    flags.StringValue   `mapstructure:"syslog_facility"`
	LogJSON           flags.BoolValue     `mapstructure:"log_json"`
	LogFile           flags.StringValue   `mapstructure:"log_file"`
	LogRotateBytes    intValue            `mapstructure:"log_rotate_bytes"`
	LogRotateMaxFiles intValue            `mapstructure:"log_rotate_max_files"`
	LogRotateDuration flags.DurationValue `mapstructure:"log_rotate_duration"`

	InstanceID flags.StringValue   `mapstructure:"instance_id"`
	Service    flags.StringValue   `mapstructure:"consul_service"`
	Tag        flags.StringValue   `mapstructure:"consul_service_tag"`
	KVPath     flags.StringValue   `mapstructure:"consul_kv_path"`
	NodeMeta   []map[string]string `mapstructure:"external_node_meta"`
	Partition  flags.StringValue   `mapstructure:"partition"`

	NodeReconnectTimeout      flags.DurationValue `mapstructure:"node_reconnect_timeout"`
	NodeProbeInterval         flags.DurationValue `mapstructure:"node_probe_interval"`
	NodeHealthRefreshInterval flags.DurationValue `mapstructure:"node_health_refresh_interval"`

	HTTPAddr      flags.StringValue `mapstructure:"http_addr"`
	Token         flags.StringValue `mapstructure:"token"`
	Datacenter    flags.StringValue `mapstructure:"datacenter"`
	CAFile        flags.StringValue `mapstructure:"ca_file"`
	CAPath        flags.StringValue `mapstructure:"ca_path"`
	CertFile      flags.StringValue `mapstructure:"cert_file"`
	KeyFile       flags.StringValue `mapstructure:"key_file"`
	TLSServerName flags.StringValue `mapstructure:"tls_server_name"`

	HTTPSCAFile   flags.StringValue `mapstructure:"https_ca_file"`
	HTTPSCAPath   flags.StringValue `mapstructure:"https_ca_path"`
	HTTPSCertFile flags.StringValue `mapstructure:"https_cert_file"`
	HTTPSKeyFile  flags.StringValue `mapstructure:"https_key_file"`

	ClientAddress flags.StringValue `mapstructure:"client_address"`

	PingType flags.StringValue `mapstructure:"ping_type"`

	DisableCoordinateUpdates flags.BoolValue `mapstructure:"disable_coordinate_updates"`

	Telemetry []Telemetry `mapstructure:"telemetry"`

	PassingThreshold  intValue `mapstructure:"passing_threshold"`
	CriticalThreshold intValue `mapstructure:"critical_threshold"`

	ServiceDeregisterHttpHook flags.StringValue `mapstructure:"service_deregister_http_hook"`
	NodeDeregisterHttpHook    flags.StringValue `mapstructure:"node_deregister_http_hook"`
}

// intValue provides a flag value that's aware if it has been set.
type intValue struct {
	v *int
}

// Merge will overlay this value if it has been set.
func (i *intValue) Merge(onto *int) {
	if i.v != nil {
		*onto = *(i.v)
	}
}

func intTointValueFunc() mapstructure.DecodeHookFunc {
	return func(
		f reflect.Type,
		t reflect.Type,
		data interface{}) (interface{}, error) {
		if f.Kind() != reflect.Int {
			return data, nil
		}

		val := intValue{}
		if t != reflect.TypeOf(val) {
			return data, nil
		}

		val.v = new(int)
		*(val.v) = data.(int)
		return val, nil
	}
}

// configDecodeHook should be passed to mapstructure in order to decode into
// the *Value objects here.
var configDecodeHook = mapstructure.ComposeDecodeHookFunc(
	flags.BoolToBoolValueFunc(),
	flags.StringToDurationValueFunc(),
	flags.StringToStringValueFunc(),
	intTointValueFunc(),
)

// DecodeConfig takes a reader containing config file and returns
// configuration struct
func DecodeConfig(r io.Reader) (*HumanConfig, error) {
	// Parse the file (could be HCL or JSON)
	bytes, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	root, err := hcl.Parse(string(bytes))
	if err != nil {
		return nil, fmt.Errorf("error parsing: %s", err)
	}

	// Top-level item should be a list
	list, ok := root.Node.(*ast.ObjectList)
	if !ok {
		return nil, fmt.Errorf("error parsing: root should be an object")
	}

	list = list.Children()
	nodeMeta := list.Filter("external_node_meta")
	if len(nodeMeta.Elem().Items) > 1 {
		return nil, fmt.Errorf("only one node_meta block allowed")
	}

	telemetry := list.Filter("telemetry")
	if len(telemetry.Elem().Items) > 1 {
		return nil, fmt.Errorf("only one telemetry block allowed")
	}

	// Decode the full thing into a map[string]interface for ease of use
	var config HumanConfig
	var m map[string]interface{}
	if err := hcl.DecodeObject(&m, list); err != nil {
		return nil, err
	}

	// Decode the simple (non service/handler) objects into Config
	msdec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:  configDecodeHook,
		Result:      &config,
		ErrorUnused: true,
	})
	if err := msdec.Decode(m); err != nil {
		return nil, err
	}

	return &config, nil
}

// BuildConfig builds a new Config object from the default configuration
// and the list of config files given and returns it after validation.
func BuildConfig(configFiles []string) (*Config, error) {
	config, err := DefaultConfig()
	if err != nil {
		return nil, err
	}
	if err := MergeConfigPaths(config, configFiles); err != nil {
		return nil, fmt.Errorf("Error loading config: %v", err)
	}

	if !strings.HasSuffix(config.KVPath, "/") {
		config.KVPath = config.KVPath + "/"
	}

	if err := ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("Error parsing config: %v", err)
	}

	return config, nil
}

// ValidateConfig verifies that the given Config object is valid.
func ValidateConfig(conf *Config) error {
	switch conf.PingType {
	case PingTypeUDP, PingTypeSocket:
		break
	default:
		return fmt.Errorf("ping_type must be one of either \"udp\" or \"socket\"")
	}

	if conf.CoordinateUpdateInterval < time.Second {
		return fmt.Errorf("node_probe_interval cannot be lower than 1 second")
	}

	if conf.PassingThreshold < 0 {
		return fmt.Errorf("passing_threshold cannot be negative")
	}

	if conf.CriticalThreshold < 0 {
		return fmt.Errorf("critical_threshold cannot be negative")
	}

	return nil
}

// MergeConfigPaths takes a list of config files or config directories and
// merges them on top of the given config.
func MergeConfigPaths(dst *Config, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	visitor := func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !strings.HasSuffix(fi.Name(), ".json") && !strings.HasSuffix(fi.Name(), ".hcl") {
			return nil
		}
		if fi.Size() == 0 {
			return nil
		}

		src, err := DecodeConfig(f)
		if err != nil {
			return err
		}

		return MergeConfig(dst, src)
	}

	for _, path := range paths {
		if err := flags.Visit(path, visitor); err != nil {
			return err
		}
	}

	return nil
}

func stringVal(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func boolVal(v *bool) bool {
	if v == nil {
		return false
	}

	return *v
}

// convertTelemetry converts the HumanConfig{} telemetry to the telemetry
// structure needed by Config{}
func convertTelemetry(telemetry Telemetry) (lib.TelemetryConfig, error) {
	// split metric filters into allow vs. blocked
	var telemetryAllowedPrefixes, telemetryBlockedPrefixes []string
	for _, rule := range telemetry.PrefixFilter {
		if rule == "" {
			fmt.Println("[WARN] Cannot have empty filter rule in prefix_filter")
			continue
		}
		switch rule[0] {
		case '+':
			telemetryAllowedPrefixes = append(telemetryAllowedPrefixes, rule[1:])
		case '-':
			telemetryBlockedPrefixes = append(telemetryBlockedPrefixes, rule[1:])
		default:
			fmt.Printf("[WARN] Filter rule must begin with either '+' or '-': %q\n", rule)
		}
	}

	var prometheusRetentionTime time.Duration
	if telemetry.PrometheusRetentionTime != nil {
		d, err := time.ParseDuration(*telemetry.PrometheusRetentionTime)
		if err != nil {
			return lib.TelemetryConfig{},
				fmt.Errorf("prometheus_retention_time: invalid duration: %q: %s", *telemetry.PrometheusRetentionTime, err)
		}

		prometheusRetentionTime = d
	}

	return lib.TelemetryConfig{
		CirconusAPIApp:                     stringVal(telemetry.CirconusAPIApp),
		CirconusAPIToken:                   stringVal(telemetry.CirconusAPIToken),
		CirconusAPIURL:                     stringVal(telemetry.CirconusAPIURL),
		CirconusBrokerID:                   stringVal(telemetry.CirconusBrokerID),
		CirconusBrokerSelectTag:            stringVal(telemetry.CirconusBrokerSelectTag),
		CirconusCheckDisplayName:           stringVal(telemetry.CirconusCheckDisplayName),
		CirconusCheckForceMetricActivation: stringVal(telemetry.CirconusCheckForceMetricActivation),
		CirconusCheckID:                    stringVal(telemetry.CirconusCheckID),
		CirconusCheckInstanceID:            stringVal(telemetry.CirconusCheckInstanceID),
		CirconusCheckSearchTag:             stringVal(telemetry.CirconusCheckSearchTag),
		CirconusCheckTags:                  stringVal(telemetry.CirconusCheckTags),
		CirconusSubmissionInterval:         stringVal(telemetry.CirconusSubmissionInterval),
		CirconusSubmissionURL:              stringVal(telemetry.CirconusSubmissionURL),
		DisableHostname:                    boolVal(telemetry.DisableHostname),
		DogstatsdAddr:                      stringVal(telemetry.DogstatsdAddr),
		DogstatsdTags:                      telemetry.DogstatsdTags,
		FilterDefault:                      boolVal(telemetry.FilterDefault),
		AllowedPrefixes:                    telemetryAllowedPrefixes,
		BlockedPrefixes:                    telemetryBlockedPrefixes,
		MetricsPrefix:                      stringVal(telemetry.MetricsPrefix),
		StatsdAddr:                         stringVal(telemetry.StatsdAddr),
		StatsiteAddr:                       stringVal(telemetry.StatsiteAddr),
		PrometheusOpts:                     prometheus.PrometheusOpts{Expiration: prometheusRetentionTime},
	}, nil
}

// MergeConfig merges the default config with any configuration
// set by the practitioner
func MergeConfig(dst *Config, src *HumanConfig) error {
	src.LogLevel.Merge(&dst.LogLevel)
	src.LogJSON.Merge(&dst.LogJSON)
	src.EnableDebug.Merge(&dst.EnableDebug)
	src.EnableSyslog.Merge(&dst.EnableSyslog)
	src.InstanceID.Merge(&dst.InstanceID)
	src.Service.Merge(&dst.Service)
	src.Partition.Merge(&dst.Partition)
	src.Tag.Merge(&dst.Tag)
	src.KVPath.Merge(&dst.KVPath)
	if len(src.NodeMeta) == 1 {
		dst.NodeMeta = src.NodeMeta[0]
	}
	src.NodeReconnectTimeout.Merge(&dst.NodeReconnectTimeout)
	src.NodeProbeInterval.Merge(&dst.CoordinateUpdateInterval)
	src.NodeHealthRefreshInterval.Merge(&dst.NodeHealthRefreshInterval)
	src.HTTPAddr.Merge(&dst.HTTPAddr)
	src.Token.Merge(&dst.Token)
	src.Datacenter.Merge(&dst.Datacenter)
	src.CAFile.Merge(&dst.CAFile)
	src.CAPath.Merge(&dst.CAPath)
	src.CertFile.Merge(&dst.CertFile)
	src.KeyFile.Merge(&dst.KeyFile)
	src.TLSServerName.Merge(&dst.TLSServerName)
	src.HTTPSCAFile.Merge(&dst.HTTPSCAFile)
	src.HTTPSCAPath.Merge(&dst.HTTPSCAPath)
	src.HTTPSCertFile.Merge(&dst.HTTPSCertFile)
	src.HTTPSKeyFile.Merge(&dst.HTTPSKeyFile)
	src.ClientAddress.Merge(&dst.ClientAddress)
	src.PingType.Merge(&dst.PingType)
	src.DisableCoordinateUpdates.Merge(&dst.DisableCoordinateUpdates)
	if len(src.Telemetry) == 1 {
		t, err := convertTelemetry(src.Telemetry[0])
		if err != nil {
			return err
		}
		dst.Telemetry = t
	}

	src.PassingThreshold.Merge(&dst.PassingThreshold)
	src.CriticalThreshold.Merge(&dst.CriticalThreshold)

	src.LogFile.Merge(&dst.LogFile)
	src.LogRotateBytes.Merge(&dst.LogRotateBytes)
	src.LogRotateMaxFiles.Merge(&dst.LogRotateMaxFiles)
	src.LogRotateDuration.Merge(&dst.LogRotateDuration)

	src.EnableAgentless.Merge(&dst.EnableAgentless)

	src.ServiceDeregisterHttpHook.Merge(&dst.ServiceDeregisterHttpHook)
	src.NodeDeregisterHttpHook.Merge(&dst.NodeDeregisterHttpHook)

	return nil
}
