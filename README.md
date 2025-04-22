# Elasticlone

Elasticlone is a simple command-line utility written in Go to clone an Elasticsearch index from one cluster (source) to another (target). It copies the index mapping, settings (excluding non-transferable ones), and all documents.

## Features

* Clones index mappings.
* Clones compatible index settings.
* Copies all documents using efficient Scroll API.
* Supports basic authentication for source and target clusters.
* Uses concurrent workers for faster data indexing on the target.
* Supports wildcard index patterns (e.g., `my-index-*`).
* Allows disabling SSL/TLS verification (use with caution).

## Installation

### Prerequisites

* Go (version 1.18 or later recommended) installed on your system.
* Access to both source and target Elasticsearch clusters.

### Build from Source

1.  Clone the repository:
    ```bash
    git clone [https://github.com/MedUnes/elasticlone.git](https://github.com/MedUnes/elasticlone.git)
    cd elasticlone
    ```
2.  Build the executable:
    ```bash
    go build -o elasticlone main.go
    ```
    This will create an executable file named `elasticlone` (or `elasticlone.exe` on Windows) in the current directory.

### Using `go install` (Recommended)

```bash
go install [github.com/MedUnes/elasticlone@latest](https://github.com/MedUnes/elasticlone@latest)
```
This will download, build, and install the binary into your `$GOPATH/bin` or `$HOME/go/bin` directory. Ensure this directory is in your system's `PATH`.

## Configuration

`elasticlone` is configured via environment variables, typically loaded from a `.env` file located in the same directory where you run the tool.

1.  **Copy the Example:**
    Copy the example configuration file `.env.dist` to `.env`:
    ```bash
    cp .env.dist .env
    ```
2.  **Edit `.env`:**
    Open the `.env` file with a text editor and fill in the details for your source and target Elasticsearch clusters, and the index you want to clone.

    ```dotenv
    # .env file content

    # --- Source Elasticsearch Cluster ---
    SOURCE_HOST=YOUR_SOURCE_HOST_IP_OR_DNS
    SOURCE_PORT=9200
    SOURCE_USER= # Optional: elastic
    SOURCE_PASS= # Optional: changeme

    # --- Target Elasticsearch Cluster ---
    TARGET_HOST=YOUR_TARGET_HOST_IP_OR_DNS
    TARGET_PORT=9200
    TARGET_USER= # Optional: elastic
    TARGET_PASS= # Optional: changeme

    # --- Cloning Parameters ---
    INDEX_NAME=your-index-name-or-pattern # e.g., my-app-logs-*, specific-index
    BATCH_SIZE=1000 # Optional: documents per scroll request
    WORKERS=4       # Optional: concurrent indexing workers
    SSL_VERIFY=true # Optional: set to "false" to disable certificate checks

    # --- Advanced ---
    # SCROLL_DURATION=1m # Optional: scroll context duration
    # MAX_RETRIES=3      # Optional: max retries for ES operations
    ```

### Configuration Keys

* `SOURCE_HOST` (Required): Hostname or IP address of the source Elasticsearch cluster.
* `SOURCE_PORT` (Required): Port number of the source Elasticsearch cluster.
* `SOURCE_USER` (Optional): Username for source cluster basic authentication. Leave blank if no authentication is needed.
* `SOURCE_PASS` (Optional): Password for source cluster basic authentication.
* `TARGET_HOST` (Required): Hostname or IP address of the target Elasticsearch cluster.
* `TARGET_PORT` (Required): Port number of the target Elasticsearch cluster.
* `TARGET_USER` (Optional): Username for target cluster basic authentication.
* `TARGET_PASS` (Optional): Password for target cluster basic authentication.
* `INDEX_NAME` (Required): The name or wildcard pattern of the index/indices to clone (e.g., `my-index`, `logstash-*`).
* `BATCH_SIZE` (Optional): Number of documents to fetch per scroll request (default: `1000`).
* `WORKERS` (Optional): Number of concurrent workers for indexing data into the target cluster (default: `4`).
* `SSL_VERIFY` (Optional): Set to `false` to disable SSL/TLS certificate verification for both connections (default: `true`). **Warning:** Setting this to `false` is insecure and should only be used in trusted environments or for testing.
* `SCROLL_DURATION` (Optional): How long the scroll context should be kept alive on the source cluster (default: `1m`). Format examples: `1m`, `5m`, `1h`.
* `MAX_RETRIES` (Optional): Maximum number of retries for Elasticsearch operations that fail with retryable status codes (e.g., 502, 503, 504, 429) (default: `3`).

**Note:** You can also set these values directly as environment variables in your shell, which will override the values in the `.env` file if `godotenv` loading fails or is bypassed.

## Usage

1.  Ensure your `.env` file is correctly configured in the directory where you plan to run the command.
2.  Navigate to the directory containing the `elasticlone` executable (or ensure it's in your PATH).
3.  Run the executable:
    ```bash
    ./elasticlone
    ```
    Or, if installed via `go install`:
    ```bash
    go run main.go
    ```

The tool will output logs to the console indicating the connection status, indices being processed, progress, and any errors encountered.

## How it Works

1.  Loads configuration from the `.env` file or environment variables.
2.  Connects to both source and target Elasticsearch clusters.
3.  Resolves the `INDEX_NAME` pattern to a list of specific indices on the source cluster.
4.  For each source index found:
    a. Retrieves the index mappings and settings from the source.
    b. Cleans non-transferable settings (like `uuid`, `creation_date`).
    c. Creates a new index on the target cluster with the same name, using the retrieved mappings and cleaned settings. (Skips creation if the target index already exists).
    d. Uses the Elasticsearch Scroll API to efficiently retrieve all documents from the source index in batches.
    e. Uses the Elasticsearch Bulk API with concurrent workers to index these documents into the corresponding target index.
    f. Refreshes the target index upon completion.

## Contributing

Contributions are welcome! Please feel free to submit pull requests or open issues on GitHub.

## License

This project is licensed under the MIT License - see the LICENSE file for details (You should add a LICENSE file if you haven't already).
```