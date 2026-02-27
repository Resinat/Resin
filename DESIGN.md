# Resin 项目说明

## 项目概述

本项目旨在构建一个高性能、智能化的 10k~100k 规模节点代理池管理系统。与传统的 IP 轮询池不同，本项目引入了 **“平台(Platform)”** 与 **“账号(Account)”** 的业务概念，将底层的机场订阅、零散节点聚合成一个支持**会话保持（粘性路由）** 的 http 流量网关。

系统支持正向代理与反向代理两种接入模式，能够根据业务需求自动维护 IP 租约，屏蔽底层节点的复杂性，为上层多账号运营业务提供稳定、可控的网络环境。

## Platform 定义了什么
* ID：全不可变 UUID，作为主键。
* Name：平台名，全局唯一。
* StickyTTL: time.Duration，该平台的粘性租约寿命。
* RegexFilters：一个正则表达式列表。按照节点的 Tag 的正则表达式过滤器。同时满足所有过滤器才符合条件。
* RegexFiltersCompiled：编译后的正则表达式列表。用于运行时匹配。随着 RegexFilters 更新。
* RegionFilters：一个地区列表。小写 ISO codes (e.g., "hk", "us")。节点的出口 IP 地区属于该列表才符合条件。空表示不做地区筛选。
* 反向代理 Account 为空时的行为：随机路由 / 固定 Header 提取 / 按 Account Header Rule 提取。
* 反向代理匹配 Account 失败后的行为：随机路由 或 拒绝请求。默认是随机路由。
* 分配新节点的策略：偏好低延迟、偏好闲置 IP、均衡。默认是均衡。

> 默认平台：系统有一个名为 Default 的平台。该平台不可删除，数据库不存在时自动创建。

每个 Platform 会拥有自己的可路由节点池。每个 Platform 的请求是从这个节点池里挑节点路由的。

## 业务身份如何稳定映射到网络身份
### 需求
同一业务账号要在一段时间内保持出口 IP 一致，不同账号要尽量隔离；同时客户端接入形式不统一（正向代理与反向代理并存）。

### 设计目标
把所有请求统一归一为 `Platform + Account` 上下文，在不暴露节点细节的前提下实现粘性路由。

### 设计决策
系统把路由输入统一为 `(platformID, account, targetDomain)`：  
注意：外部接口（HTTP 头、反向代理路径）通常使用 Platform Name 作为输入，但在入口处应立即通过 Name -> ID 映射转换为 ID。后续逻辑只认 ID，不认 Name。
1. 正向代理通过 `Proxy-Authorization: Basic PROXY_TOKEN:Platform:Account` 同时做代理认证与业务身份解析。PROXY_TOKEN 字段用于代理认证。Platform:Account 提供业务身份。PROXY_TOKEN 映射到 Basic 认证的 user 字段，Platform:Account 映射到 pass 字段。解析 pass 时按**第一个** `:` 切分为 `Platform` 与 `Account`。PROXY_TOKEN 与 Platform 不能包含 : @ 符号。Account 不限制符号。该约束属于解析约定，控制面不对 `:` 与 `@` 做额外校验。
2. 反向代理通过路径 `/PROXY_TOKEN/Platform:Account/protocol/host/path?query` 解析；其中 `Platform:Account` 必须是单个路径段。当 `Account` 为空时，按平台配置 `ReverseProxyEmptyAccountBehavior` 处理：`RANDOM`（直接随机）、`FIXED_HEADER`（按 `ReverseProxyFixedAccountHeader` 列表提取）、`ACCOUNT_HEADER_RULE`（按全局规则提取）。`ReverseProxyFixedAccountHeader` 支持多行，每行一个 Header，按顺序取第一个非空值。protocol 可选 `http/https`；host 可以是域名，可以是 IP，可以加端口。若 `Account` 含 `/` 导致路径分段变化，按反向代理 URL 解析失败处理；该类 malformed 输入不要求固定到某一个错误码，只要返回明确的解析类错误即可（如 `URL_PARSE_ERROR` / `INVALID_PROTOCOL` / `INVALID_HOST`）。
3. 当 Platform 未提供，默认使用 Default 平台。当正向代理的 Account 未提供，默认使用平台内的随机路由。

正向代理例子：
* `resin-123456:Google:Tom`：ProxyToken 是 resin-123456；Platform 是 Google；Account 是 Tom。
* `resin-123456::Tom`：ProxyToken 是 resin-123456；Platform 为空；Account 是 Tom。
* `resin-123456:Google`：ProxyToken 是 resin-123456；Platform 是 Google，Account 为空。
* `resin-123456:Google:`：ProxyToken 是 resin-123456；Platform 是 Google，Account 为空。
* `resin-123456:MyHub:bEA:234`：ProxyToken 是 resin-123456；Platform 是 MyHub；Account 是 bEA:234。


## 反向代理如何在 Account 为空时处理
当反向代理路径里未提供 Account 时，每个平台独立配置 `ReverseProxyEmptyAccountBehavior`：
* `RANDOM`：不做提取，直接按平台随机路由。
* `FIXED_HEADER`：使用该平台的 `ReverseProxyFixedAccountHeader`（多行列表，每行一个 Header）提取。
* `ACCOUNT_HEADER_RULE`：使用全局 Account Header Rules（下文）做最长前缀匹配后提取。

当行为为 `ACCOUNT_HEADER_RULE` 时，全局配置中的“Account Header 表”生效。每个记录结构是：(URL, 请求头列表)。例如：
* api.example.com/v1, ["Authorization"]
* api.example.com/v2, ["x-api-key"]
* "*", ["Authorization", "x-api-key"]

匹配算法是按段匹配。
* 首先匹配 domain 字段。此部分不区分大小写，统一归一化到小写进行匹配。
* 然后匹配 URL 后面被 / 隔开的每一段。

为了提升匹配性能，使用前缀哈希表进行匹配。

"*" 就是最后的兜底。一个条目可以包含多个请求头，意味着可以进行猜测。依次查询请求头的值，第一个存在且非空的被采用。
当提取模式为 `FIXED_HEADER` 或 `ACCOUNT_HEADER_RULE` 且提取失败时，根据 Platform 的 `ReverseProxyMissAction` 配置，决定是做随机路由还是拒绝本次代理。

URL 不允许包含查询部分与 ? 字符。

设计决策提取出来的数据不做脱敏处理。


## 代理错误处理

当代理请求处理过程中遇到错误时，Resin 需要向客户端返回明确的错误信息。以下定义了所有代理错误场景的响应规范。

### 错误响应格式
代理错误使用标准 HTTP 状态码，同时附加 `X-Resin-Error` 响应头用于程序化识别错误类型。

* **HTTP Status Code**：标准 HTTP 状态码，见下文各场景定义。
* **X-Resin-Error**：自定义响应头，值为错误类型的标识字符串（如 `AUTH_FAILED`、`URL_PARSE_ERROR`）。
* **Body**：纯文本 (text/plain)，简短的人类可读错误描述。

### 认证阶段错误

| 场景 | HTTP Code | X-Resin-Error | 说明 |
|------|-----------|---------------|------|
| PROXY_TOKEN 缺失或格式错误 (正向代理) | 407 Proxy Authentication Required | `AUTH_REQUIRED` | 正向代理未提供 `Proxy-Authorization` 头或格式不正确。响应附加 `Proxy-Authenticate: Basic realm="Resin"` 头。 |
| PROXY_TOKEN 校验失败 (正向代理) | 403 Forbidden | `AUTH_FAILED` | 提供的 Token 格式正确但校验不通过（如未知 Token）。 |
| PROXY_TOKEN 校验失败 (反向代理) | 403 Forbidden | `AUTH_FAILED` | 反向代理路径中缺少 Token 或 Token 不匹配。 |

### 反向代理 URL 解析错误

反向代理路径格式为 `/PROXY_TOKEN/Platform:Account/protocol/host/path?query`。如果路径解析失败，在认证通过后返回以下错误：

| 场景 | HTTP Code | X-Resin-Error | 说明 |
|------|-----------|---------------|------|
| 路径为空 | 400 Bad Request | `URL_PARSE_ERROR` | 路径去掉 Token 后为空，缺少必要的路由信息。 |
| 路径段不足 | 400 Bad Request | `URL_PARSE_ERROR` | 路径至少需要 `protocol/host` 两段；缺少任一段则拒绝。 |
| protocol 无效 | 400 Bad Request | `INVALID_PROTOCOL` | protocol 必须为 `http` 或 `https`。 |
| host 无效 | 400 Bad Request | `INVALID_HOST` | host 字段为空或包含非法字符。 |

说明：当输入本身已发生路径分段错位（例如 `Account` 含 `/`）时，实现可在不同校验阶段失败，因此不强制返回某一个固定错误码；返回上述任一解析类错误都视为符合要求。

### 路由阶段错误

| 场景 | HTTP Code | X-Resin-Error | 说明 |
|------|-----------|---------------|------|
| Platform 不存在 | 404 Not Found | `PLATFORM_NOT_FOUND` | 指定的 Platform Name 找不到对应的 Platform。 |
| Account 匹配失败且策略为 REJECT | 403 Forbidden | `ACCOUNT_REJECTED` | 反向代理中 Account 提取/匹配失败，且该 Platform 的 `ReverseProxyMissAction` 配置为 `REJECT`。 |
| 无可用节点（可路由集合为空） | 503 Service Unavailable | `NO_AVAILABLE_NODES` | Platform 的可路由节点池为空，无法分配节点。 |

### 上游连接/请求错误

| 场景 | HTTP Code | X-Resin-Error | 说明 |
|------|-----------|---------------|------|
| Dial 上游失败（正向代理 CONNECT） | 502 Bad Gateway | `UPSTREAM_CONNECT_FAILED` | 通过节点拨号连接目标失败（连接被拒、网络不可达等）。同时触发 `RecordResult(false)`。 |
| 上游连接超时 | 504 Gateway Timeout | `UPSTREAM_TIMEOUT` | 连接上游或等待上游响应超时。同时触发 `RecordResult(false)`。 |
| 上游请求失败（正向代理 HTTP） | 502 Bad Gateway | `UPSTREAM_REQUEST_FAILED` | 通过节点转发 HTTP 请求失败（如读写错误）。同时触发 `RecordResult(false)`。 |
| 上游请求失败（反向代理） | 502 Bad Gateway | `UPSTREAM_REQUEST_FAILED` | 反向代理通过 `httputil.ReverseProxy` 的 `ErrorHandler` 回调捕获的上游错误。同时触发 `RecordResult(false)`。 |

### 正向代理 CONNECT 特殊行为
正向代理的 CONNECT 请求在成功建立隧道后，返回 `HTTP/1.1 200 Connection Established`。此后 Resin 不再介入 HTTP 语义——连接变为双向 TCP 隧道。隧道建立后的网络错误不会产生 HTTP 错误响应；连接将直接中断。

### 错误响应示例
正向代理认证失败：
```
HTTP/1.1 407 Proxy Authentication Required
Proxy-Authenticate: Basic realm="Resin"
X-Resin-Error: AUTH_REQUIRED
Content-Type: text/plain; charset=utf-8

Proxy authentication required
```

反向代理无可用节点：
```
HTTP/1.1 503 Service Unavailable
X-Resin-Error: NO_AVAILABLE_NODES
Content-Type: text/plain; charset=utf-8

No available proxy nodes
```


## 节点管理

### 三种节点视图

#### 订阅视图
* 结构：`xsync.Map<NodeHash, []string>`
* 职责：记录该订阅包含哪些节点，并保留节点的原始 Tags。注意这里是 Tags（复数），因为同一个订阅中可能存在多个配置完全相同（Hash 相同）但 Tag 不同的节点。
* 同步机制：
    * 当订阅更新时，构建一个新的 `newManagedNodes` 视图。
    * 比较 `newManagedNodes` 与旧视图的 **Hash Diff**（注意不是 Tag 集合的 Diff）。
    * 用原子指针替换 `ManagedNodes` 指向 `newManagedNodes`。
    * 对于刚刚得到的新增与不变的 Hash：调用全局节点池的 `AddNodeFromSub` 接口。（`AddNodeFromSub` 幂等）
    * 对于刚刚得到的移除的 Hash：调用全局节点池的 `RemoveNodeFromSub` 接口。（`RemoveNodeFromSub` 幂等）

    > 注意，要先用原子指针替换 `ManagedNodes` 指向 `newManagedNodes`。，再调用 `AddNodeFromSub` 与 `RemoveNodeFromSub`，来保证 `AddNodeFromSub` 与 `RemoveNodeFromSub` 触发的 Platform Tag 重新检查生效。
    

