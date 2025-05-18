<p align="center">
  <img src="cover-image.png" alt="Label Backup Cover Image"/>
</p>

# Label Backup Agent

**Label Backup is a lightweight, Docker-aware backup agent that automatically discovers and backs up your containerized databases based on Docker labels.**

It runs as a side-car or standalone container in your Docker environment, monitors container events, and triggers backups for databases (PostgreSQL, MySQL, MongoDB, Redis) according to cron schedules you define in labels. Backups can be streamed to local storage or Amazon S3 (and S3-compatible services), with webhook notifications on completion and automatic garbage collection of old backups.

## Features

- **Automatic Discovery:** Watches Docker events and inspects container labels to manage backup jobs dynamically.
- **Flexible Scheduling:** Uses `cron` expressions (with seconds field support) for per-container backup schedules.
- **Multiple Database Engines:** Supports PostgreSQL, MySQL, MongoDB, and Redis.
- **Multiple Destinations:** Store backups locally or on Amazon S3 (and S3-compatible services like MinIO, Cloudflare R2).
- **Compression:** On-the-fly Gzip compression of backup streams.
- **Webhook Notifications:** Sends JSON payloads to your specified URLs on backup success or failure.
- **Retention Policies:** Automatic pruning of old backups based on configurable retention periods (e.g., days, hours, minutes - global or per-backup).
- **Easy Configuration:** Configure globally via environment variables and override per container via Docker labels.

## Quick Start with Docker Compose

Refer to the `docker-compose-test.yml` in this repository for a comprehensive example of how to run the Label Backup agent alongside your database services.

**Key steps:**

1.  **Define your database services** (e.g., Postgres, MySQL) in `docker-compose.yml`.
2.  **Add labels to your database services** to configure backups (see Label Reference below).
3.  **Include the `label-backup` service:**

    ```yaml
    services:
      # ... your database services ...

      label-backup:
        # To build from local Dockerfile:
        # build:
        #   context: .
        #   dockerfile: Dockerfile
        # Or, to use a pre-built image (replace with actual image name when available):
        image: resulgg/label-backup # Use the image from Docker Hub
        container_name: label_backup_agent # User may prefer label_backup
        restart: unless-stopped
        volumes:
          - /var/run/docker.sock:/var/run/docker.sock:ro # Docker socket access (read-only)
          - ./backups:/backups # Mount for local backups
        environment:
          LOG_LEVEL: "info"
          GLOBAL_RETENTION_PERIOD: "7d" # Default retention: 7 days
          GC_DRY_RUN: "false"
          # Add other global configurations (see Configuration section)
    ```

4.  Run `docker-compose up --build -d` (the `--build` is important after code changes).

## Comprehensive Docker Compose Example

For a more detailed example showcasing various features of `label-backup` within a `docker-compose` environment, refer to the setup below. This example includes:

- The `label-backup` agent service itself, configured for both local and remote (S3 via MinIO) backups.
- A MinIO service for local S3-compatible storage, allowing you to test `backup.dest=remote`.
- A PostgreSQL service configured for daily remote backups to MinIO.
- A MySQL service configured for frequent local backups.
- An Alpine service demonstrating how to back up a generic named volume (e.g., application data or logs) to MinIO.

This setup allows you to see various features in action, including different backup types, schedules, retention policies, and destinations.

**`docker-compose.yml` example:**

