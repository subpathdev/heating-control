# heating-control

A small web page to adjust the heating per room from a phone on the home network: change a room's target temperature, or mark a window as open so the room doesn't heat. It controls Sonoff TRVZB thermostats through Zigbee2MQTT.

## Airing

"Airing 15/30/60" sends `system_mode: off`. When the time is up, or on "Heat again", the room gets back the mode and target from before. The end time is saved, so it survives a restart. If the service is down when the time is up, heating comes back when it starts again. Until then the thermostat's own frost protection still applies.

- A target changed during airing is applied when it ends.
- A thermostat leaving off during airing gets off once more. If it leaves off again, someone is at the radiator, so airing ends.
- Commands repeat until the thermostat confirms them, every 30 s for about 5 minutes. A target then gives up and shows the thermostat's value. Off and heat-again keep trying every 5 minutes and show a problem on the page, because giving up would mean heating with the window open or a cold room.
- A thermostat that turned itself off is left alone, because only rooms this program turned off get restored. The page offers "Heat again" for it.

## Deployment

The Helm chart in [`charts/heating-control`](charts/heating-control) is published with every release as `oci://ghcr.io/subpathdev/charts/heating-control`. Set `config.broker` and `config.rooms`; adding a room is one more entry in `config.rooms`. Run one replica only, and give any other instance its own `config.clientId`: a second client with the same ID disconnects the first. The chart keeps saved state on a small PVC.

## API

[`internal/api/openapi.yaml`](internal/api/openapi.yaml) (OpenAPI 3.0) is the contract between the backend and the page, and a running server also serves it at `/api/openapi.yaml`. The Go types and server interface are generated from it, so change the file first, then run `go generate ./...`. CI fails if the generated code is out of date. `/healthz`, `/readyz` and `/metrics` are for operations and aren't part of the contract.

## Releases

Merging to `main` releases automatically, based on [conventional commits](https://www.conventionalcommits.org/): `feat:` is a minor bump and `fix:` a patch, while `docs:`, `ci:`, `chore:` and the like don't release. While the version is 0.x, a breaking change is a minor bump too. A release publishes `ghcr.io/subpathdev/heating-control:<version>`, the Helm chart of the same version and a GitHub release. To deploy it, merge the chart bump in the repo that runs it.