#### 全局节点池
* 结构：`xsync.Map<NodeHash, NodeEntry>`。NodeEntry 包含了节点的详细信息。
* 职责：维护每个节点的详细信息。作为系统状态的收敛点，保证节点管理的幂等性。
* 节点增删接口：
    * `AddNodeFromSub(node, subID)`: 如果节点存在，将 subID 加入引用集合；如果不存在，创建节点并将 subID 加入引用集合。新创建的节点默认进入熔断状态（`CircuitOpenSince = now`），需要后续 `RecordResult(true)`（通常由主动/被动探测触发）恢复为可路由。此操作注意给目标节点的 SubscriptionIDs 加锁。重复添加相同的 (node, subID) 对是幂等的。另外，每次调用 `AddNodeFromSub`，都要重新检查每个平台对这个节点的过滤。因为 `AddNodeFromSub` 可能引入新 Tag。
    * `RemoveNodeFromSub(nodeHash, subID)`: 将 subID 从引用集合移除。如果引用集合为空，则从全局池物理删除节点。此操作注意给目标节点的 SubscriptionIDs 加锁。重复删除是幂等的。调用不存在的 `nodeHash` 也不报错。另外，每次调用 `RemoveNodeFromSub`，都要重新检查每个平台对这个节点的过滤。因为 `RemoveNodeFromSub` 可能删除 Tag。

* NodeEntry 结构：
	* Outbound：atomic.Pointer[adapter.Outbound]，sing-box 的 Outbound 实例。可能为空，例如 Outbound 创建失败。空 Outbound 节点不可路由。
	* --- Dynamic information ---
	* FailureCount：连续失败的次数。
	* LatencyTable：`otter.Must(&otter.Options[string, DomainLatencyStats]{ MaximumSize: RESIN_MAX_LATENCY_TABLE_ENTRIES })` 类型，使用 eTLD+1 域名作为索引，记录各站点的节点延迟。使用 TD-EWMA 维护。
		* DomainLatencyStats 包含 Latency 与 LastUpdated 两个字段。
	* EgressInfo：netip.Addr 类型，节点的出口 IP。
	* LastEgressUpdate：最后一次成功更新出口 IP 的时间戳。
	* LastLatencyProbeAttempt：最后一次延迟探测尝试时间戳（主动/被动、成功/失败都会更新）。
	* LastAuthorityLatencyProbeAttempt：最后一次权威域名延迟探测尝试时间戳（主动/被动、成功/失败都会更新）。
	* LastEgressUpdateAttempt：最后一次出口 IP 探测尝试时间戳（成功/失败都会更新）。
  * CircuitOpenSince：空表示节点未熔断；非空则表示进入熔断状态，具体值是熔断开始的时间。
	* --- Static information ---
	* NodeHash：节点的 Hash。
	* RawOptions：节点的原始配置 JSON 数据。
	* LastError：节点的错误信息。例如 RawOptions JSON 解析错误、Outbound 启动失败等。
	* CreatedAt：节点的创建时间。指的是节点首次被添加到全局池的时间。程序重启不变。
	* SubscriptionIDs：一个 slice。表示持有该节点的订阅 ID 集合。
  * --- 方法 ---
	* MatchRegexs(regexes []*regexp.Regexp) bool：用于判断该节点是否符合给定的正则表达式列表。
        * 逻辑：遍历 `SubscriptionIDs`，找到这些订阅及其 View 中的 Tags。遍历的时候加读锁。
        * 只有当节点在**任意一个** Enabled 订阅下的**任意一个** Tag 满足**所有**正则表达式时，返回 true。Tag 匹配时使用 `<订阅名>/<Tag>` 的格式。

本项目不使用原生的 sing-box OutboundManager，而是实现上述高性能 Outbound Manager。

#### Platform 可路由视图
* 结构：`Custom Set：64 分片 * (RWMutex + Slice + Map)`
* 职责：维护当前 Platform *此刻* 可用的节点列表。
* 特质：支持 O(1) 的随机选取与 O(1) 的增删查。
* 过滤条件：
    1. 节点状态正常（非 Circuit Break）。
    2. 调用 `NodeEntry.MatchRegexs(Platform.RegexFilters)` 判断 Tag 是否匹配。
    3. 节点必须有出口 IP（无论 Platform 是否配置 `RegionFilters`）。
    4. 若 `RegionFilters` 非空，则节点出口 IP 地区必须符合 `RegionFilters`。
    5. 有至少一条延迟信息。
    6. Outbound 不为空
* 过滤源：遍历全局节点池中的所有 `NodeEntry`。

##### Platform 节点视图动态更新
动态更新分为脏更新与全量重建两类。
* 全量重建的执行步骤是：直接扫描全局节点池的所有节点，重新进行全量过滤重建。
* 脏更新是指某节点发生了新增/修改/删除，需要检查这个节点是否符合 Platform 的过滤条件。如果符合条件的，加入可路由视图。如果不符合条件，从可路由视图中移除。

Platform 应该向外提供一个脏更新的接口，用来通知脏节点。外部模块不应该直接写入 Platform 的可路由视图。

动态更新的时机：
	* 出口 IP 变更：当 `ProbeManager` 探测到节点出口 IP 发生变化（或从无到有）。属于脏更新。
	* 节点引用变更：当节点的 SubscriptionIDs 发生变化，可能会影响 MatchRegexs 的结果（因为 Tag 集合变了）。属于脏更新。
	* 熔断触发 / 恢复：属于脏更新。	
	* Platform 过滤器配置变更：全量重建。

### 订阅
Resin 从订阅中获取节点配置。

#### 订阅的结构
* ID：全不可变 UUID，作为主键。
* Name：订阅的名称，允许重名，许修改。
* URL：订阅的 URL
* UpdateInterval：订阅的更新间隔，最短 30 秒。
* Ephemeral：是否为临时订阅。临时订阅指的是，当节点被连续熔断超过该订阅的 EphemeralNodeEvictDelay 时间后，会从订阅中物理移除该节点（从而全局池的引用减 1）。
* EphemeralNodeEvictDelay：临时节点驱逐延迟（每个订阅独立配置）。仅对 Ephemeral=True 的订阅生效。默认 72 小时。
* Enabled：订阅是否启用。当订阅被禁用，会重建各 Platform 的可路由池，而不会把节点从全局池删除。
* CreatedAt：订阅创建时间
* LastChecked：订阅的最后检查时间
* LastUpdated：订阅的最后更新时间
* LastError：订阅的最后错误信息，主要指的是下载订阅内容的失败。节点解析失败不在此列。
* ManagedNodes：xsync.Map<NodeHash, []string>，即订阅视图。Value 是 Tag 列表。

#### 订阅解析器
订阅解析器的职责是从订阅中提取节点信息。虽然未来可能会引入更多类型的订阅解析器，但是现阶段 Resin 只计划引入一种订阅解析器 `SingboxSubscriptionParser`。该解析器从机场提供的 Singbox json 订阅解析 outbounds 字段，得到 socks、http、shadowsocks、vmess、trojan、wireguard、hysteria、vless、shadowtls、tuic、hysteria2、anytls、tor、ssh、naive 类型的节点，返回节点的原始 json 数据。

#### 订阅的更新
后台有一个订阅更新服务，每 13～17 秒扫描所有订阅。如果未来 15 秒内，订阅将达到或者已达到 UpdateInterval 时间没有更新（对比 LastChecked）会进行更新。
无论更新下载成功还是失败，都会更新 LastChecked。
如果更新下载失败，记录 LastError。现有节点保持不变。
如果更新下载成功，更新 LastUpdated，并执行以下逻辑：
1. 解析新下载的节点列表，构建 `newManagedNodes` (Hash -> Tags)。
2. 保存旧视图 `oldManagedNodes := ManagedNodes`，并比较 `newManagedNodes` 与 `oldManagedNodes` 的 **Hash Set**。
3. 原子替换 `ManagedNodes = newManagedNodes`。
4. 更新：
    * New & Keep: 对于 `newManagedNodes` 中存在的所有 Hash（包含新增的和保留的），调用 `GlobalNodePool.AddNodeFromSub(node, subID)`。
    * Delete: 对于 `oldManagedNodes` 中存在但 `newManagedNodes` 中不存在的 Hash，调用 `GlobalNodePool.RemoveNodeFromSub(hash, subID)`。

> 注意：对于 "Keep" 的节点，我们也调用 `AddNodeFromSub`。因为 `AddNodeFromSub` 是幂等的，多调用一次无妨。同时也增强了一致性维护。

#### 订阅改名
订阅允许改名。订阅改名后，要重新 `AddNodeFromSub` 一遍节点，来保证 Tag 过滤器重新过滤。

#### EphemeralNode 后台清理服务
每隔 13～17 秒扫描所有 Ephemeral = True 的订阅。扫描其节点。如果一个节点已经连续熔断超过该订阅的 `EphemeralNodeEvictDelay`，就将其从订阅的 ManagedNodes 中移除，并调用 `RemoveNodeFromSub` 减引用。


### 节点唯一标识
sing-box 原版使用节点的名字 Tag 作为节点的 ID。但 Resin 使用 Node Hash 作为节点的全局唯一 ID。
* 计算方式：对节点配置（`Options`）的规范化 JSON 进行 128 位 xxHash 计算。参考附录中的 HashFromRawOptions。
* 去重：计算 Hash 时会忽略 `tag` 字段。这意味着来自不同订阅、但配置内容（如 IP、端口、密钥等）完全相同的节点，会被视为同一个节点。
* 作用：在全局范围内实现节点去重，共享连接状态与健康监测信息。

### 节点的 Tag
全局池中的节点没有 Tag 的概念。Tag 是订阅视图下的概念。
Tag 的统一格式是 `<订阅名>/<原始 Tag>`。
Platform 过滤时，通过 `NodeEntry.MatchRegexs` 方法，反向查询 Referencing Subscriptions 的视图来获取 Tag。


### 由热路径 / 冷路径决定的设计决策
节点的路由是系统的热路径。绝对不能在路由的时候现场执行节点池全量筛选。而节点的增删、Platform 的过滤器的修改是冷路径。
因此，我们选择给每个 Platform 维护一个可路由视图。Platform 配置修改时，全量重建该集合；节点增删时，相应地增删该集合中的 Hash。这样就把过滤的开销从热路径移到了冷路径。


## 节点健康管理

### 健康管理接口
全局节点池提供一组线程安全的健康管理接口，供被动反馈与主动探测调用。这些接口是系统维护节点状态的唯一入口。

* `RecordResult(id NodeHash, success bool)`：提交节点的一次网络请求结果。
	* `success=true`：重置连续失败计数 (`FailureCount = 0`)。若节点当前处于熔断状态，立即恢复（`清空 CircuitOpenSince`）。
	* `success=false`：原子递增连续失败计数。若计数达到配置的阈值 (`MaxConsecutiveFailures`)，触发熔断（`CircuitOpenSince = 当前时间`）。
* `RecordLatency(id NodeHash, domain string, latency *Duration)`：提交节点对特定域名的延迟探测尝试。`latency=nil` 表示“仅记录本次探测尝试，不写延迟样本”；`latency!=nil` 时按 TD-EWMA 更新延迟统计。无论 `latency` 是否为空，都会更新 `LastLatencyProbeAttempt`，若域名属于 `LatencyAuthorities` 还会更新 `LastAuthorityLatencyProbeAttempt`。如果调用本次 `RecordLatency` 之前，节点的 `LatencyTable` 为空，需要通知各 Platform 重新过滤这个节点。
* `UpdateNodeEgressIP(id NodeHash, ip *netip.Addr)`：记录一次出口 IP 探测尝试并可选更新出口 IP。`ip=nil` 表示“仅记录尝试”；`ip!=nil` 时更新出口 IP（若变更则触发 Platform 脏更新）。无论 `ip` 是否为空，都会更新 `LastEgressUpdateAttempt`。

### 熔断与恢复机制
Resin 使用计数器熔断机制保护系统稳定性。
* 默认状态：新节点加入系统时默认是熔断状态。包括订阅同步创建的新节点，以及启动恢复时从 `nodes_static` 注入但尚未被 `nodes_dynamic` 覆盖的节点。（节点默认不熔断其实也能工作，因为进入平台路由池的条件还有“有出口”与“有延迟”，已经有这层兜底在了。把默认状态改成熔断其实是为了逻辑上更清晰。另外，把没准备好的节点与熔断分为一类。前端进行过滤的时候，不会在健康过滤器下看到“待测”状态）
* 熔断触发：仅由 `RecordResult(id, false)` 触发。当连续失败次数 >= 阈值时，节点进入熔断状态。熔断的节点会立即从所有 Platform 的可路由视图中移除，不再承载用户流量。
* 熔断恢复：熔断后的节点依然保留在全局池中，接受 ProbeManager 的主动探测。一旦 `RecordResult(id, true)` 被调用（通常由主动探测触发），节点立即恢复，重新加入可路由视图。
* 熔断逻辑由全局代理池管理。禁止其他模块直接修改节点的熔断状态。

### 延迟统计 (TD-EWMA)
为了平滑网络抖动并保留时效性，系统使用 **TD-EWMA** 算法维护延迟。
* 多维度记录：延迟数据按 **域名 (eTLD+1)** 分桶记录在 `LatencyTable` 中。例如访问 Google 的延迟与访问 GitHub 的延迟是分开记录的。
* 时间衰减：更新延迟时，旧值的权重随时间指数衰减。公式逻辑为：
  $$ \text{Weight} = e^{-\frac{\Delta t}{\text{LatencyDecayWindow}}} $$
  $$ \text{NewEWMA} = \text{OldEWMA} \times \text{Weight} + \text{NewLatency} \times (1 - \text{Weight}) $$