```yaml
version: "3.8"

services:
  label-backup:
    image: resulgg/label-backup # Assuming you are using the Docker Hub image
    # To build from local Dockerfile (if you have the source code):
    # build:
    #   context: . # Path to your label-backup project directory
    #   dockerfile: Dockerfile
    container_name: label_backup_agent
    restart: unless-stopped
    ports:
      - "8080:8080" # Optional: if the agent exposes an API/UI on this port
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro # Essential for Docker API access
      - ./my-local-backups:/backups # Host directory for storing local backups
    environment:
      - LOG_LEVEL=info
      - GLOBAL_RETENTION_PERIOD=7d # Default retention for all backups
      - LOCAL_BACKUP_PATH=/backups # Internal path for local backups, matches volume mount
      - RECONCILE_INTERVAL_SECONDS=60 # How often to check for new/updated containers
      # S3 Configuration (for backup.dest=remote) - pointing to our MinIO service below
      - BUCKET_NAME=my-backup-bucket
      - REGION=us-east-1 # Common default, adjust if needed for your S3 provider
      - ENDPOINT=http://minio:9000 # Service name of our MinIO container
      - ACCESS_KEY_ID=minioadmin # MinIO root user
      - SECRET_ACCESS_KEY=minioadmin # MinIO root password
      - S3_USE_PATH_STYLE=true # Crucial for MinIO and some other S3-compatibles
    depends_on:
      minio: # Ensures MinIO is attempted to start and healthy before label-backup
        condition: service_healthy
    networks:
      - backup_net # Custom network for inter-service communication

  minio: # Local S3-compatible server for testing remote backups
    image: minio/minio:latest
    container_name: minio_s3_server_example
    restart: unless-stopped
    ports:
      # Expose MinIO API and Console on different host ports to avoid conflicts
      # if you run other MinIO instances or the main project's docker-compose.
      - "9005:9000" # MinIO API Port (host:container)
      - "9006:9001" # MinIO Console Port (host:container) - Access via http://localhost:9006
    volumes:
      - minio_storage_example:/data # Named volume for MinIO to persist its data
    environment:
      - MINIO_ROOT_USER=minioadmin
      - MINIO_ROOT_PASSWORD=minioadmin
      # You can pre-create buckets if desired, but label-backup will create BUCKET_NAME if it doesn't exist.
      # - MINIO_DEFAULT_BUCKETS=my-backup-bucket
    command: server /data --console-address ":9001" # Start MinIO server and enable console
    healthcheck: # Ensures MinIO is healthy before other services depend on it
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 30s
      timeout: 20s
      retries: 3
    networks:
      - backup_net

  my-app-db-postgres: # Example PostgreSQL service to be backed up
    image: postgres:15
    container_name: my_postgres_service_example
    restart: unless-stopped
    environment:
      - POSTGRES_USER=pguser
      - POSTGRES_PASSWORD=pgpass
      - POSTGRES_DB=appdb
    volumes:
      - pg_data_example:/var/lib/postgresql/data # Persist PostgreSQL data
    labels:
      - "backup.enabled=true" # Enable backup for this service
      - "backup.type=postgres" # Specify dumper type
      - "backup.cron=0 1 * * *" # Schedule: Daily at 1 AM
      - "backup.conn=postgresql://pguser:pgpass@my-app-db-postgres:5432/appdb?sslmode=disable" # Connection string
      - "backup.database=appdb" # Database name (often inferred from conn string but good to be explicit)
      - "backup.dest=remote" # Destination: remote S3 (our MinIO service)
      - "backup.retention=14d" # Custom retention: 14 days for these backups
      - "backup.prefix=postgres-appdb" # Prefix for backup filenames/objects
    networks:
      - backup_net

  my-app-db-mysql: # Example MySQL service to be backed up
    image: mysql:8.0
    container_name: my_mysql_service_example
    restart: unless-stopped
    command: ["mysqld", "--default-authentication-plugin=mysql_native_password"] # For MySQL 8 compatibility
    environment:
      - MYSQL_ROOT_PASSWORD=rootpass
      - MYSQL_DATABASE=another_appdb
      - MYSQL_USER=mysqluser
      - MYSQL_PASSWORD=mysqlpass
    volumes:
      - mysql_data_example:/var/lib/mysql # Persist MySQL data
    labels:
      - "backup.enabled=true"
      - "backup.type=mysql"
      - "backup.cron=0 */6 * * *" # Schedule: Every 6 hours
      - "backup.conn=mysql://mysqluser:mysqlpass@my-app-db-mysql:3306/another_appdb?sslmode=disabled"
      - "backup.database=another_appdb"
      - "backup.dest=local" # Destination: local path (defined in label-backup's LOCAL_BACKUP_PATH)
      - "backup.retention=3d" # Custom retention: 3 days
      - "backup.prefix=mysql-another-appdb"
    networks:
      - backup_net

  my-app-configs: # Example of backing up a generic volume (e.g., application configurations or logs)
    image: alpine # A simple base image
    container_name: my_app_configs_example
    volumes:
      - app_configs_vol_example:/etc/appconfig # Mount a volume containing important data
    # This command just creates a dummy file and keeps the container alive
    command: sh -c "mkdir -p /etc/appconfig; echo 'sensitive_setting=true' > /etc/appconfig/settings.conf; while true; do sleep 3600; done"
    labels:
      - "backup.enabled=true"
      - "backup.type=volume" # Special type for generic volume backup
      - "backup.cron=0 0 * * 0" # Schedule: Weekly, on Sunday at midnight
      - "backup.path=/etc/appconfig" # The path *inside this container* to be backed up
      - "backup.dest=remote" # Destination: remote S3 (our MinIO service)
      - "backup.retention=4w" # Custom retention: 4 weeks
      - "backup.prefix=app-configs"
    networks:
      - backup_net

volumes: # Define all named volumes used by the services
  minio_storage_example: {}
  pg_data_example: {}
  mysql_data_example: {}
  app_configs_vol_example: {}
  # Note: 'my-local-backups' (used by label-backup service) is a host-mounted volume,
  # so it's defined directly in the service's volume mapping, not in this top-level 'volumes' section.

networks: # Define the custom network
  backup_net:
    driver: bridge # Default bridge driver is usually fine
```

