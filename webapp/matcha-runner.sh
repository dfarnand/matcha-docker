#!/bin/sh

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Starting Matcha..."
/usr/local/bin/matcha -c /app/config/config.yaml
exit_code=$?
echo "[$(date '+%Y-%m-%d %H:%M:%S')] Matcha completed with exit code $exit_code"
exit $exit_code
