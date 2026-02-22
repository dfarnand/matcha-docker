#!/bin/sh

cp /app/webapp/matcha-runner.sh /usr/local/bin/matcha-runner
chmod +x /usr/local/bin/matcha-runner

echo "${CRON_SCHEDULE} /usr/local/bin/matcha-runner" > /etc/crontabs/root

echo "Starting cron daemon..."
crond -b -l 2

echo "Starting webapp..."
exec /usr/local/bin/webapp
