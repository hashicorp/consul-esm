## v0.3.0

IMPROVEMENTS:

  * The work of health checking and node probing will now be divided up amongst all ESM agents that share a `consul_service`/`consul_service_tag`/`consul_kv_path` combination. This is done by the leader using the KV store for coordination. The `consul_leader_key` field has been replaced by `consul_kv_path`, which is a path to a KV directory for a coordinating set of ESM nodes to share.
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
