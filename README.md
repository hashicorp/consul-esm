[![Build Status](https://travis-ci.org/hashicorp/consul-esm.svg?branch=master)](https://travis-ci.org/hashicorp/consul-esm)  
Consul ESM (External Service Monitor)
================

This project provides a daemon to run alongside Consul in order to run health checks
for external nodes and update the status of those health checks in the catalog. It can also
manage updating the coordinates of these external nodes, if enabled. See Consul's
[External Services](https://www.consul.io/docs/guides/external.html) guide for some more information
about external nodes.

## Prerequisites

Consul ESM requires at least version 1.0.1 of Consul.

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

// The method to use for pinging external nodes. Defaults to "udp" but can
// also be set to "socket" to use ICMP (which requires root privileges).
ping_type = "udp"
```

[HCL]: https://github.com/hashicorp/hcl "HashiCorp Configuration Language (HCL)"

## Contributing

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
