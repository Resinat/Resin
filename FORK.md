# Fork 说明 — Resin v1.2.0-egressipv

本仓库 = 上游 [Resinat/Resin](https://github.com/Resinat/Resin) v1.2.0(tag `v1.2.0`,commit `42dff8c0`)+ 一个自维护补丁。

## 补丁内容:平台按出口 IP 版本过滤

**动机**:原生 Platform 过滤只有 tag 正则(`regex_filters`)和 GeoIP 地区(`region_filters`)两个维度,
无法按出口 IP 是 IPv4 还是 IPv6 分流。典型场景:下游业务只想要 IPv4 出口。
注意:出口版本是探活后的动态属性(同一台 IPv4 服务器也可能从 IPv6 出口),所以必须在路由判定时过滤,
不可能靠订阅入口端拆分实现。

**改动**:Platform 新增字段 `egress_ip_version`,取值:

| 值 | 含义 |
|----|------|
| `""`(默认) | 不过滤,兼容旧行为 |
| `"ipv4"` | 只路由 IPv4 出口的节点 |
| `"ipv6"` | 只路由 IPv6 出口的节点(4-in-6 视为 IPv4) |

其他值一律 400 拒绝。生效点:`Platform.evaluateNode()` 第 3 步(出口 IP 已知)之后。

**API**:
```bash
# 创建
curl -X POST -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"name":"IPv4","egress_ip_version":"ipv4"}' $BASE/api/v1/platforms

# 修改既有平台(热重建可路由视图)
curl -X PATCH -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"egress_ip_version":"ipv4"}' $BASE/api/v1/platforms/{id}

# preview-filter 的 platform_spec 也支持该字段
```

## 改动文件清单

| 文件 | 改动 |
|------|------|
| `internal/model/models.go` | `Platform.EgressIPVersion` 字段 |
| `internal/platform/platform.go` | runtime 字段 + `evaluateNode` 版本判定(3b 步) |
| `internal/platform/model_codec.go` | `ValidateEgressIPVersion` + `NewConfiguredPlatform`/`BuildFromModel` 透传 |
| `internal/service/control_plane_platform.go` | 请求/响应/config 透传、Create/PATCH/preview 支持、校验 |
| `internal/service/control_plane_system.go` | PATCH 白名单加 `egress_ip_version` |
| `internal/service/control_plane_test.go` | 3 处 `NewConfiguredPlatform` 调用补参 |
| `internal/state/repo_state.go` | platforms 表 INSERT/SELECT 含新列 |
| `internal/state/migrate.go` | `stateVersionAddEgressIPVersion = 9`,`stateLatestVersion = 9` |
| `internal/state/migrations/state/000009_platforms_add_egress_ip_version.up/down.sql` | 新迁移(up 带 `CREATE TABLE IF NOT EXISTS` 兜底,兼容无 platforms 表的 legacy 库) |

## 构建

```bash
# 前端(需 node/npm;产出被 embed)
cd webui && npm ci && npm run build && cd ..

# linux/arm64 示例
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w \
    -X github.com/Resinat/Resin/internal/buildinfo.Version=v1.2.0-egressipv \
    -X github.com/Resinat/Resin/internal/buildinfo.GitCommit=42dff8c0+egressipv \
    -X github.com/Resinat/Resin/internal/buildinfo.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o resin-linux-arm64 ./cmd/resin
```

## 版本兼容注意

- 本补丁引入 state migration 000009(表版本 9)。从打过本补丁的 state.db 回滚到官方 v1.2.0
  二进制前,需同时回滚 state 库(官方二进制最新只认版本 8,直接回滚会因库版本过新无法启动)。
- 若上游出新版,合并上游后重新套用:`git diff v1.2.0..HEAD`。
