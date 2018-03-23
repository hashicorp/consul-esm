## (UNRELEASED)

IMPROVEMENTS:

  * Correctly handle SIGTERM and other signals like SIGHUP/SIGQUIT. [GH-6]
  * Use a simpler UDP ping that doesn't require root privileges. [GH-5]

BUG FIXES:

  * Fixed the DeregisterCriticalServiceAfter field not being handled correctly. [GH-7]

## v0.1.0 (January 12, 2018)

  * Initial release
