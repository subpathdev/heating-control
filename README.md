# heating-control

A small web page to adjust the heating per room from a phone on the home network: change a room's target temperature, or mark a window as open so the room doesn't heat. It controls Sonoff TRVZB thermostats through Zigbee2MQTT.

Work in progress.

## Releases

Merging to `main` releases automatically, based on [conventional commits](https://www.conventionalcommits.org/): `feat:` is a minor bump and `fix:` a patch, while `docs:`, `ci:`, `chore:` and the like don't release. While the version is 0.x, a breaking change is a minor bump too. A release publishes `ghcr.io/subpathdev/heating-control:<version>` and a GitHub release. To deploy it, merge the image bump in the repo that runs it.
