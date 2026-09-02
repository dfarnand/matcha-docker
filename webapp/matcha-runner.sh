#!/bin/sh

# Manual run helper: docker exec <container> matcha-runner
#
# This goes through the webapp binary rather than calling matcha directly so
# that config.yaml is regenerated from the current settings first, and so the
# run is recorded in last-run.json like any other.

exec /usr/local/bin/webapp -run -source manual-cli