* 延迟统计由全局代理池管理。禁止其他模块直接修改节点的延迟统计。

### 主动探测
#### 出口探测
* 目标：全局节点池所有节点。
* 职责：定期刷新节点的出口 IP 与地区信息，确保路由策略（如同 IP 关联、地区过滤）的准确性。
* 探测时机与调度策略：每隔 13～17 秒全局扫描一次。调度依据是 `LastEgressUpdateAttempt`：对未来 15 秒内将会或者已经超过 `MaxEgressTestInterval` 的节点进行探测。另外，新的节点加入全局节点池时，需要立即进行一次出口探测。
* 探测动作：通过节点请求 `https://cloudflare.com/cdn-cgi/trace` (GET)。
* 结果处理：
	* 记录本次网络请求的结果。调用 `RecordResult(id, true/false)`。
  * 如果成功，作为副作用，会更新对 Cloudflare 的延迟统计，调用 `RecordLatency(id, "cloudflare.com", &latency)`。
	* 无论成功或失败，都会调用 `UpdateNodeEgressIP` 记录尝试时间；成功时携带解析后的 `ip`，失败时传 `nil`。

#### 主动延迟探测
* 目标：全局节点池所有节点。
* 职责：确保节点对关键域名的延迟数据保持鲜活。
* 探测时机：以下情况，需要对一个节点进行主动延迟探测：
	* `LastLatencyProbeAttempt` 已超过 `MaxLatencyTestInterval`
	* `LastAuthorityLatencyProbeAttempt` 已超过 `MaxAuthorityLatencyTestInterval`
* 调度策略：每隔 13～17 秒全局扫描一次，对未来 15 秒内将会或者已经满足探测时机的节点进行探测。
* 探测动作：对全局配置的延迟探测站点发起 HTTP GET 请求，优先测量 **TLS Handshake** 耗时；若未产生 TLS 握手事件（如连接复用或明文 HTTP），回退为请求级 RTT（请求发起到首字节/请求完成）。
* 结果处理：
    * 成功：先调用 `RecordResult(true)`；调用 `RecordLatency(..., &latency)`（`latency<=0` 时仅记录尝试，不写样本）。
    * 失败：调用 `RecordResult(false)` 与 `RecordLatency(..., nil)`。连续失败将导致节点熔断。

#### 主动探测的并发控制
ProbeManager 采用 **SPSC (Single Producer Single Consumer)** 变体模型进行调度：
* **执行**：所有主动探测任务共享一个全局信号量（默认 1000 并发），异步执行。
* **背压**：受限于信号量，当并发满载时，调度协程会阻塞等待，天然形成了背压，防止创建过多 goroutine。

### 被动延迟探测

被动探测利用用户的真实业务流量来评估节点状态。相比主动探测，被动探测能提供更真实、更高频的反馈，且不消耗额外的网络资源。

#### 信号源
* **连通性信号**：
    * **正向代理**：`http.Client.Do` 或 `TcpDial` 的返回结果。
    * **反向代理**：`httputil.ReverseProxy` 的 `ErrorHandler` 回调。
    * 只要网络层建立连接成功（即便 HTTP 返回 500），通常视为节点健康（`RecordResult(true)`）；仅当网络握手失败、超时或连接被重置时，视为失败（`RecordResult(false)`）。
* **延迟信号 (TLS Handshake)**：
    * 绝大多数业务流量为 HTTPS。Resin 正向与反向代理均通过测量 **TLS Handshake RTT** 来评估节点延迟。
    * **正向代理 (CONNECT)**：劫持 `net.Conn`，记录从写入 Client Hello (首字节) 到收到 Server Hello (由于 TCP 流式特性，通常是首个读操作) 的时间差。参考附录中的 `tlsLatencyConn` 实现。
    * **反向代理**：使用 `httptrace` 钩子监听 `TLSHandshakeStart` 与 `TLSHandshakeDone`。
	* 对于非 HTTPS 协议，不进行延迟统计。

#### 采样与反馈
为避免阻塞数据链路，所有被动探测数据的记录均为**异步**执行。
* **采样率**：100%。由于被动反馈极其廉价（仅为内存计数与原子操作），系统对所有业务流量进行采样。
* **尝试时间戳更新规则**：
    * 普通站点访问：更新 `LastLatencyProbeAttempt`。
    * 权威站点访问：更新 `LastLatencyProbeAttempt` 与 `LastAuthorityLatencyProbeAttempt`。
    * 访问失败时仍会记录尝试（`RecordLatency(..., nil)`）。
* **反馈回路**：
    1. 流量经过代理。
    2. 代理捕获连接状态与握手耗时。
    3. 异步调用 `RecordResult` 与 `RecordLatency`。
    4. 全局节点池更新节点的 `FailureCount`、`CircuitOpenSince` 状态及 `LatencyTable`。

另外，如果节点的 Outbound 为空，跳过对其的探测，而不是记做失败。不能因为 Outbound 为空而把一个节点熔断。

## GeoIP 服务

Resin 内置了 GeoIP 服务，用于支持 Platform 的 RegionFilters 功能。

### 数据源与更新
* **数据格式**：使用通用 `mmdb` 格式（默认文件 `country.mmdb`）。
* **数据源**：默认从 `MetaCubeX/meta-rules-dat` GitHub Release 获取。下载到 Cache Dir。
* **自动更新**：
    * **调度**：支持 Cron 表达式配置更新时间（默认每天 07:00）。启动时检查 geoip 的 mtime 确认是否需要更新，启动后按照 CRON 更新。时区使用本地时间。
    * **原子性**：通过 `https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/latest`，下载到临时文件 -> 校验 SHA256 -> 原子 Rename 覆盖。
    * **可靠性**：下载失败时会自动尝试使用代理节点重试。
* **查询**：提供 `Lookup(ip netip.Addr) string` 接口，返回 ISO 3166-1 alpha-2 小写国家代码（如 "cn", "us", "hk"）。

## 节点路由策略
- 随机模式：从指定平台的节点池中，根据算法概率性选择一个节点。
- 粘性模式：
  - 逻辑：
    1. 初次请求（粘性路由表 Miss）：执行随机模式，记录分配节点的**出口 IP**。
    2. 后续请求（粘性路由表 Hit）：在租约有效期内，系统保证该 `[Platform:Account]` 的流量始终通过具有相同**出口 IP** 的节点转发。
  - 锚点：粘性锚定于“出口 IP”。如果多个节点出口 IP 相同，它们在粘性会话中可互换。
  - 租约释放：租约到期、节点不可达、或节点不再属于该平台时，属于路由表 Miss，旧租约释放。

### 路由数据结构

每个 Platform 维护自己**独立**的租约表。表结构：xsync.Map<Account, Lease>。Lease 包含：
* NodeHash：当前绑定的节点 Hash
* EgressIP：锚定的出口 IP
* Expiry：租约过期时间（UnixNano）。一个租约的过期时间固定，不会续期。
* LastAccessed：最后访问时间

另外，每个 Platform 维护一个出口 IP 租约数表 IPLoadStats。xsync.Map<EgressIP, int>。用于原子计数每个出口 IP 当前的租约数。当租约增加、删除的时候，都要相应更新 IPLoadStats。

### 路由整体流程
请求到来时，如果 Account 为空，走随机路由，并且不分配新租约表项；否则，查询租约表：
* 查到表项过期：删除表项，视为没查到。
* 没查到表项：使用随机路由算法，并分配一个新租约表项。
* 查到表项了，并且节点依然存在于 Platform 的可路由节点中，并且出口 IP 没变：使用这个节点。
* 查到表项了，但节点不存在于 Platform 的可路由节点中，或者节点的出口 IP 变了：使用同 IP 轮换，选个新节点，更新表项。


### 随机路由算法
随机模式采用 **P2C** 算法，结合延迟与负载进行加权选择。

#### 选择流程
若可路由集合为空：路由失败。
若可路由集合大小为 1，直接选择。
若可路由集合大小 >= 2：
3. 从 Platform 的可路由集合中随机挑选两个候选节点。
4. 比较两个候选节点的**综合评分**，选择评分较低者。
5. 我们信任 Platform 的可路由集合，不进行额外的节点可用性检查。当可路由集合大小为 0，路由失败，拒绝请求。

#### 综合评分公式
- **LeaseCount**：该节点出口 IP 在当前 Platform 被多少个活跃租约占用。从 IPLoadStats 获取。
- **Latency**：节点到目标域名的加权移动平均延迟（TD-EWMA），从节点的 LatencyTable 属性获取。
	- 如果这次要访问的目标站点在两个节点的 LatencyTable 中**都有记录**，且记录在 P2CLatencyWindow 时间内，两个节点的 Latency 值取 LatencyTable[目标站点]。
	- 否则，找到两个节点的 LatencyTable **共同都有**，且在 P2CLatencyWindow 时间内的权威站点，计算这些权威站点的平均延迟作为 Latency。
	- 如果上面都不满足，两个节点的 Latency 都为空。

> 也就是说，只在“两个候选都能算出同口径延迟”时，才用延迟比。只要不是共同可比（例如只有一个节点有数据），就把两者 Latency 视为空。

如果 Latency 为空，那么 $\text{Score} = \text{LeaseCount}$
如果 Latency 不为空，Score 取决于 Platform 的配置：
* 如果 Platform 配置的节点分配策略是“偏好低延迟”，那么 $\text{Score} = \text{Latency}$
* 如果是“偏好闲置 IP”，那么 $\text{Score} = \text{LeaseCount}$
* 如果“均衡”，那么 $\text{Score} = (\text{LeaseCount} + 1) \times \text{Latency}$

> Latency 为空时统一按 LeaseCount 打分（即使策略是 PREFER_LOW_LATENCY）。

评分越低越优。

### 同 IP 轮换细节
由于同 IP 轮换是冷路径，因此可以直接线性扫描 Platform 的可路由集合，选个延迟最小的同 IP 点节点。
如果找不到同 IP 点节点，就走随机分配。
注意：如果是同 IP 轮换，属于就地更新 Lease 的节点，不修改 Expiry。如果是随机分配，要释放旧租约，分配全新租约，Expiry 也是新的。

## 租约生命周期
| 事件 | 行为 |
|------|------|
| 首次请求 | 随机选择节点，创建 Lease |
| 后续请求 | 复用 Lease，更新 `LastAccessed` |
| 租约节点失效 | 就地修改 Lease 绑定的节点 |
| 同 IP 节点耗尽 | 释放 Lease，重新分配 |
| 租约过期 | 立刻清理 |

### 租约的过期清理机制
租约采用固定过期的机制，一个租约创建后不会续期。
租约过期后必须及时清理。因为需要及时释放失效租约来保证 `IPLoadStats` 的准确性。

运行一个后台服务，每隔 13～17 秒遍历所有 Platform 的租约表，清理过期的租约。

> 除了自然过期，也会有节点失效 / 节点出口 IP 改变导致租约失效的情况。但是这种情况处理起来比较麻烦，不能简单粗暴地删除租约。因为还有同 IP 节点轮转机制。目前暂定节点失效/出口 IP 改变的情况不归租约过期清理服务管，对 IPLoadStats 的准确度影响也不大。

## 持久化系统
数据持久化分为强持久化与弱持久化。强持久化不允许数据丢失，弱持久化允许一定程度的数据丢失。
强持久化数据写入 <RESIN_STATE_DIR>/state.db。
弱持久化数据写入 <RESIN_CACHE_DIR>/cache.db。

### 持久化需求
配置、平台、订阅需要强持久化，API 成功即可靠落盘。
节点、租约使用弱持久化。
启动恢复顺序必须稳定：先静态，再动态，再重建运行态索引。
必须避免持久化风暴、并发覆盖和分叉真源。

## 设计目标
持久化路径唯一，禁止“业务代码随手写盘”。

### 总体架构
StateEngine（单写入口）
所有业务写操作统一进入 StateEngine；模块禁止直接写数据库。

StateRepo（state.db）
保存强一致核心状态。

CacheRepo（cache.db）
保存弱一致运行时快照。

CacheFlushWorker
收集运行时脏数据，做批量、去抖、周期写入 cache.db。

BootstrapLoader
统一启动恢复流程，负责按固定顺序加载并注入内存管理器。

Resin 项目中所有的数据库都设计为单写，不会有多进程写入。

数据库 schema 使用 golang-migrate 做版本化 migration。

### SQLite 数据模型
#### state.db
* system_config(config_json, version, updated_at_ns)
* platforms(id PK, name UNIQUE, sticky_ttl_ns, regex_filters_json, region_filters_json, reverse_proxy_miss_action, reverse_proxy_empty_account_behavior, reverse_proxy_fixed_account_header, allocation_policy, updated_at_ns)
* subscriptions(id PK, name, url, update_interval_ns, enabled, ephemeral, created_at_ns, updated_at_ns)
* account_header_rules(url_prefix PK, headers_json, updated_at_ns)

> 订阅的 LastCheck、LastError、LastUpdate 不进行持久化。因为启动时总是会更新一次。

