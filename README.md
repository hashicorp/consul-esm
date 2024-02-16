[![Go Reference](https://pkg.go.dev/badge/github.com/hashicorp/consul-esm.svg)](https://pkg.go.dev/github.com/hashicorp/consul-esm)
[![build](https://github.com/hashicorp/consul-esm/actions/workflows/build.yml/badge.svg)](https://github.com/hashicorp/consul-esm/actions/workflows/build.yml)
[![ci](https://github.com/hashicorp/consul-esm/actions/workflows/ci.yml/badge.svg)](https://github.com/hashicorp/consul-esm/actions/workflows/ci.yml)

Consul ESM (External Service Monitor)
================

This project provides a daemon to run alongside Consul in order to run health
checks for external nodes and update the status of those health checks in the
catalog. It can also manage updating the coordinates of these external nodes,
if enabled. See Consul's [External
Services](https://learn.hashicorp.com/tutorials/consul/service-registration-external-services)
guide for some more information about external nodes.

## Community Support

If you have questions about how consul-esm works, its capabilities or
anything other than a bug or feature request (use github's issue tracker for
those), please see our community support resources.

Community portal: https://discuss.hashicorp.com/tags/c/consul/29/consul-esm

Other resources: https://www.consul.io/community.html

Additionally, for issues and pull requests, we'll be using the :+1: reactions
as a rough voting system to help gauge community priorities. So please add :+1:
to any issue or pull request you'd like to see worked on. Thanks.

## Prerequisites

Consul ESM requires at least version 1.4.1 of Consul.

| ESM version | Consul version required |
|-------------|-------------------------|
| 0.3.2 and higher | 1.4.1+ |
| 0.3.1 and lower | 1.0.1-1.4.0 |

## Installation

1. Download a pre-compiled, released version from the [Consul ESM releases page][releases].

1. Extract the binary using `unzip` or `tar`.

1. Move the binary into `$PATH`.

To compile from source, please see the instructions in the
[contributing section](#contributing).

## Usage

In order for the ESM to detect external nodes and health checks, any external nodes must be registered
directly with the catalog with `"external-node": "true"` set in the node metadata. Health checks can
also be registered with a 'Definition' field which includes the details of running the check. For example:

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
  },
  "Service": {
    "ID": "web1",
    "Service": "web",
    "Tags": [
      "v1"
    ],
    "Address": "127.0.0.1",
    "Port": 8000
  },
  "Checks": [{
    "Node": "foo",
    "CheckID": "service:web1",
    "Name": "Web HTTP check",
    "Notes": "",
    "Status": "passing",
    "ServiceID": "web1",
    "Definition": {
      "HTTP": "http://localhost:8000/health",
      "Interval": "10s",
      "Timeout": "5s"
    }
  },{
    "Node": "foo",
    "CheckID": "service:web2",
    "Name": "Web TCP check",
    "Notes": "",
    "Status": "passing",
    "ServiceID": "web1",
    "Definition": {
      "TCP": "localhost:8000",
      "Interval": "5s",
      "Timeout": "1s",
      "DeregisterCriticalServiceAfter": "30s"
     }
  }]
}
```

The `external-probe` field determines whether the ESM will do regular pings to the node and
maintain an `externalNodeHealth` check for the node (similar to the `serfHealth` check used
by Consul agents).

The ESM will perform a leader election by holding a lock in Consul, and the leader will then
continually watch Consul for updates to the catalog and perform health checks defined on any
external nodes it discovers. This allows externally registered services and checks to access
the same features as if they were registered locally on Consul agents.

Each ESM registers a health check for itself with the agent with
`"DeregisterCriticalServiceAfter": "30m"`, which is currently not configurable. This means after
failing its health check, the ESM will switch from passing status to critical status. If the ESM
remains in critical status for 30 minutes, then the agent will attempt to deregister the ESM. During
critical status the ESM’s assigned external health checks will be reassigned to another ESM with
passing status to monitor. Note: this is separate from the example JSON above for registering an
external health check which has a `DeregisterCriticalServiceAfter` of 30 seconds.

### Command Line
To run the daemon, pass the `-config-file` or `-config-dir` flag, giving the location of a config file
or a directory containing .json or .hcl files.

```
$ consul-esm -config-file=/path/to/config.hcl -config-dir /etc/consul-esm.d
Consul ESM running!
            Datacenter: "dc1"
               Service: "consul-esm"
           Service Tag: ""
            Service ID: "consul-esm:5a6411b3-1c41-f272-b719-99b4f958fa97"
Node Reconnect Timeout: "72h"

Log data will now stream in as it occurs:

2017/10/31 21:59:41 [INFO] Waiting to obtain leadership...
2017/10/31 21:59:41 [INFO] Obtained leadership
2017/10/31 21:59:42 [DEBUG] agent: Check 'foo/service:web1' is passing
2017/10/31 21:59:42 [DEBUG] agent: Check 'foo/service:web2' is passing
```

### Configuration

Configuration files can be provided in either JSON or [HashiCorp Configuration Language (HCL)][HCL] format.
For more information, please see the [HCL specification][HCL]. The following is an example HCL config file,
with the default values filled in:

```hcl
// The log level to use.
log_level = "INFO"

// Controls whether to enable logging to syslog.
enable_syslog = false

// The syslog facility to use, if enabled.
syslog_facility = ""

// Whether to log in json format
log_json = false

// The unique id for this agent to use when registering itself with Consul.
// If unconfigured, a UUID will be generated for the instance id.
// Note: do not reuse the same instance id value for other agents. This id
// must be unique to disambiguate different instances on the same host.
// Failure to maintain uniqueness will result in an already-exists error.
instance_id = ""

// The service name for this agent to use when registering itself with Consul.
consul_service = "consul-esm"

// The service tag for this agent to use when registering itself with Consul.
// ESM instances that share a service name/tag combination will have the work
// of running health checks and pings for any external nodes in the catalog
// divided evenly amongst themselves.
consul_service_tag = ""

// The directory in the Consul KV store to use for storing runtime data.
consul_kv_path = "consul-esm/"

// The node metadata values used for the ESM to qualify a node in the catalog
// as an "external node".
external_node_meta {
    "external-node" = "true"
}

// The length of time to wait before reaping an external node due to failed
// pings.
node_reconnect_timeout = "72h"

// The interval to ping and update coordinates for external nodes that have
// 'external-probe' set to true. By default, ESM will attempt to ping and
// update the coordinates for all nodes it is watching every 10 seconds.
node_probe_interval = "10s"

// The length of time to wait before updating the health status of a node.
// Note that the reaping process due to failed pings wont be triggered after
// the health status is updated, so if node_reconnect_timeout is lower than
// this parameter, it won't be triggered until this interval has expired.
node_health_refresh_interval = "1h"

// Controls whether or not to disable calculating and updating node coordinates
// when doing the node probe. Defaults to false i.e. coordinate updates
// are enabled.
disable_coordinate_updates = false

// Enable or disable agentless mode.
// When set to true, ESM will operate without a Consul Client Agent dependency.
// Can also be provided through the CONSUL_ENABLEAGENTLESS environment variable.
enable_agentless = true/false

// The address of the local Consul agent.
// or
// The address of the Consul server to use if `enable_agentless` is set to true.
// Can also be provided through the CONSUL_HTTP_ADDR environment variable.
http_addr = "localhost:8500"

// The ACL token to use when communicating with the local Consul agent. Can
// also be provided through the CONSUL_HTTP_TOKEN environment variable.
token = ""

// The Consul datacenter to use.
datacenter = "dc1"

// The target Admin Partition to use.
// Can also be provided through the CONSUL_PARTITION environment variable.
partition = ""

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

// The CA file to use for talking to HTTPS checks.
https_ca_file = ""

// The path to a directory of CA certs to use for talking to HTTPS checks.
https_ca_path = ""

// The client cert file to use for talking to HTTPS checks.
https_cert_file = ""

// The client key file to use for talking to HTTPS checks.
https_key_file = ""

// Client address to expose API endpoints. Required in order to expose /metrics endpoint for Prometheus. Example: "127.0.0.1:8080"
client_address = ""

// The method to use for pinging external nodes. Defaults to "udp" but can
// also be set to "socket" to use ICMP (which requires root privileges).
ping_type = "udp"

// The telemetry configuration which matches Consul's telemetry config options.
// See Consul's documentation https://www.consul.io/docs/agent/options#telemetry
// for more details on how to configure
telemetry {
	circonus_api_app = ""
 	circonus_api_token = ""
 	circonus_api_url = ""
 	circonus_broker_id = ""
 	circonus_broker_select_tag = ""
 	circonus_check_display_name = ""
 	circonus_check_force_metric_activation = ""
 	circonus_check_id = ""
 	circonus_check_instance_id = ""
 	circonus_check_search_tag = ""
 	circonus_check_tags = ""
 	circonus_submission_interval = ""
 	circonus_submission_url = ""
 	disable_hostname = false
 	dogstatsd_addr = ""
 	dogstatsd_tags = []
 	filter_default = false
 	prefix_filter = []
 	metrics_prefix = ""
 	prometheus_retention_time = "0"
 	statsd_address = ""
 	statsite_address = ""
}

// The number of additional successful checks needed to trigger a status update to
// passing. Defaults to 0, meaning the status will update to passing on the
// first successful check.
passing_threshold = 0

// The number of additional failed checks needed to trigger a status update to
// critical. Defaults to 0, meaning the status will update to critical on the
// first failed check.
critical_threshold = 0
```

[HCL]: https://github.com/hashicorp/hcl "HashiCorp Configuration Language (HCL)"

### Threshold for Updating Check Status

To prevent flapping, thresholds for updating a check status can be configured by `passing_threshold`
and `critical_threshold` such that a check will update and switch to be passing / critical after an
additional number of consecutive or non-consecutive checks.

By default, these configurations are set to 0, which retains the original ESM behavior. If the status
of a check is 'passing', then the next failed check will cause the status to update to be 'critical'.
Hence, the first failed check causes the update and 0 additional checks are needed.

If a check is currently 'passing' and configuration is `critical_threshold=3`, then after the first
failure, 3 additional consecutive failures (4 in total) are needed in order to update the status to 'critical'.

ESM also employs a counting system that allows for non-consecutive checks to aggregate and update
the check status. This counting system increments when a check result is the opposite of the current status
and decrements when same as the current status.

For an example of how non-consecutive checks are counted, we have a check that has the status
'passing', `critical_threshold=3`, and the counter is at 0 (c=0). The following pattern of pass/fail
will decrement/increment the counter as such:

`PASS (c=0), FAIL (c=1), FAIL (c=2), PASS (c=1), FAIL (c=2), FAIL (c=3), PASS (c=2), FAIL (c=3), FAIL (c=4)`

When the counter reaches 4 (1 initial fail + 3 additional fails), the critical_threshold is met and
the check status will update to 'critical' and the counter will reset.

Note: this implementation diverges from [Consul's anti-flapping thresholds][Consul Anti-Flapping], which
counts total consecutive checks.

[Consul Anti-Flapping]: https://www.consul.io/docs/agent/checks#success-failures-before-passing-warning-critical "Consul Agent Success/Failures before passing/warning/critical"

### Consul ACL Policies

With [ACL system][ACL] enabled on Consul agents, a specific ACL policy may be
required for ESM's token in order for ESM to perform its functions. To narrow
down the privileges required for ESM the following [ACL policy rules][rules]
can be used:

```hcl
agent_prefix "" {
  policy = "read"
}

key_prefix "consul-esm/" {
  policy = "write"
}

node_prefix "" {
  policy = "write"
}

service_prefix "" {
  policy = "write"
}

session_prefix "" {
   policy = "write"
}
```

The `key_prefix` rule is set to allow the `consul-esm/` KV prefix, which is
defined in the config file using the `consul_kv_path` parameter.

[ACL]: https://www.consul.io/docs/acl/acl-system.html "Consul ACL System"
[rules]: https://www.consul.io/docs/acl/acl-rules "Consul ACL Rules"

It is possible to have even finer-grained ACL policies if you know the
the set name of the consul agent that ESM is registered with and a set list of
nodes that ESM will monitor.
 - `<consul-agent-node-name>`: insert the node name for the consul agent that
consul-esm is registered with
 - `<monitored-node-name>`: insert the name of the nodes that ESM will
 monitor
 - `<consul-esm-name>`: insert the name that ESM is registered with. Default
 value is 'consul-esm' if not defined in config file using the `consul_service`
 parameter

```hcl
agent "<consul-agent-node-name>" {
  policy = "read"
}

key_prefix "consul-esm/" {
  policy = "write"
}

node "<monitored-node-name: one acl block needed per node>" {
  policy = "write"
}

node_prefix "" {
  policy = "read"
}

service "<consul-esm-name>" {
  policy = "write"
}

session "<consul-agent-node-name>" {
   policy = "write"
}
```

For context on usage of each ACL:

- `agent:read` - for features to check version compatibility and calculating network coordinates
- `key:write` - to store assigned checks
- `node:write` - to update the status of each node that esm monitors
- `node:read` - to retrieve nodes that need to be monitored
- `service:write` - to register esm service
- `session:write` - to acquire esm cluster leader lock

### Consul Namespaces (Enterprise Feature)

ESM supports [Consul Enterprise Namespaces
](https://www.consul.io/docs/enterprise/namespaces). When run with enterprise
Consul servers it will scan all accessible Namespaces for external nodes and
health checks to monitor. What is meant by "all accessible" is all Namespaces
accessible via [Namespace ACL rules](
https://www.consul.io/docs/security/acl/acl-rules) that provide `read` level
access to the Namespace. The simplest case of wanting to access all Namespaces
would add the below rule to the ESM ACL policy in the previous section...

```hcl
namespace_prefix "" {
  acl = "read"
}
```

If an ESM instance needs to monitor only a subset of existing Namespaces, the
policy will need to grant access to each Namespace explicitly. For example lets
say we have 3 Namespaces, "foo", "bar" and "zed" and you want this ESM to only
monitor "foo" and "bar". Your policy would need to have these listed (or a
common prefix would work)...

```hcl
namespace "foo" {
  acl = "read"
}
namespace "bar" {
  acl = "read"
}
```

#### Namespaces + `consul_kv_path` config setting:

* If you have multiple ESMs for HA (secondary, backup ESMs) have the **same**
  value set to `consul_kv_path`. (in practice these configs are identical)

* If you have multiple ESMs for separate Namespaces each must use a
  **different** setting for `consul_kv_path`.

ESM uses the `consul_kv_path` to determine where to keep its meta data. This
meta data will be different for each ESM monitoring different Namespaces.

Note you can have both, those in HA clusters would have the same value and each
separate HA cluster would use different values.

## Contributing

**Note** if you run Linux and see `socket: permission denied` errors with UDP
ping, you probably need to modify system permissions to allow for non-root
access to the ports. Running `sudo sysctl -w net.ipv4.ping_group_range="0
65535"` should fix the problem (until you reboot, see sysctl man page for how
to persist).

To build and install Consul ESM locally, you will need to install the
Docker engine:

- [Docker for Mac](https://docs.docker.com/engine/installation/mac/)
- [Docker for Windows](https://docs.docker.com/engine/installation/windows/)
- [Docker for Linux](https://docs.docker.com/engine/installation/linux/ubuntulinux/)

Clone the repository:

```shell
$ git clone https://github.com/hashicorp/consul-esm.git
```

To compile the `consul-esm` binary for your local machine:

```shell
$ make dev
```

This will compile the `consul-esm` binary into `bin/consul-esm` as
well as your `$GOPATH` and run the test suite.

If you want to compile a specific binary, run `make XC_OS/XC_ARCH`.
For example:
```
make darwin/amd64
```

Or run the following to generate all binaries:

```shell
$ make build
```

If you just want to run the tests:

```shell
$ make test
```

Or to run a specific test in the suite:

```shell
go test ./... -run SomeTestFunction_name
```

[releases]: https://releases.hashicorp.com/consul-esm "Consul ESM Releases"
