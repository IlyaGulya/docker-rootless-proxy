# Docker Socket Router

A Go-based routing daemon that enables secure access to rootless Docker sockets. This application routes Docker API requests from the system socket to user-specific rootless Docker instances.

## Features

- Routes Docker API requests to user-specific rootless Docker sockets
- Maintains security by authenticating users based on Unix socket credentials
- Provides detailed logging via syslog and stdout
- Graceful shutdown handling
- Automatic stale socket cleanup
- Comprehensive connection monitoring with byte transfer statistics

## Requirements

- Linux operating system
- Go 1.19 or later
- Systemd (for running as a service)
- Root privileges for initial setup

## Installation

1. Build the binary:
```bash
go build -o docker-socket-router
```

2. Move the binary to system location:
```bash
sudo mv docker-socket-router /usr/local/bin/
```

3. Set up systemd service:
```bash
sudo tee /etc/systemd/system/docker-socket-router.service <<EOF
[Unit]
Description=Docker Socket Router
After=network.target

[Service]
ExecStart=/usr/local/bin/docker-socket-router
Restart=always
User=root
Group=root

[Install]
WantedBy=multi-user.target
EOF
```

4. Start and enable the service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable docker-socket-router
sudo systemctl start docker-socket-router
```

## Socket Locations

- System socket: `/var/run/docker.sock`
- User socket format: `/run/user/%d/docker.sock` (where %d is the user's UID)

## Logging

The application logs to both syslog (daemon facility) and stdout. Log messages include:
- Connection events
- Data transfer statistics
- Error conditions
- Startup/shutdown events

## Security Considerations

- The system socket is created with 0666 permissions to allow all users access
- User authentication is performed using Unix socket peer credentials
- Each user's requests are routed only to their own rootless Docker socket
- Stale socket detection prevents conflicts with previous instances

## Monitoring

The application provides detailed metrics for each connection:
- Connection duration
- Bytes transferred to Docker
- Bytes received from Docker
- User ID information

## Development

### Building from Source

```bash
go build -o docker-socket-router
```

### Running Tests

```bash
go test ./...
```

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.
