#!/usr/bin/dumb-init /bin/sh
set -e

# Note above that we run dumb-init as PID 1 in order to reap zombie processes
# as well as forward signals to all processes in its session. Normally, sh
# wouldn't do either of these functions so we'd leak zombies as well as do
# unclean termination of all our sub-processes.

# CESM_CONFIG_DIR isn't exposed as a volume but you can compose additional config
# files in there if you use this image as a base.
CESM_CONFIG_DIR=/consul-esm/config

# If the user is trying to run consul-esm directly with some arguments,
# then pass them to consul-esm.
# On alpine /bin/sh is busybox which supports the bashism below.
if [ "${1:0:1}" = '-' ]
then
    set -- /bin/consul-esm "$@"
fi

# Set the configuration directory
if [ "$1" = '/bin/consul-esm' ]
then
  shift
  set -- /bin/consul-esm \
    -config-dir="$CESM_CONFIG_DIR" \
    "$@"
fi

exec "$@"
