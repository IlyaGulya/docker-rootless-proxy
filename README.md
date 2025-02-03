# Docker Socket Router

A secure routing daemon that enables safe access to rootless Docker sockets. This service routes Docker API requests from the system socket to user-specific rootless Docker instances, maintaining security through Unix socket credentials.

## Key Features

- **Secure Socket Routing**: Routes Docker API requests to user-specific rootless Docker instances based on Unix socket credentials
- **Robust Error Handling**: Returns Docker API-compatible error responses for common failure scenarios
- **Connection Monitoring**: Detailed metrics for each connection including duration and data transfer
- **Secure Socket Management**: Handles socket lifecycle with proper locking and cleanup
- **Graceful Shutdown**: Manages clean termination of active connections
- **Comprehensive Logging**: Structured logging via zap with configurable verbosity

## Technical Requirements

- Linux operating system
- Go 1.23 or later
- Systemd (for service management)
- Root privileges are required:
  - To be able to check if user has access to specified socket
  - To actually have access to any socket on the system

## Installation

### Building from Source

```bash
# Clone the repository
git clone https://github.com/IlyaGulya/docker-socket-router.git
cd docker-socket-router

# Build the binary
go build
```

### Debian Packages

The following packages are available from the [releases page](https://github.com/IlyaGulya/docker-socket-router/releases/latest):

- **AMD64 (x86_64)**
    - Debian package (`.deb`): `docker-socket-router_<version>_linux_amd64.deb`
    - Tarball: `docker-socket-router_<version>_linux_amd64.tar.gz`

- **ARM64 (aarch64)**
    - Debian package (`.deb`): `docker-socket-router_<version>_linux_arm64.deb`
    - Tarball: `docker-socket-router_<version>_linux_arm64.tar.gz`

Each release includes SHA256 checksums in the `docker-socket-router_<version>_checksums.txt` file.

### Manual System Installation

```bash
# Install binary
sudo mv docker-socket-router /usr/local/bin/

# Install systemd service
sudo cp packaging/systemd/docker-socket-router.service /etc/systemd/system/

# Reload systemd and start service
sudo systemctl daemon-reload
sudo systemctl enable docker-socket-router
sudo systemctl start docker-socket-router
```

## Configuration

The service uses the following default socket paths:
- System socket: `/var/run/docker.sock`
- User socket format: `/run/user/%d/docker.sock` (where %d is the user's UID)

## Security Model

- **Authentication**: Uses Unix socket peer credentials for user identification
- **Access Control**: Routes requests only to the requesting user's rootless Docker socket
- **Socket Permissions**: System socket created with 0666 permissions to allow user access
- **Socket Management**: Implements proper locking to prevent socket conflicts
- **Error Handling**: Returns standard Docker API error responses for security-related failures

## Monitoring and Debugging

### Logging

The service uses structured logging with the following information:
- Connection events (establishment and termination)
- Data transfer statistics (bytes to/from Docker)
- Error conditions with detailed context
- Socket management events

### Metrics

For each connection, the service tracks:
- Connection duration
- Bytes transferred to Docker daemon
- Bytes received from Docker daemon
- User identification information

### Debugging

Run with verbose logging enabled:
```bash
docker-socket-router -verbose
```

View service logs:
```bash
journalctl -u docker-socket-router
```

## Development

### Testing

Run the test suite:
```bash
# Run quick tests
go test -v -short ./...

# Run full test suite with race detection
go test -v -race ./...

# Run with coverage
go test -v -race -coverprofile=coverage.txt -covermode=atomic ./...
```

### Release Process

The project uses GitHub Actions for automated releases:
1. Tag a new version: `git tag -a v1.0.0 -m "Release v1.0.0"`
2. Push the tag: `git push origin v1.0.0`
3. The release workflow will:
    - Run tests
    - Generate binaries
    - Create a GitHub release

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/new-feature`
3. Make your changes
4. Run tests: `go test ./...`
5. Commit changes: `git commit -m 'Add new feature'`
6. Push to the branch: `git push origin feature/new-feature`
7. Submit a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Version Information

View version information:
```bash
docker-socket-router -version
```