## Standalone `docker run` Setup

If you prefer not to use Docker Compose, or want to integrate the Label Backup agent into an existing environment managed by `docker run` commands, you can run it as a standalone container.

Ensure the Docker image is available (e.g., pulled from Docker Hub: `resulgg/label-backup`).

Here's an example `docker run` command:

```bash
docker run -d \
  --name label_backup_agent \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /path/on/your/host/for/backups:/backups \
  -e LOG_LEVEL="info" \
  -e GLOBAL_RETENTION_PERIOD="7d" \
  -e GC_DRY_RUN="false" \
  -e LOCAL_BACKUP_PATH="/backups" \
  # Add S3 credentials and other environment variables as needed:
  # -e BUCKET_NAME="your-s3-bucket" \
  # -e REGION="your-s3-region" \
  # -e ENDPOINT="your-s3-endpoint" \
  # -e ACCESS_KEY_ID="your-access-key-id" \
  # -e SECRET_ACCESS_KEY="your-secret-access-key" \
  # Ensure this container can reach other database containers,
  # typically by attaching it to the same Docker network(s)
  # --network your_existing_network \
  resulgg/label-backup
```

**Key considerations for `docker run`:**

- **`-d`**: Runs the container in detached mode (in the background).
- **`--name label_backup_agent`**: Assigns a convenient name to the container.
- **`--restart unless-stopped`**: Ensures the agent restarts automatically unless manually stopped.
- **`-v /var/run/docker.sock:/var/run/docker.sock:ro`**: **Crucial.** Mounts the Docker socket read-only into the agent container, allowing it to listen to Docker events and inspect other containers.
- **`-v /path/on/your/host/for/backups:/backups`**: Mounts a directory from your host machine into the container at `/backups`. This is where backups will be stored if `LOCAL_BACKUP_PATH` is `/backups` and `backup.dest=local` is used. Adjust `/path/on/your/host/for/backups` to your desired location.
- **Environment Variables (`-e ...`)**: Set these according to your global configuration needs (log level, S3 details, retention, etc.).
- **Networking (`--network ...`)**: For the backup agent to connect to your database containers (e.g., `test-db`, `test-mysql-db`), it must be on the same Docker network(s) as those database containers. If your databases are on a custom Docker network, add the `--network your_existing_network` flag to the `docker run` command. If they are on the default bridge network and you are referencing them by container IP or a hostname resolvable on that network, ensure connectivity. For service discovery by container name (e.g. `test-db` in a connection string), both `label-backup` and the target database must be on the same user-defined Docker network.
- **Image Name**: Use `resulgg/label-backup` for the Docker Hub image.

