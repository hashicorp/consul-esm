## Upcoming

IMPROVEMENTS:

  * Add consul version compatibility check on startup. [[GH-62](https://github.com/hashicorp/consul-esm/pull/62)]

## v0.3.3 (April 12, 2019)

BUG FIXES:

  * Set a default check interval of 30s. This prevents the check from running in a busy loop if consul-esm gets back an empty check interval from the api.
  * Fixed an issue where the catalog would not be updated despite a change in a health probe result.[[GH-36](https://github.com/hashicorp/consul-esm/issues/36)]

## v0.3.2 (January 23, 2019)

BUG FIXES:

  * Fixed an issue where updates to external nodes or their health by the user could be overwritten by Consul-ESM. Now uses the transaction API in Consul for catalog operations.

## v0.3.1 (October 31, 2018)

IMPROVEMENTS:

  * Pings to external nodes now run in parallel over the `node_probe_interval` instead of serially.

BUG FIXES:

  * Fixed an issue where the wrong KV path was used for node health.

## v0.3.0 (August 9, 2018)

IMPROVEMENTS:

  * The work of health checking and node probing will now be divided up amongst all ESM agents that share a `consul_service`/`consul_service_tag`/`consul_kv_path` combination. This is done by the leader using the KV store for coordination. The `consul_leader_key` field has been replaced by `consul_kv_path`, which is a path to a KV directory for a coordinating set of ESM nodes to share.
  * Added the `node_probe_interval` config field for controlling how often ESM will attempt to probe each external node.
  * Check definitions can now be updated in-place. [GH-17]

BUG FIXES:

  * Fixed an issue where the coordinate loop would run constantly and burn CPU when there were no nodes to probe.

## v0.2.0 (March 27, 2018)

IMPROVEMENTS:

  * Correctly exit on SIGTERM as well as SIGINT.
  * Use a simpler UDP ping that doesn't require root privileges. [GH-5]
  * Add a config option for setting the ping method (UDP or socket).

BUG FIXES:

  * Fixed the DeregisterCriticalServiceAfter field not being handled correctly. [GH-7]

## v0.1.0 (January 12, 2018)

  * Initial release
