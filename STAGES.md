阶段 1：工程骨架与配置系统

在仓库根目录工作。先阅读 DESIGN.md 的以下章节：
- 项目概述
- Resin 全局设置
- WebAPI > 健康检查 / 系统信息 / 获取全局配置

目标：
实现可运行的工程骨架与配置系统，为后续阶段提供稳定基线。

任务：
1. 建立/整理 Go 项目结构（建议 cmd/resin, internal/config, internal/server, internal/api）。
2. 实现环境变量配置加载（含默认值）与校验，覆盖 DESIGN.md 中环境变量项。
3. 定义运行时全局配置模型（可热更新字段先建模，不必全部实现更新逻辑）。
4. 实现最小 HTTP 服务：`GET /healthz` 无鉴权返回 `{"status":"ok"}`。
5. 实现 `GET /system/info` 与 `GET /system/config` 的最小只读版本（可先返回内存配置）。
6. 补齐单元测试（配置解析、默认值、非法值校验、healthz）。

约束：
- 不实现业务路由、节点管理、代理逻辑。
- API 层不写业务逻辑，保留 service interface。

交付与验收：
- `go test ./...` 通过。
- 服务可启动，`/healthz` 返回 200。
- 输出：变更文件列表、架构说明、已知未实现项。



阶段 2：持久化内核（StateEngine + SQLite）

先阅读 DESIGN.md 的以下章节：
- 持久化系统（持久化需求、总体架构、SQLite 数据模型、写入语义、一致性修复、启动恢复流程）

目标：
实现单写入口 StateEngine、state.db/cache.db、脏集合刷盘机制。

任务：
1. 设计并实现 StateEngine 作为唯一写入口（强持久化与弱持久化分流）。
2. 实现 `state.db` 与 `cache.db` 建表与仓储层（repo）。
3. 强持久化：系统配置、platform、subscription、account_header_rules 走事务提交后返回成功。
4. 弱持久化：nodes/leases/subscription_nodes/node_latency 用脏集合 + flush worker 批量写入。
5. 实现一致性修复 SQL（按 DESIGN.md 的孤儿清理规则）。
6. 实现启动时数据库初始化与一致性修复入口。
7. 为 repo 与 StateEngine 编写测试（含 upsert、delete、重启加载、幂等写入）。

约束：
- 禁止业务模块直接写 DB。
- 不实现 migration（按设计要求）。

交付与验收：
- `go test ./...` 通过。
- 能演示：写入配置/平台后重启仍存在；弱持久化数据按阈值或周期落盘。
- 输出：schema、事务语义、flush 触发策略说明。



阶段 3：订阅与节点管理主链路

先阅读 DESIGN.md：
- 节点管理（订阅视图、全局节点池、Platform 可路由视图）
- 订阅（结构、解析器、更新、改名、Ephemeral 清理）
- 节点唯一标识、Tag、热路径/冷路径设计决策

目标：
打通“订阅 -> 节点池 -> 平台可路由视图”的核心链路。

任务：
1. 实现 Subscription 模型与 ManagedNodes 视图。
2. 实现 SingboxSubscriptionParser（解析 outbounds，生成节点 raw options）。
3. 实现 NodeHash（忽略 tag 的规范化 JSON xxhash128）。
4. 实现 GlobalNodePool 与 `AddNodeFromSub/RemoveNodeFromSub` 幂等逻辑。
5. 实现 NodeEntry.MatchRegexs（基于订阅视图反查 `<sub>/<tag>`）。
6. 实现 Platform 可路由视图（全量重建 + 脏更新接口）。
7. 实现订阅更新调度与订阅改名重过滤触发。
8. 实现 Ephemeral 节点后台清理逻辑。

约束：
- 热路径不得全量过滤节点。
- 平台视图外部不可直接写入。

交付与验收：
- 订阅更新后可正确增删节点引用。
- 相同节点配置跨订阅可去重。
- 平台过滤（regex/region/状态）结果正确。
- 提供并发安全测试与幂等测试。



阶段 4：健康管理、探测与 GeoIP（基础能力）

先阅读 DESIGN.md：
- 节点健康管理（RecordResult / RecordLatency / UpdateNodeEgressIP、熔断恢复、TD-EWMA）
- 主动探测/被动探测
- GeoIP 服务
- 附录的 GeoIP 查询实现

