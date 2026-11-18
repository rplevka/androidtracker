# Kidstracker

A small, self-hosted location tracker for a dedicated Android phone. The initial implementation is intentionally a **modular monolith**: Go HTTP API + PostgreSQL/PostGIS + admin page. Location writes go directly to PostgreSQL; Kafka would add more operational cost than value at this scale. A database-backed worker/outbox or NATS can be added if real asynchronous work appears.

> This is a prototype, not an emergency-service or guaranteed real-time safety system. Android Doze, radio coverage, GPS conditions, battery state, and ROM behavior can delay reports. Test it with the actual phone and network before relying on it.

Location acquisition prefers GPS and falls back to Android's network provider (cell/Wi-Fi location), or the Play Services fused provider where one is available, when GPS is disabled or does not produce a fix within the GPS timeout. Every fix records its `location_source` (`gps`, `network`, or `fused`), which the admin dashboard and Home Assistant expose.

## Components

- `cmd/server`, `internal/api`: Go API, one-time device enrollment, bearer-token authentication, config polling, idempotent batch uploads, admin API, and enrollment QR generation.
- `internal/store/migrations`: PostgreSQL schema with PostGIS `geography(Point,4326)` and spatial index.
- `web/index.html`: admin device list, current-position map, history display and configuration controls.
- `android/`: native Android client targeting API 28 / Android 9.
- `scripts/android-debloat.sh`: opt-in, reversible ADB package disabling. It does not run automatically and does not uninstall packages.

## Local run

Requirements: Go 1.26.11+, Docker or Podman Compose, a random admin token (at least 32 characters), and a local database password.

```sh
cd kidstracker
export ADMIN_TOKEN="$(openssl rand -hex 32)"
export POSTGRES_PASSWORD="$(openssl rand -hex 24)"
docker compose up --build
```

Open <http://127.0.0.1:8080>. The admin page uses Leaflet and OSM's canonical public tile endpoint. Browser requests send the site origin as `Referer`, as required by OSM's tile policy; tile requests reveal the viewed map area to OSM. For stronger location privacy or guaranteed availability, use a separately approved or self-hosted provider. Device traffic must use HTTPS in deployment. The development compose file binds the API/database to loopback only.

For a trusted LAN-only HTTPS setup, use the server's LAN IP and generate a local CA plus an IP-SAN certificate. The examples use `192.0.2.10` as a documentation IP; replace it with your LAN address.

```sh
export TRACKER_DOMAIN=192.0.2.10
export TRACKER_BIND_IP="$TRACKER_DOMAIN"
export ADB_SERIAL=192.0.2.10:5555
TRACKER_DOMAIN="$TRACKER_DOMAIN" ./scripts/generate-local-certs.sh
TRACKER_DOMAIN="$TRACKER_DOMAIN" TRACKER_BIND_IP="$TRACKER_BIND_IP" \
ADMIN_TOKEN="$ADMIN_TOKEN" POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  podman compose --profile https up -d --build
adb -s "$ADB_SERIAL" push traefik/certs/local-ca-cert.pem /sdcard/Download/kidstracker-local-ca.crt
```

Then install the CA on the phone: **Settings → Security → Encryption & credentials → Install a certificate → CA certificate**, select the downloaded file, and confirm Android's warning. Android may require a screen lock. The debug tracker build explicitly trusts user-installed CAs; release builds trust only system CAs. On the admin computer, open `https://192.0.2.10:8443`; you may also need to trust the local CA in that browser. The dashboard's QR contains only the single-use enrollment code; enter `https://192.0.2.10:8443` in the app if its server field is empty.

The `https` Compose profile adds Traefik v3, the local certificate, HTTP-to-HTTPS redirect middleware, HSTS and browser security headers. Rootless Podman cannot bind privileged ports on this host, so use `https://192.0.2.10:8443`; HTTP on host port 8081 redirects to it. Traefik uses a file provider and does not mount the Docker/Podman socket. API and PostGIS are not publicly exposed; their host-published ports bind to loopback. The normal Compose profile remains suitable for local HTTP development and does not start Traefik or display a scannable QR. Keep `traefik/certs/local-ca-key.pem` private and out of Git. This local CA is for a trusted LAN only, not a public production deployment.

## Container image

Pushes to `main` or `master` publish the API/frontend image to GitHub Container Registry:

```text
ghcr.io/rplevka/androidtracker-api:latest
ghcr.io/rplevka/androidtracker-api:main
ghcr.io/rplevka/androidtracker-api:sha-<commit>
```

The package is public, so these can be pulled anonymously:

