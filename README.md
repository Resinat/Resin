[English](README.md) | [简体中文](README.zh-CN.md)

<div align="center">
  <img src="webui/public/vite.svg" width="48" alt="Resin Logo" />
  <h1>Resin</h1>
  <p><strong>Turn massive proxy subscriptions into a stable, smart, observable unified proxy entrypoint with sticky sessions.</strong></p>
</div>

<p align="center">
  <a href="https://github.com/Resinat/Resin/releases"><img src="https://img.shields.io/github/v/release/Resinat/Resin?style=flat-square&label=release&sort=semver" alt="Release" /></a>
  <a href="https://github.com/Resinat/Resin/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/Resinat/Resin/release.yml?style=flat-square&label=release%20pipeline" alt="Release Pipeline" /></a>
  <a href="https://github.com/Resinat/Resin/pkgs/container/resin"><img src="https://img.shields.io/badge/ghcr-ghcr.io%2Fresinat%2Fresin-2496ED?style=flat-square&logo=docker&logoColor=white" alt="GHCR Image" /></a>
  <a href="https://github.com/Resinat/Resin/blob/master/LICENSE"><img src="https://img.shields.io/github/license/Resinat/Resin?style=flat-square" alt="License" /></a>
  <a href="https://github.com/Resinat/Resin/blob/master/go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Resinat/Resin?style=flat-square" alt="Go Version" /></a>
  <a href="https://github.com/Resinat/Resin/releases"><img src="https://img.shields.io/badge/support-linux%20%7C%20macOS%20%7C%20windows%20%C2%B7%20amd64%20%7C%20arm64-0A7EA4?style=flat-square" alt="Support Matrix" /></a>
  <a href="DESIGN.md"><img src="https://img.shields.io/badge/docs-DESIGN.md-1F6FEB?style=flat-square" alt="Design Docs" /></a>
</p>

---

**Resin** is a **high-performance intelligent proxy pool gateway** built for operating massive numbers of proxy nodes.

It helps shield your services from unstable underlying proxy nodes by aggregating distributed proxy resources into a unified proxy entrypoint with **session stickiness (sticky routing)**.

## 💡 Why Resin?

- **Massive-scale management**: Easily handles 100k+ proxy nodes with native high-concurrency performance.
- **Smart scheduling and circuit breaking**: Fully automated **passive + active** health checks, outbound IP probing, and latency analysis to remove bad nodes precisely. Uses P2C plus domain-aware latency-weighted scoring for optimal node selection.
- **Business-friendly sticky proxying**: Keeps the same business account bound to a stable outbound IP. If a node fails, Resin seamlessly switches to another node with the same IP.
- **Multiple access modes**: Supports HTTP forward proxy, SOCKS5 forward proxy, and URL-based reverse proxy for different clients and integration styles.
- **Multiple inbound endpoints**: Add hot-reloaded listening ports in the WebUI and independently control admin-console, HTTP forward, HTTP reverse, and SOCKS5 access on each port.
- **Observability**: Detailed metrics and logs, plus a visual Web UI. Includes complete structured request logs for querying and auditing by platform, account, target site, and more.
- **Simple and powerful**: Works out of the box with default settings, while still offering deep customization for enterprise-grade needs.
- **Cross-subscription deduplication**: Automatically merges identical nodes from different subscriptions and shares their health state.
- **Hot reload**: Update common settings without restart. Refresh subscriptions without dropping existing traffic.
- **Persistent state**: Keeps node health, latency stats, and lease bindings across restarts.
- **Zero-intrusion sticky access**: Can extract account identity from existing request headers (for example API keys), so clients often need no code changes.
- **Incremental subscription refresh**: Syncs subscription updates without interrupting current connections.
- **Flexible node isolation**: Use Platform rules (regex, region, etc.) to build independent proxy pools for different business scenarios.

> [!TIP]
> You can feed this README and [`DESIGN.md`](DESIGN.md) to AI and ask it anything about the project.

![](doc/images/dashboard_en-us.png)

---

## 🔌 Supported Protocols and Subscription Formats

### Subscription sources

- Remote subscription URL: `http://` or `https://`.
- Local subscription content: paste subscription content directly in the UI/API.

### Subscription content formats