目标：
实现节点主动健康闭环与 GeoIP 基础能力，确保平台可路由视图可动态收敛。

任务：
1. 实现全局节点池健康接口作为唯一状态入口：`RecordResult` / `RecordLatency` / `UpdateNodeEgressIP`，并在熔断变更、首条延迟、出口 IP 变化时触发 Platform 脏更新。
2. 实现 TD-EWMA（按 eTLD+1 分桶）与延迟表上限控制（`RESIN_MAX_LATENCY_TABLE_ENTRIES`）。
3. 实现 ProbeManager 调度：13-17 秒扫描、未来 15 秒预检查窗口、新节点入池后立即触发一次出口探测。
4. 实现主动探测两类任务：出口探测（cloudflare trace）与主动延迟探测（LatencyTestURL + 权威域名时效规则）。探测执行层通过可注入 Fetcher/Transport 抽象接入，不在本阶段落地独立 Outbound 创建系统。
5. 实现全局并发信号量与背压（并发满载时调度阻塞等待）。
6. 实现 GeoIP 更新：启动 mtime 检查 + CRON 调度 + latest release 下载 + SHA256 校验 + 原子替换 + `Lookup`。
7. 抽象下载器接口（供 GeoIP/订阅复用），完成直连路径与失败可观测性；本阶段不接入“经 Default 平台随机节点重试”。
8. 将探测结果按设计回写健康接口：成功写 `RecordResult(true)`，失败写 `RecordResult(false)`，并回写延迟与出口 IP。
9. （承接阶段 3 Review）修复 Ephemeral 清理与健康恢复 TOCTOU：清理执行前二次校验，避免恢复后误驱逐（使用二次校验实现简单，并容忍微小竞态窗口）；补充并发回归测试。仅修复并发正确性，不重写阶段 3 已实现的 Ephemeral 清理算法与外部行为。

约束：
- 熔断状态只能由健康管理接口改写。
- 探测失败不能阻塞主流程。
- 下载器抽象不得在 GeoIP 与订阅模块重复实现。
- 本阶段仅定义探测执行抽象，不得实现与后续阶段并行演进的第二套 Outbound 管理系统。
- 本阶段允许 Probe 使用直连/占位 Fetcher；不要求“按节点真实出网”探测。若节点 Outbound 尚未创建（nil）可跳过主动探测，不记为失败。
- 阶段 3 已对外暴露的订阅/节点接口语义保持不变；本阶段仅补内部一致性与并发正确性。

交付与验收：
- 连续失败达到阈值会熔断并移出可路由集合。
- 成功反馈可恢复节点并重新参与路由。
- RegionFilters 能基于 GeoIP 正确过滤（含“无出口 IP + 非空 RegionFilters 不通过”）。
- 提供探测、熔断恢复、TOCTOU 回归测试。
- GeoIP 下载的 SHA256 校验失败时，不得替换现有 `geoip.db`。
- GeoIP 原子替换完成后，`Lookup` 立即可用且能返回新库结果。
- GeoIP 与订阅下载复用同一下载器抽象（单实现），阶段 4 仅验证直连下载路径。
- Probe 对执行层依赖接口化，不引入与阶段 5/6 重复的 Outbound 创建与生命周期管理实现。
- 阶段 4 不验收“节点级真实链路探测准确性”（包括按节点出口 IP 真实性）；该能力在阶段 5 统一节点出站执行能力落地后验收（可由 OutboundManager 生命周期管理 + 共享执行抽象实现）。



阶段 5：路由与租约系统

先阅读 DESIGN.md：
- 节点路由策略（随机、粘性、P2C、同 IP 轮换）
- 路由数据结构
- 租约生命周期与过期清理机制

目标：
实现 `(platformID, account, targetDomain)` 路由核心逻辑与租约管理。