```sh
docker pull ghcr.io/rplevka/androidtracker-api:latest
docker compose pull api
```

The Compose file references this image and still allows `docker compose up --build` for local development. Set `KIDSTRACKER_API_IMAGE` to use a different tag or registry.

### Portainer

`portainer-stack.yml` is a standalone Portainer stack definition. It uses the published image, an external `traefik` network, and the existing `*.lan` default certificate. Set `POSTGRES_PASSWORD` and `ADMIN_TOKEN` as stack environment variables before deploying. The resulting UI is `https://tracker.lan`; HTTP is redirected to HTTPS. `tracker.lan` must resolve to the Portainer host, and the Traefik network must already exist.

To run only the database and API locally:

```sh
docker compose up -d db
export DATABASE_URL="postgres://tracker:$POSTGRES_PASSWORD@localhost:5432/kidstracker?sslmode=disable"
go run ./cmd/server
```

## Enrollment and API

1. In the admin page, create a device. Over HTTPS the page displays a QR containing only the one-time enrollment code; manual code entry remains available.
2. In the Android app, choose **Scan enrollment QR** (camera permission is requested only then), or enter the code manually. Scanning fills the code field; enter the HTTPS server URL if needed, review both fields, and tap **Activate tracker**.
3. Enrollment returns a high-entropy device bearer token; the server stores only its SHA-256 digest. Enrollment codes expire after 24 hours and are single-use. Admins can revoke a device from the UI, which immediately invalidates its token.
4. The app collects fixes on its location interval; on the sync interval it fetches `/v1/config`, acknowledges the applied version at `/v1/config/ack`, and uploads the batch, using `Authorization: Bearer <device-token>`.

Admin API requests use `Authorization: Bearer $ADMIN_TOKEN`:

```text
POST  /api/admin/devices
POST  /api/admin/enrollment-qr
GET   /api/admin/devices
PATCH /api/admin/devices/{id}/config
GET   /api/admin/devices/{id}/locations?from=<RFC3339>&to=<RFC3339>&limit=1000
GET   /api/admin/devices/{id}/telemetry?limit=200
GET   /api/admin/mqtt
PUT   /api/admin/mqtt
POST  /api/admin/devices/{id}/revoke
DELETE /api/admin/devices/{id}
```

Configuration bounds are `location_interval_seconds` 5–86400 seconds, `sync_interval_seconds` 5–86400 seconds, `sleep_duration_seconds` 10–604800 seconds, and GPS timeout 5–180 seconds. `PATCH` accepts a partial configuration object; omitted fields keep their current values. The short 5/10-second minima are supported for debugging but can consume substantial battery. Android Doze and alarm quotas can still defer background wakes. Location interval controls location fixes (GPS first, with network fallback) while enabled; sync interval controls batch upload and config polling; sleep duration controls config polling while tracking is disabled. Each client location ID is unique per device, so retried batches do not create duplicate history points.

Deleting a device is permanent: it removes the device and cascades to all of its stored location history. The Android client will detect the invalid token at its next sync and show enrollment again.

The history control offers a days-back slider plus exact local start/end date-times; these are sent as RFC3339 bounds so a multi-day history can be narrowed to a particular trip.

## Telemetry

Each sync acknowledges config with a telemetry sample, avoiding an extra request. Samples record screen on/off, lock state, Doze/idle state, battery-saver state, app standby bucket, network type, GPS-provider state, battery percentage/temperature, wake delay from the scheduled alarm, sync duration, queue size and applied config version. Each location also records screen/Doze/power-save/network context, the fix source (`gps`, `network`, or `fused`), and fix acquisition duration. The dashboard shows the source of the latest fix and includes it in the telemetry view.

Open **Telemetry** on a device in the dashboard to inspect recent samples, or query:

```text
GET /api/admin/devices/{id}/telemetry?limit=200
```

This is sensitive behavioral metadata. Define a retention policy and restrict admin access; do not retain it longer than needed for battery/config tuning.

Telemetry volume depends on the sync interval, not the location interval:

| Sync interval | Telemetry samples/year | Approx. raw telemetry JSON/year |
| ---: | ---: | ---: |
| 5 seconds | 6,307,200 | ~2 GB |
| 15 minutes | 35,040 | ~11 MB |
| 1 hour | 8,760 | ~3 MB |

At a 5-second sync interval, telemetry adds substantial database growth and request overhead on top of the location-data estimates above.

## Home Assistant MQTT

