# Persisto

> [!WARNING]
> This is a hobby project and absolutely not intended to be used in production.

<p align="center">
  <img src="./assets/persisto-cropped.png" alt="Persisto Logo" width="256" />
</p>

A lightweight Go server for managing SQLite databases with automatic memory caching and multi-stage storage support.

## Features & Roadmap

### Core Database Operations

- [x] List all available databases
- [x] Query databases (Read/Write operations)
- [ ] Download database files
- [ ] Upload database files
- [ ] Realtime database updates listening

### Storage & Performance

- [x] Multi-stage storage (disk → memory caching)
- [ ] Configurable database limits (count & size)
- [ ] Batched syncing for cost efficiency
- [ ] Partial database stage promotion (table/row level)

### Security & Access Control

- [ ] JWT-based authentication with Claims
- [ ] Database access permissions
- [ ] Rolling JWT key support
- [ ] Regex & Wildcard authorization patterns

### Management & Monitoring

- [ ] Web dashboard UI
- [ ] Infrastructure as code
- [ ] Usage analytics
- [ ] Health monitoring

### Developer Experience

- [ ] API documentation
- [ ] Usage examples
- [ ] Client SDKs

## Architecture

Persisto uses a simple architecture:

- **SQLite3 driver** for database operations
- **Abstracted file system** supporting local disk, remote storage, and memory
- **Automatic caching** moves frequently accessed data from disk to memory
- **Multi-stage storage** optimizes for both performance and cost

## Quick Start

### Local Development

```bash
# NOTE: build and run locally
make build && make run

# NOTE: run with Docker
docker build -t persisto . && docker run -p 8080:8080 persisto

# NOTE: run with Docker Compose
docker-compose up -d
```

### Environment Variables

#### Server Configuration

| Variable                       | Description              | Default                                                 |
| ------------------------------ | ------------------------ | ------------------------------------------------------- |
| `SERVER_PORT`                  | Server port              | 8080                                                    |
| `SERVER_VERSION`               | API version              | 1.0.0                                                   |
| `SERVER_NAME`                  | Server name              | SQLite Backend API                                      |
| `SERVER_DESCRIPTION`           | Server description       | API for managing SQLite databases and monitoring stages |
| `SERVER_CONTACT_NAME`          | Contact name             | Unknown                                                 |
| `SERVER_CONTACT_EMAIL`         | Contact email            | unspecified                                             |
| `SERVER_READ_TIMEOUT_SECONDS`  | Read timeout in seconds  | 10                                                      |
| `SERVER_WRITE_TIMEOUT_SECONDS` | Write timeout in seconds | 10                                                      |
| `SERVER_IDLE_TIMEOUT_SECONDS`  | Idle timeout in seconds  | 15                                                      |

#### Logging

| Variable                   | Description                                     | Default  |
| -------------------------- | ----------------------------------------------- | -------- |
| `LOGGING_LEVEL`            | Logging level (debug, info, warn, error, fatal) | info     |
| `LOGGING_OUTPUT_FILE_PATH` | Log file path                                   | logs.log |

#### Settings

| Variable                                   | Description                      | Default |
| ------------------------------------------ | -------------------------------- | ------- |
| `SETTINGS_AUTO_STAGE_MOVEMENT`             | Enable automatic stage movement  | true    |
| `SETTINGS_DEFAULT_DATABASE_CREATION_STAGE` | Default stage for new databases  | 3       |
| `SETTINGS_PERSISTENCE_STAGE`               | Persistence stage level          | 3       |
| `SETTINGS_STAGE_TIMEOUT_SECONDS`           | Stage timeout in seconds         | 300     |
| `SETTINGS_REQUEST_COUNT_THRESHOLD`         | Request count threshold          | 2       |
| `SETTINGS_AUTO_SYNC_ENABLED`               | Enable automatic synchronization | true    |

#### Storage - Memory

| Variable              | Description         | Default        |
| --------------------- | ------------------- | -------------- |
| `STORAGE_MEMORY_NAME` | Memory storage name | Memory Storage |

#### Storage - Local

| Variable                       | Description             | Default       |
| ------------------------------ | ----------------------- | ------------- |
| `STORAGE_LOCAL_NAME`           | Local storage name      | Local Storage |
| `STORAGE_LOCAL_DIRECTORY_PATH` | Local storage directory | ./storage     |

#### Storage - Remote (S3/R2)

| Variable                       | Description         | Default          |
| ------------------------------ | ------------------- | ---------------- |
| `STORAGE_REMOTE_NAME`          | Remote storage name | Remote Storage   |
| `STORAGE_REMOTE_ACCESS_KEY_ID` | S3/R2 access key ID | -                |
| `STORAGE_REMOTE_SECRET_KEY`    | S3/R2 secret key    | -                |
| `STORAGE_REMOTE_BUCKET_NAME`   | S3/R2 bucket name   | sqlite-databases |
| `STORAGE_REMOTE_ENDPOINT`      | S3/R2 endpoint URL  | -                |
| `STORAGE_REMOTE_REGION`        | S3/R2 region        | auto             |

#### GitHub Integration

| Variable                  | Description             | Default |
| ------------------------- | ----------------------- | ------- |
| `GITHUB_REPOSITORY_OWNER` | GitHub repository owner | -       |
| `GITHUB_REPOSITORY_NAME`  | GitHub repository name  | -       |
| `GITHUB_REPOSITORY_TOKEN` | GitHub access token     | -       |

## Development

### Prerequisites

- Go 1.22+
- Docker (optional)
- Make

### Setup

```bash
# NOTE: install development tools
make install-tools

# NOTE: run quality checks
make check

# NOTE: build locally
make build

# NOTE: run tests (when available)
make test
```

### Release Process

```bash
# NOTE: create a new release
make tag-release TAG=v1.0.0

# NOTE: or manually
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

Releases are automatically built with GoReleaser and published to GitHub Container Registry.

## Security Notice

⚠️ **Important**: For now this server does not include built-in authentication or authorization. Deploy only in secure, controlled environments. Future versions will include JWT-based authentication.
