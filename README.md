# SimpleMiningProxy

A Stratum V1 proxy for SHA-256 ASIC miners. Miners connect to the proxy over TCP or TLS. For each miner the proxy opens its own connection to the active pool, rewrites the worker login to the pool account, and moves the farm to another pool when you switch it or when the pool goes down. It is one Go binary with an embedded web admin UI and runs in a single Docker container.

It was written for BTC and BCH pools. Pools for XEC, DigiByte (SHA-256) and Fractal Bitcoin that speak standard Stratum V1 should work too: the proxy does not look inside jobs or blocks, it only relays Stratum messages.

## Features

- Stratum TCP and TLS listeners at the same time (`stratum+tls`, also written `stratum+ssl`).
- TCP or TLS to each pool, independent of how the miner connected.
- One pool connection per miner session.
- `mining.authorize` and the worker field of `mining.submit` are rewritten to the selected pool's account. All other submit fields, including version-rolling (ASICBoost) bits, pass through unchanged.
- An active pool and an ordered list of fallback pools, with health checks, failover and delayed failback.
- Several addresses per pool; new sessions go to the fastest working one.
- Pool switches reconnect miners gradually over a configurable window.
- Accepted and rejected shares, reject reasons and a 10-minute hashrate estimate, per pool and per miner.
- Charts of hashrate, shares and miners from one hour to one year, stored on disk, including a history for each miner.
- Optional profit switching between SHA-256 coins (BTC, BCH, BSV, XEC, DGB, FB) using WhatToMine and WhatsOnChain data: advice only, or automatic.
- Optional timed switching: part of every period on another pool, for example 10 minutes of every 30 on a solo pool. On eCash it waits out the minutes after a block, when the real-time target makes a solo block practically impossible.
- Network difficulty and the farm's solo odds for every coin, with eCash's Real Time Targeting taken into account.
- Runtime settings with descriptions and validation, applied without a restart.
- A self-signed TLS certificate on first start, or your own certificate, reloaded when it changes.
- Connection limits and timeouts against slow or broken clients.
- Admin UI in English and Russian that works on a phone. Everything it does is also available through a REST API.

## Quick start

You need Docker Engine with Compose v2.

```bash
git clone https://github.com/famfamfam/simple-mining-proxy.git
cd simple-mining-proxy
cp .env.example .env
```

Set at least these in `.env`:

- `ADMIN_PASSWORD`: a long, unique password;
- `PUBLIC_HOST`: the IP address or DNS name your miners will connect to;
- `TLS_SELF_SIGNED_HOSTS`: the DNS names and IP addresses miners use for the TLS port.