任务：
1. 实现路由入口标准化：外部平台名在入口即映射为 `platformID`；平台未提供时落到 Default。
2. 实现 `targetDomain` 提取（`ExtractDomain`，eTLD+1/IP/localhost 兜底）。
3. 实现每 Platform 的租约表与 IPLoadStats，并保证租约增删改与计数原子一致。
4. 实现随机路由（P2C 二选一 + score 公式 + “仅同口径可比延迟才比较延迟”规则）。
5. 实现粘性路由：命中时更新 `LastAccessed`；`Expiry` 固定不续期；过期即删除并重建租约。
6. 实现租约失效处理：节点不可路由或出口 IP 变化时先走同 IP 轮换，失败则释放旧租约并随机新分配（新 `Expiry`）。
7. 实现租约后台过期清理（13-17 秒周期）并同步回收 IPLoadStats。
8. 实现统一节点 Outbound 生命周期管理（OutboundManager）与内部下载/探测传输通道（不依赖 Forward/Reverse 代理服务）：支持“选中节点 -> 发起 HTTP 下载请求”的最小能力，并作为 Probe/GeoIP/订阅下载/阶段 6 数据面的统一节点执行能力基础；同时承接阶段 4 的占位探测执行，落地“按节点真实出网”探测。
9. 在阶段 4 下载器抽象基础上补齐 GeoIP/订阅下载代理重试闭环：先直连，失败后经 Default 平台随机节点立刻重试 2 次（无退避）。
10. 提供 lease 事件接口（lease create/replace/remove/expire）。

约束：
- Account 为空时只走随机，不创建租约。
- 视图为空时明确失败，不额外做可用性扫描。
- 同 IP 轮换属于“就地更新租约”时不得改写 `Expiry`。
- 下载代理重试属于系统内部流量，不创建租约，不影响业务粘性状态。
- 不得为 Probe/下载/数据面分别实现不同的节点 Outbound 创建/生命周期系统；执行路径可在不同模块调用同一批共享执行抽象（如统一 Dial/HTTP 执行库），不强制所有请求都经 OutboundManager 暴露专用接口。

交付与验收：
- 同账号在 TTL 内出口 IP 粘性稳定。
- 节点失效/出口变化时按设计执行轮换或重分配。
- IPLoadStats 与租约增删一致。
- GeoIP/订阅下载在直连失败后可经 Default 平台随机节点完成重试。
- 提供路由决策矩阵测试（含可比延迟/不可比延迟、同 IP 轮换命中/未命中）。



阶段 6：代理数据面（Forward/Reverse）与错误规范

先阅读 DESIGN.md：
- 业务身份映射
- 反向代理自动提取 Account
- 代理错误处理（状态码 + X-Resin-Error）
- 正向代理 CONNECT 特殊行为

目标：
实现可用的正向/反向代理入口，并严格符合错误规范。

任务：
1. 实现正向代理认证与身份解析：`Proxy-Authorization: Basic PROXY_TOKEN:Platform:Account`，严格按“第一个冒号切分”规则处理 `Platform/Account`。
2. 实现反向代理路径解析：`/PROXY_TOKEN/Platform:Account/protocol/host/path?query`，并处理 URL 解析失败分支。对 `Account` 含 `/` 等导致的路径分段错位，不强制特定错误码，能明确返回解析类错误即可（如 `URL_PARSE_ERROR` / `INVALID_PROTOCOL` / `INVALID_HOST`）。
3. 实现 Account Header Rules 最长前缀匹配（domain 大小写不敏感、按路径段匹配、`*` 兜底）与 `ReverseProxyMissAction`（RANDOM/REJECT）。
4. 接入阶段 5 路由与统一节点出站执行能力（OutboundManager 生命周期管理 + 共享执行抽象），覆盖正向 HTTP、正向 CONNECT、反向代理三种数据面路径。
5. 实现统一代理错误响应：HTTP 状态码 + `X-Resin-Error` + `text/plain`；正向鉴权失败补 `Proxy-Authenticate`。
6. 实现 CONNECT 成功后隧道语义：返回 `200 Connection Established` 后不再返回 HTTP 语义错误。
7. 实现被动反馈并异步上报：连通性 `RecordResult` 与 TLS 延迟（CONNECT 用 `tlsLatencyConn`，反向代理用 `httptrace`）。
8. 在代理链路埋点并通过接口发出 requestlog/metrics 事件（实现可为 no-op 适配层）。

约束：
- API 层与代理层不直接写领域状态，必须调用 service/manager。
- 错误码命名与行为必须对齐 DESIGN.md。
- 被动反馈与埋点不可阻塞请求主链路。
- 不得新建第二套 Outbound 创建/生命周期系统；阶段 6 仅在阶段 5 的统一节点出站执行能力上扩展数据面能力，不强制通过 OutboundManager 暴露数据面专用请求接口。

