#!/bin/sh

set -e

# Start tailscaled if not already running
if ! pgrep tailscaled > /dev/null 2>&1; then
    echo "Starting tailscaled daemon..."
    tailscaled --state=/var/lib/tailscale/tailscaled.state --socket=/var/run/tailscale/tailscaled.sock &
    
    # Wait for tailscaled to be ready
    sleep 3
fi

# first arg is `-f` or `--some-option`
if [ "${1#-}" != "$1" ]; then
    set -- gerbil "$@"
fi

exec "$@"