Start it:

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f proxy
```

Ports published by `docker-compose.yml`:

| Purpose | Default host address |
|---|---|
| Stratum TCP | `0.0.0.0:13333` |
| Stratum TLS | `0.0.0.0:14443` |
| Admin UI and API | `127.0.0.1:18080` |

Pools, settings, certificates and history are stored in the Docker volume `<project>_proxy-data` (`simpleminingproxy_proxy-data` with the project name from `.env.example`). Container logs are capped at five files of 10 MiB.

## Admin UI

The admin port listens on localhost only. Reach it through an SSH tunnel:

```bash
ssh -L 18080:127.0.0.1:18080 user@your-server
```

Open http://127.0.0.1:18080 and log in with `ADMIN_USERNAME` and `ADMIN_PASSWORD`. Sessions are kept in memory for 12 hours and end when the process restarts.

To reach the admin UI from the internet, change its port binding in `docker-compose.yml`, for example to `"18080:18080/tcp"`. The UI is plain HTTP, so the password and the session cookie travel unencrypted: put it behind an HTTPS reverse proxy, or keep using the tunnel. Failed logins are limited (password and API token failures count together):

| Rule | Value |
|---|---|
| Lock an IP address | after 5 failures within 15 minutes |
| IP lock duration | 15 min, then 30 min, 1 h and so on up to 24 h; a day without failures starts over |
| Lock password logins for everyone | after 100 failures from all addresses within an hour; existing sessions keep working |
| Successful login | clears the counter of that address |

The counters are saved in `/data/auth-guard.json` and survive restarts. Locks show up under Events. If you locked yourself out, wait, or delete the file:

```bash
docker compose stop proxy
docker run --rm -v simpleminingproxy_proxy-data:/d alpine rm -f /d/auth-guard.json
docker compose start proxy
```

## First setup

1. On the Dashboard, add the main pool: name, coin, addresses, login template and password. Pool names must be unique; for two pools of the same service add the coin, for example "EMCD BTC" and "EMCD BCH".

   Addresses takes one `host:port` per line, for servers of the same pool in different regions. The proxy measures the connect time to each (the median of the last probes), sends new sessions to the fastest working address and falls back to the next one if it fails. The fastest address is marked with ★.
2. Choose TLS if the pool requires it. Pasting an address with a scheme (`stratum+tcp://`, `stratum+tls://` or `stratum+ssl://`) selects the transport. Certificate verification is on by default; turn it off only for a known pool with a self-signed certificate.
3. Test checks the form before you save it: it connects, sends `mining.subscribe` and `mining.authorize` with the test worker, on every address. Then save the pool and make it active.
4. Add fallback pools and set their order.
5. Review the test worker, timeouts and limits in Settings.
6. Copy the TCP or TLS address from the Dashboard into your miners:

```text
stratum+tcp://proxy.example.com:13333
stratum+tls://proxy.example.com:14443
```

`stratum+tls` and `stratum+ssl` are the same thing under two names. Whatsminer accepts only `stratum+tls`; some other firmware accepts only `stratum+ssl`.

## Worker names

A pool's login template may contain:

- `{worker}`: the part of the miner's login after the first dot;
- `{login}`: the miner's whole login;
- `{password}`: the password the miner sent.

With the template `account.{worker}`, a miner logged in as `farm.S21-0042` works on the pool as `account.S21-0042`.

Every valid `mining.authorize` that passes through the proxy is rewritten to the selected pool's login and password, and so is `params[0]` of every `mining.submit`. Firmware dev-fee traffic is not treated specially: a separate connection the firmware opens to its own pool does not pass through the proxy, and an authorize sent through the proxy is rewritten like any other.

## Firmware notes

| Firmware | Notes |
|---|---|
| Stock Antminer | Use the TCP port: stock firmware on many models cannot use TLS to a custom Stratum server. |
| Stock Whatsminer | TLS works; an M50 accepts the self-signed certificate. Other models and firmware versions may not; then use a trusted certificate, or TCP on a network you trust. |
| VNish | Check TLS on your version. Dev-fee connections the firmware opens itself bypass the proxy. |
| WMOC | Check TCP and TLS on your version. |

## TLS certificate

If neither `/data/certs/fullchain.pem` nor `/data/certs/privkey.pem` exists, the first start creates a self-signed certificate, valid for 10 years, for the names in `TLS_SELF_SIGNED_HOSTS`. It is reused after restarts; changing the variable does not reissue an existing certificate.

To use a trusted certificate, mount both files into the container, readable by UID 65532, and set `TLS_CERT_FILE` and `TLS_KEY_FILE` in `docker-compose.yml`. A changed pair is picked up without a restart (the files are checked at most once a minute).

Miners must use TLS 1.2 or newer by default. Lower `tls_min_version` only for old firmware.

## Environment variables

These configure the process and take effect after a container restart:

