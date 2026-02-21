#!/bin/sh

echo "${CRON_SCHEDULE} /usr/local/bin/matcha -c /app/config/config.yaml" > /etc/crontabs/root

echo "Starting cron daemon..."
crond -b -l 2

echo "Starting webapp..."
exec /usr/local/bin/webapp
