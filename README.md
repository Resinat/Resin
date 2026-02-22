<div align="center">
  <img src="webui/public/vite.svg" width="120" />
</div>
<h1 align="center">Resin</h1>

<div align="center">
  <strong>高性能、智能化的 10k~100k 节点代理池管理系统</strong>
</div>
<br>

**Resin** 是专为需要管理海量节点规模、且对业务网络环境稳定性有极高要求的自动化团队设计的网关系统。

与传统的单纯 IP 轮询池不同，Resin 引入了**“平台 (Platform)”**与**“账号 (Account)”**两个核心业务概念，将大量松散的机场订阅、零碎节点聚合为一个**支持会话保持（粘性路由）**的代理网关。

---

## ✨ 核心价值

* **📦 业务身份与网络隔离**：支持以 `Platform + Account` 的维度对请求进行编排。不同账号分配独立的网络上下文，同一个账号能够在指定生命周期内保持稳定一致的出口 IP。再也不用担心频繁更换 IP 导致账号风控。
* **🚀 大规模与高性能**：核心业务为极速单点架构结构，在纯内存中完成 O(1) 的超高速热路径路由。轻松管理 10k~100k 级别的底层节点。
* **🧠 智能节点健康管理**：自动从订阅服务定时同步更新节点（兼容 Singbox 等格式）。系统于后台自动对节点的连通性、出口 IP 变更、真实延迟进行探测或收集，并在秒级内熔断异常节点。
* **🛡️ 灵活的双向代理模式**：无论是将它作为传统 HTTP/HTTPS 代理供爬虫与无头浏览器使用，还是将其部署为 API 反向代理直接穿透调用下游接口，Resin 都能完美支持。
* **📊 强大的可视化面板**：自带精致入微的 Web 界面，整合了 GeoIP 以实现精细化地区分流、全面的仪表盘监控，内置基于 SQLite 的毫秒级海量结构化请求日志查询平台。

## 💡 快速上手

你可以通过 **正向代理** 与 **反向代理** 两种方式无缝接入 Resin。请求均会绑定一致的出口 IP 进行流量清洗。

### 1. 正向代理场景 (Forward Proxy)
通过 HTTP 代理基础认证的 `Proxy-Authorization` 请求头进行细粒度控制：
```http
Proxy-Authorization: Basic <PROXY_TOKEN>:<PlatformName>:<AccountID>
```
*例如：客户端请求使用全局密码 `MyToken`，并指定属于 `Google` 平台下的 `User-1024` 账号。Resin 将自动寻找池中符合配置策略的优质空闲节点，并在接下来一段时间内保证 `User-1024` 的出口 IP 稳定不变。*

### 2. 反向代理场景 (Reverse Proxy)
将 Resin 当作反代 API 网关，在请求路径中拼接相关参数：
```http
http://<resin_ip>:<port>/PROXY_TOKEN/PlatformName:AccountID/https/api.example.com/v1/user
```
得益于强大的 Header 提取引擎引擎，反向代理也可以配置为自动从请求头的 `Authorization` 等字段中提取 `AccountID`，而无需修改原业务请求的 URL ：
```http
http://<resin_ip>:<port>/PROXY_TOKEN/PlatformName/https/api.example.com/v1/user
Authorization: Bearer User-1024
```

## 🛠️ 部署指南

### 前置要求
* **Go 1.25.5+** (编译后端)
* **Node.js 18+ & pnpm** (编译前端 Web 面板)

### 构建与启动

1. **克隆项目仓库**
```bash
git clone https://github.com/Resinat/Resin.git
cd resin
```

2. **构建 WebUI 并编译后端（自动内嵌到二进制）**
```bash
cd webui
pnpm install
pnpm build
cd ..

go build -o resin ./cmd/resin
./resin
```
> *(程序启动后，会在指定目录生成 `state.db` 与相关工作目录。具体信息见系统日志。WebUI 可直接通过 API 端口根路径访问，例如 `http://127.0.0.1:2620/`。)*

3. **可选：独立托管 WebUI**
如果你仍希望将前端独立部署，可继续使用 `webui/dist` 通过 Nginx 等静态服务托管。

---

## 🏗️ 设计与实现
想了解 Resin 的底层并发调度细节、内存缓存设计，强烈建议阅读详尽的开发者文档：[DESIGN.md](./DESIGN.md)。

---
> 🌟 如果本框架解决了您的痛点，欢迎点击右上角留下一个 Star ⭐️ 支持！