#### cache.db
* nodes_static(hash PK, raw_options_json, created_at_ns)
* nodes_dynamic(hash PK, failure_count, circuit_open_since, egress_ip, egress_updated_at_ns, last_latency_probe_attempt_ns, last_authority_latency_probe_attempt_ns, last_egress_update_attempt_ns)
* node_latency(node_hash, domain, ewma_ns, last_updated_ns, PK(node_hash,domain))。
* leases(platform_id, account, node_hash, egress_ip, expiry_ns, last_accessed_ns, PK(platform_id,account))。
* subscription_nodes(subscription_id, node_hash, tags_json, PK(subscription_id,node_hash))

> 备注
> node 不用持久化保存 error 状态。恢复的时候如果节点启动失败，自然会产生错误状态。

### 写入语义
强状态（配置/平台/订阅）走 StateEngine + state.db 事务，提交成功才返回成功。
弱状态（节点信息 / leases）走脏集合，批量写 cache.db。并且利用脏集合实现仅写脏数据，避免全量写。只有集合满或者定时器触发才 flush，避免频繁写盘。

由于 entry 的写回顺序无关紧要，因此采用脏集合而不是脏队列。脏集合的优势在于对于同一个元素的修改只会保留最后一次，避免重复写入。为每个数据库表构建一个 xsync 集合，key 是表的主键。value 的状态可以是 Upsert 或者 Delete。

在全局配置里定义写回容量阈值 CacheFlushDirtyThreshold（所有脏集合的元素数量总和超过阈值时写回）与写回时间阈值 CacheFlushInterval（多久没有写回了就写回一次）。

程序退出前要 flush 脏集合。

### 一致性修复
弱持久化丢一个窗口的数据，可能导致数据陈旧，这完全能接受。但是还可能造成数据不一致。
因此，需要写一个数据库一致性修复的功能。主要是做外键的一致性修复。推荐使用 ATTACH 联合两个数据库进行查询。
* 删除 subscription_nodes 中，subscription_id 不存在于 subscriptions，或者 node_hash 不存在于 nodes_static 的记录。
* 删除 nodes_static 中，hash 不存在于 subscription_nodes 的记录。
* 删除 nodes_dynamic 中，hash 不存在于 nodes_static 的记录。
* 删除 node_latency 中，node_hash 不存在于 nodes_static 的记录。
* 删除 leases 中，platform_id 不存在于 platforms，或者 node_hash 不存在于 nodes_static 的记录。

启动时，先执行一次一致性修复，再重新加载数据进行初始化。

### 启动恢复流程

启动恢复由 `BootstrapLoader` 组件负责。为了保证“只有一套数据管理系统”，恢复流程本质上是通过 `StateEngine` 读取快照，然后通过内存管理器的 `Load*` 专用接口注入状态。这些 `Load*` 接口仅更新内存状态，**不**触发持久化脏标记。

1. 数据库一致性检查
    * 在打开数据库后，立即执行 SQL 清理脚本（如上文“一致性修复”所述），移除孤儿数据，确保持久化数据的完整性。

2. 加载全局配置与静态资源
    * 从 `state.db` 加载 `system_config`，初始化全局配置。
    * 加载 `account_header_rules`，初始化反向代理识别规则。
    * 加载 `platforms`，初始化平台。此时平台均为空，无节点。
    * 加载 `subscriptions`，初始化订阅管理器。此时订阅均为空，无关联节点。

3. 加载节点基础数据
    * 从 `cache.db` 加载 `nodes_static`。
    * 节点注入全局节点池时默认标记为熔断（`CircuitOpenSince=now`）。
    * 并行初始化：对于每个节点，进行 `json.Unmarshal` 解析 `RawOptions`，并尝试通过 `sing-box` 创建 `Outbound` 实例。
    * 将节点注入全局节点池（此时订阅集合为空）。

4. 重建订阅视图与节点持有关系
    * 从 `cache.db` 加载 `subscription_nodes`。
    * 对于每条记录 `(sub_id, node_hash, tags_json)`：
        * 找到对应的 Subscription 对象和 Node 对象。
        * 解析 `tags_json` 为 `[]string`。
        * 将 Node Hash 与 Tags 列表加入 Subscription 的视图 (`ManagedNodes`)。
        * 将 Subscription ID 加入 Node 的 `SubscriptionIDs` 集合。

5. 加载节点动态状态
    * 从 `cache.db` 加载 `nodes_dynamic`，更新节点的熔断状态、失败计数、出口 IP、探测尝试时间戳。若某节点缺少 `nodes_dynamic` 记录，则保留步骤 3 的默认熔断状态。
    * 加载 `node_latency`，恢复节点的延迟统计表。

6. 重建 Platform 可路由视图
    * 遍历所有 Platform。
    * 对每个 Platform，触发一次全量视图重建。
    * 重建逻辑复用运行时的标准流程：遍历所有已启用订阅的节点，进行相应筛选。

7. 恢复租约
    * 从 `cache.db` 加载 `leases`。
    * 租约信息加入租约表 与 IPLoadStats。

8. 启动后台服务
	* 第一批启动
		* CacheFlushWorker。需要首先启动，因为后面的服务很多都需要写持久化
		* GeoIP Updater（启动时检查 geoip 的 mtime 确认是否需要更新）
	* 第二批启动
		* ProbeManager（启动后正常进行周期性检查，不用像 订阅更新服务 强制全部重新检查一遍）
		* 结构化请求日志相关服务
		* 租约清理服务
    * EphemeralNode 后台清理服务
	* 第三批启动
		* 订阅更新服务（启动强制触发一轮全部订阅更新，不管是否过期。这是为了避免弱持久化丢节点。之后周期性检查）
	* 第四批启动
		* API Server
		* Forward Proxy Server
		* Reverse Proxy Server

## 结构化请求日志
结构化请求日志记录每次代理请求的详细信息。日志记录动作由 `requestlog` 模块异步执行，确保不阻塞代理主流程。这些日志主要用于运维监控、审计与故障排查，不参与路由决策。

### 存储机制
日志存储基于 SQLite。

#### 分库滚动策略
* 文件存储：日志保存在 `RESIN_LOG_DIR` 的 `request_logs-<Timestamp>.db` 文件中。
* 自动滚动：当当前数据库文件大小超过配置阈值 (`RESIN_REQUEST_LOG_DB_MAX_MB`) 时，自动关闭当前 DB 并创建新 DB。
* 自动清理：系统启动与运行时会检查 DB 文件数量，保留最近的 `RESIN_REQUEST_LOG_DB_RETAIN_COUNT` 个文件，自动删除旧文件。
* 日志查询：查询所有的库。接受弱一致分页（日志查询是实时视图，容忍新日志插入导致分页边界变化）。按照时间戳优先，同时间戳按 UUID 排序的方法。

##### 异步写入与背压
* 内存队列：日志产生后首先进入内存队列。队列大小由 `RESIN_REQUEST_LOG_QUEUE_SIZE` 控制。
* 非阻塞丢弃：如果队列已满，新的日志记录会被直接丢弃。
* 批量提交：后台协程从队列取出日志，积累到一定数量 (`RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE`) 或距离上次提交超过 `RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL` 时间间隔后，批量开启事务写入 SQLite。
* WAL 模式：数据库配置为 `PRAGMA journal_mode=WAL` 与 `synchronous=NORMAL`。

### 记录逻辑

#### 记录时机
日志记录发生于请求处理结束的 `defer` 阶段。这确保了无论是正常结束、超时取消，还是发生 Panic 被 Recover，都能准确记录请求的最终状态与耗时。

#### request_logs 表
* `id`：UUID 主键
* `ts_ns`：请求开始时间戳（Unix Nano）。
* `proxy_type`：代理类型。1=正向代理，2=反向代理。
* `client_ip`：客户端 IP 地址。
* `platform_id`：所属平台 ID。
* `platform_name`：所属平台名称（快照）。
* `account`：所属账号。
* `target_host`：目标完整 Host (e.g., `www.google.com:443`)。
* `target_url`：目标完整 URL (e.g., `https://www.google.com/search?q=hello`)。正向代理的 CONNECT 请求为空。
* `node_hash`：出口节点的 Hash。
* `node_tag`：出口节点展示 Tag (`<Subscription>/<Tag>`)。如果有多个订阅持有节点，选所属 subscription 创建最早的那个，同 subscription 选字典序最小的。
* `egress_ip`：实际出口 IP 地址。
* `duration_ns`：请求耗时。CONNECT 模式记录隧道保留时长。
* `net_ok`：网络层是否成功 (0/1)。
* `http_method`：HTTP 方法。CONNECT 模式记录为 CONNECT。
* `http_status`：HTTP 状态码。
* `resin_error`：Resin 的逻辑错误码快照（如 `UPSTREAM_TIMEOUT`、`UPSTREAM_REQUEST_FAILED`）。
* `upstream_stage`：失败阶段（例如 `forward_roundtrip`、`connect_dial`、`connect_upstream_to_client_copy`）。
* `upstream_err_kind`：上游错误归类（例如 `dns_error`、`timeout`、`connection_refused`）。
* `upstream_errno`：提取到的 errno 码（例如 `ECONNREFUSED`）。
* `upstream_err_msg`：上游错误消息（归一化并截断）。
* `ingress_bytes`：下行字节数（从上游到客户端，header+body）。
* `egress_bytes`：上行字节数（从客户端到上游，header+body）。
* `payload_present`：是否包含 Payload (0/1)。
* `req_headers_len`：请求头长度（字节）。
* `req_body_len`：请求体长度（字节）。
* `resp_headers_len`：响应头长度（字节）。
* `resp_body_len`：响应体长度（字节）。
* `req_headers_truncated`：请求头是否被截断 (0/1)。
* `req_body_truncated`：请求体是否被截断 (0/1)。
* `resp_headers_truncated`：响应头是否被截断 (0/1)。
* `resp_body_truncated`：响应体是否被截断 (0/1)。

#### request_log_payloads 表
采用双表设计，将“元数据”与“大体积载荷”分离，提高查询与存储效率。仅当 `payload_present=1` 时存在记录。存储详细的 Header/Body 二进制数据。

* `log_id`：外键，关联 `request_logs(id)`。级联删除。
* `req_headers`：BLOB，请求头数据。超过 `ReverseProxyLogReqHeadersMaxBytes` 部分被截断。
* `req_body`：BLOB，请求体数据。超过 `ReverseProxyLogReqBodyMaxBytes` 部分被截断。
* `resp_headers`：BLOB，响应头数据。超过 `ReverseProxyLogRespHeadersMaxBytes` 部分被截断。
* `resp_body`：BLOB，响应体数据。超过 `ReverseProxyLogRespBodyMaxBytes` 部分被截断。

#### 索引
* ts_ns
* proxy_type
* platform_id
* platform_id, account
* target_host
* egress_ip

## 数据统计
Resin 需要做实事与历史的统计数据，用于 Dashboard 展示。

### 统计项

#### 吞吐（网速）
* 统计实时的上行、下行网速。
* 每 `RESIN_METRIC_THROUGHPUT_INTERVAL_SECONDS` 作为一个统计窗口。
* 保留最近 `RESIN_METRIC_THROUGHPUT_RETENTION_SECONDS` 的历史数据。
* 只在全局视角统计。
* 不做持久化。

#### 流量消耗
* 统计累计上行、下行流量。
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 只在全局视角统计。
* 持久化到 `metrics.db`。

#### 请求数
* 统计累计请求数。
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 全局视角、每平台视角。
* 持久化到 `metrics.db`。

#### 连接数
* 统计实时的连接数。这里的连接指的是TCP 连接（一个打开的 socket / 一个 4 元组会话）。分为入站连接与出站连接。
  * 入站是客户端到 Resin 的连接数。从 accept 成功开始计数，直到该 socket 被关闭。包括：Reverse Proxy / Forward Proxy 的所有客户端 TCP 连接、keep-alive 的空闲连接（idle）、CONNECT 被 hijack 后仍在传输的那条客户端 socket。
  * 出站是 Resin 发起到目标站点/上游的 TCP socket 数，从 Dial 成功开始计数，直到该 socket 关闭。包括：正向代理 CONNECT 产生的到 target 的 TCP 连接、正向代理普通 HTTP 代理到上游的 TCP 连接、反向代理到上游 origin 的 TCP 连接（包括连接池里 idle 的连接）、ProbeManager 的探测连接。
* 每 `RESIN_METRIC_CONNECTIONS_INTERVAL_SECONDS` 统计一次。
* 保留最近 `RESIN_METRIC_CONNECTIONS_RETENTION_SECONDS` 统计数据。
* 只在全局视角统计。
* 不做持久化。

#### 主动探测次数
* 统计主动延迟探测 与 主动 IP 探测的次数总和。发一次请求计做一次。
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 全局视角。
* 持久化到 `metrics.db`。

#### 节点数量
* 统计全局池节点总数量、健康节点数量（未熔断且 Outbound 非空）、出口 IP 数量。
* 每 `RESIN_METRIC_BUCKET_SECONDS` 统计一次。支持手动按需一次性统计。手动按需统计不计入持久化。
* 持久化到 `metrics.db`。
* 全局视角。