Configure the broker under **MQTT / Home Assistant** in the admin page. The backend supports any MQTT 3.1.1/5-compatible broker; point it at Home Assistant's Mosquitto broker (typically `homeassistant.local:1883`). Discovery is optional, but enabled by default with prefix `homeassistant`. The MQTT password is encrypted at rest using a key derived from `ADMIN_TOKEN`; changes to `ADMIN_TOKEN` require re-entering the MQTT password. The API has a separate non-internal egress network for reaching a LAN MQTT broker while the database and proxy networks remain isolated.

The backend publishes a retained Home Assistant device tracker (`source_type: gps`) plus sensors/binary sensors for battery, charging, location accuracy, speed, altitude, bearing, last fix, location source, screen, lock, Doze, power save, network, standby bucket, fix duration, battery temperature, wake delay, sync duration, queue size, GPS availability, tracking enabled and configured intervals. The tracker's JSON attributes include `location_source` (`gps`, `network`, or `fused`), and a dedicated **Location source** sensor exposes the same value. `fused` means the fix came from Play Services' fused provider. Home Assistant uses the tracker's GPS attributes and its zones to derive zone state.

The integration also publishes writable Home Assistant entities: a **Tracking control** switch and `(set)` number entities for location interval, sync interval, disabled poll interval, GPS timeout, and minimum distance. They publish partial config JSON to `<base_topic>/<device_id>/config/set`, for example:

```json
{"sync_interval_seconds": 300}
```

The backend applies the same bounds as the admin API, increments the config version, and republishes the retained config state. The device picks the change up on its next config fetch, so a change can take up to the currently configured sync interval (or the sleep interval while tracking is disabled). The existing read-only config sensors remain available for visibility. Anyone who can publish to the MQTT broker can change these settings, so secure the broker and use per-client ACLs.

There is no `gps` sensor device class: use a `device_tracker` with `source_type: gps`, which this integration does. To associate it with a person, create/edit the person in Home Assistant (**Settings → People**) and select the discovered `device_tracker` entity; MQTT discovery does not assign people automatically.

Native Home Assistant zones are circles with a radius and latitude/longitude. Associate the tracker with a person (or use the tracker directly) and use **zone entered/left** triggers in automations; the device tracker also exposes `in_zones`. For polygon/arbitrary drawn areas, Home Assistant does not provide native polygon zones; use a custom integration such as Polygonal Zones or Poly-Zone, or a template point-in-polygon helper. The server does not evaluate geofences itself. Automations can read `state_attr('device_tracker.<name>', 'location_source')` to distinguish a GPS fix from a cell/Wi-Fi fallback.

```yaml
automation:
  - alias: "Notify when the tracker enters school"
    triggers:
      - trigger: zone
        entity_id: device_tracker.backpack
        zone: zone.school
        event: enter
    actions:
      - action: notify.mobile_app
        data:
          message: "The tracker arrived at school"
```

The backend publishes discovery and state to `<base_topic>/<device_id>/...`; configure the discovery prefix and base topic from the UI. Deleting or revoking a device removes its retained discovery configs.

## Android 9 / battery notes

The client uses a short-lived foreground service for a location acquisition or batch sync, releases its wakelock, and schedules the next one-shot wake with `AlarmManager.setAndAllowWhileIdle`. It uses Android's `LocationManager` rather than requiring Google Play Services. It requests GPS, the network provider, and the Play Services fused provider where available; GPS wins when it succeeds, and the best network fix is used if GPS is disabled or times out. The network provider is Android's cell/Wi-Fi location source; some ROMs disable it at the platform level, in which case it will not produce fixes, and the app still records the GPS source. On a ROM without a Location mode screen, enable **Google Location Accuracy** under **Settings → Security & location → Location → Advanced**; this adds `network` to the provider list. Fine or coarse location permission is sufficient; coarse-only permits network/fused fixes but not GPS. Android can still defer wakeups; reliable, frequent background location is inherently more battery-intensive. Keep the enabled cadence conservative, test overnight and with mobile data, and check actual battery drain. The app queues a bounded number of fixes between syncs and while offline. It re-arms the alarm after reboot and after an APK update.

Battery level and charging state are included with location reports and shown in the admin list. On battery, the client doubles its wake interval at 30% or below, quadruples it at 15% or below, and skips location acquisition at 5% or below (retaining the offline queue). Plugged-in tracking returns to the configured cadence. These conservative thresholds are currently fixed in the client. The wakelock is time-bounded, location acquisition has a configurable GPS timeout, and no battery-optimization exemption is requested.

The activated app has a **Status** tab showing the fetched config, battery/charging state, queued fixes, last location/sync (including whether the last fix came from GPS or the network provider) and next wake. **Sync now** uploads cached fixes and fetches config immediately without requesting a new location fix; a successful config refresh adjusts the next scheduled wake. If the server returns 401/403 (for example, after device revocation), the app drops the token and shows enrollment again.