Remember to label your database containers as described in the "Docker Labels" section for the agent to discover and back them up.

## Configuration

The agent can be configured using environment variables (for global settings) and Docker labels (for per-container settings, which override global ones where applicable).

### Environment Variables (for `label-backup` service)

- `LOG_LEVEL`: Logging level (e.g., `debug`, `info`, `warn`, `error`). Default: `info` (if not set or invalid).
  - `debug`: Most verbose. Detailed internal operations, variable states. Useful for troubleshooting. Includes all levels below.
  - `info`: Standard operational messages, major lifecycle events (start, stop, backup triggered). Includes `warn` and `error`.
  - `warn` (or `warning`): Potentially problematic situations that don't stop execution. Includes `error`.
  - `error`: Errors that prevented an operation from completing.
  - Higher levels like `dpanic`, `panic`, and `fatal` are also supported by the underlying logger but are typically for critical application failures.
- `GLOBAL_RETENTION_PERIOD`: Default retention period if not specified by a container's `backup.retention` label. Examples: `"30d"` (30 days), `"48h"` (48 hours), `"7d12h"` (if supported by parser, current supports simple d,h,m or full Go duration strings). Default: `"7d"`.
- `GC_DRY_RUN`: If `"true"` or `"1"`, the garbage collector will only log what it would delete. Default: `"false"`.
- `LOCAL_BACKUP_PATH`: Base path for the `local` writer. Default: `/backups` (inside the agent container).
- `RECONCILE_INTERVAL_SECONDS`: How often to reconcile scheduler with discovered containers. Default: `10`.

- **S3 Writer:**

  - `BUCKET_NAME`: (Required if using `remote` destination) Your S3 bucket name.
  - `REGION`: AWS region for the S3 bucket. (Can also be set via standard AWS SDK mechanisms). If using a custom `ENDPOINT`, this might be optional or a default like `us-east-1` can be used.
  - `ENDPOINT`: (Optional) Custom S3 API endpoint URL (e.g., `http://minio:9000` for MinIO, or your R2 endpoint). If provided, the S3 client will be configured for S3-compatible storage (path-style addressing will be enabled).
  - `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY`: S3 credentials. If using AWS S3 and these are not set, the SDK will attempt its default credential chain (e.g., IAM roles, shared credentials file). For S3-compatible services, these are typically required.

- **Webhook Notifications:**

  - `WEBHOOK_URL`: Global default URL for notifications.
  - `WEBHOOK_SECRET`: Global secret key for HMAC-SHA256 signing of webhook payloads.
  - `WEBHOOK_TIMEOUT_SECONDS`: HTTP client timeout for sending webhooks. Default: `10`.
  - `WEBHOOK_MAX_RETRIES`: Maximum number of retries for a failed webhook. Default: `3`.

### Docker Labels (on database containers)

All labels are strings. Boolean values are typically `"true"` or `"false"`.

- `backup.enabled`: (`"true"` or `"false"`) Master switch for enabling backups on this container.
- `backup.type`: Database type. Required if `backup.enabled` is `"true"`.
  - Values: `postgres`, `mysql`, `mongodb`, `redis`
