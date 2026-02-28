<div align="center">
  <img src="webui/public/vite.svg" width="48" alt="Resin Logo" />
  <h1>Resin</h1>
  <p><strong>将大量的代理订阅转化为一个稳定、智能、可观测且支持会话保持的网络。</strong></p>
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

**Resin** 是一个专为接管海量节点设计的**高性能智能代理池网关**。

它可以帮你彻底屏蔽底层代理节点的不稳定性，化繁为简，将它们聚合成一个支持 **“会话保持（粘性路由）”** 的超级 HTTP 流量网关。

## 💡 为什么选择 Resin？

- **海量接管**：轻松管理十万级规模的代理节点。高性能，原生支持高并发。
- **智能调度与熔断**：全自动的 **被动+主动** 健康探测、出口 IP 探测、延迟分析，精准剔除坏节点。采用 P2C 算法结合按域名的延迟加权评分，智能选择最优节点。
- **业务友好的粘性代理**：让同一业务账号稳定绑定同一出口 IP，节点断线自动无感切换同 IP 节点，极致提升服务稳定性。
- **双模接入**：同时支持标准正向代理（HTTP Proxy）与 URL 反向代理（Reverse Proxy）。
- **可观测性**：提供详细的性能指标与日志记录，快速掌控全局（可视化 Web 管理后台）。包括完整的结构化请求日志，支持按平台、账号、目标站点等维度查询与审计。
- **简单与强大兼得**：开箱即用的默认配置与深度自定义功能。无论你是只需几分钟跑通简单场景的个人使用者，还是需要高并发与高可用性的企业级团队，Resin 都能游刃有余。
- **跨订阅智能去重**：不同订阅中配置相同的节点自动合并，共享健康状态，避免重复探测。
- **热更新**：更新常用配置不用重启，更新订阅节点不断连。
- **状态持久化**：重启不丢失节点健康数据、延迟统计与租约绑定，生产环境无忧。
- **零侵入粘性接入**：支持从业务原有请求头（如 API Key）自动提取账号身份，客户端无需任何代码改动即可获得 IP 粘性。
- **订阅热更新**：节点订阅刷新时增量同步，不中断现有连接。
- **灵活的节点隔离**：通过 Platform 概念，按正则表达式、地区等规则筛选节点，为不同业务构建独立的代理池。


> [!TIP]
> 您可以把本文档与项目详细设计文档 [`DESIGN.md`](DESIGN.md) 丢给 AI，然后问它任何你感兴趣的问题！


![](doc/images/dashboard_zh-cn.png)

---

## 🚀 Quick Start

只需简单三步，即可将你的节点订阅转化为高可用代理池。

### 第一步：一键部署启动
推荐使用 Docker Compose 快速启动：

```yaml
# docker-compose.yml
services:
  resin:
    image: ghcr.io/resinat/resin:latest
    container_name: resin
    restart: unless-stopped
    environment:
      RESIN_ADMIN_TOKEN: "admin123" # 修改为你的管理后台密码
      RESIN_PROXY_TOKEN: "my-token" # 修改为你的代理密码
      RESIN_LISTEN_ADDRESS: 0.0.0.0
      RESIN_PORT: 2260
    ports:
      - "2260:2260"
    volumes:
      - ./data/cache:/var/cache/resin
      - ./data/state:/var/lib/resin
      - ./data/log:/var/log/resin
```
运行 `docker compose up -d` 启动服务。