#### Platform 节点数量
* 统计 Platform 的可路由节点数量与出口 IP 数量。
* 需要时一次性统计。
* Platform 视角。

#### 节点延迟分布
* 统计节点的延迟分布。延迟按照权威站点的平均延迟计算。给出分桶统计的结果。
* 需要时一次性统计。
* 全局视角、Platform 视角。

#### 访问延迟
* 统计最近的实际访问延迟分布。不包含 CONNECT 时长。给出分桶统计的结果。
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 持久化到 `metrics.db`。
* 全局视角、Platform 视角。

#### 访问成功率
* 统计实际访问成功率（net_ok）
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 持久化到 `metrics.db`。
* 全局视角、Platform 视角。

#### 租约数量
* 每 `RESIN_METRIC_LEASES_INTERVAL_SECONDS` 统计一次。
* 保留最近 `RESIN_METRIC_LEASES_RETENTION_SECONDS` 统计数据。
* 不做持久化。
* Platform 视角。

#### 租约平均存活时间分布
* 统计租约的平均存活时间分布。记录 P1、P5、P50 存活时间。
* 以 `UNIX_SECONDS % RESIN_METRIC_BUCKET_SECONDS == 0` 作为一个统计窗口。
* 持久化到 `metrics.db`。
* Platform 视角。


### 统计实现

#### 目标与边界
* 统计覆盖数据面（Forward/Reverse 代理）与主动探测（ProbeManager）。
* 控制面 API、GeoIP 下载、订阅下载不计入吞吐/请求/连接统计。
* 热路径只允许 O(1) 原子更新，不允许 DB I/O、全局锁与高频内存分配。
* 指标按两类管理：
  * 实时指标：仅内存保存（用于 Dashboard 曲线）。
  * 历史指标：按 bucket 聚合后持久化到 `metrics.db`。

#### 模块职责
* `MetricsManager`：统计域服务唯一入口。对外统一提供写入事件接口与查询接口；提供实时曲线、历史 bucket、一次性快照查询能力；业务模块只与 `MetricsManager` 交互。
* 采用 xsync.Counter 作为热路径计数器。
* `RealtimeSampler`：周期采样，写实时 ring buffer。
* `BucketAggregator`：按 `RESIN_METRIC_BUCKET_SECONDS` 做窗口聚合。
* `MetricsRepo`：`metrics.db` 批量 upsert。

#### 事件模型（统一输入）
* `RequestFinishedEvent`：
  * 字段：`platform_id, proxy_type, is_connect, net_ok, duration_ns`。
  * 用于请求数、成功率、访问延迟分布。
* `TrafficDeltaEvent`：
  * 字段：`ingress_bytes, egress_bytes`。
  * 用于吞吐与流量累计（字节增量统计）。
* `ConnectionLifecycleEvent`：
  * 字段：`direction(inbound|outbound), op(open|close)`。
  * 用于实时连接数。
* `ProbeEvent`：
  * 字段：`kind(egress|latency)`。
  * 用于主动探测次数。
* `LeaseEvent`：
  * 字段：`platform_id, account, op(create|replace|remove|expire)`。
  * 用于实时租约数量与租约存活时间统计。

#### 数据流
* 采集层：业务代码在关键生命周期发事件到 `MetricsManager`。
* 聚合层：
  * 高频 ticker（1s）从 Collector 读快照，生成实时点。
  * bucket ticker 在窗口边界触发历史聚合与落库。
* 存储层：`MetricsRepo` 单线程批量事务写入 `metrics.db`，失败重试但不阻塞热路径。

#### 连接数实现
* 入站连接：
  * 对 Forward/Reverse listener 统一包裹连接生命周期钩子。
  * `Accept` 成功记 `open`，`Close` 记 `close`。
  * keep-alive idle 与 CONNECT hijack 后连接自然计入（直到 socket 关闭）。
* 出站连接：
  * 对统一 Dial 抽象层做包装（包括代理转发与 Probe 使用的 Dial）。
  * Dial 成功记 `open`，连接关闭记 `close`。
  * 连接池 idle 连接仍处于 open 状态，直到真实 close 才扣减。

#### 各统计项落地规则
* 吞吐（实时）：
  * 基于 `TrafficDeltaEvent` 的累计值做差分，按采样周期换算 B/s。
* 流量消耗（历史）：
  * bucket 内累计 `TrafficDeltaEvent` 字节量，维度为全局。
* 请求数与成功率（历史）：
  * 由 `RequestFinishedEvent` 聚合 `total_requests` 与 `success_requests`，维度为全局 + platform。
* 访问延迟分布（历史）：
  * 只统计非 CONNECT 请求；CONNECT 明确排除。
  * 使用固定延迟桶聚合（每 `RESIN_METRIC_LATENCY_BIN_WIDTH_MS` 一个桶，溢出桶是 `RESIN_METRIC_LATENCY_BIN_OVERFLOW_MS`），维度为全局 + platform。
* 主动探测次数（历史）：
  * egress/latency probe 每发起一次请求记 1 次，维度为全局。
* 节点数量（历史 + 手动）：
  * 每 bucket 统计全局 `total_nodes / healthy_nodes / egress_ip_count` 并落库。
  * 同时提供手动即时统计（不落库），返回 `total_nodes / healthy_nodes / egress_ip_count / healthy_egress_ip_count`。
* Platform 节点数量（手动）：
  * 即时统计 `routable_node_count / egress_ip_count`，维度为 platform。
* 节点延迟分布（手动）：
  * 取权威域名延迟（LatencyAuthorities）做节点级汇总，再分桶返回；维度为全局 + platform。
* 租约数量（实时）：
  * 按 `RESIN_METRIC_LEASES_INTERVAL_SECONDS` 采样各平台当前 active lease 数，内存保留窗口。
* 租约存活时间分布（历史）：
  * 在 lease remove/expire 时记录该 lease 存活时长样本。
  * bucket 结束计算 P1/P5/P50，维度为 platform。

#### `metrics.db`
保存在 LOG_DIR。bucket ticker 在窗口边界触发落库。

模型：
按 bucket 表拆分，统一使用 `(bucket_start_unix, dimension...)` 作为主键：
* `metric_traffic_bucket(bucket_start_unix, ingress_bytes, egress_bytes)`
* `metric_request_bucket(bucket_start_unix, platform_id NULLABLE, total_requests, success_requests)`
* `metric_access_latency_bucket(bucket_start_unix, platform_id NULLABLE, buckets_json)`
* `metric_probe_bucket(bucket_start_unix, total_count)`
* `metric_node_pool_bucket(bucket_start_unix, total_nodes, healthy_nodes, egress_ip_count)`
* `metric_lease_lifetime_bucket(bucket_start_unix, platform_id, sample_count, p1_ms, p5_ms, p50_ms)`

说明：
* `platform_id=NULL` 表示全局视角。
* 支持 `platform` 维度的历史统计按 `platform_id` 存储，不依赖可变的 `platform_name`。

#### 时钟与窗口边界
* bucket 对齐规则：`bucket_start_unix = (ts_unix / RESIN_METRIC_BUCKET_SECONDS) * RESIN_METRIC_BUCKET_SECONDS`。
* 采样与聚合都基于单调时钟触发，避免 wall clock 回拨带来的重复窗口。
* 写库幂等：同一主键行使用 upsert，允许重试和重复提交。

#### 启动恢复与容错
* 实时 ring buffer 不恢复（重启后重新采样）。
* 历史统计直接从 `metrics.db` 读取；上一个未完成 bucket 允许丢失（弱一致，可接受）。
* 正常退出执行一次 flush，尽量写入当前可提交窗口。
* `metrics.db` 写失败只影响统计可见性，不影响代理主流程。




## WebAPI
### 概览

* Base URL：`http://<host>:${RESIN_PORT}/api/v1`
* Content-Type：`application/json; charset=utf-8`
* 鉴权：控制面 Admin Token

实现 WebAPI 时，严禁在 API 层直接实现业务逻辑。

### 安全与鉴权
#### Admin API Token
* Header：`Authorization: Bearer <admin_token>`
* 失败返回：`401 Unauthorized`

除了 `/healthz`，所有的请求都需要鉴权。

### 通用约定

#### ID 与类型

* `platform_id`：UUID（不可变）
* `subscription_id`：UUID（不可变）
* `node_hash`：128-bit xxHash，**hex 字符串**（32 字符，大小写均可，例如 `"9f2c...e1a0"`）
* `egress_ip`：字符串（IPv4/IPv6）
* `region`：ISO 3166-1 alpha-2 小写（如 `"us"`, `"hk"`）
* `duration`：字符串（Go duration，如 `"30s"`, `"15m"`, `"24h"`）
* `timestamp`：RFC3339Nano（如 `"2026-02-10T12:34:56.123456789Z"`）

#### 响应包装

所有成功响应直接返回资源，例如 `{ "id": "...", "name": "Default", ... }`.

错误响应：

```json
{
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "sticky_ttl is invalid",
  }
}
```

#### 常见错误码

* `INVALID_ARGUMENT` (400)
* `NOT_FOUND` (404)
* `CONFLICT` (409) — 唯一约束冲突（如 platform name）
* `UNAUTHORIZED` (401)
* `INTERNAL` (500)

#### 分页
* Query：`limit`（默认 50，最大 10000）、`offset`
* 响应：
```json
{
  "items": [ ... ]
}
```

#### 排序
* Query：`sort_by`（排序字段）、`sort_order`（`asc` 或 `desc`，默认 `asc`）
* 每个列表端点定义自己支持的 `sort_by` 字段与默认排序。
* 不传 `sort_by` 时使用端点的默认排序。

#### PATCH 语义

* `PATCH` 使用受限的 JSON 对象 partial patch（**非 RFC 7396 JSON Merge Patch**）
* 更新成功返回更新后的完整资源。

#### 写接口请求体与字段校验约定

* 除明确声明“无请求体”的接口外，请求体必须是 JSON Object。
* 未声明字段、只读字段、类型不匹配一律返回 `400 INVALID_ARGUMENT`。
* 对于本规范中的 `PATCH` 接口，字段值为 `null` 视为非法输入（不支持删除字段）。
* 错误码映射优先使用端点内定义；未单独列出的场景按“常见错误码”处理。

### 系统

#### 健康检查
**GET** `/healthz`（无需鉴权）
返回：`200 OK`

```json
{ "status": "ok" }
```

#### 系统信息

**GET** `/system/info`
返回版本、构建信息、启动时间等。

```json
{
  "version": "1.0.0",
  "git_commit": "abc123",
  "build_time": "2026-02-10T01:02:03Z",
  "started_at": "2026-02-10T12:00:00Z"
}
```

#### 获取全局配置

**GET** `/system/config`

返回：

```json
{
  "user_agent": "sing-box",
  "request_log_enabled": false,
  "reverse_proxy_log_detail_enabled": false,
  "reverse_proxy_log_req_headers_max_bytes": 4096,
  "reverse_proxy_log_req_body_max_bytes": 1024,
  "reverse_proxy_log_resp_headers_max_bytes": 1024,
  "reverse_proxy_log_resp_body_max_bytes": 1024,
  "max_consecutive_failures": 3,
  "max_latency_test_interval": "5m",
  "max_authority_latency_test_interval": "1h",
  "max_egress_test_interval": "24h",
  "latency_test_url": "https://www.gstatic.com/generate_204",
  "latency_authorities": ["gstatic.com", "google.com", "cloudflare.com", "github.com"],
  "p2c_latency_window": "10m",
  "latency_decay_window": "10m",
  "cache_flush_interval": "5m",
  "cache_flush_dirty_threshold": 1000
}
```

#### 获取全局配置默认值

**GET** `/system/config/default`

返回内置默认配置（编译期默认值），不受当前运行中 `PATCH /system/config` 改动影响。

```json
{
  "user_agent": "sing-box",
  "request_log_enabled": false,
  "reverse_proxy_log_detail_enabled": false,
  "reverse_proxy_log_req_headers_max_bytes": 4096,
  "reverse_proxy_log_req_body_max_bytes": 1024,
  "reverse_proxy_log_resp_headers_max_bytes": 1024,
  "reverse_proxy_log_resp_body_max_bytes": 1024,
  "max_consecutive_failures": 3,
  "max_latency_test_interval": "5m",
  "max_authority_latency_test_interval": "1h",
  "max_egress_test_interval": "24h",
  "latency_test_url": "https://www.gstatic.com/generate_204",
  "latency_authorities": ["gstatic.com", "google.com", "cloudflare.com", "github.com"],
  "p2c_latency_window": "10m",
  "latency_decay_window": "10m",
  "cache_flush_interval": "5m",
  "cache_flush_dirty_threshold": 1000
}
```

#### 更新全局配置

**PATCH** `/system/config`
Body（partial patch 示例）：

```json
{
  "request_log_enabled": true,
  "cache_flush_dirty_threshold": 2000
}
```

字段要求：

* 必填字段：无
* 可改字段：仅允许 `GET /system/config` 返回的全部顶层字段。
* 不可改字段：未在配置对象中声明的任意字段。

