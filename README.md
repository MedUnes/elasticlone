# Elasticlone

Elasticlone is a simple command-line utility written in Go to clone an Elasticsearch index from one cluster (source) to another (target). It copies the index mapping, settings (excluding non-transferable ones), and documents. It supports cloning to the same index name or a different one, can use source connection details for the target if specific target details are omitted, and optionally filters documents based on a time range using the `@timestamp` field.

## Features

* Clones index mappings.
* Clones compatible index settings.
* Copies documents using efficient Scroll API.
* **Optionally filters documents based on a time range (`START_TIME`, `END_TIME`) if the `@timestamp` field exists in the source index.**
* Supports basic authentication for source and target clusters.
* Uses concurrent workers for faster data indexing on the target.
* Supports wildcard index patterns (e.g., `my-index-*`) for the source.
* Optionally allows specifying a different target index name (for single source index matches).
* Target connection details (host, port, user, pass) default to source details if not provided.
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
    Open the `.env` file with a text editor and fill in the details. Target connection details and time filters are optional.

    ```dotenv
    # .env file content

    # --- Source Elasticsearch Cluster ---
    SOURCE_HOST=YOUR_SOURCE_HOST_IP_OR_DNS   # Required
    SOURCE_PORT=9200                         # Required
    SOURCE_USER=                             # Optional: elastic
    SOURCE_PASS=                             # Optional: changeme

    # --- Target Elasticsearch Cluster (Optional - Defaults to Source if Blank) ---
    TARGET_HOST=                             # Optional: Defaults to SOURCE_HOST
    TARGET_PORT=                             # Optional: Defaults to SOURCE_PORT
    TARGET_USER=                             # Optional: Defaults to SOURCE_USER
    TARGET_PASS=                             # Optional: Defaults to SOURCE_PASS

    # --- Cloning Parameters ---
    INDEX_NAME=your-source-index-pattern     # Required: e.g., my-app-logs-*, specific-index
    TARGET_INDEX_NAME=                       # Optional: Target name. Defaults to source name(s). Only if INDEX_NAME matches exactly one source index.
    BATCH_SIZE=1000                          # Optional: documents per scroll request
    WORKERS=4                                # Optional: concurrent indexing workers

    # --- Time Filtering (Optional) ---
    # Filters documents based on @timestamp field, if it exists in the source index.
    # Use ISO 8601 format (e.g., 2025-04-22T00:00:00Z, 2025-01-01, 2025-04-22T10:30:00+02:00)
    # START_TIME: Clone documents with @timestamp >= START_TIME (inclusive)
    START_TIME=
    # END_TIME: Clone documents with @timestamp < END_TIME (exclusive)
    END_TIME=

    # --- Security ---
    SSL_VERIFY=true                          # Optional: set to "false" to disable certificate checks

    # --- Advanced ---
    # SCROLL_DURATION=1m                     # Optional: scroll context duration
    # MAX_RETRIES=3                          # Optional: max retries for ES operations
    ```

### Configuration Keys

* `SOURCE_HOST` (Required): Hostname or IP address of the source Elasticsearch cluster.
* `SOURCE_PORT` (Required): Port number of the source Elasticsearch cluster.
* `SOURCE_USER` (Optional): Username for source cluster basic authentication.
* `SOURCE_PASS` (Optional): Password for source cluster basic authentication.
* `TARGET_HOST` (Optional): Hostname or IP address of the target cluster. **Defaults to `SOURCE_HOST` if empty.**
* `TARGET_PORT` (Optional): Port number of the target cluster. **Defaults to `SOURCE_PORT` if empty.**
* `TARGET_USER` (Optional): Username for target cluster basic authentication. **Defaults to `SOURCE_USER` if empty.**
* `TARGET_PASS` (Optional): Password for target cluster basic authentication. **Defaults to `SOURCE_PASS` if empty.**
* `INDEX_NAME` (Required): The name or wildcard pattern of the index/indices on the **source** cluster (e.g., `my-index`, `logstash-*`).
* `TARGET_INDEX_NAME` (Optional): The specific name for the index on the **target** cluster. **Defaults to the source index name.** **Important:** This setting can *only* be used if the `INDEX_NAME` pattern matches *exactly one* source index.
* `BATCH_SIZE` (Optional): Number of documents to fetch per scroll request (default: `1000`).
* `WORKERS` (Optional): Number of concurrent workers for indexing data into the target cluster (default: `4`).
* `START_TIME` (Optional): If set, and the source index contains an `@timestamp` field, only documents with `@timestamp` greater than or equal to this time will be cloned. Accepts various formats like `YYYY-MM-DDTHH:mm:ssZ`, `YYYY-MM-DDTHH:mm:ss+HH:MM`, `YYYY-MM-DD`.
* `END_TIME` (Optional): If set, and the source index contains an `@timestamp` field, only documents with `@timestamp` strictly less than this time will be cloned. Accepts the same formats as `START_TIME`.
* `SSL_VERIFY` (Optional): Set to `false` to disable SSL/TLS certificate verification for both connections (default: `true`). **Warning:** Setting this to `false` is insecure.
* `SCROLL_DURATION` (Optional): How long the scroll context should be kept alive on the source cluster (default: `1m`).
* `MAX_RETRIES` (Optional): Maximum number of retries for failed Elasticsearch operations (default: `3`).

**Note:** Values can also be set directly as environment variables, overriding the `.env` file.

## Usage

1.  Ensure your `.env` file is correctly configured.
2.  Run the executable from the same directory (or ensure it's in your PATH):
    ```bash
    ./elasticlone
    ```
    Or, if installed via `go install`:
    ```bash
    elasticlone
    ```

The tool will log its progress, including connection details, indices being processed, target names used, and whether a time filter is being applied.

## How it Works

1.  Loads configuration from the `.env` file and environment variables.
2.  Parses configuration values, including optional start/end times.
3.  Applies defaulting logic for target connection details if they are missing.
4.  Connects to both source and target Elasticsearch clusters.
5.  Resolves the `INDEX_NAME` pattern to a list of specific indices on the source cluster.
6.  Checks if `TARGET_INDEX_NAME` is set and validates compatibility.
7.  For each source index found:
    a. Determines the correct `targetIndexName`.
    b. Retrieves the index mappings and settings, **and checks if an `@timestamp` field exists in the mapping.**
    c. Cleans non-transferable settings.
    d. Creates the `targetIndexName` index on the target cluster (skips if exists).
    e. **Constructs the appropriate Scroll API query: `match_all` or a `bool` query with a `range` filter on `@timestamp` if `START_TIME`/`END_TIME` are set and the field exists.**
    f. Uses the Scroll API with the constructed query to retrieve matching documents.
    g. Uses the Bulk API with concurrent workers to index documents into the `targetIndexName` index.
    h. Refreshes the target index.

## Contributing

Contributions are welcome! Please feel free to submit pull requests or open issues on GitHub.

## License

This project is licensed under the MIT License - see the LICENSE file for details (You should add a LICENSE file if you haven't already).
```