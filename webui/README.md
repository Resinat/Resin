# Resin WebUI (v1)

## 启动

```bash
cd webui
npm install
npm run dev
```

默认开发地址：`http://127.0.0.1:5173`

开发代理默认转发到 `http://127.0.0.1:2620`：
- `/api/*`
- `/healthz`

可通过环境变量覆盖：

```bash
VITE_DEV_API_TARGET=http://127.0.0.1:2620 npm run dev
```

注意：
- `npm run dev` 才会启用 Vite 代理（`/api -> :2620`）。
- `npm run preview` 不走代理；如果用 preview，需要在构建前设置 `VITE_API_BASE_URL` 指向同源可访问的 API 地址，并保证后端允许该访问方式。

## 当前范围

- 已完成：应用框架（登录、鉴权守卫、导航壳层）
- 已完成：`Dashboard` 页面
  - 时间窗口切换（15m / 1h / 6h / 24h）
  - 平台维度选择（自动首个平台或手动指定）
  - KPI 总览：吞吐、连接、节点健康、active leases
  - 趋势可视化：吞吐、连接、请求成功率、流量、节点池、probe、租约寿命分位
  - 分布可视化：节点延迟直方图（global + platform）
- 已完成：`Platform` 管理页面
  - 列表查询
  - 搜索过滤
  - 创建 Platform
  - 编辑 Platform
  - 删除 Platform
  - 重置为默认配置
  - 重建 Routable View
- 已完成：`Subscription` 管理页面
  - 列表查询（支持启用状态筛选）
  - 搜索过滤
  - 创建 Subscription
  - 编辑 Subscription
  - 删除 Subscription
  - 手动刷新 Subscription
- 已完成：`Nodes` 页面
  - 服务端筛选（platform/subscription/region/egress/circuit/outbound/updated_since）
  - 列表检索与详情展示
  - 触发出口探测
  - 触发延迟探测
- 已完成：`Request Logs` 页面
  - 游标分页（上一页/下一页）
  - 条件筛选（from/to/platform/account/target/egress/proxy/net/http_status）
  - 日志表格详情查看
  - payload 拉取与 base64 解码显示
- 已完成：`Header Rules` 页面
  - 规则列表与关键字过滤（prefix/header）
  - 规则创建/编辑（PUT upsert）
  - 规则删除（DELETE）
  - Resolve 调试（POST `:resolve`）
- 已完成：`GeoIP` 页面
  - GeoIP 数据库状态查看（db_mtime / next_scheduled_update）
  - 单 IP 查询（GET `/api/v1/geoip/lookup`）
  - 批量查询（POST `/api/v1/geoip/lookup`）
  - 立即更新数据库（POST `/api/v1/geoip/actions/update-now`）
- 已完成：`System Config` 页面
  - RuntimeConfig 全量分组编辑
  - 本地差异计算（仅提交变更字段）
  - PATCH Preview JSON 预览
  - 重新加载与草稿重置

## 技术栈

- React + TypeScript + Vite
- React Router
- TanStack Query
- React Hook Form + Zod
- Zustand
- 自定义亮色主题（Manrope + Sora）