| Variable | Default | Purpose |
|---|---|---|
| `STRATUM_TCP_ADDR` | `:13333` | Stratum TCP listen address; `off` disables it. |
| `STRATUM_TLS_ADDR` | `:14443` | Stratum TLS listen address; `off` disables it. |
| `ADMIN_ADDR` | `:18080` | Admin UI and API listen address. |
| `ADMIN_USERNAME` | `admin` | Admin user name. |
| `ADMIN_PASSWORD` | required | Admin password. |
| `API_TOKEN` | empty | Bearer token for API clients; empty disables token login. |
| `DATA_DIR` | `/data` | Directory for `state.json`, history and certificates. |
| `TLS_CERT_FILE` | `/data/certs/fullchain.pem` | Certificate of the TLS listener. |
| `TLS_KEY_FILE` | `/data/certs/privkey.pem` | Private key of the TLS listener. |
| `TLS_SELF_SIGNED_HOSTS` | container host name | Comma-separated names for the self-signed certificate. |
| `PUBLIC_HOST` | empty | Host shown in the connection hints for miners. |
| `PUBLIC_TCP_PORT` | listener port | TCP port shown in the hints. |
| `PUBLIC_TLS_PORT` | listener port | TLS port shown in the hints. |
| `LOG_FORMAT` | `json` | Log format: `json` or `text`. |

`docker-compose.yml` fixes the listen addresses inside the container; set the published ports with `PUBLIC_TCP_PORT`, `PUBLIC_TLS_PORT` and `ADMIN_PORT` in `.env`.

## Runtime settings

Everything else is set in Settings in the admin UI (or with `PUT /api/settings`), stored in `state.json` and applied without a restart. If any value in a change is invalid, nothing is changed.

| Setting | Default | Range | Meaning |
|---|---|---|---|
| `test_worker` | `proxytest` | 1–32 characters | Worker name used by pool checks. |
| `switch_drain` | 10 s | 0 s – 5 min | Time over which a switch, failback or "reconnect all" spreads the disconnects. |
| `failback_delay` | 2 min | 30 s – 1 h | How long the active pool must stay up before sessions return to it. |
| `pool_probe_interval` | 30 s | 10 s – 10 min | Health check period of every pool address. |
| `profit_switch` | off | off, advise, auto | Profit switching mode. |
| `profit_interval` | 24 h | 1 h – 7 days | How often the coins are compared. |
| `profit_margin` | 5 % | 0–50 % | How much more another coin must earn before switching to it. |
| `timed_switch` | off | off, on | Timed switching. |
| `timed_period` | 30 min | 10 min – 24 h | How often the farm moves to the timer pool. |
| `timed_duration` | 10 min | 1 min – 12 h | How long it stays there in every period; shorter than `timed_period`. |
| `tls_handshake_timeout` | 10 s | 1–60 s | Time a miner has to finish the TLS handshake. |
| `first_message_timeout` | 15 s | 5 s – 2 min | Time a new connection has to send its first Stratum message. |
| `upstream_dial_timeout` | 5 s | 1–30 s | Connect and TLS handshake timeout of one pool address. |
| `upstream_connect_budget` | 15 s | 5 s – 2 min | Total time a session may spend trying addresses and pools; at least `upstream_dial_timeout`. |
| `miner_idle_timeout` | 10 min | 1 min – 1 h | A miner that sends nothing for this long is disconnected. |
| `upstream_idle_timeout` | 15 min | 1 min – 1 h | If the pool sends nothing for this long, the session is closed and the miner reconnects. |
| `max_connections` | 5000 | 10 – 100000 | Miner connections in total. |
| `max_pending_connections` | 500 | 10 – 10000 | Connections that have not sent their first message yet; at most `max_connections`. |
| `max_conn_per_ip` | 0 (off) | 0 – 100000 | Connections from one IP address. |
| `max_line_bytes` | 64 KiB | 4–1024 KiB | Longest Stratum message from a miner or a pool; a longer one closes the session. |
| `tls_min_version` | 1.2 | 1.0, 1.1, 1.2, 1.3 | Oldest TLS version accepted from miners. |
| `history_detail_retention` | 7 days | 1–90 days | Per-minute totals and 10-minute points per miner. |
| `history_retention` | 365 days | 7 days – 10 years | Hourly totals; at least `history_detail_retention`. |
| `history_miner_retention` | 90 days | 7 days – 10 years | Hourly points per miner; at least `history_detail_retention`. |
| `log_level` | info | debug, info, warn, error | Log detail. Passwords are never logged. |

