#!/bin/sh
set -e

# Remove the service on package removal
if [ "$1" = "remove" ]; then
    systemctl stop docker-socket-router.service || true
    systemctl disable docker-socket-router.service || true
fi

# Clean up systemd on purge
if [ "$1" = "purge" ]; then
    rm -f /etc/systemd/system/docker-socket-router.service
    systemctl daemon-reload
fi

exit 0
