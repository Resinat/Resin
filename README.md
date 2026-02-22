
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

启动后，Resin 会在本地开启代理网关和控制面板（端口由配置指定）。

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
curl -x http://127.0.0.1:2621 \
  -U "my-token:Default:Tom" \
  https://api.example.com/ip
```
*（多次运行该命令，系统将始终为 Account `Tom` 返回稳定一致的出口 IP。）*

👉 **方式 B：反向代理 (Reverse Proxy)**
如果业务程序不支持配置 HTTP 代理，可直接通过 Resin 的反向代理协议进行网络请求。
URL 格式为：`/令牌/平台:账号/协议/目标地址`：

```bash
curl "http://127.0.0.1:2622/my-token/Default:Tom/https/api.example.com/ip"
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

### 深度接入
对于纯 API 请求，推荐接入反向代理。
给 Coding Agent 的 Prompt：
> 修改这个项目。
> 配置文件里引入 resin_reverse_proxy_url 与 resin_platform_name 配置。
> Resin 是一个外部代理池，通过反向代理的形式提供代理。
> Resin 通过路径 `<resin-reverse-proxy-url>/PROXY_TOKEN/Platform:Account/protocol/host/path?query` 解析反向代理请求；其中 `Platform:Account` 必须是单个路径段。protocol 是 `http/https` 之一；host 可以是域名，可以是 IP，可以加端口。例子：http://resin:2622/dummy-token/Default:Tom/https/api.example.com/healthz，表示 Resin 以 Default:Tom 作为业务身份，智能选择节点请求 https://api.example.com/healthz。
> 该项目中任何涉及具体账号的网络请求，都必须走 Resin 反向代理。其中 Platform 字段是 resin_platform_name。Account 字段是可以区分账号的标识，包括但不限于账号 ID、账号邮箱、账号 Token、账号哈希值等。

---

## ⚠️ 免责声明

本开源项目仅作为一个学术和技术研究的网络代理调度管理工具，旨在探索大规模代理节点的调度与管理策略。
使用本项目的用户必须遵守其所在国家和地区的法律法规，并确保对网络资源的使用符合各服务提供商的服务条款（ToS）。
开发者不对任何人因使用 Resin 造成的任何直接或间接的违法行为、违约责任及损失承担任何法律责任。请合法、合规地使用本项目。
