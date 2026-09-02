#!/bin/sh

# The webapp schedules matcha itself, so there is no cron daemon to start.
# CRON_SCHEDULE is only read on first start, to seed settings.json; after that
# the schedule is managed at /settings.

echo "Starting webapp..."
exec /usr/local/bin/webapp