关键校验（最小集）：

* duration 字段必须可解析。
* `latency_test_url` 必须是 `http/https` 绝对 URL。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：空 patch、字段非法、类型错误、校验失败。

返回：更新后的 config。

### Platform

#### Platform 模型

```json
{
  "id": "uuid",
  "name": "Default",
  "sticky_ttl": "30m",
  "regex_filters": ["^sub1/.*", ".*hk.*"],
  "region_filters": ["hk","us"],
  "routable_node_count": 123,
  "reverse_proxy_miss_action": "RANDOM|REJECT",
  "reverse_proxy_empty_account_behavior": "RANDOM|FIXED_HEADER|ACCOUNT_HEADER_RULE",
  "reverse_proxy_fixed_account_header": "Authorization\nX-Account-Id",
  "allocation_policy": "BALANCED|PREFER_LOW_LATENCY|PREFER_IDLE_IP",
  "updated_at": "2026-02-10T12:34:56Z"
}
```

#### 列出平台

**GET** `/platforms?keyword=&limit=&offset=`

#### 创建平台

**POST** `/platforms`

Body：

```json
{
  "name": "Platform-A",
  "sticky_ttl": "168h",
  "regex_filters": ["^sub1/.*"],
  "region_filters": ["hk", "us"],
  "reverse_proxy_miss_action": "RANDOM",
  "reverse_proxy_empty_account_behavior": "ACCOUNT_HEADER_RULE",
  "reverse_proxy_fixed_account_header": "Authorization\nX-Account-Id",
  "allocation_policy": "BALANCED"
}
```

字段要求：

* 必填字段：`name`
* 可选字段：`sticky_ttl`、`regex_filters`、`region_filters`、`reverse_proxy_miss_action`、`reverse_proxy_empty_account_behavior`、`reverse_proxy_fixed_account_header`、`allocation_policy`
* 不可传字段：`id`、`updated_at`、`routable_node_count`
* 省略可选字段时，使用当前环境变量默认平台设置（`RESIN_DEFAULT_PLATFORM_*`）对应值

关键校验：

* `name`：trim 后非空、全局唯一、且不能为保留名 `Default`。
* `sticky_ttl`：合法 Go duration。
* `regex_filters`：每项可被 regexp 编译。
* `region_filters`：每项为 ISO 3166-1 alpha-2 小写代码。
* 枚举字段：`reverse_proxy_miss_action` 仅 `RANDOM|REJECT`；`reverse_proxy_empty_account_behavior` 仅 `RANDOM|FIXED_HEADER|ACCOUNT_HEADER_RULE`；`allocation_policy` 仅 `BALANCED|PREFER_LOW_LATENCY|PREFER_IDLE_IP`。
* 组合约束：当 `reverse_proxy_empty_account_behavior=FIXED_HEADER` 时，`reverse_proxy_fixed_account_header` 必填；其值支持多行，每行一个合法 HTTP Header 字段名（会按顺序尝试提取）。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：缺少必填、字段非法、类型错误、校验失败。
* `409 CONFLICT`：`name` 冲突或命中保留约束。

返回：`201 Created` + 平台对象

#### 获取平台

**GET** `/platforms/{platform_id}`

#### 更新平台
**PATCH** `/platforms/{platform_id}`

Body（partial patch 示例）：

```json
{
  "name": "Platform-B",
  "sticky_ttl": "72h"
}
```

字段要求：

* 必填字段：无
* 可改字段：`name`、`sticky_ttl`、`regex_filters`、`region_filters`、`reverse_proxy_miss_action`、`reverse_proxy_empty_account_behavior`、`reverse_proxy_fixed_account_header`、`allocation_policy`
* 不可改字段：`id`、`updated_at`、`routable_node_count`

关键校验：与“创建平台”一致。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：空 patch、不可改字段、字段校验失败。
* `404 NOT_FOUND`：`platform_id` 不存在。
* `409 CONFLICT`：修改目标为 `Default`，或 `name` 与其他平台冲突。

返回：更新后的平台对象。

#### 删除平台

**DELETE** `/platforms/{platform_id}`

请求体：无。

错误码映射（最小集）：

* `404 NOT_FOUND`：`platform_id` 不存在。
* `409 CONFLICT`：删除目标为 `Default` 平台。

返回：`204 No Content`

#### 重置平台为默认配置（Action）

**POST** `/platforms/{platform_id}/actions/reset-to-default`

行为：
* 将目标平台的可配置字段重置为当前环境变量默认平台设置（`RESIN_DEFAULT_PLATFORM_*`）。
* 重置后立即触发该平台可路由视图全量重建。
* 不删除该平台已有租约。

请求体：无。

错误码映射（最小集）：

* `404 NOT_FOUND`：`platform_id` 不存在。

返回：`200 OK` + 平台对象

#### 预览过滤器

**POST** `/platforms/preview-filter?limit=&offset=`

参数二选一：用现有平台配置预览，或传临时过滤器预览。

```json
{
  "platform_id": "uuid"
}
```

```json
{
  "platform_spec": {
    "regex_filters": ["^subA/.*"],
    "region_filters": ["hk", "us"]
  }
}
```

字段要求：

* 必填字段：`platform_id` 与 `platform_spec` 二选一，且只能出现一个。
* `platform_spec` 仅允许字段：`regex_filters`、`region_filters`。

关键校验（最小集）：

* `platform_id`：必须存在。
* `platform_spec.regex_filters`：每项可被 regexp 编译。
* `platform_spec.region_filters`：每项为 ISO 3166-1 alpha-2 小写代码。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：二选一规则不满足、字段非法或校验失败。
* `404 NOT_FOUND`：`platform_id` 不存在。

按 Node 摘要模型返回节点列表。


#### 触发平台可路由视图全量重建（Action）

**POST** `/platforms/{platform_id}/actions/rebuild-routable-view`
API 阻塞到重建完成为止。

请求体：无。

错误码映射（最小集）：

* `404 NOT_FOUND`：`platform_id` 不存在。

### Subscription

#### Subscription 模型

```json
{
  "id": "uuid",
  "name": "sub-A",
  "url": "https://example.com/sub",
  "update_interval": "5m",
  "node_count": 1200,
  "healthy_node_count": 980,
  "ephemeral": false,
  "ephemeral_node_evict_delay": "72h",
  "enabled": true,
  "created_at": "2026-02-10T12:00:00Z",
  "last_checked": "2026-02-10T12:10:00Z",
  "last_updated": "2026-02-10T12:09:58Z",
  "last_error": ""
}
```

`healthy_node_count` 规则：节点 `Outbound` 非空且节点未熔断。

#### 列出订阅

**GET** `/subscriptions?enabled=&keyword=&limit=&offset=&sort_by=&sort_order=`

支持的 `sort_by`：`name`、`created_at`、`last_checked`、`last_updated`。默认按 `created_at` `asc` 排序。

#### 创建订阅

**POST** `/subscriptions`

Body：

```json
{
  "name": "sub-A",
  "url": "https://example.com/sub",
  "update_interval": "5m",
  "enabled": true,
  "ephemeral": false,
  "ephemeral_node_evict_delay": "72h"
}
```

字段要求：

* 必填字段：`name`、`url`
* 可选字段：`update_interval`、`enabled`、`ephemeral`、`ephemeral_node_evict_delay`
* 不可传字段：`id`、`node_count`、`healthy_node_count`、`created_at`、`last_checked`、`last_updated`、`last_error`
* 默认值：`update_interval="5m"`、`enabled=true`、`ephemeral=false`、`ephemeral_node_evict_delay="72h"`

关键校验（最小集）：

* `name`：trim 后非空。
* `url`：`http/https` 绝对 URL。
* `update_interval`：合法 Go duration，且 `>=30s`。
* `ephemeral_node_evict_delay`：合法 Go duration，且 `>=0s`。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：缺少必填、字段非法、类型错误、校验失败。

返回：`201 Created` + 订阅对象。

#### 获取订阅

**GET** `/subscriptions/{subscription_id}`

#### 更新订阅

**PATCH** `/subscriptions/{subscription_id}`

Body（partial patch 示例）：

```json
{
  "url": "https://example.com/sub-new",
  "enabled": false
}
```

字段要求：

* 必填字段：无
* 可改字段：`name`、`url`、`update_interval`、`enabled`、`ephemeral`、`ephemeral_node_evict_delay`
* 不可改字段：`id`、`node_count`、`healthy_node_count`、`created_at`、`last_checked`、`last_updated`、`last_error`

关键校验：与“创建订阅”一致。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：空 patch、不可改字段、字段校验失败。
* `404 NOT_FOUND`：`subscription_id` 不存在。

返回：更新后的订阅对象。

#### 删除订阅

**DELETE** `/subscriptions/{subscription_id}`

请求体：无。

错误码映射（最小集）：

* `404 NOT_FOUND`：`subscription_id` 不存在。

返回：`204 No Content`

#### 手动触发订阅刷新（Action）

**POST** `/subscriptions/{subscription_id}/actions/refresh`
API 阻塞到更新完成为止。

请求体：无。

错误码映射（最小集）：

* `404 NOT_FOUND`：`subscription_id` 不存在。

### 反向代理 Account Header Rules

对应 `account_header_rules(url_prefix PK, headers_json...)`，并支持字典树最长前缀匹配。

#### Rule 模型

```json
{
  "url_prefix": "api.example.com/v1",
  "headers": ["Authorization", "x-api-key"],
  "updated_at": "2026-02-10T12:00:00Z"
}
```

#### 列出规则

**GET** `/account-header-rules?keyword=&limit=&offset=`

#### Upsert 规则（幂等）

**PUT** `/account-header-rules/{url_prefix}`

url_prefix 不允许包含查询部分与 ? 字符。由于 `url_prefix` 通过路径参数传输，调用方应按 URL Path 规则编码（例如 `api.example.com/v1` 传为 `api.example.com%2Fv1`），服务端解码后参与匹配。

Body：

```json
{ "headers": ["Authorization"] }
```

字段要求：

* 必填字段：`headers`
* 可改字段：`headers`（`url_prefix` 由路径参数提供，不可在 body 修改）

关键校验（最小集）：

* `url_prefix`（path）：decode 后非空，且不能包含 `?`。
* `headers`：非空数组。
* `headers[]`：必须是合法 HTTP Header 名（token）。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：字段缺失、类型错误或校验失败。

返回：`201 Created`（创建）或 `200 OK`（覆盖）+ 更新后的 rule。

#### 删除规则

**DELETE** `/account-header-rules/{url_prefix}`

请求体：无。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：`url_prefix` 非法。
* `404 NOT_FOUND`：规则不存在。

返回：`204 No Content`

#### 调试：给定 URL 解析会命中的规则

**POST** `/account-header-rules:resolve`
Body：

```json
{ "url": "https://api.example.com/v1/orders/123?a=1" }
```

字段要求：

* 必填字段：`url`

关键校验（最小集）：

* `url`：必须是 `http/https` 绝对 URL。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：缺少 `url`、类型错误或 URL 非法。

返回：

```json
{
  "matched_url_prefix": "api.example.com/v1",
  "headers": ["Authorization"]
}
```

### Nodes（全局节点池）

#### Node 摘要模型

```json
{
  "node_hash": "....",
  "created_at": "2026-02-10T12:00:00Z",
  "egress_ip": "1.2.3.4",
  "region": "us",
  "failure_count": 0,
  "circuit_open_since": null,
  "last_egress_update": "2026-02-10T12:20:00Z",
  "last_latency_probe_attempt": "2026-02-10T12:21:00Z",
  "last_authority_latency_probe_attempt": "2026-02-10T12:21:00Z",
  "last_egress_update_attempt": "2026-02-10T12:20:00Z",
  "subscriptions": ["uuid1","uuid2"],
  "last_error": "..."
}
```

#### NodeTag 模型
```json
{
  "subscription_id": "uuid",
  "subscription_name": "sub-A",
  "tag": "sub-A/HK-01",
}
```

#### 列出节点

**GET** `/nodes`
Query：
* `limit`：默认 50，最大 10000
* `offset`：分页偏移
* `platform_id`：只列出该 Platform 的“可路由集合”内节点（可选）
* `subscription_id`：只列出该订阅持有的节点（可选）
* `region`：hk/us/...（可选）
* `circuit_open`：true|false（可选）
* `has_outbound`：true|false（可选）
* `egress_ip`：IP 地址（可选）
* `probed_since`：RFC3339Nano（可选），按节点 `LastLatencyProbeAttempt` 过滤
* `sort_by`：排序字段（可选）
* `sort_order`：`asc` 或 `desc`（可选）

支持的 `sort_by`：`tag`、`created_at`、`failure_count`、`region`。其中 `tag` 表示按照节点 tag 排序。如果有多个 tag，按照所属 subscription 创建最早的那个。同 subscription 选字典序最小的。默认按 `tag` `asc` 排序。

