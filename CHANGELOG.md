## v0.8.0 (Sep 24, 2024)

IMPROVEMENTS:
* Add support for Admin-Partitions [[GH-259](https://github.com/hashicorp/consul-esm/pull/281)]

SECURITY FIXES:
  * Updated go modules `github.com/hashicorp/go-retryablehttp`  and `github.com/hashicorp/go-retryablehttp` to fix the security vulnerability GO-2024-2947, GO-2024-2948, GHSA-xr7q-jx4m-x55m and GHSA-v6v8-xj6m-xwqh

## v0.7.2 (May 22, 2024)

BUG FIXES:
* fix updates for checks in namespaces [[GH-259](https://github.com/hashicorp/consul-esm/pull/259)]

## v0.7.1 (Jan 09, 2023)

BUG FIXES:
* fix goroutine/memory leak [[GH-196](https://github.com/hashicorp/consul-esm/pull/196), [GH-194](https://github.com/hashicorp/consul-esm/issues/194)]

## v0.7.0 (Dec 09, 2022)

IMPROVEMENTS:
* Enable ESM single instance to monitor all namespaces [[GH-191](https://github.com/hashicorp/consul-esm/pull/191), [GH-143](https://github.com/hashicorp/consul-esm/issues/143)]
* Add support for pprof endpoints [[GH-189](https://github.com/hashicorp/consul-esm/pull/189)]

BUG FIXES:
* Fix deadlock on checkrunner [[GH-193](https://github.com/hashicorp/consul-esm/pull/193), [GH-192](https://github.com/hashicorp/consul-esm/issues/192)]
* Fix USER setting in docker config [[GH-176](https://github.com/hashicorp/consul-esm/pull/176)]

## v0.6.2 (Oct 12, 2022)

BUG FIXES:

* Fix issue with spurious HTTP checks [[GH-171](https://github.com/hashicorp/consul-esm/pull/171), [GH-158](https://github.com/hashicorp/consul-esm/issues/158)]
* Fix deadlock in check updates [[GH-170](https://github.com/hashicorp/consul-esm/pull/170), [GH-159](https://github.com/hashicorp/consul-esm/issues/159)]
* Discard startup text when using JSON logging [[GH-169](https://github.com/hashicorp/consul-esm/pull/169), [GH-161](https://github.com/hashicorp/consul-esm/issues/161)]
* Fix regression with log_json setting [[GH-168](https://github.com/hashicorp/consul-esm/pull/168)]

## v0.6.1 (June 2, 2021)

IMPROVEMENTS:

  * Add support for TLSServerName for HTTP health checks [[GH-116](https://github.com/hashicorp/consul-esm/pull/116)]
  * Add external-source Metadata when registering consul-esm as a service [[GH-95](https://github.com/hashicorp/consul-esm/pull/95)]

BUG FIXES:

  * Handle case of same service ID on different nodes [[GH-135](https://github.com/hashicorp/consul-esm/pull/135)]
  * Dependency Updates [GH-134](https://github.com/hashicorp/consul-esm/pull/134)], [GH-132](https://github.com/hashicorp/consul-esm/issues/132)]
    * Update go-ping dependency to fix deadlock[[GH-129](https://github.com/hashicorp/consul-esm/pull/129)]
    * Fix issue with cross signed certs [[GH-84](https://github.com/hashicorp/consul-esm/issues/84)]
  * ESM doesn't deregister if status and output are same as before [[GH-130](https://github.com/hashicorp/consul-esm/pull/130)]
  * Add -v and use standard version output [[GH-126](https://github.com/hashicorp/consul-esm/pull/126), [GH-125](https://github.com/hashicorp/consul-esm/issues/125)]
  * External node checks disappear after re-registration of nodes [[GH-128](https://github.com/hashicorp/consul-esm/pull/128), [GH-119](https://github.com/hashicorp/consul-esm/issues/119)]


## v0.6.0 (September 23, 2021)

IMPROVEMENTS:

  * Add official docker image [[GH-108](https://github.com/hashicorp/consul-esm/pull/108), [GH-19](https://github.com/hashicorp/consul-esm/issues/19)]
  * Add support for Consul Namespaces and controlling which namespaces ESM monitors. [[GH-115](https://github.com/hashicorp/consul-esm/pull/115)]
  * Add support for arm64 builds. [[GH-98](https://github.com/hashicorp/consul-esm/pull/98), [GH-88](https://github.com/hashicorp/consul-esm/issues/88)]
  * Update logging to use hclog. [[GH-97](https://github.com/hashicorp/consul-esm/pull/97)]
  * Add `log_json` configuration option to allow enabling JSON logging. [[GH-105](https://github.com/hashicorp/consul-esm/pull/105), [GH-82](https://github.com/hashicorp/consul-esm/issues/82)]
  * Update `-version` output to standardize with other ecosystem projects. [[GH-99](https://github.com/hashicorp/consul-esm/pull/99), [GH-87](https://github.com/hashicorp/consul-esm/issues/87)]

BUG FIXES:

  * Fixed issue where anti-flapping counters were reset to zero when updating checks with latest from catalog. [[GH-103](https://github.com/hashicorp/consul-esm/pull/103)]

## v0.5.0 (December 7, 2020)

IMPROVEMENTS:

  * Add metrics support with configurable `telemetry` block. [[GH-67](https://github.com/hashicorp/consul-esm/pull/67)]
  * Add configurable http endpoint to expose telemetry metrics. [[GH-90](https://github.com/hashicorp/consul-esm/pull/90), [GH-89](https://github.com/hashicorp/consul-esm/issues/89)]
  * Support anti-flapping with configuration options `passing_threshold` and `critical_threshold`. [[GH-78](https://github.com/hashicorp/consul-esm/pull/78), [GH-50](https://github.com/hashicorp/consul-esm/issues/50)]
  * Update caught signal log from info-level to debug-level. [[GH-79](https://github.com/hashicorp/consul-esm/pull/79)]
  * Improve flaky tests. [[GH-80](https://github.com/hashicorp/consul-esm/pull/80)]
  * Add mTLS support for HTTPS checks with configuration options `https_ca_file`, `https_ca_path`, `https_cert_file`, and `https_key_file`. [[GH-81](https://github.com/hashicorp/consul-esm/pull/81), [GH-72](https://github.com/hashicorp/consul-esm/issues/72)]

BUG FIXES:

  * Remove checking status when syncing checks, which can cause flapping. [[GH-83](https://github.com/hashicorp/consul-esm/pull/83)]
  * Reduce goroutines used in external-probe ping. [[GH-85](https://github.com/hashicorp/consul-esm/pull/85)]

DOCUMENTATION:

  * Fix outdated "Consul ACL Policies" to include `operator = "read"` needed for 0.4.0 feature to check ESM and Consul version compatibility. [[GH-75](https://github.com/hashicorp/consul-esm/pull/75) & [GH-91](https://github.com/hashicorp/consul-esm/pull/91), [GH-74](https://github.com/hashicorp/consul-esm/issues/74)]
  * New documentation on finer-grained ACL policies and context on how each ACL is used. [[GH-76](https://github.com/hashicorp/consul-esm/pull/76), [GH-77](https://github.com/hashicorp/consul-esm/issues/77)]

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