- `backup.conn`: Connection string/URI for the database.
- `backup.database`: Name of the specific database to dump.
- `backup.cron`: Cron expression for scheduling. **Seconds field is supported.**
  - Examples: `"@daily"`, `"0 */5 * * * *"` (every 5 minutes with seconds field), `"@every 1h30m"`
- `backup.dest`: Backup destination type. Default: `local`.
  - Values: `local`, `remote` (for S3 or S3-compatible storage)
- `backup.prefix`: A prefix string for the backup object/filename (e.g., `customer_xyz/daily_backups`).
- `backup.webhook`: URL to send a notification JSON payload to after a backup attempt. Overrides global.
- `backup.retention`: Retention period for backups from this container. Overrides `GLOBAL_RETENTION_PERIOD`.
  - Examples: `"7d"` (7 days), `"24h"` (24 hours), `"90m"` (90 minutes). A plain number like `"3"` is interpreted as days (e.g., `"3d"`).
  - If empty or invalid, the `GLOBAL_RETENTION_PERIOD` is used. `"0"` or a negative value effectively means use global.

## Retention / Garbage Collection (GC)

- A global GC job runs daily (default: 4 AM, hardcoded in `main.go` for now).
- It checks all backups managed by the agent in all configured destinations.
- **Retention Logic:**
  1.  If a container has `backup.retention` set to a valid positive duration (e.g., `"12h"`, `"3d"`), that duration is used for its backups.
  2.  If `backup.retention` is empty, invalid, zero, or negative, the `GLOBAL_RETENTION_PERIOD` environment variable value is used.
  3.  If `GLOBAL_RETENTION_PERIOD` is also not set or invalid, a default of `"7d"` (7 days) is used.
- Backups older than their determined effective retention period are deleted.
- Set `GC_DRY_RUN="true"` (or `"1"`) in the agent's environment to see what would be deleted without actually deleting anything.

## Important Notes

### MySQL 8.x Authentication Compatibility

When using `label-backup` (which currently uses an Alpine-based Docker image with MariaDB client tools) to back up a MySQL 8.x server, you might encounter authentication errors related to the `caching_sha2_password` plugin. This is because MySQL 8.x defaults to this plugin, while the MariaDB client may expect the older `mysql_native_password`.

To ensure compatibility:

1.  **Configure your MySQL 8.x server to use `mysql_native_password` as the default authentication plugin.**
    If you are running your MySQL server via Docker Compose, you can achieve this by adding the `command` directive to your MySQL service definition in `docker-compose.yml`:

    ```yaml
    services:
      # ... your other services ...

      your-mysql-service-name: # e.g., test-mysql-db
        image: mysql:8.0 # Or your specific MySQL 8.x image
        # ... other configurations like environment, volumes ...
        command:
          ["mysqld", "--default-authentication-plugin=mysql_native_password"]
        environment:
          MYSQL_ROOT_PASSWORD: your_root_password
          MYSQL_DATABASE: your_database
          MYSQL_USER: your_user
          MYSQL_PASSWORD: your_password
        # ... rest of your MySQL service config ...
    ```

2.  **Ensure a clean start for your MySQL server after applying this change.**
    If you had run the MySQL server previously without this command, it's crucial to:
    - Stop your Docker Compose stack (e.g., `docker-compose down`).
    - Remove the MySQL data volume (e.g., `docker volume rm <your_mysql_volume_name>` or delete the contents of the host-mounted data directory). This ensures the user accounts are created by the server while `mysql_native_password` is the active default.
    - Restart your stack (e.g., `docker-compose up --build -d`).

This server-side configuration allows the MariaDB client used by `label-backup` to connect successfully.

## Building from Source

1.  Clone the repository.
2.  Ensure you have Go (1.22+) installed.
3.  Run `go build -o label-backup .` (builds in the current directory if `main.go` is there).

To build the Docker image locally:
`docker build -t label-backup-agent:custom .`

## Contributing

Contributions are welcome! Please feel free to open an issue or submit a pull request.

## License

This project is open-source and available under the MIT License.