## Charts and history

Every minute the proxy records what changed: hashrate (from accepted share difficulty), accepted and rejected shares, and connected miners, in total and per pool. For each miner (ASIC login) it records hashrate, shares and time online in 10-minute points. Finished hours are rolled up into hourly points.

| File | Contents | Kept for |
|---|---|---|
| `/data/history/minutes/YYYY-MM-DD.jsonl` | per-minute totals | `history_detail_retention` |
| `/data/history/hours/YYYY-MM.jsonl` | hourly totals | `history_retention` |
| `/data/history/workers-10m/YYYY-MM-DD.jsonl` | 10-minute points per miner | `history_detail_retention` |
| `/data/history/workers-hours/YYYY-MM.jsonl` | hourly points per miner | `history_miner_retention` |

Ranges of up to three days are drawn from the detailed points, longer ones from the hourly points. The totals take about 0.3–0.5 MB a day per minute and 10 KB a day hourly; each miner adds about 8 KB a day in 10-minute points and 1.3 KB a day hourly. Expired files are deleted whole. The files are JSON lines, one point per line, and are backed up with the volume. There is no database.

A crash loses at most the unfinished interval: one minute of totals and ten minutes of per-miner data. A normal stop writes both.

On the Miners page, click a miner to see its charts. The same page lists miners seen in the last 24 hours that are not connected now.

## Profit switching

The proxy can compare the SHA-256 coins of your pools at a set interval and pick the most profitable one, for example BTC or BCH.

1. Add pools for different coins (the Coin field: BTC, BCH, BSV, XEC, DGB or FB) and tick "Take part in profit switching" in each of them.
2. In Settings, set profit switching to Advise, which shows the result on the Dashboard with a button to switch, or to Auto, which switches by itself.
3. Adjust the interval (24 hours by default) and the margin (5 % by default: another coin must earn at least that much more than the current one).

