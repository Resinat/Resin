
<div align="center">
  <img src="webui/public/vite.svg" width="96" alt="Resin Logo" />
  <h1>Resin</h1>
  <p><strong>把大量代理订阅，变成稳定、好用、可观测的代理池。</strong></p>
</div>

---

**Resin** 是一个专为接管 10k~100k 海量节点设计的**高性能智能代理池网关**。

当你手握几大量的代理节点，却苦于难以将它们稳定地分配给业务侧使用时，Resin 能够帮你彻底屏蔽底层节点的不稳定性，化繁为简，将它们聚合成一个支持 **“会话保持（粘性路由）”** 的超级 HTTP 流量网关。

简单来说——你只需要告诉 Resin：“这是我的业务账号 Tom”。Resin 就会自动在这个账号有效期间内，一直为 Tom 分配稳定不变的低延迟出口 IP；哪怕背后使用的节点断线，Resin 也会无感地为 Tom 切换到同 IP 的健康节点，或者智能分配新的最佳节点。


## 💡 为什么选择 Resin？

- **海量接管**：轻松管理十万级规模的代理节点。
- **智能调度与熔断**：全自动的**被动+主动**节点健康探测、出口 IP 探测、延迟分析，坏节点剔除。
- **业务友好的粘性路由**：让同一个业务账号（Account）不仅稳定使用同一个出口 IP，还能自动重试与切换，极大降低风控拦截或封号风险。
- **正反向代理双模接入**：同时支持标准正向代理与 URL 反向代理，你的任何自动化程序或客户端怎么接都行！

---

## 🎯 核心概念

Resin 抽象出了两个极简的业务概念：**Platform（平台）** 与 **Account（账号）**。

### 1. Platform (平台)：节点隔离池
Platform 可以看作是一个按特定规则筛选出的节点池。例如：
- 建一个名为 `SearchEngine` 的 Platform，配置为：仅使用美国 (us)、日本 (jp) 地区，且延迟最低的节点。
- 建一个名为 `VideoService` 的 Platform，配置为：仅使用英国 (gb) 的节点，且优先选择空闲 IP。

### 2. Account (账号)：业务身份识别
Account 用于标识业务侧的请求发起者（例如 `Tom`、`Alice`，或任意字符串）。
当业务请求携带 `Account` 经过 Resin 时，系统会在指定的 Platform 中为其**锚定一个专属的高速出口节点**。
在活跃期间，Resin 将保证该 `Account` 的流量始终使用同一个出口 IP，避免频繁变动。

---

## 🚀 快速开始

### 方式一：运行预编译二进制文件（推荐）

前往项目的 Release 页面，下载适合您操作系统架构的程序包。解压后准备好配置文件，即可直接运行。

### 方式二：源码编译

前提条件：请确保环境中已安装 Go 1.21 或以上版本。

```bash
# 1. 下载 Resin 源码
git clone https://github.com/your-org/resin.git
cd resin

# 2. 编译项目
go build -tags "with_quic with_wireguard with_grpc with_utls with_embedded_tor with_naive_outbound" -o resin ./cmd/resin

# 3. 运行程序
./resin
```

启动后，Resin 会在本地开启统一单端口服务（默认 `2260`）。WebUI 挂载在 `/ui/`，访问根路径 `/` 会自动重定向到 `/ui/`。

---

## 🎮 快速上手示例

以下是一个简单的范例，演示如何通过 Resin 发起稳定的代理请求：

**第一步：导入代理节点**
通过 Resin 的管理后台或控制 API，添加您的代理订阅链接。Resin 会在后台自动完成节点下载、解析与测速。

**第二步：准备 Platform**
系统内置了一个名为 `Default` 的 Platform。您可以直接使用它，或根据需求创建新的平台。

**第三步：发起请求**
您可以选择适合的代理方式。假设系统配置的网关安全令牌（Proxy Token）为 `my-token`：

👉 **方式 A：正向代理 (HTTP Proxy)**
主流开发语言（如 Python requests、Golang）和终端工具（curl）均原生支持正向代理。
请使用 `Proxy-Authorization` 头传递业务信息，格式为：`令牌:平台:账号`。

```bash
curl -x http://127.0.0.1:2260 \
  -U "my-token:Default:Tom" \
  https://api.example.com/ip
```
*（多次运行该命令，系统将始终为 Account `Tom` 返回稳定一致的出口 IP。）*

👉 **方式 B：反向代理 (Reverse Proxy)**
如果业务程序不支持配置 HTTP 代理，可直接通过 Resin 的反向代理协议进行网络请求。
URL 格式为：`/令牌/平台:账号/协议/目标地址`：

```bash
curl "http://127.0.0.1:2260/my-token/Default:Tom/https/api.example.com/ip"
```
*（Resin 会通过绑定的固定 IP 向上游网站发起请求，并返回结果。）*

**总结**
节点连通性检测、节点优选、出口 IP 的绑定与自动切换等繁杂流程，均由 Resin 在后台自动完成。业务代码中无需再实现复杂的连接重试与维护逻辑。

---

## 📦 核心特性与进阶用法

