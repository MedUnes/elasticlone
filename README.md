# elasticlone
A tiny CLI tool to clone elastic search  indexes

[![Build](https://github.com/MedUnes/elasticlone/actions/workflows/test.yml/badge.svg)](https://github.com/MedUnes/elasticlone/actions/workflows/test.yml) [![Release](https://github.com/MedUnes/elasticlone/actions/workflows/release.yml/badge.svg)](https://github.com/MedUnes/elasticlone/actions/workflows/release.yml)

## Build
```bahs
go build
```
## Usage
```bash
Usage of ./elasticlone:
  -F int
        Start copying from this document number (default 1)
  -H string
        Source host
  -I string
        Source index name
  -P string
        Source password
  -R string
        Source port
  -S    Use SSL/HTTPS for source
  -T int
        Stop copying at this document number (0 for no limit)
  -U string
        Source username
  -h string
        Target host
  -i string
        Target index name
  --insecure
        Skip SSL certificate verification for source
  -p string
        Target password
  -r string
        Target port
  -s    Use SSL/HTTPS for target
  -u string
        Target username
```