交付与验收：
- 文档列出的错误场景全部可复现并返回正确响应。
- Forward/Reverse 端到端可代理请求。
- CONNECT 行为符合规范（建立后不再返回 HTTP 语义错误）。
- 被动健康反馈在高并发下不回压主请求路径。




阶段 7：WebAPI 控制面（核心资源）

先阅读 DESIGN.md 的 WebAPI 全章节。

目标：
实现核心控制面 API（不含 request logs / dashboard metrics），先完成与现有领域能力同阶段对齐的端点。

任务：
1. 实现 Admin Token 鉴权中间件（`/healthz` 例外）。
2. 实现通用能力：分页、排序、错误包装、JSON Merge Patch。
3. 实现模块端点：system、platform、subscription、account header rules、nodes、leases、geoip。
4. 实现 action 端点：重建视图、刷新订阅、触发探测、释放租约、GeoIP 立即更新等。
5. 实现 `PATCH /system/config` 完整热更新链路：JSON Merge Patch -> 字段/枚举/类型校验 -> 通过 StateEngine 强持久化 -> 运行时原子替换。
6. 实现运行时配置分发：维护线程安全配置快照，依赖模块读取最新配置（至少覆盖请求日志开关与截断阈值、路由/延迟参数、缓存刷盘参数）。
7. 实现字段校验、枚举校验、只读字段保护、冲突处理。
8. 编写 API 契约测试（重点：错误码、状态码、返回体结构、PATCH 语义）。
9. 编写热更新行为测试：更新后无需重启即可生效，非法 patch 不得产生部分生效。

约束：
- 禁止在 handler 写复杂业务逻辑。
- `platform_id` 等 ID 类型和格式按文档执行。
- `PATCH /system/config` 必须全量原子生效（校验失败或持久化失败时，内存与数据库都不改变）。

交付与验收：
- 关键端点契约测试通过。
- 文档定义的最小错误码映射覆盖到位。
- `PATCH /system/config` 支持 DESIGN.md 允许的可改字段，且拒绝未声明字段与 `null` 值。
- 热更新生效可验证：至少覆盖 `request_log_enabled`、`reverse_proxy_log_req_headers_max_bytes`、`p2c_latency_window`、`cache_flush_interval`。
- 热更新后重启，配置仍保持为更新后的值。
- 输出 API 覆盖率与未覆盖项清单（仅核心资源）。





阶段 8：观测、恢复闭环与剩余 API

先阅读 DESIGN.md：
- 结构化请求日志
- 数据统计（MetricsManager / metrics.db）
- 启动恢复流程（BootstrapLoader）

目标：
完成运维可观测能力与重启恢复闭环，形成可上线最小可用版本。

任务：
1. 实现 requestlog 异步队列、批量刷盘、分库滚动、旧库清理、payload 截断。
2. 将 requestlog 接到代理 `defer` 记录点，完成摘要与 payload 双表写入链路。
3. 实现 MetricsManager（事件入口 + realtime ring buffer + bucket 聚合 + `metrics.db` upsert）。
4. 回补事件接线：代理数据面、ProbeManager、租约管理分别发出 Request/Traffic/Connection/Probe/Lease 事件。
5. 实现剩余 API：`/request-logs*` 与 `/metrics*`（实时、历史、快照）查询端点。
6. 实现 BootstrapLoader 全流程恢复与分批启动顺序（严格对齐 DESIGN.md 第 1-8 步）。
7. 实现优雅退出 flush（cache/log/metrics）与容错（失败不影响主流程）。
8. 补齐冷启动/重启集成测试：`启动 -> 运行 -> 重启 -> 校验`。

约束：
- 日志与统计失败不能影响代理主流程。
- 重启恢复必须遵循 DESIGN.md 指定顺序。

交付与验收：
- 重启后核心状态可恢复并继续服务。
- 日志队列满时按策略丢弃但主链路不阻塞。
- metrics 落库可重试且幂等。
- `/request-logs*` 与 `/metrics*` 契约与查询语义对齐 DESIGN.md。
- 提供一次完整冷启动->运行->重启->验证的集成测试报告。