熟悉了基本用法后，您可以通过 Resin 探索更多高级特性：
- **通过 Header 提取 Account**：从 Authorization 等 Header 提取 Account 身份，实现无侵入/低侵入接入第三方程序。
- **正则表达式智能过滤**：按标签规则精准筛选专线、BGP 节点，构建优质 Platform。
- **灵活的流量分配策略**：支持按闲置优先（最大化 IP 利用率）或 延迟优先（极致速度）进行流量调度。
- **全局观测与可视化**：提供全链路延迟分布、租约存活时间、流量概览统计，支持对接各类 Dashboard 监控大屏。

部署 Resin，构建您的专属智能代理池。🌊

## 接入第三方项目

### 低侵入接入
TODO

# 深度接入

修改这个项目，使其接入外部粘性代理池（Resin）。
Resin 是一个外部代理池，支持正向代理与反向代理两种接入方式。Resin 通过 `Platform:Account` 的组合来识别业务身份，从而提供基于身份的粘性代理。

**接入策略指导：**
* **推荐使用反向代理**：对于大多数纯 Web API 类型的请求，建议使用反向代理接入。
* **按需使用正向代理**：当反向代理无法处理时（例如非纯 Web API 请求、客户端有专门的 uTLS 伪装需求等），建议使用正向代理。
* **同项目灵活混用**：正向代理与反向代理不必在这个项目中二选一。可以两者都接入，根据每个具体网络请求的特征灵活选择正代或反代。

**通用参数与账号约束：**
* 引入 `resin_url` 与 `resin_platform_name` 配置。`resin_url` 包含了代理基础地址与 Token，例如：`http://127.0.0.1:2260/my-token`。
* 该项目中任何涉及具体账号的网络请求，都必须走 Resin 代理。
* `Platform` 字段的值必须使用配置的 `resin_platform_name`。
* `Account` 字段是可以区分账号的标识，包括但不限于账号 ID、账号邮箱、唯一用户名、账号稳定 Token 等。**同一个账号的标识一定要稳定**。不能一会使用该账号的 Email 一会使用该账号的 Token，Resin 会把两个不同的标识符识别成两个不同的网络身份。
* 推荐使用**账号登录前就有的标识**，来防止登录阶段的请求没有标识可用而导致路由错误。
* 如果确实存在账号登录前没有标识可用的情况，可先使用一个临时标识（TempIdentity）发请求；等到登录成功并获得稳定标识后，调用 `POST <resin_url>/api/v1/<PLATFORM>/actions/inherit-lease`，Body 传入 `{"parent_account": "<TempIdentity>", "new_account": "<StableIdentity>"}`，来将历史临时身份的 IP 租约平滑继承给新的稳定身份。

**反向代理调用规范：**
* Resin 通过路径拼接的方式解析反向代理请求，格式为：`<resin_url>/Platform:Account/protocol/host/path?query`。
* 其中 `Platform:Account` 必须是单个完整路径段；`protocol` 为 `http` 或 `https` 之一（代表目标服务使用的底层协议类型）；`host` 可以是域名或 IP，也可以携带端口。
* **HTTP 代理例子**：设 `resin_url` 值为 `http://127.0.0.1:2260/my-token`，你要用反代请求 `https://api.example.com/healthz` 且业务身份为 `Default:Tom`。则应直接向 `http://127.0.0.1:2260/my-token/Default:Tom/https/api.example.com/healthz` 发起请求即可，Resin 会自动分配对应粘性节点完成真实的请求。
* **WebSocket 代理支持**：Resin 同样支持对 `ws` / `wss` 进行反向代理。注意两项强制约定：
  1. **从客户端连接到 Resin 的这一段只支持 `ws` 协议**。
  2. 路径中的 `protocol` 字段**必须填写 `http` 或 `https`**（对应目标是 ws 还是 wss），不能填 `ws` 或 `wss`。
* **WebSocket 代理例子**：同上配置，你要建立目标为 `wss://ws.example.com/chat` 的连接。客户端应当向 `ws://127.0.0.1:2260/my-token/Default:Tom/https/ws.example.com/chat` 拨号建立 WebSocket 连接。

**正向代理调用规范：**
* Resin 通过 HTTP 代理的 Proxy Auth 认证信息来获取业务身份。认证凭证（Credentials）由三部分构成：`RESIN_TOKEN:Platform:Account`。
* 在配置客户端的网络请求库时，需自行从 `resin_url` 中拆分出「代理服务器地址」和「Token」。把代理地址设置为发请求的 Proxy，把 Token 和业务身份塞入代理认证信息。
* 例子：设 `resin_url` 为 `http://127.0.0.1:2260/my-token`。通过 curl 请求的示例如下：`curl -x http://127.0.0.1:2260 -U "my-token:Default:Tom" https://api.example.com/ip`。其中 `-x` 指定 `http://127.0.0.1:2260` 为代理服务器，`-U` 的用户名传入 Token `my-token`，密码传入业务身份 `Default:Tom`。



## ⚠️ 免责声明

本开源项目仅作为一个学术和技术研究的网络代理调度管理工具，旨在探索大规模代理节点的调度与管理策略。
使用本项目的用户必须遵守其所在国家和地区的法律法规，并确保对网络资源的使用符合各服务提供商的服务条款（ToS）。
开发者不对任何人因使用 Resin 造成的任何直接或间接的违法行为、违约责任及损失承担任何法律责任。请合法、合规地使用本项目。
