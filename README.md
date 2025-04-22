# Elasticlone

Elasticsearch data replication tool with automatic proxy server.

## Features
- Copies indices, mappings, and data between Elasticsearch clusters
- Configurable via configuration file
- Local proxy server for data redirection
- SSL support with certificate verification toggle

## Usage

1. Create a configuration file named `auth.conf`, or copy it from the dist one: [auth.conf.dist](./auth.conf.dist):
```ini
# Required Configuration
SOURCE_URL=your_source_elasticsearch_url
SOURCE_USER=source_username
SOURCE_PASS=source_password
DEST_URL=your_destination_elasticsearch_url
DEST_USER=destination_username
DEST_PASS=destination_password

# Optional Configuration (defaults shown)
LOCAL_PORT=9200
COPY_MAPPINGS=true
COPY_DATA=true
COPY_TASKS=false
COPY_PIPELINES=false
INSECURE=false
DEBUG=false
```

2. Run the tool:
```bash
./elasticlone
```

## Configuration Options
| Key              | Description                                  | Default |
|------------------|----------------------------------------------|---------|
| `SOURCE_URL`     | Source Elasticsearch URL                     | -       |
| `SOURCE_USER`    | Source cluster username                      | -       |
| `SOURCE_PASS`    | Source cluster password                      | -       |
| `DEST_URL`      | Destination Elasticsearch URL               | -       |
| `DEST_USER`     | Destination cluster username                | -       |
| `DEST_PASS`     | Destination cluster password                | -       |
| `LOCAL_PORT`    | Local proxy port                            | 9200    |
| `COPY_MAPPINGS` | Copy index mappings                         | true    |
| `COPY_DATA`     | Copy index data                             | true    |
| `COPY_TASKS`    | Copy tasks                                  | false   |
| `COPY_PIPELINES`| Copy ingest pipelines                       | false   |
| `INSECURE`      | Disable SSL certificate verification       | false   |
| `DEBUG`         | Enable debug logging                       | false   |

## Building
```bash
go build -o elasticlone main.go
```

## Requirements
- Go 1.20+
- Elasticsearch 7.x+
```