Response：
```json
{
  "total": 1,
  "limit": 50,
  "offset": 0,
  "unique_egress_ips": 1,
  "unique_healthy_egress_ips": 1,
  "items": [
    {
      "node_hash": "9f2c0b1a6d3e4f5c8a9b0c1d2e3f4a5b",
      "created_at": "2026-02-10T12:00:00Z",

      "has_outbound": true,
      "last_error": "...",
      "circuit_open_since": null,
      "failure_count": 0,

      "egress_ip": "1.2.3.4",
      "region": "us",
      "last_egress_update": "2026-02-10T12:20:00Z",
      "last_latency_probe_attempt": "2026-02-10T12:21:00Z",
      "last_authority_latency_probe_attempt": "2026-02-10T12:21:00Z",
      "last_egress_update_attempt": "2026-02-10T12:20:00Z",

      "tags": [
        {
          "subscription_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
          "subscription_name": "sub-A",
          "tag": "sub-A/HK-01"
        },
        {
          "subscription_id": "7b2a1c11-2222-4333-8d9f-1a2b3c4d5e6f",
          "subscription_name": "sub-B",
          "tag": "sub-B/HK-Backup"
        }
      ]
    }
  ]
}
```

`unique_egress_ips` 说明：
* 统计对象是“当前过滤条件命中的全部节点”（即分页前结果）。
* 不受 `limit` / `offset` 影响。
* 仅统计有有效 `egress_ip` 的节点（空值不计入）。

`unique_healthy_egress_ips` 说明：unique_egress_ips 中健康的部分。

#### 获取单个节点

**GET** `/nodes/{node_hash}`

#### 触发节点出口探测（Action）

**POST** `/nodes/{node_hash}/actions/probe-egress`

请求体：无。

校验规则：

* `node_hash` 必须为 32 位十六进制字符串（大小写均可）。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：`node_hash` 格式非法。
* `404 NOT_FOUND`：节点不存在。

返回 cloudflare.com 在 TD-EWMA 后的延迟 `latency_ewma_ms` 与出口 IP。

#### 触发节点延迟探测（Action）

**POST** `/nodes/{node_hash}/actions/probe-latency`

请求体：无。

校验规则：

* `node_hash` 必须为 32 位十六进制字符串（大小写均可）。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：`node_hash` 格式非法。
* `404 NOT_FOUND`：节点不存在。

返回 LatencyTestURL 的站点在 TD-EWMA 后的延迟 `latency_ewma_ms`。

### Leases

#### Lease 模型

```json
{
  "platform_id": "uuid",
  "account": "acc1",
  "node_hash": "....",
  "egress_ip": "1.2.3.4",
  "expiry": "2026-02-10T13:00:00Z",
  "last_accessed": "2026-02-10T12:59:50Z"
}
```

#### 列出租约（按平台）

**GET** `/platforms/{platform_id}/leases?account=&limit=&offset=&sort_by=&sort_order=`

支持的 `sort_by`：`account`、`expiry`、`last_accessed`。默认按 `expiry` `asc` 排序。

#### 获取租约

**GET** `/platforms/{platform_id}/leases/{account}`

#### 释放租约（运维手动驱逐）

**DELETE** `/platforms/{platform_id}/leases/{account}`

请求体：无。

校验规则：

* `platform_id` 必须是合法 UUID。
* `account` 路径参数 URL decode 后 trim 非空。

行为：删除租约表项。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：路径参数非法。
* `404 NOT_FOUND`：平台不存在或租约不存在。

返回：`204 No Content`

#### 释放全部租约

**DELETE** `/platforms/{platform_id}/leases`

请求体：无。

行为：删除该平台所有租约。

错误码映射（最小集）：

* `400 INVALID_ARGUMENT`：`platform_id` 非法。
* `404 NOT_FOUND`：平台不存在。

返回：`204 No Content`

#### 平台出口 IP 负载统计

**GET** `/platforms/{platform_id}/ip-load?limit=&offset=&sort_by=&sort_order=`

支持的 `sort_by`：`egress_ip`、`lease_count`。默认按 `lease_count` `desc` 排序。

```json
{
  "items": [
    { "egress_ip": "1.2.3.4", "lease_count": 120 },
    { "egress_ip": "2.2.2.2", "lease_count": 98 }
  ],
}
```

### Request Logs

#### 查询日志列表

**GET** `/request-logs`
Query（建议）：
* `from`: 时间窗起始，可选，RFC3339Nano
* `to`: 时间窗结束，可选，RFC3339Nano
* `limit`: 分页大小，可选，默认 50，最大 10000
* `cursor`: 游标分页位置，可选。由上一页返回的 `next_cursor` 透传。
* `platform_id`: 平台ID，可选
* `platform_name`: 平台名称，可选（精确匹配）
* `account`: 账号，可选
* `target_host`: 目标主机，可选
* `egress_ip`: 出口IP，可选
* `proxy_type`: 代理类型，可选，1/2
* `net_ok`: 网络是否成功，可选，0/1
* `http_status`: HTTP状态码，可选

返回结果按照时间倒序排序。返回摘要（不含 payload）：

```json
{
  "limit": 50,
  "has_more": true,
  "next_cursor": "Mzc2MDkzNDA3MDAwMDAwMDA6Y2RjYjVkZmQtMDQ2MC00ZjIzLWFlZWEtMWEzMjE2NmY2Y2I4",
  "items": [
    {
      "id": "uuid",
      "ts": "2026-02-10T12:34:56.123Z",
      "proxy_type": 2,
      "client_ip": "10.0.0.1",
      "platform_id": "uuid",
      "platform_name": "Default",
      "account": "acc1",
      "target_host": "www.google.com:443",
      "target_url": "https://www.google.com/search?q=hello",
      "node_hash": "....",
      "node_tag": "sub-A/HK-01",
      "egress_ip": "1.2.3.4",
      "duration_ms": 532,
      "net_ok": 1,
      "http_method": "GET",
      "http_status": 200,
      "ingress_bytes": 1024,
      "egress_bytes": 512,
      "payload_present": 0,
      "req_headers_len": 0,
      "req_body_len": 0,
      "resp_headers_len": 0,
      "resp_body_len": 0,
      "req_headers_truncated": false,
      "req_body_truncated": false,
      "resp_headers_truncated": false,
      "resp_body_truncated": false
    }
  ]
}
```

#### 获取单条日志

**GET** `/request-logs/{log_id}`

#### 获取 payload

**GET** `/request-logs/{log_id}/payloads`
返回（base64 编码）：

```json
{
  "req_headers_b64": "....",
  "req_body_b64": "....",
  "resp_headers_b64": "....",
  "resp_body_b64": "....",
  "truncated": {
    "req_headers": false,
    "req_body": true,
    "resp_headers": false,
    "resp_body": true
  }
}
```

### GeoIP

#### GeoIP 状态

**GET** `/geoip/status`

```json
{
  "db_mtime": "2026-02-12T07:00:00Z",
  "next_scheduled_update": "2026-02-13T07:00:00Z"
}
```

#### GeoIP 查询（调试）

**GET** `/geoip/lookup?ip=1.2.3.4`

```json
{
  "ip": "1.2.3.4",
  "region": "us"
}
```

#### 触发 GeoIP 立即更新（Action）

**POST** `/geoip/actions/update-now`
API 阻塞到更新完成

请求体：无。

错误码映射（最小集）：

* `500 INTERNAL`：下载或校验失败。

### 数据统计（用于 Dashboard）

#### 目标
* 为 Dashboard 提供只读查询接口，不在 API 层做二次聚合业务逻辑。
* 与 MetricsManager 的“实时 ring buffer + 历史 bucket + 一次性快照”三类能力一一对应。
* 保持全局视角与 Platform 视角的边界一致，不支持的维度组合直接返回 `400 INVALID_ARGUMENT`。
* 接口保持原子化，不提供固定聚合总览端点；前端按需组合请求。

#### 通用约定
* Dashboard 端点统一前缀：`/api/v1/metrics`
* 默认返回：
```json
{
  "items": [ ... ]
}
```
* 时间参数：
  * `from`、`to`：RFC3339Nano，可选。
  * 默认：`to=now`，`from=to-1h`。
  * 要求 `from < to`，否则 `400 INVALID_ARGUMENT`。
* `platform_id`：
  * 仅在支持 Platform 维度的端点可用。
  * 指定了不存在的平台时返回 `404 NOT_FOUND`。

#### 实时曲线（内存 ring buffer）

##### 吞吐（全局）
**GET** `/metrics/realtime/throughput?from=&to=`

```json
{
  "step_seconds": 1,
  "items": [
    {
      "ts": "2026-02-12T12:00:01Z",
      "ingress_bps": 12345,
      "egress_bps": 23456
    }
  ]
}
```

##### 连接数（全局）
**GET** `/metrics/realtime/connections?from=&to=`

```json
{
  "step_seconds": 5,
  "items": [
    {
      "ts": "2026-02-12T12:00:05Z",
      "inbound_connections": 220,
      "outbound_connections": 180
    }
  ]
}
```

##### 租约数量（Platform）
**GET** `/metrics/realtime/leases?platform_id=&from=&to=`

```json
{
  "platform_id": "uuid",
  "step_seconds": 5,
  "items": [
    {
      "ts": "2026-02-12T12:00:05Z",
      "active_leases": 612
    }
  ]
}
```

#### 历史曲线（`metrics.db` bucket）

##### 流量消耗（全局）
**GET** `/metrics/history/traffic?from=&to=`

```json
{
  "bucket_seconds": 3600,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "ingress_bytes": 123456789,
      "egress_bytes": 223456789
    }
  ]
}
```

##### 请求数与成功率（全局/Platform）
**GET** `/metrics/history/requests?from=&to=&platform_id=`

```json
{
  "bucket_seconds": 3600,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "total_requests": 100000,
      "success_requests": 99700,
      "success_rate": 0.997
    }
  ]
}
```

##### 访问延迟分布（全局/Platform）
**GET** `/metrics/history/access-latency?from=&to=&platform_id=`

```json
{
  "bucket_seconds": 3600,
  "bin_width_ms": 100,
  "overflow_ms": 3000,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "sample_count": 45678,
      "buckets": [
        { "le_ms": 100, "count": 1000 },
        { "le_ms": 200, "count": 2500 },
        ...
        { "le_ms": 3000, "count": 42 }
      ],
      "overflow_count": 8
    }
  ]
}
```

##### 主动探测次数（全局）
**GET** `/metrics/history/probes?from=&to=`

```json
{
  "bucket_seconds": 3600,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "total_count": 12000
    }
  ]
}
```

##### 节点数量（全局）
**GET** `/metrics/history/node-pool?from=&to=`

```json
{
  "bucket_seconds": 3600,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "total_nodes": 1500,
      "healthy_nodes": 1400,
      "egress_ip_count": 420
    }
  ]
}
```

##### 租约平均存活时间分布（Platform）
**GET** `/metrics/history/lease-lifetime?platform_id=&from=&to=`

```json
{
  "platform_id": "uuid",
  "bucket_seconds": 3600,
  "items": [
    {
      "bucket_start": "2026-02-12T11:00:00Z",
      "bucket_end": "2026-02-12T12:00:00Z",
      "sample_count": 3200,
      "p1_ms": 120000,
      "p5_ms": 300000,
      "p50_ms": 7200000
    }
  ]
}
```

#### 一次性快照（实时计算，不落库）

##### 全局节点池快照
**GET** `/metrics/snapshots/node-pool`

```json
{
  "generated_at": "2026-02-12T12:00:00Z",
  "total_nodes": 1500,
  "healthy_nodes": 1400,
  "egress_ip_count": 420,
  "healthy_egress_ip_count": 360
}
```

##### Platform 可路由节点快照
**GET** `/metrics/snapshots/platform-node-pool?platform_id=`

```json
{
  "generated_at": "2026-02-12T12:00:00Z",
  "platform_id": "uuid",
  "routable_node_count": 800,
  "egress_ip_count": 260
}
```

##### 节点延迟分布快照（全局/Platform）
**GET** `/metrics/snapshots/node-latency-distribution?platform_id=`

```json
{
  "generated_at": "2026-02-12T12:00:00Z",
  "scope": "global|platform",
  "platform_id": "uuid or null",
  "bin_width_ms": 100,
  "overflow_ms": 3000,
  "sample_count": 1400,
  "buckets": [
    { "le_ms": 100, "count": 120 },
    { "le_ms": 200, "count": 300 },
    ...
    { "le_ms": 3000, "count": 40 }
  ],
  "overflow_count": 6
}
```

#### Dashboard 端点与统计项映射
* 吞吐（实时）：`GET /metrics/realtime/throughput`
* 流量消耗（历史）：`GET /metrics/history/traffic`
* 请求数（历史）：`GET /metrics/history/requests`
* 连接数（实时）：`GET /metrics/realtime/connections`
* 主动探测次数（历史）：`GET /metrics/history/probes`
* 节点数量（历史 + 快照）：`GET /metrics/history/node-pool`、`GET /metrics/snapshots/node-pool`
* Platform 节点数量（快照）：`GET /metrics/snapshots/platform-node-pool`
* 节点延迟分布（快照）：`GET /metrics/snapshots/node-latency-distribution`
* 访问延迟（历史）：`GET /metrics/history/access-latency`
* 访问成功率（历史）：`GET /metrics/history/requests`（由 `success_requests / total_requests` 计算）
* 租约数量（实时）：`GET /metrics/realtime/leases`
* 租约平均存活时间分布（历史）：`GET /metrics/history/lease-lifetime`