For measurement, compare 24-hour screen-off runs with tracking disabled, 1-hour disabled-mode polling, and the intended tracking cadence. Record battery percentage, GPS fix time, upload duration, and signal conditions. Use `adb shell dumpsys batterystats`, `adb shell dumpsys alarm`, and `adb shell dumpsys deviceidle`; tune cadence/timeouts from measured drain rather than removing Android power-management services.

## Location data volume and batching

The table estimates one uncompressed JSON location record at **about 275 bytes** and PostgreSQL/PostGIS storage at **about 400–600 bytes per stored point**, including indexes. These are planning estimates, not measurements; optional sensor fields, database bloat, HTTP headers and TLS overhead are excluded. The Android client gzip-compresses each location batch; the Go API accepts gzip and caps both compressed and expanded JSON request bodies. A sync uploads all fixes collected since its previous successful batch, so changing the sync interval changes network-session frequency, not the number of location records.

| Location interval | Sync interval | Fixes/year | Raw location JSON/year | Approx. DB growth/device/year |
| --- | ---: | ---: | ---: | ---: |
| 5 seconds (debug stress) | 5 seconds | 6,307,200 | ~1.73 GB | ~2.5–3.8 GB |
| 5 seconds (debug stress) | 15 minutes | 6,307,200 | ~1.73 GB | ~2.5–3.8 GB |
| 5 minutes | 15 minutes | 105,120 | ~29 MB | ~42–63 MB |
| 15 minutes | 1 hour | 35,040 | ~9.6 MB | ~14–21 MB |

At the 5s/5s debug setting there are about **6.3 million sync cycles and up to 18.9 million HTTPS requests per year** (config fetch, config acknowledgement and location upload). Their protocol and radio overhead can exceed the JSON body size. Batching 5-second fixes every 15 minutes reduces sync cycles to about 35,000/year, with roughly 180 points or 50 KB of raw JSON per batch, but does not reduce location acquisitions or database rows. The phone's offline queue is capped at 500 points, so at a 5-second location interval it holds only about 42 minutes before the oldest unsent points are dropped.

Gzip reduces repeated JSON keys most effectively in larger batches; a one-point batch may save little or even gain a few framing bytes. The precise ratio depends on the data and batch size, so benchmark with real traces before relying on a compression estimate.

## Tests and CI

```sh
go test -race -cover ./...
go vet ./...
bash scripts/test-android-debloat.sh
bash scripts/check-web.sh
cd android && gradle testDebugUnitTest assembleDebug
```

The Go integration test uses `DATABASE_URL` when set and skips otherwise; CI runs it against PostGIS. GitHub Actions checks Go formatting/vet/tests, scans Go vulnerabilities, enforces 60% statement coverage for the API package, runs Android unit tests/build (including QR payload validation), tests the ADB script and web JavaScript, validates the HTTPS Compose profile, and builds the container image.

## ADB package disabling

The target device is Android 9/API 28. Package names vary between AEX builds. Set `ADB_SERIAL` to the device’s ADB serial, then start with inventory and inspect the package list manually:

```sh
export ADB_SERIAL='<phone-adb-serial>'
adb -s "$ADB_SERIAL" shell pm list packages -3
./scripts/android-debloat.sh list
```

`apply` is a dry run by default. Review the package list, then pass `--confirm` to make changes. It only disables packages, logs exactly what it changed, and refuses critical/emergency/dialer package names. Third-party apps are the default scope. System packages require the additional `--allow-system` flag and confirmation. Restore changes with:

```sh
./scripts/android-debloat.sh restore
```

Do not disable telephony, location, package installer, System UI, Settings, alarms/jobs, keystore, or emergency-related components. ADB-over-Wi-Fi port 5555 should not be exposed outside a trusted network.

## Still to do before a real deployment

- Admin login beyond a shared bearer token, token rotation, audit log, retention/deletion controls, and backups.
- Publicly trusted TLS (for non-LAN deployment), reverse-proxy/rate-limit hardening and secret management.
- E2E hardware tests on the exact AEX build; geofence/alert behavior and Android crash/reboot testing.
- Privacy review, access controls, consent, and a clear retention policy for children's location data.
- **Deferred / lower priority:** kiosk/device-owner setup and anti-accidental-call controls. Prefer kiosk restrictions and blocking ordinary outgoing calls while preserving cellular data and emergency calling. Any lock-screen emergency-shortcut changes must be reviewed separately for this exact AEX build; do not disable telephony/emergency components as part of debloating.