- sing-box JSON: `{"outbounds":[...]}` or raw outbound array `[...]`.
- Clash JSON/YAML: `{"proxies":[...]}` or YAML `proxies:`.
- URI line format (one node per line): `vmess://`, `vless://`, `trojan://`, `ss://`, `hysteria2://`, `http://`, `https://`, `socks5://`, `socks5h://`.
  For `http://`, `https://`, `socks5://`, `socks5h://`, use `scheme://[user:pass@]host:port` (optional `#tag`; `https` also supports `sni`/`servername`/`peer` and `allowInsecure`/`insecure` query parameters).
- Plain HTTP proxy lines: `IP:PORT` or `IP:PORT:USER:PASS` (IPv4 and IPv6).
- Base64-wrapped text subscriptions (for URI lines/plain-text node lists).

### Supported outbound node types

- For sing-box JSON/raw outbounds: `socks`, `http`, `shadowsocks`, `vmess`, `trojan`, `wireguard`, `hysteria`, `vless`, `shadowtls`, `tuic`, `hysteria2`, `anytls`, `ssh`.
- For Clash conversion: `ss`/`shadowsocks`, `socks`/`socks4`/`socks4a`/`socks5`, `http`, `vmess`, `vless`, `trojan`, `wireguard`/`wg`, `hysteria`, `hysteria2`/`hy2`, `tuic`, `anytls`, `ssh`.

## 🚀 Quick Start

In just three steps, you can turn your proxy subscriptions into a highly available proxy pool.

### Step 1: Deploy and start

Docker Compose is the recommended quick-start path:

```yaml
# docker-compose.yml
services:
  resin:
    image: ghcr.io/resinat/resin:latest
    container_name: resin
    restart: unless-stopped
    environment:
      RESIN_ADMIN_TOKEN: "admin123" # Change to your admin dashboard password
      RESIN_PROXY_TOKEN: "my-token" # Change to your proxy password
      RESIN_LISTEN_ADDRESS: 0.0.0.0
      RESIN_PORT: 2260
    ports:
      - "2260:2260"
    volumes:
      - ./data/cache:/var/cache/resin
      - ./data/state:/var/lib/resin
      - ./data/log:/var/log/resin
```

Run `docker compose up -d` to start the service.

Custom endpoint ports must also be reachable from outside the container. Docker cannot add published ports to an already-running container, so pre-publish the required port range in `ports` (for example `"2300-2399:2300-2399"`) or use host networking where appropriate.