## 其他细节

### GeoIP 下载与订阅下载的错误重试机制抽象
GeoIP 与订阅的下载都有错误重试的需求。
第一次使用本地网络下载。如果网络层面失败，再尝试两次从 Default 平台随机选代理节点进行下载。不使用任何退避算法，失败立刻重试。
这套机制要抽象出来，不能在 GeoIP 和订阅下载中都单独实现一遍。

## Resin 全局设置
### 环境变量设置项（不支持热更新）
* RESIN_CACHE_DIR：缓存目录。默认 /var/cache/resin
* RESIN_STATE_DIR：配置目录。默认 /var/lib/resin
* RESIN_LOG_DIR：日志目录。默认 /var/log/resin
* RESIN_LISTEN_ADDRESS：Resin 统一监听地址。默认 `0.0.0.0`
* RESIN_PORT：Resin 单端口（控制面 API + 正向代理 + 反向代理 + WebUI）。默认 2260
* RESIN_API_MAX_BODY_BYTES：控制面 API（`/api/*`）请求体最大字节数。超限返回 `413 PAYLOAD_TOO_LARGE`。仅作用于控制面，不作用于正/反向代理数据面。默认 1048576（1 MiB）。

核心设置：
* `RESIN_MAX_LATENCY_TABLE_ENTRIES`：每个节点的延迟表的最大表项数。默认 128。
* `RESIN_PROBE_CONCURRENCY`：节点探测的最大并发数量，默认 1000。
* `RESIN_GEOIP_UPDATE_SCHEDULE`：GeoIP 数据库自动更新的 Cron 表达式。默认 "0 7 * * *"。
* `RESIN_DEFAULT_PLATFORM_STICKY_TTL`：默认平台粘性会话时长。默认 "168h"。
* `RESIN_DEFAULT_PLATFORM_REGEX_FILTERS`：默认平台正则过滤器（JSON 字符串数组）。默认 `[]`。
* `RESIN_DEFAULT_PLATFORM_REGION_FILTERS`：默认平台地区过滤器（JSON 字符串数组，小写 ISO 3166-1 alpha-2）。默认 `[]`。
* `RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION`：默认平台反代 miss 行为。枚举：`RANDOM|REJECT`。默认 `RANDOM`。
* `RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR`：默认平台在反代 Account 为空时的行为。枚举：`RANDOM|FIXED_HEADER|ACCOUNT_HEADER_RULE`。默认 `ACCOUNT_HEADER_RULE`。
* `RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER`：默认平台固定提取 Header 列表（多行，每行一个 Header）。仅当上项为 `FIXED_HEADER` 时必须至少提供一个合法 Header。默认 `Authorization`。
* `RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY`：默认平台分配策略。枚举：`BALANCED|PREFER_LOW_LATENCY|PREFER_IDLE_IP`。默认 `BALANCED`。
* `RESIN_PROBE_TIMEOUT`：单次探测请求超时。默认 "15s"。
* `RESIN_RESOURCE_FETCH_TIMEOUT`：资源下载（订阅/GeoIP）单次尝试超时。默认 "30s"。

日志相关配置：
* `RESIN_REQUEST_LOG_QUEUE_SIZE`：日志写入队列大小。至少是 RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE 的两倍。默认 8192。
* `RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE`：批量写入数据库的大小。默认 4096.
* `RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL`：写库间隔。默认 "5m"。
* `RESIN_REQUEST_LOG_DB_MAX_MB`：SQLite 当前活动日志数据库的最大字节数。默认 512。
* `RESIN_REQUEST_LOG_DB_RETAIN_COUNT`：保留的历史日志数据库文件数量（滚动日志），默认 5。

认证设置：
* `RESIN_ADMIN_TOKEN`：访问 WebAPI 的认证 Token。环境变量必须定义；允许为空字符串。为空时关闭控制面鉴权。
* `RESIN_PROXY_TOKEN`：访问代理的认证 Token。环境变量必须定义；允许为空字符串。为空时关闭正向/反向代理鉴权；非空时不能包含 : @ 符号，且不能取保留值 `api`、`healthz`、`ui`。
* 当 Token 非空但强度较弱时，WebUI 首页会显示安全告警条幅（不阻止启动）。

数据统计设置：
* `RESIN_METRIC_THROUGHPUT_INTERVAL_SECONDS`：统计网速的时间间隔，默认 1s。
* `RESIN_METRIC_THROUGHPUT_RETENTION_SECONDS`：保留网速统计数据的时间，默认 3600s。
* `RESIN_METRIC_BUCKET_SECONDS`：统计窗口大小，默认 3600s。
* `RESIN_METRIC_CONNECTIONS_INTERVAL_SECONDS`：统计连接数的时间间隔，默认 5s。
* `RESIN_METRIC_CONNECTIONS_RETENTION_SECONDS`：保留连接数统计数据的时间，默认 18000s。
* `RESIN_METRIC_LEASES_INTERVAL_SECONDS`：统计租约数量的时间间隔，默认 5s。
* `RESIN_METRIC_LEASES_RETENTION_SECONDS`：保留租约数量统计数据的时间，默认 18000s。
* `RESIN_METRIC_LATENCY_BIN_WIDTH_MS`：延迟统计桶大小，默认 100ms。
* `RESIN_METRIC_LATENCY_BIN_OVERFLOW_MS`：延迟统计溢出值，默认 3000ms。

### 运行时全局设置项（支持热更新）
Resin 支持通过 API (`PATCH /system/config`) 动态调整大部分全局运行参数。配置文件存储于数据库。
以下所有配置项支持热更新。

#### 基础设置
* `UserAgent`: Resin 发起资源下载（订阅/GeoIP）HTTP 请求时使用的 User-Agent 头。默认 "sing-box"。

#### 请求日志设置
* `RequestLogEnabled`: 是否开启请求日志记录。此开关实时生效。默认 False。
* `ReverseProxyLogDetailEnabled`: 是否记录反向代理的详细日志（请求/响应头与体）。默认 False。
* `ReverseProxyLogReqHeadersMaxBytes`: 记录请求头的最大字节数。默认 4KB。
* `ReverseProxyLogReqBodyMaxBytes`: 记录请求体的最大字节数。默认 1KB。
* `ReverseProxyLogRespHeadersMaxBytes`: 记录响应头的最大字节数。默认 1KB。
* `ReverseProxyLogRespBodyMaxBytes`: 记录响应体的最大字节数。默认 1KB。

#### 健康检查参数
* `MaxConsecutiveFailures`: 触发熔断的连续失败次数阈值。默认 3。
* `MaxLatencyTestInterval`: 节点最大延迟探测间隔。最小 30 秒。默认 5 分钟。
* `MaxAuthorityLatencyTestInterval`: 权威域名（如 cloudflare.com）的最大延迟探测间隔。最小 30 秒。默认 1 小时。
* `MaxEgressTestInterval`: 节点出口 IP 探测的最大间隔。最小 30 秒。默认 1 天。

#### 探测设置
* `LatencyTestURL`: 主动延迟探测的目标 URL。默认 `https://www.gstatic.com/generate_204`。一定属于 LatencyAuthorities 之一。如果不属于就加入。
* `LatencyAuthorities`: 权威域名列表。默认 `["gstatic.com", "google.com", "cloudflare.com", "github.com"]`。

#### P2C 选路设置
* `P2CLatencyWindow`: 在 P2C 选路时，仅考虑该时间窗口内更新过的延迟数据。默认 10 分钟。
* `LatencyDecayWindow`: TD-EWMA 算法的时间衰减窗口。默认 10 分钟。

#### 持久化设置
* `CacheFlushInterval`: 运行时脏数据（Cache）刷盘到磁盘的时间间隔。默认 5 分钟。
* `CacheFlushDirtyThreshold`: 触发刷盘的脏数据条目数阈值。默认 1000。

> `EphemeralNodeEvictDelay` 不属于全局配置，已改为订阅级字段 `ephemeral_node_evict_delay`（默认 72h）。

# 附录

## ExtractDomain 实现
```go
import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ExtractDomain 从 host:port 字符串中提取有效的主域名 (eTLD+1)。
//
// 示例:
//
//	"www.google.co.uk:443" -> "google.co.uk"
//	"api.sina.com.cn"      -> "sina.com.cn"
//	"192.168.1.1:8080"     -> "192.168.1.1"
//	"localhost"            -> "localhost"
//	"[::1]:80"             -> "::1"
func ExtractDomain(target string) string {
	// 如果是 URL (包含 :// 或以 // 开头)，先解析出 Host
	if strings.Contains(target, "://") || strings.HasPrefix(target, "//") {
		if u, err := url.Parse(target); err == nil && u.Host != "" {
			target = u.Host
		}
	}

	host := target

	// 分离端口
	// net.SplitHostPort 可以处理 "host:port" 和 "[ipv6]:port"
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		// 如果分离失败（例如没有端口），我们需要手动处理 IPv6 的方括号
		// 例如 "[::1]" -> "::1"
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}

	// 使用 Public Suffix List 提取主域名 (eTLD+1)
	// 如果是 IP 地址、localhost 或顶级后缀（如 "co.uk"），这里会返回 error
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}

	// 兜底策略：如果提取失败（比如是 IP 地址或内网域名），直接返回 host
	return host
}
```

## HashFromRawOptions 实现
```go
import (
	"encoding/json"

	"github.com/zeebo/xxh3"
)

// NodeHash is a 16-byte unique identifier for a node.
type NodeHash [16]byte

func hashBytes128(input []byte) NodeHash {
	sum := xxh3.Hash128(input)
	// sum.Bytes() returns [16]byte in LittleEndian
	return NodeHash(sum.Bytes())
}

// HashFromRawOptions calculates canonical node hash from raw uncanonicalized options.
// If JSON parsing fails, it falls back to hashing raw bytes directly.
func HashFromRawOptions(rawOptions []byte) NodeHash {
	var generic map[string]any
	if err := json.Unmarshal(rawOptions, &generic); err == nil {
		delete(generic, "tag")
		if canonical, err := json.Marshal(generic); err == nil {
			return hashBytes128(canonical)
		}
	}
	return hashBytes128(rawOptions)
}
```

## tlsLatencyConn 实现
```go
// tlsLatencyConn 包装 net.Conn 以测量 TLS 握手延迟。
// 它通过劫持并分析流量特征（Client Hello 发出时间 vs Server Hello 收到时间）来估算 RTT。
type tlsLatencyConn struct {
	net.Conn
	// ... fields
	state     uint32 // 0=init, 1=handshake_started, 2=done
	startTime int64
}

func (c *tlsLatencyConn) Write(b []byte) (int, error) {
	// Fast path: 握手已完成
	if atomic.LoadUint32(&c.state) != 0 {
		return c.Conn.Write(b)
	}

	n, err := c.Conn.Write(b)

	// 捕获第一个写入包 (Client Hello)，开始计时
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 0, 1) {
			atomic.StoreInt64(&c.startTime, time.Now().UnixNano())
		}
	}
	return n, err
}

func (c *tlsLatencyConn) Read(b []byte) (int, error) {
	// Fast path: 并非握手阶段
	if atomic.LoadUint32(&c.state) != 1 {
		return c.Conn.Read(b)
	}

	n, err := c.Conn.Read(b)

	// 捕获第一个读取包 (Server Hello)，结束计时
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 1, 2) {
			startNano := atomic.LoadInt64(&c.startTime)
			if startNano > 0 {
				latency := time.Duration(time.Now().UnixNano() - startNano)
				// 异步上报延迟...
				go c.recordLatency(latency)
			}
		}
	}
	return n, err
}
```

## httptrace Latency Probe 实现 (反向代理)
```go
// ReverseProxy 中使用 httptrace 测量 TLS 握手延迟
// 这种方式利用 Go 标准库钩子，无需劫持底层连接
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ...
    var tlsStart time.Time
    if protocol == "https" {
        trace := &httptrace.ClientTrace{
            TLSHandshakeStart: func() {
                tlsStart = time.Now()
            },
            TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
                if err == nil && !tlsStart.IsZero() {
                    latency := time.Since(tlsStart)
                    // 异步记录延迟（latency=nil 时仅记录尝试）
                    go p.health.RecordLatency(result.NodeID, domain, &latency)
                }
            },
        }
        requestCtx = httptrace.WithClientTrace(requestCtx, trace)
    }
    // ...
}
```

## GeoIP 查询参考

```go
import (
	"net"
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

// 查询 IP 地区：直接调用 maxminddb reader
func lookupRegion(dbPath string, ip netip.Addr) string {
	reader, _ := maxminddb.Open(dbPath)
	defer reader.Close()

	var result struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	_ = reader.Lookup(net.IP(ip.AsSlice()), &result)
	return strings.ToLower(result.Country.ISOCode) // 返回 ISO 3166-1 alpha-2 小写代码，如 "cn", "us"
}
```
