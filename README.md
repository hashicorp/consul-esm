Consul ESM (External Service Monitor)
================

This project provides a daemon to run alongside Consul in order to run health checks
for external nodes and update the status of those health checks in the catalog. It can also
manage updating the coordinates of these external nodes, if enabled.

In order for the ESM to detect external nodes and health checks, any external nodes must be registered
directly with the catalog with `"external-node": "true"` set in the node metadata. For example:

```
$ curl --request PUT --data @node.json localhost:8500/v1/catalog/register

node.json:

{
  "Datacenter": "dc1",
  "ID": "40e4a748-2192-161a-0510-9bf59fe950b5",
  "Node": "foo",
  "Address": "192.168.0.1",
  "TaggedAddresses": {
    "lan": "192.168.0.1",
    "wan": "192.168.0.1"
  },
  "NodeMeta": {
    "external-node": "true",
    "external-probe": "true"
  }
}
```

The `external-probe` field determines whether the ESM will do regular pings to the node and
maintain an "externalNodeHealth" check for the node (similar to the `serfHealth` check used
by Consul agents).

The ESM will perform a leader election by holding a lock in Consul, and the leader will then
continually watch Consul for updates to the catalog and perform health checks defined on any
external nodes it discovers. This allows externally registered services and checks to access
the same features as if they were registered locally on Consul agents.

### Command Line
To run the daemon, pass the `-config-file` or `-config-dir` flag, giving the location of a config file
or a directory containing .json or .hcl files.

```
$ consul-esm -config-file=/path/to/config.hcl -config-dir /etc/consul-esm.d
Consul ESM running!
            Datacenter: "dc1"
               Service: "consul-esm"
            Leader Key: "consul-esm/lock"
Node Reconnect Timeout: "72h"

Log data will now stream in as it occurs:

2017/10/31 21:59:41 [INFO] Waiting to obtain leadership...
2017/10/31 21:59:41 [INFO] Obtained leadership
2017/10/31 21:59:42 [DEBUG] agent: Check 'foobar/service:redis1' is passing
```

### Configuration

Configuration files can be provided in either JSON or [HashiCorp Configuration Language (HCL)][HCL] format.
For more information, please see the [HCL specification][HCL]. The following is an example HCL config file,
with the default values filled in:

```hcl
// The log level to filter by.
log_level = "INFO"

// Controls whether to enable logging to syslog.
enable_syslog = false

// The syslog facility to use, if enabled.
syslog_facility = ""

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
