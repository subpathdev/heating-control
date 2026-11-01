# heating-control

A small web page to adjust the heating per room from a phone on the home network: change a room's target temperature, or mark a window as open so the room doesn't heat. It controls Sonoff TRVZB thermostats through Zigbee2MQTT.

Work in progress.

## Deployment

The Helm chart in [`charts/heating-control`](charts/heating-control) is published with every release as `oci://ghcr.io/subpathdev/charts/heating-control`. Set `config.broker` and `config.rooms`; adding a room is one more entry in `config.rooms`. Run one replica only, because the MQTT client ID is fixed.

## API

[`internal/api/openapi.yaml`](internal/api/openapi.yaml) (OpenAPI 3.0) is the contract between the backend and the page, and a running server also serves it at `/api/openapi.yaml`. The Go types and server interface are generated from it, so change the file first, then run `go generate ./...`. CI fails if the generated code is out of date. `/healthz`, `/readyz` and `/metrics` are for operations and aren't part of the contract.

## Releases

Merging to `main` releases automatically, based on [conventional commits](https://www.conventionalcommits.org/): `feat:` is a minor bump and `fix:` a patch, while `docs:`, `ci:`, `chore:` and the like don't release. While the version is 0.x, a breaking change is a minor bump too. A release publishes `ghcr.io/subpathdev/heating-control:<version>`, the Helm chart of the same version and a GitHub release. To deploy it, merge the chart bump in the repo that runs it.
