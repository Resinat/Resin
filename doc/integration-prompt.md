# 中侵入接入指南（AI Prompt 模板）

如果您需要使用粘性代理，并且选择原生接入 Resin，可以把下面的 Prompt 提供给 AI Agent 来做接入改造。

```text
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
* 如果确实存在账号登录前没有标识可用的情况，可先使用一个临时标识（TempIdentity）发请求；等到登录成功并获得稳定标识后，调用 `POST <resin_url>/api/v1/<PLATFORM>/actions/inherit-lease`，Body 传入 `{"parent_account": "<TempIdentity>", "new_account": "<StableIdentity>"}`，来将历史临时身份的 IP 租约平滑继承给新的稳定身份。注意不要把 TempIdentity 固定，否则所有的账号都会继承自同一个租约！

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
```
