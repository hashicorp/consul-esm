Consul ESM
================

This project provides a daemon to run alongside Consul in order to run health checks
for external nodes and update the status of those health checks in the catalog. It can also
manage updating the coordinates of these external nodes, if enabled.

### Command Line
To run the daemon, pass the `-config-file` or `config-dir` flag, giving the location of a config file
or a directory containing .json or .hcl files.

`consul-esm [--help] -config-file=/path/to/config.hcl -config-dir /etc/consul-esm.d`

### Configuration

Configuration files can be provided in either JSON or [HashiCorp Configuration Language (HCL)][HCL] format.
For more information, please see the [HCL specification][HCL]. The following is an example HCL config file,
with the default values filled in:

```hcl
// The service name for this agent to use when registering itself with Consul.
consul_service = "consul-esm"

// The path of the leader key that this agent will try to acquire before
// stepping up as a leader for the local Consul cluster. Should be the same
// across all Consul ESM agents in the datacenter.
consul_leader_key = "consul-esm/lock"

// The length of time to wait before reaping an external node due to failed
// pings.
node_reconnect_timeout = "72h"

// The address of the local Consul agent. Can also be provided through the
// CONSUL_HTTP_ADDR environment variable.
http_addr = "localhost:8500"

// The ACL token to use when communicating with the local Consul agent. Can
// also be provided through the CONSUL_HTTP_TOKEN environment variable.
token = ""

// The Consul datacenter to use.
datacenter = "dc1"

// The CA file to use for talking to Consul over TLS. Can also be provided
// though the CONSUL_CACERT environment variable.
ca_file = ""

// The path to a directory of CA certs to use for talking to Consul over TLS.
// Can also be provided through the CONSUL_CAPATH environment variable.
ca_path = ""

// The client cert file to use for talking to Consul over TLS. Can also be
// provided through the CONSUL_CLIENT_CERT environment variable.
cert_file = ""

// The client key file to use for talking to Consul over TLS. Can also be
// provided through the CONSUL_CLIENT_KEY environment variable.
key_file = ""

// The server name to use as the SNI host when connecting to Consul via TLS.
// Can also be provided through the CONSUL_TLS_SERVER_NAME environment
// variable.
tls_server_name = ""
```

[HCL]: https://github.com/hashicorp/hcl "HashiCorp Configuration Language (HCL)"