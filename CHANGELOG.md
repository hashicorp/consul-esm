## Upcoming

## v0.4.0 (July 27, 2020)

IMPROVEMENTS:

  * Update to compile with Go version 1.13. [[GH-64](https://github.com/hashicorp/consul-esm/pull/64)]
  * Prevent health checks from running more frequently than once per second. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Prevent spurious node status updates so that they only update on status change or status expiration. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Allow disabling coordinate updates with `disable_coordinate_updates` configuration option. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Support clusters of over 64 ESM instances. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Skip updating small changes in node coordinates. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Add more logging. [[GH-63](https://github.com/hashicorp/consul-esm/pull/63)]
  * Add consul version compatibility check on startup. [[GH-62](https://github.com/hashicorp/consul-esm/pull/62)]
  * Switch to Go Modules. [[GH-47](https://github.com/hashicorp/consul-esm/pull/47)]
  * Allow setting a unique ESM instance id with `instance_id` configuration option. [[GH-61](https://github.com/hashicorp/consul-esm/pull/61), [GH-60](https://github.com/hashicorp/consul-esm/issues/60)]

BUG FIXES:

  * Fix an issue when there are no healthy Consul instances. [[GH-48](https://github.com/hashicorp/consul-esm/pull/48), [GH-43](https://github.com/hashicorp/consul-esm/issues/43)]
  * Fix broken ping by switching to new library. [[GH-46](https://github.com/hashicorp/consul-esm/pull/46), [GH-45](https://github.com/hashicorp/consul-esm/issues/45)]
  * Fix spurious updates that cause delays in updating health checks by reading health check in consistent mode. [[GH-68](https://github.com/hashicorp/consul-esm/pull/68)]

DOCUMENTATION:

  * Request users to use :+1: voting system to help prioritize issues and pull requests. [[GH-57](https://github.com/hashicorp/consul-esm/pull/57)]
  * Clarification on time when ESM becomes critical and deregisters. [[GH-54](https://github.com/hashicorp/consul-esm/pull/54)]
  * Minimum ACL rules required to run ESM. [[GH-66](https://github.com/hashicorp/consul-esm/pull/66)]

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