Revenue is estimated from 24-hour averages of network difficulty, block reward (fees included) and price from [WhatToMine](https://whattomine.com): one request per check, no API key. WhatToMine does not list BSV; its difficulty and price come from [WhatsOnChain](https://whatsonchain.com), and its block reward is the subsidy (BSV fees are negligible). Pool fees and payout schemes are not taken into account. When there is no data, or it is stale, nothing changes.

eCash (XEC) needs a correction. Its nodes enforce Real Time Targeting: besides the target in the block header, a block has to meet a real-time target that depends on how long ago the last blocks arrived. A block 30 seconds after the previous one has to be about 800 times harder, after a minute 25 times, after two minutes it no longer matters. Nobody finds blocks in those minutes, and the difficulty algorithm lowers the header difficulty so that blocks still come every 10 minutes. A difficulty-based estimate therefore overstates what miners get; simulating the node's rule gives 0.757 of it, and XEC revenue and odds are multiplied by that. The formula is the one in Bitcoin ABC, `src/policy/block/rtt.cpp`.

A profit switch works like a manual one: the target pool is checked, the change is saved and miners reconnect gradually. Its reason, `profit`, shows in the events and in the last switch on the Dashboard. If the active pool does not take part, it is left alone. "Check now" only shows the decision and never switches. The time of the last scheduled check is stored in `/data/profit.json`, so a restart does not move the schedule.

On PPLNS pools frequent switching loses earnings. Use FPPS or PPS+ pools and an interval of a day or more, and start with Advise.

## Timed switching

Timed switching sends the farm to another pool for part of every period and brings it back, for example to a solo pool for 10 minutes of every 30 minutes: a third of the hashrate plays the solo lottery.

1. Add the pool, for a solo pool usually with your payout address in the login template (`bc1q....{worker}`), and tick "Switch to this pool on the timer" in its editor. Only one pool can have it.
2. In Settings, turn on timed switching and set the period (30 minutes by default) and the time on the timer pool (10 minutes by default).

Periods start at multiples of the period: with 30 minutes, at :00 and :30. From the start of each, the farm spends the set time on the timer pool: the timer pool is checked and made active, and when the time is up the farm returns to the pool that was active before, with the fallback order from before. While the farm is on the timer pool, the pool it came from is the first fallback. The switches show in the events with the reason `timer`.

- If the timer pool fails its check, the farm stays where it is until the next period.
- If you switch pools by hand during that time, your choice stays and the timer does not switch back.
- A restart in the middle does not strand the farm: the way back is saved in `/data/timed.json`.
- Profit switching waits with its scheduled check until the farm is back, and compares coins for the pool it returns to.

When the timer pool mines eCash, the timer follows the real-time target (see profit switching above). It does not go to the pool while a block would be more than 1.2 times harder than its header says, which is the first two minutes or so after a block. When a block arrives while the farm is there and the next one becomes more than twice as hard, the farm goes back to the other pool until that eases, and the time is made up later in the period. With less than a minute of time left, it just stays. When a period ends while the farm is on the timer pool, it stays there for the new period instead of leaving and coming back.

To know when eCash blocks arrive, the proxy keeps one quiet Stratum connection to an eCash pool (the timer pool if it mines eCash, else the first one) and watches its jobs: a job with a new previous-block hash means a block. It logs in as the test worker, which the pool may show as an idle worker. The times of the 17 blocks before are read once from Blockchair at start.

The Dashboard shows the network difficulty of every coin (latest and against the 24-hour average) and the farm's chances to find a block solo at its current hashrate: per hour, per day and the average time to a block, plus the chance per day with the timer. For eCash it also shows the real-time difficulty now and when the last block came, like solo pools do. The market data is cached for 10 minutes; the panel works with profit switching off.

Every switch reconnects all miners within `switch_drain`, so a 30-minute period costs four reconnects an hour. Switching away from a PPLNS pool also loses part of its reward window.

## API

The admin UI uses a JSON API under `/api/`. Scripts authenticate with `Authorization: Bearer <API_TOKEN>`. Requests that change something must be sent with `Content-Type: application/json`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | Liveness check, no login needed |
| GET | `/api/status` | Mode, active pool, miners, shares, hashrate |
| GET | `/api/miners` | Connected miners |
| POST | `/api/miners/reconnect` | Reconnect all miners over `switch_drain` |
| GET | `/api/events?limit=100` | Recent events |
| GET | `/api/pools` | Pools; passwords are never returned |
| POST | `/api/pools` | Add a pool |
| PUT | `/api/pools/{id}?reconnect=true` | Change a pool; `reconnect` applies it to connected miners now |
| DELETE | `/api/pools/{id}` | Delete a pool (not the active one) |
| POST | `/api/pools/test` | Check pool settings before saving |
| POST | `/api/pools/{id}/test` | Check a saved pool |
| POST | `/api/pools/{id}/activate?force=true` | Make a pool active; `force` skips the check |
| PUT | `/api/fallback` | Fallback order: `{"pools": ["pool_b", "pool_c"]}` |
| GET, PUT | `/api/settings` | Runtime settings; `null` resets a value to its default |
| GET | `/api/history?from=&to=&points=` | Totals and per-pool history; times in unix seconds |
| GET | `/api/history/worker?name=&from=&to=&points=` | One miner's history and a summary of the range |
| GET | `/api/history/workers?from=&to=` | Every miner seen in the range |
| GET | `/api/profit` | Profit switching status and the last report |
| POST | `/api/profit/check` | Compare the coins now; never switches |
| GET | `/api/timed` | Timed switching: target pool, time still due, next period, eCash wait |
| GET | `/api/network` | Difficulty, block reward, price and efficiency of the coins; eCash real-time target |

Errors have the form `{"error": "validation", "key": "...", "params": {...}, "message": "..."}` with status 400, 404, 409 or 422 (pool check failed). `message` is English text for scripts; the UI translates `key` with `params`.

## Network and firewall

Allow the Stratum ports only from your farms' IP addresses where you can. Ports published by Docker bypass UFW rules, so use your provider's firewall or the `DOCKER-USER` iptables chain.

`max_conn_per_ip` is off by default because a farm usually connects from a single NAT address. If you turn it on, leave room above the number of miners behind one address.

The proxy makes outgoing HTTPS requests for market data to whattomine.com and api.whatsonchain.com: with profit switching on, once per interval, and while the Dashboard is open, at most once every 10 minutes. With an eCash pool configured, it also reads recent block times from api.blockchair.com once at start and keeps one Stratum connection to that pool.

## Operations

```bash
docker compose ps
docker compose exec proxy /app/proxy healthcheck
docker compose logs --tail=200 proxy
```

To update, back up the volume first, then:

```bash
git pull
docker compose up -d --build
```

`state.json` stores pool passwords in plain text: protect the backups and access to the Docker host.

On stop (SIGTERM) the proxy stops accepting connections, closes the miner sessions and exits within 10 seconds. Miners reconnect by themselves.

## Development

You need Go 1.24 or newer and Node.js 24 or newer. Node only builds the admin UI; the Docker build runs it in a separate stage.

```bash
# Admin UI: dependencies, type check, lint, tests, build into web/dist
cd web
npm ci
npm run check
npm run build
cd ..

# The proxy, with the built UI embedded
go vet ./...
go test ./...
ADMIN_PASSWORD=dev-password DATA_DIR=./data LOG_FORMAT=text go run ./cmd/proxy
```

It listens on `:13333`, `:14443` and `:18080`; `STRATUM_TLS_ADDR=off` turns the TLS listener off. Without `npm run build` the proxy still runs and shows how to build the UI in its place.

For UI work, run `npm run dev` in `web/`: Vite reloads the page on changes and forwards `/api` to a proxy running on `127.0.0.1:18080`.

| Path | Contents |
|---|---|
| `cmd/proxy` | Entry point, wiring, graceful shutdown |
| `internal/session`, `internal/stratum` | Miner-to-pool relay, Stratum parsing and rewriting |
| `internal/listener` | TCP and TLS listeners, connection limits |
| `internal/pool` | Pools, address checks, failover and failback, drain |
| `internal/stats`, `internal/history` | Counters in memory, history on disk |
| `internal/profit` | Coin revenue data and profit switching |
| `internal/rtt` | eCash Real Time Targeting: the formula and the connection that sees blocks arrive |
| `internal/timed` | Timed switching to another pool and back |
| `internal/admin`, `internal/apierr` | REST API, login and brute-force protection, error keys |
| `internal/settings`, `internal/state` | Runtime settings, `state.json` |
| `internal/tlsutil`, `internal/atomicfile` | TLS certificates, atomic file writes |
| `web/src` | Admin UI in React and TypeScript; translations in `web/src/i18n/locales` |

The server does not translate text. API errors carry a key and parameters, and the UI translates them; `web/src/i18n/locales.test.ts` checks that every key used in the Go code and every setting is translated into both languages.

## Limitations

- Stratum V1 only (line-delimited JSON-RPC), no Stratum V2.
- One pool connection per miner; connections are not aggregated.
- The counters in the miners table start from zero after a restart; the history for the charts is kept on disk.
- Profit switching ignores pool fees, payout schemes and payout delays.
- No built-in ACME / Let's Encrypt.
- Firmware, certificates and pools differ: try a few miners before moving the whole farm, and compare the reject rate with a direct pool connection.

## License

GPL-3.0. See [LICENSE](LICENSE).
