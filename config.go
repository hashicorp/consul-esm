package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/command/flags"
	"github.com/hashicorp/hcl"
	"github.com/hashicorp/hcl/hcl/ast"
	"github.com/mitchellh/mapstructure"
)

const (
	PingTypeUDP    = "udp"
	PingTypeSocket = "socket"
)

type Config struct {
	LogLevel       string
	EnableSyslog   bool
	SyslogFacility string

	Service string
	Tag     string

	NodeMeta                 map[string]string
	Interval                 time.Duration
	DeregisterAfter          time.Duration
	CheckUpdateInterval      time.Duration
	CoordinateUpdateInterval time.Duration
	NodeReconnectTimeout     time.Duration

	HTTPAddr      string
	Token         string
	Datacenter    string
	CAFile        string
	CAPath        string
	CertFile      string
	KeyFile       string
	TLSServerName string

	PingType string
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

func DefaultConfig() *Config {
	return &Config{
		LogLevel: "INFO",
		Service:  "consul-esm",
		NodeMeta: map[string]string{
			"external-node": "true",
		},
		Interval:                 10 * time.Second,
		DeregisterAfter:          72 * time.Hour,
		CheckUpdateInterval:      5 * time.Minute,
		CoordinateUpdateInterval: 1 * time.Second,
		NodeReconnectTimeout:     72 * time.Hour,
		PingType:                 PingTypeUDP,
	}
}

type HumanConfig struct {
	LogLevel       flags.StringValue `mapstructure:"log_level"`
	EnableSyslog   flags.BoolValue   `mapstructure:"enable_syslog"`
	SyslogFacility flags.StringValue `mapstructure:"syslog_facility"`

	Service   flags.StringValue   `mapstructure:"consul_service"`
	LeaderKey flags.StringValue   `mapstructure:"consul_leader_key"`
	NodeMeta  []map[string]string `mapstructure:"external_node_meta"`

	NodeReconnectTimeout flags.DurationValue `mapstructure:"node_reconnect_timeout"`

	HTTPAddr      flags.StringValue `mapstructure:"http_addr"`
	Token         flags.StringValue `mapstructure:"token"`
	Datacenter    flags.StringValue `mapstructure:"datacenter"`
	CAFile        flags.StringValue `mapstructure:"ca_file"`
	CAPath        flags.StringValue `mapstructure:"ca_path"`
	CertFile      flags.StringValue `mapstructure:"cert_file"`
	KeyFile       flags.StringValue `mapstructure:"key_file"`
	TLSServerName flags.StringValue `mapstructure:"tls_server_name"`

	PingType flags.StringValue `mapstructure:"ping_type"`
}

func DecodeConfig(r io.Reader) (*HumanConfig, error) {
	// Parse the file (could be HCL or JSON)
	bytes, err := ioutil.ReadAll(r)
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

	// Decode the full thing into a map[string]interface for ease of use
	var config HumanConfig
	var m map[string]interface{}
	if err := hcl.DecodeObject(&m, list); err != nil {
		return nil, err
	}

	// Decode the simple (non service/handler) objects into Config
	msdec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:  flags.ConfigDecodeHook,
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
	config := DefaultConfig()
	if err := MergeConfigPaths(config, configFiles); err != nil {
		return nil, fmt.Errorf("Error loading config: %v", err)
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
		MergeConfig(dst, src)

		return nil
	}

	for _, path := range paths {
		if err := flags.Visit(path, visitor); err != nil {
			return err
		}
	}

	return nil
}

func MergeConfig(dst *Config, src *HumanConfig) {
	src.LogLevel.Merge(&dst.LogLevel)
	src.Service.Merge(&dst.Service)
	if len(src.NodeMeta) == 1 {
		dst.NodeMeta = src.NodeMeta[0]
	}
	src.NodeReconnectTimeout.Merge(&dst.NodeReconnectTimeout)
	src.HTTPAddr.Merge(&dst.HTTPAddr)
	src.Token.Merge(&dst.Token)
	src.Datacenter.Merge(&dst.Datacenter)
	src.CAFile.Merge(&dst.CAFile)
	src.CAPath.Merge(&dst.CAPath)
	src.CertFile.Merge(&dst.CertFile)
	src.KeyFile.Merge(&dst.KeyFile)
	src.TLSServerName.Merge(&dst.TLSServerName)
	src.PingType.Merge(&dst.PingType)
}
