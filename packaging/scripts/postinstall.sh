#!/bin/sh
set -e

# Reload systemd configuration
systemctl daemon-reload

# Enable and start the service if this is a first install
if [ "$1" = "configure" ] && [ -z "$2" ]; then
    systemctl enable docker-socket-router.service || true
    systemctl start docker-socket-router.service || true
fi

# Just reload the service if this is an upgrade
if [ "$1" = "configure" ] && [ -n "$2" ]; then
    systemctl try-restart docker-socket-router.service || true
fi

exit 0