*(If you don't want Docker, jump to [Other Deployment Options](#other-deployment-options).)*

### Step 2: Import proxy nodes

1. Open `http://127.0.0.1:2260` in your browser (replace with your server IP if needed).
2. Log in with the `RESIN_ADMIN_TOKEN` you set.
3. Go to **Subscriptions** in the left menu and add your node subscription.
4. Wait briefly for the node pool to refresh.

### Step 3: Start sending proxy requests

See the following sections for client access modes. In common scenarios, you can choose HTTP forward proxy, SOCKS5 forward proxy, or reverse proxy based on what your client supports.

## 🟢 Basic Usage (Non-sticky Proxy)

### Connect as a standard proxy

If you just need a high-performance, large-capacity proxy pool with automatic health management, Resin works out of the box.

Once Resin is running, you can choose HTTP forward proxy, SOCKS5 forward proxy, or reverse proxy based on what your client supports.
If you do not want a proxy password, explicitly set `RESIN_PROXY_TOKEN=""` (the variable must still be defined). Then HTTP forward proxy is available at `http://127.0.0.1:2260`, and SOCKS5 forward proxy is available at `socks5://127.0.0.1:2260`.
For binary/source runs, Resin also loads a `.env` file from the current working directory before reading configuration. Environment variables already set by the OS or shell take precedence over `.env` values.

HTTP forward proxy example:

```bash
curl -x http://127.0.0.1:2260 \
  -U ":my-token" \
  https://api.ipify.org
```

SOCKS5 forward proxy example:

```bash
curl --proxy socks5h://127.0.0.1:2260 \
  -U "Default:my-token" \
  https://api.ipify.org
```

When `RESIN_PROXY_TOKEN=""`, SOCKS5 also allows unauthenticated access.

If your client supports overriding `BASE_URL`, you can also use reverse-proxy mode.
URL format: `/token/Platform(optional).Account(optional)/protocol/target`.
Example request to `https://api.ipify.org`:

```bash
curl http://127.0.0.1:2260/my-token/./https/api.ipify.org
```

> Choosing forward vs reverse proxy: when possible, reverse proxy is recommended for better observability. If your client cannot change BaseURL, or requires scenarios better served by forward proxy (such as uTLS or non-WebAPI traffic), use forward proxy.

### Filter nodes

If your service needs specific nodes (for example by region, subscription source, or name regex), use Resin's Platform feature.

Open `http://127.0.0.1:2260/ui/platforms` and create a Platform. For example, to use only US and HK nodes, create `MyPlatform` and set region filters to:

```
us
hk
```

Node tag rules match `<SubscriptionName>/<NodeTag>`, with one regular expression per line. Plain rules are ORed, a leading `*` marks a required rule, and a leading `!` excludes a node on any match. For example, this selects dedicated Hong Kong or Japan nodes while excluding expired and invalid nodes:

```text
Hong Kong
Japan
*Dedicated
!Expired
!Invalid
```

Only the first character is interpreted as a rule prefix; the rest remains a Go regular expression. Escape a literal leading `!` as `\!`.

For forward proxy (HTTP / SOCKS5), include Platform in the auth info. Examples:

```bash
curl -x http://127.0.0.1:2260 \
  -U "MyPlatform:my-token" \
  https://api.ipify.org
```

```bash
curl --proxy socks5h://127.0.0.1:2260 \
  -U "MyPlatform:my-token" \
  https://api.ipify.org
```

For reverse proxy, include Platform in the URL prefix:

```bash
curl http://127.0.0.1:2260/my-token/MyPlatform/https/api.ipify.org
```

## 🔓 Free-Mode Ports (Password-less)

If you want to use the proxy **without a password** and have “**one local port = one fixed egress IP**”, enable free-mode ports.

Beyond the main port, Resin listens on a range of consecutive ports (starting at `21000` by default). The whole range is bound to a single platform (`MM` by default, **auto-created** and labeled as free-mode-only if missing); **each port** pins its own egress node/IP via a sticky lease — the same port keeps a stable egress, while different ports map to different egresses. Free-mode ports serve **both HTTP forward proxy and SOCKS5**, and expose **no** admin UI / reverse proxy.

### Enabling (environment variables)

| Variable | Description | Default |
| --- | --- | --- |
| `RESIN_FREE_PORT_START` | Start port; `0`/unset disables the feature | `0` |
| `RESIN_FREE_PORT_COUNT` | Number of ports to open (max 256) | `0` |
| `RESIN_FREE_PORT_PLATFORM` | Platform bound to the whole range (auto-created if missing) | empty |
| `RESIN_FREE_PORT_ACCESS_MODE` | Access control: `intranet` / `whitelist` | `intranet` |
| `RESIN_FREE_PORT_WHITELIST` | IP/CIDR list (JSON array) for whitelist mode | `[]` |

> ⚠️ Security: free-mode ports rely entirely on access control. `intranet` (default) allows only private/loopback/link-local clients; `whitelist` allows only listed IP/CIDRs. **Never expose free-mode ports to the public internet with an empty or overly broad whitelist.**

### Examples

```bash
# HTTP forward proxy on port 21000, no auth required
curl -x http://127.0.0.1:21000 https://api.ipify.org

# SOCKS5 on the same port
curl --proxy socks5h://127.0.0.1:21000 https://api.ipify.org

# Different port = different fixed egress
curl -x http://127.0.0.1:21001 https://api.ipify.org
```

> For Docker, `docker-compose.yml.example` maps `21000-22000` by default. Note: mapping the whole span spawns ~1000 docker-proxy processes; in production, narrow the mapping to your actual count or use `network_mode: host`.

## 📖 Advanced Usage: Sticky Session Proxy

When your business depends on IP continuity or long-lived interactions, use Resin's core feature: **sticky proxying**.

First, understand two core concepts:

### 🎯 Core Concepts: Platform and Account

- **Platform**: An isolated node pool. You can build it with filters (for example, only US nodes). Resin provides a default `Default` platform containing all available nodes.
- **Account**: A unique business identity (for example `Tom` or `user_1`). For requests carrying an Account, Resin anchors traffic to a dedicated high-quality outbound node. If that node fails, Resin retries seamlessly and switches to another node with the same IP.

### Sticky proxy access formats

#### Method 1: Forward proxy (HTTP Proxy / SOCKS5)

HTTP forward proxy and SOCKS5 forward proxy share the same identity format: `Platform.Account:RESIN_PROXY_TOKEN`.

Just put the identity in proxy authentication:

```bash
# HTTP forward proxy: bind business account user_tom to a stable dedicated outbound IP
curl -x http://127.0.0.1:2260 \
  -U "Default.user_tom:my-token" \
  https://api.ipify.org
```

```bash
# SOCKS5 forward proxy: uses the same identity format as HTTP forward proxy
curl --proxy socks5h://127.0.0.1:2260 \
  -U "Default.user_tom:my-token" \
  https://api.ipify.org
```

#### Method 2: Reverse proxy (URL Account, quick/manual debug)

By replacing your service BaseURL with Resin reverse-proxy URL, traffic goes through Resin directly.
Advanced URL format: `http://host:2260/token/platform.account/protocol/target`:

```bash
# Example: user_tom accesses api.ipify.org over https
curl "http://127.0.0.1:2260/my-token/Default.user_tom/https/api.ipify.org"
```

> The URL Account segment is designed for quick use and manual debugging.
> For long-running production integrations, prefer passing Account by header (`X-Resin-Account`).

#### Method 3: Reverse proxy + `X-Resin-Account` header (recommended production integration)

If your client/SDK supports custom request headers, pass Account explicitly with `X-Resin-Account`.
This is the recommended and most stable method.

Account source priority: `X-Resin-Account` header > Account in reverse-proxy URL > header extraction rules.

Example:

```bash
curl "http://127.0.0.1:2260/my-token/MyPlatform/https/api.example.com/v1/orders" \
  -H "X-Resin-Account: user_tom"
```

#### Method 4: Reverse proxy + header rules (zero/low-intrusion integration)

If your client cannot set `X-Resin-Account`, Resin can still extract Account from existing business headers (for example API Key, Token, Cookie) via header rules.

Assume your requests already include an `Authorization` header:

1. In Platform Configuration, set `Reverse-proxy empty-account behavior` to `Extract specified request headers as Account`.
2. Set `Headers used to extract Account` to `Authorization`.

Then even if Account is omitted in URL, Resin can still parse it from headers:

```bash
curl "http://127.0.0.1:2260/my-token/MyPlatform/https/api.example.com/v1/orders" \
  -H "Authorization: sk-abc123"
```

In this example, Resin uses `sk-abc123` as Account. Future requests with the same key are intended to stay bound to the same outbound IP whenever routing conditions allow.

> [!TIP]
> Beyond Platform header config, Resin also supports advanced rules that pick extraction headers by URL prefix. You can ask AI to explain both modes with this README and [DESIGN.md](DESIGN.md).

> Enable header-based Account extraction only when you have a valid legal basis (for example user authorization or contractual permission), and ensure your logging, retention, and access-control policies comply with applicable laws and target-service terms.

---

## 🤖 Integrating Third-party Projects

Different clients integrate Resin differently, with different code-intrusion levels.

💡 **If you do not need sticky proxying**

| Access Method | Code Intrusion | Notes |
| :--- | :--- | :--- |
| Forward proxy | 🟢 **Zero intrusion** | Just configure an HTTP or SOCKS5 proxy address plus credentials. |
| Reverse proxy | 🟢 **Zero/low intrusion** | Usually only requires changing service BaseURL. |

💡 **If you need sticky proxying**

| Access Method | Code Intrusion | Notes |
| :--- | :--- | :--- |
| Forward proxy | 🟡 **Medium intrusion** | Per-user requests need different auth info. Both HTTP and SOCKS5 use `platform.account:token`. |
| Reverse proxy | 🟡 **Medium intrusion** | Add `X-Resin-Account` request header or build reverse-proxy URL paths dynamically with account information. |
| Reverse proxy + header rules | 🟢 **Zero/low intrusion** | Resin can extract Account from original headers (for example `Authorization`) and bind IP automatically. |

👉 **Fast integration script/prompt:**
If you are a developer and want AI to help modify an existing project for native Resin sticky integration, use:

- [doc/integration-prompt.md](doc/integration-prompt.md)

---

## Other Deployment Options

<details>
<summary><b>Option 1: Run prebuilt binary</b></summary>
<br>
Go to the project's <a href="https://github.com/Resinat/Resin/releases">Release</a> page and download the package for your OS/architecture. After extraction, run the single binary <code>resin</code>.

```bash
RESIN_ADMIN_TOKEN=<admin-dashboard-password> \
RESIN_PROXY_TOKEN=<proxy-password> \
RESIN_STATE_DIR=./data/state \
RESIN_CACHE_DIR=./data/cache \
RESIN_LOG_DIR=./data/log \
RESIN_LISTEN_ADDRESS=0.0.0.0 \
RESIN_PORT=2260 \
./resin
```

Alternatively, create a `.env` file in the working directory and run `./resin`:

```dotenv
RESIN_ADMIN_TOKEN=<admin-dashboard-password>
RESIN_PROXY_TOKEN=<proxy-password>
RESIN_STATE_DIR=./data/state
RESIN_CACHE_DIR=./data/cache
RESIN_LOG_DIR=./data/log
RESIN_LISTEN_ADDRESS=0.0.0.0
RESIN_PORT=2260
```
</details>

<details>
<summary><b>Option 2: Build from source</b></summary>
<br>
Prerequisites: Go 1.25+ and Node.js installed.

```bash
# 1. Clone Resin source
git clone https://github.com/Resinat/Resin.git

# 2. Build WebUI
cd Resin/webui
npm install && npm run build
cd ..

# 3. Build Resin core
go build -tags "with_quic with_wireguard with_grpc with_utls" -o resin ./cmd/resin

# 4. Run
RESIN_ADMIN_TOKEN=<admin-dashboard-password> \
RESIN_PROXY_TOKEN=<proxy-password> \
RESIN_STATE_DIR=./data/state \
RESIN_CACHE_DIR=./data/cache \
RESIN_LOG_DIR=./data/log \
RESIN_LISTEN_ADDRESS=127.0.0.1 \
RESIN_PORT=2260 \
./resin
```
</details>

---

## 🛠️ FAQ

- **Q: How do I let LAN or localhost targets skip proxy nodes?**
  - **A**: Set `RESIN_PROXY_BYPASS` to a semicolon/comma/newline-separated rule list. Matching requests are dialed directly by Resin instead of through a proxy node. Example: `RESIN_PROXY_BYPASS="localhost;127.*;10.*;172.16.0.0/12;192.168.*;<local>"`. Supported rules include exact hosts, `*`/`?` wildcards, CIDR ranges, and `<local>` for hostnames without dots.
- **Q: Startup fails with `RESIN_PROXY_TOKEN` undefined?**
  - **A**: Even if you do not want a proxy password, you must explicitly set it to empty: `RESIN_PROXY_TOKEN=""`. On shells that drop empty environment variables, create a `.env` file with `RESIN_PROXY_TOKEN=`.
- **Q: Why does Resin reject `RESIN_AUTH_VERSION=LEGACY_V0`?**
  - **A**: `LEGACY_V0` is no longer supported. Remove `RESIN_AUTH_VERSION` or set it to `V1`. If you are upgrading from a release that used legacy authentication, see the [v1.0.0 auth migration guide](doc/v1.0.0-migration-guide.md).
- **Q: Why can't my SOCKS5 client connect?**
  - **A**: If `RESIN_PROXY_TOKEN` is non-empty, the client must send SOCKS5 username/password authentication. If it is explicitly set to an empty string, `NO AUTH` is also allowed.
- **Q: How to write reverse-proxy paths for WebSocket (ws/wss)?**
  - **A**: In the URL path, the protocol field must still be `http` or `https` (not `ws`/`wss`). Resin auto-detects and handles WebSocket upgrade.

---

## ⚠️ License and Disclaimer

- **License**: This project is licensed under the [MIT License](LICENSE).
- **Use scope**: This project is provided for technical research and engineering practice in proxy scheduling and management. It does not constitute legal, compliance, audit, or security advice.
- **Lawful use requirement**: You are responsible for ensuring your usage complies with applicable laws, regulations, target-service Terms of Service (ToS), and data-processing obligations, and for ensuring you have proper authorization for proxy nodes, target resources, and related data.
- **Prohibited use**: You must not use this project for unauthorized access, control-evasion, fraud, attacks, abusive traffic generation, or other illegal or non-compliant activity.
- **No warranty**: This project is provided on an "AS IS" basis, without express or implied warranties, including but not limited to merchantability, fitness for a particular purpose, and non-infringement.
- **Limitation of liability**: To the maximum extent permitted by applicable law, authors and contributors are not liable for any direct, indirect, incidental, special, exemplary, or consequential damages arising from use of, or inability to use, this project.
