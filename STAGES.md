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



阶段 4：健康管理、主动/被动探测、GeoIP

先阅读 DESIGN.md：
- 节点健康管理（RecordResult / RecordLatency / UpdateNodeEgressIP、熔断恢复、TD-EWMA）
- 主动探测/被动探测
- GeoIP 服务
- 其他细节（下载失败重试抽象）

目标：
实现节点健康闭环与 GeoIP 能力，确保平台可路由视图可动态收敛。

任务：
1. 实现健康管理接口与节点状态变更（failure count、circuit、latency table、egress ip）。
2. 实现 TD-EWMA 更新逻辑与首条延迟触发脏更新。
3. 实现 ProbeManager：出口探测 + 主动延迟探测 + 并发信号量控制。
4. 实现下载重试抽象（先直连失败后尝试代理重试）。
5. 实现 GeoIP 下载更新、原子替换、Lookup 接口。
6. 把探测结果正确反馈到 RecordResult/RecordLatency/UpdateNodeEgressIP。

约束：
- 熔断状态只能由健康管理接口改写。
- 探测失败不能阻塞主流程。

交付与验收：
- 连续失败达到阈值会熔断并移出可路由集合。
- 成功反馈可恢复节点并重新参与路由。
- RegionFilters 能基于 GeoIP 正确过滤。
- 提供探测与熔断恢复测试。



阶段 5：路由与租约系统

先阅读 DESIGN.md：
- 节点路由策略（随机、粘性、P2C、同 IP 轮换）
- 路由数据结构
- 租约生命周期与过期清理机制

目标：
实现 `(platformID, account, targetDomain)` 路由核心逻辑与租约管理。

任务：
1. 实现路由入口标准化（平台名尽早映射为 platformID）。
2. 实现平台租约表与 IPLoadStats。
3. 实现随机路由（P2C 二选一 + score 公式 + 可比延迟规则）。
4. 实现粘性路由（租约命中/过期/失效处理）。
5. 实现同 IP 轮换与回退随机分配逻辑。
6. 实现租约后台过期清理服务（固定周期）。

约束：
- Account 为空时只走随机，不创建租约。
- 视图为空时明确失败，不额外做可用性扫描。

交付与验收：
- 同账号在 TTL 内出口 IP 粘性稳定。
- 节点失效/出口变化时按设计执行轮换或重分配。
- IPLoadStats 与租约增删一致。
- 提供路由决策矩阵测试。



阶段 6：代理数据面（Forward/Reverse）与错误规范

先阅读 DESIGN.md：
- 业务身份映射
- 反向代理自动提取 Account
- 代理错误处理（状态码 + X-Resin-Error）
- 正向代理 CONNECT 特殊行为

目标：
实现可用的正向/反向代理入口，并严格符合错误规范。

任务：
1. 实现正向代理认证解析：`Proxy-Authorization: Basic PROXY_TOKEN:Platform:Account`。
2. 实现反向代理 URL 解析与 Account Header Rules 匹配（含 fallback 规则）。
3. 实现统一代理错误响应封装（状态码、`X-Resin-Error`、text/plain body）。
4. 接入阶段 5 路由，完成上游转发。
5. 正向 CONNECT 成功后正确进入隧道转发。
6. 接入被动反馈：网络成功/失败与 TLS 延迟上报。

约束：
- API 层与代理层不直接写领域状态，必须调用 service/manager。
- 错误码命名与行为必须对齐 DESIGN.md。

交付与验收：
- 文档列出的错误场景全部可复现并返回正确响应。
- Forward/Reverse 端到端可代理请求。
- CONNECT 行为符合规范（建立后不再返回 HTTP 语义错误）。




阶段 7：WebAPI 控制面完整化

先阅读 DESIGN.md 的 WebAPI 全章节。

目标：
实现完整控制面 API，字段、鉴权、错误码、PATCH 语义与文档一致。

任务：
1. 实现 Admin Token 鉴权中间件（`/healthz` 例外）。
2. 实现通用能力：分页、排序、错误包装、JSON Merge Patch。
3. 实现模块端点：system、platform、subscription、account header rules、nodes、leases、request logs、geoip、dashboard metrics。
4. 实现 action 端点：重建视图、刷新订阅、触发探测、释放租约、GeoIP 立即更新等。
5. 实现字段校验、枚举校验、只读字段保护、冲突处理。
6. 编写 API 契约测试（重点：错误码、状态码、返回体结构）。

约束：
- 禁止在 handler 写复杂业务逻辑。
- `platform_id` 等 ID 类型和格式按文档执行。

交付与验收：
- 关键端点契约测试通过。
- 文档定义的最小错误码映射覆盖到位。
- 输出 API 覆盖率与未覆盖项清单。





阶段 8：观测与启动恢复闭环

先阅读 DESIGN.md：
- 结构化请求日志
- 数据统计（MetricsManager / metrics.db）
- 启动恢复流程（BootstrapLoader）

目标：
完成运维可观测能力与重启恢复闭环，形成可上线最小可用版本。

任务：
1. 实现 requestlog 异步队列、批量刷盘、分库滚动、旧库清理、payload 截断。
2. 实现 MetricsManager 事件入口与实时 ring buffer。
3. 实现 bucket 聚合与 `metrics.db` 持久化（upsert 幂等）。
4. 实现 dashboard 查询接口所需统计读取。
5. 实现 BootstrapLoader 全流程恢复顺序与后台服务分批启动。
6. 实现优雅退出 flush（cache/log/metrics）。

约束：
- 日志与统计失败不能影响代理主流程。
- 重启恢复必须遵循 DESIGN.md 指定顺序。

交付与验收：
- 重启后核心状态可恢复并继续服务。
- 日志队列满时按策略丢弃但主链路不阻塞。
- metrics 落库可重试且幂等。
- 提供一次完整冷启动->运行->重启->验证的集成测试报告。