*(如果你不想使用 Docker，请跳转文末查看 [其他部署方式](#其他部署方式))*

### 第二步：导入代理节点
1. 浏览器打开 `http://127.0.0.1:2260`（请替换为你的服务器 IP）。
2. 输入刚才设置的 `RESIN_ADMIN_TOKEN` 登录后台。
3. 在左侧菜单找到 **「订阅管理」**，添加你的节点订阅。
4. 稍等片刻，等待节点池刷新出你的节点。

### 第三步：开始你的代理请求
客户端接入方式参考接下来的章节。

## 🟢 基础使用（非粘性代理）

### 简单接入代理
如果你只需要一个高性能、大容量、且会自动健康管理的通用代理池，Resin 开箱即用。
启动 Resin 服务后，给你的应用程序接入 `http://<RESIN_PROXY_TOKEN>::@127.0.0.1:2260` 代理即可。  
如果你不想设置代理密码，请将环境变量显式设为空字符串：`RESIN_PROXY_TOKEN=""`（变量必须定义）。此时可直接接入 `http://127.0.0.1:2260`。下面是使用 curl 的一个例子：


```bash
curl -x http://127.0.0.1:2260 \
  -U "my-token::" \
  https://api.ipify.org
```

如果你的客户端支持修改服务的 `BASE_URL`，你也可以尝试反向代理接入。URL 格式为：`/令牌/Platform(可选):/协议/目标地址`。例如，你可以通过下面的 curl 命令通过 Resin 访问 `https://api.ipify.org`。

```bash
curl http://127.0.0.1:2260/my-token/:/https/api.ipify.org
```

> 正向代理与反向代理的选择：如果条件允许，推荐尽量使用反向代理，对于可观测性更友好。如果您的客户端不支持修改 BaseURL，或者客户端需要 utls、非纯 WebAPI 请求这种反向代理不擅长的情况，请使用正向代理。

### 筛选节点
如果你的服务对节点有筛选要求，例如只需要某个地区的节点，或者只需要来自某个订阅源的节点，或者只需要名字匹配特定正则表达式的节点，可以使用 Resin 的 Platform 概念来实现。

你可以打开 `http://127.0.0.1:2260/ui/platforms` Platform 管理页面，创建一个 Platform。例如希望只使用来自美国、香港的节点，你可以创建一个名为 “MyPlatform” 的 Platform，然后在地区过滤规则中填入：
```
us
hk
```

对于正向代理，你可以在 http 代理的用户信息中填入希望使用的 Platform。下面是一个 curl 的例子：

```bash
curl -x http://127.0.0.1:2260 \
  -U "my-token:MyPlatform:" \
  https://api.ipify.org
```

对于反向代理，你可以在 URL 前缀中提供 Platform 信息。下面是一个使用 curl 的例子：

```
curl http://127.0.0.1:2260/my-token/MyPlatform:/https/api.ipify.org
```

---

## 📖 进阶使用：粘性代理 (Sticky Session)

当业务遇到**对 IP 变化敏感**的服务，或者需要持续交互时，你需要使用 Resin 的核心特性：**粘性代理**。

在此之前，先了解两个概念：

### 🎯 核心概念：平台 (Platform) 与 账号 (Account)
- **平台 (Platform)**：节点的隔离池。你可以通过规则筛选节点（例如只使用“美国”节点）组建成一个专有池。系统默认存在一个装载所有可用节点的 `Default` 平台。
- **账号 (Account)**：业务侧的唯一标识（如 `Tom` 或 `user_1`）。携带特定 Account 的请求，Resin 会在平台中为其**硬锚定一个专属的高速出口节点**，哪怕节点突然断线，也会无缝重试并切换相同 IP，彻底解放业务端代码。

### 粘性代理接入格式
无论使用何种接入协议，认证身份的核心格式均为：`RESIN_PROXY_TOKEN:Platform:Account`。想要激活粘性路由，只需在最后加上 `Account` 即可。

#### 方式一：正向代理接入 (HTTP Proxy)
直接将身份信息写入 Proxy Auth（代理用户名）中：

```bash
# 格式：-U "密码:平台:账号"
# 指定一个业务账号 user_tom，Resin 会为其长期分配一个稳定的专属 IP
curl -x http://127.0.0.1:2260 \
  -U "my-token:Default:user_tom" \
  https://api.ipify.org
```

#### 方式二：反向代理接入 (Reverse Proxy)
你可以通过替换业务的 BaseURL 为 Resin 反代地址，将请求直接发给 Resin。
URL 格式进阶为：`http://部署IP:2260/密码/平台:账号/协议/目标地址`：

```bash
# 例如让 user_tom 访问 https 协议的 cloudflare.com：
curl "http://127.0.0.1:2260/my-token/Default:user_tom/https/api.ipify.org"
```

#### 方式三：反向代理接入 + 请求头规则（零侵入方案）

如果你的客户端（或 SDK）非常死板，**不支持在反向代理 URL 里动态拼 `Account`**，怎么办？

只要你的业务请求本身携带了某种身份凭证（例如发给目标网站的 API Key、Token、Cookie 等），Resin 就可以**直接“白嫖”你原有的业务请求头**，从中自动提取 Account！

**这意味着业务侧通常只需要改一个静态的 `BaseURL`，不需要修改任何业务逻辑代码。**

假设你的服务本来每次请求目标 API 时，都会携带 `Authorization` 请求头：

1. 在管理页面修改 Platform 的配置，把 “反向代理账号为空行为” 修改为 “提取指定请求头作为 Account”。
2. 在 “用于提取 Account 的 Headers” 输入 `Authorization`。

此时，就算你在反向代理 URL 里不填 `Account`，Resin 也会在流量经过时，**顺手截获并解析**该 Header。例如：

```bash
curl "http://127.0.0.1:2260/my-token/MyPlatform:/https/api.example.com/v1/orders" \
  -H "Authorization: sk-abc123"
```

上面的请求中，Resin 发现 sk-abc123 后，会将其作为 Account。后续只要是带着同一把 API Key 的请求，都会被 Resin 稳定绑定在同一个出口 IP 上。

> 除了 Platform 请求头配置外，Resin 还支持更高级的根据 URL 前缀决定请求头的高级功能！尝试把当前文档与 [DESIGN.md](DESIGN.md) 扔给 AI，问它 “请使用简单易懂的语言，向我介绍 Resin 指定请求头提取规则的两种方式，尤其是根据 URL 前缀决定请求头的方式。”

---

## 🤖 接入第三方项目

各类第三方客户端使用 Resin 的方式有所不同，对于业务代码的侵入程度也不同，总结如下：

💡 **如果你不需要粘性代理**

| 接入方式 | 代码侵入程度 | 说明 |
| :--- | :--- | :--- |
| 接入正向代理 | 🟢 **零侵入** | 客户端填入代理地址 `http://127.0.0.1:2260` 及账号密码即可。 |
| 接入反向代理 | 🟢 **零/低侵入** | 修改服务 BaseURL 即可接入，适配极易。 |

💡 **如果你需要粘性代理**

| 接入方式 | 代码侵入程度 | 说明 |
| :--- | :--- | :--- |
| 接入正向代理 | 🟡 **中侵入** | 需稍微修改代码：为不同用户的请求附带不同认证信息（如 `密码:平台:账号）|
| 接入反向代理 | 🟡 **中侵入** | 需稍微修改代码：动态拼接带有账号的反代 URL 路径。 |
| 接入反向代理 + 请求头规则 | 🟢 **零/低侵入** | Resin 允许通过识别业务原始头（如 `Authorization`）自动提取 Account 并绑定 IP！就像非粘性反代一样简单！ |

👉 **极速集成脚本/提示词（Prompt）：**  
如果你是开发者，想要修改现有项目原生接入 Resin 粘性代理，你可以直接把下面这个模板喂给 AI 帮你写代码：
- [doc/integration-prompt.md](doc/integration-prompt.md)

---

## 其他部署方式

<details>
<summary><b>方式一：运行预编译二进制文件</b></summary>
<br>
前往项目的 <a href="https://github.com/Resinat/Resin/releases">Release</a> 页面，下载适合你操作系统架构的程序包。解压得到单个二进制文件 <code>resin</code>。

```bash
RESIN_ADMIN_TOKEN=【管理面板密码】 \
RESIN_PROXY_TOKEN=【代理密码】 \
RESIN_STATE_DIR=./data/state \
RESIN_CACHE_DIR=./data/cache \
RESIN_LOG_DIR=./data/log \
RESIN_LISTEN_ADDRESS=0.0.0.0 \
RESIN_PORT=2260 \
./resin
```
</details>

<details>
<summary><b>方式二：通过源码编译</b></summary>
<br>
前提条件：请确保环境中已安装 Go 1.25+ 和 Node.js。

```bash
# 1. 下载 Resin 源码
git clone https://github.com/Resinat/Resin.git

# 2. 编译 WebUI
cd Resin/webui
npm install && npm run build
cd ..

# 3. 编译 resin 核心
go build -tags "with_quic with_wireguard with_grpc with_utls with_embedded_tor with_naive_outbound" -o resin ./cmd/resin

# 4. 运行程序
RESIN_ADMIN_TOKEN=【管理面板密码】 \
RESIN_PROXY_TOKEN=【代理密码】 \
RESIN_STATE_DIR=./data/state \
RESIN_CACHE_DIR=./data/cache \
RESIN_LOG_DIR=./data/log \
RESIN_LISTEN_ADDRESS=127.0.0.1 \
RESIN_PORT=2260 \
./resin
```
</details>

---

## 🛠️ 常见错误 (FAQ)

- **Q: 启动失败提示 `RESIN_PROXY_TOKEN` 未定义？**
  - **A**: 就算你不打算启用代理密码，也必须显式配置它为空：`RESIN_PROXY_TOKEN=""`。
- **Q: 使用反向代理 WebSocket 协议（如 ws/wss）怎么写路径？**
  - **A**: 目标无论是不是 ws/wss，URL 路径里的协议字段**依然只能写 `http` 或 `https`**（不能写 ws/wss）。Resin 会自动探测并完成 WebSocket 协议降级（Upgrade）。

---

## ⚠️ 免责声明与许可证

- **许可证**：本项目采用 [MIT License](LICENSE)。
- **免责声明**：本开源项目仅作为一个学术和技术研究的网络代理调度管理工具，旨在探索大规模代理节点的调度与管理策略。使用本项目的用户必须遵守其所在国家和地区的法律法规，并确保对网络资源的使用符合各服务提供商的服务条款（ToS）。开发者不对任何人因使用 Resin 造成的任何直接或间接的违法行为、违约责任及损失承担任何法律责任。请合法、合规地使用本项目。
