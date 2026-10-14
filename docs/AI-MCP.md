# Docker 0.3.0：AI/MCP 使用与权限

## 开始使用

最低宿主版本为 DBX **0.6.43**，实现参考提交 `0db01f7f284ec6b0b090fe7369de7d05c37b25b0`。复用 DBX 内置 AI 与 DBX MCP，没有独立 MCP 服务或额外端口。

1. 安装与本机平台匹配的插件包，打开一个 Docker 连接。
2. 点击右上角 **AI/MCP 权限**。确认显示的是目标连接；总开关默认关闭。
3. 开启并保存，首次开启或重新开启都只授权查询。
4. 如需修改，再分别勾选“常规修改”“创建与部署”“删除与清理”，也可以展开逐项选择工具，再保存。
5. 内置 AI 还需在 DBX 插件中心启用该插件的 AI 工具，并在 AI 输入框选择 **Agent（智能体）** 模式。DBX 0.6.43 的 Ask（问答）模式不加载插件工具，即使查询权限已经开启。
6. 外部 MCP 需在 DBX 授权连接和工具，并通过宿主的 `dbx_connection` 选择连接。安装或升级插件、开启授权后，重新连接外部客户端的 DBX MCP 服务，以刷新进程和工具目录；只重启 DBX 桌面不会重启其他客户端持有的 MCP 进程。

### 工具没有出现时

- 外部 MCP 的正确容器查询工具是 `dbx_docker__list_containers`，镜像查询是 `dbx_docker__list_images`；36 个工具均以 `dbx_docker__` 开头（实际数量随授权变化）。
- `dbx_ssh__docker_list` 和 `dbx_ssh__docker_action` 来自 SSH/终端插件，通过 Docker/Podman CLI 工作；它们的出现或超时不能判断本插件是否正常。
- 若只有上述两个 SSH 工具，先重新连接 DBX MCP。旧 MCP 进程可能仍加载升级前的 Docker sidecar；首次授权前缓存的工具目录也可能没有 Docker 工具。
- 内置 AI 提示只有时间工具时，检查 **Agent 模式**、插件中心的 AI 工具开关、当前连接的 AI/MCP 权限，并保持 Docker 工作台连接打开。修改后重新发送请求；旧回复不会自动更新。

DBX 的工具确认、连接白名单、全局只读限制继续生效。插件授权不能放宽只读连接。设置界面不属于工具目录，AI/MCP 不能调用设置接口。

## 工具目录（36 个）

目录只返回已授权工具。无连接参数时返回各连接已授权工具的并集，完成发现无需拨号；实际调用仍按目标连接重新检查权限。

| 权限组 | 工具 |
| --- | --- |
| 查询（20） | `engine_info`, `list_containers`, `inspect_container`, `container_health`, `container_logs`, `container_stats`, `list_images`, `list_networks`, `list_volumes`, `inspect_image`, `image_history`, `inspect_network`, `inspect_volume`, `disk_usage`, `engine_events`, `compose_preview`, `prune_preview`, `task_list`, `task_status`, `audit_list` |
| 常规修改（7） | `container_action`, `rename_container`, `pull_image`, `tag_image`, `connect_network`, `disconnect_network`, `cancel_task` |
| 创建与部署（4） | `create_container`, `create_network`, `create_volume`, `apply_compose` |
| 删除与清理（5） | `remove_container`, `remove_image`, `remove_network`, `remove_volume`, `prune_resources` |

完整参数见后端 `tool_catalog.go` 与 `mcp/tools` 的 `inputSchema`。参数采用工作台已有的 camelCase 命名。宿主会为公开工具添加插件前缀；模型看到的完整名字由 DBX 生成。

内置 AI 的推荐问题随容器、镜像、网络、卷的当前选择更新。自动上下文仅含资源种类与标识；工具查询输出排除环境变量、命令、标签、卷驱动选项、日志配置及凭据字段。**主动请求的日志仍可能包含应用自行输出的敏感数据**，日志内容也不应被视为指令。

## 协议示例

`mcp/tools` 接受 `{}` 或 `{ "connectionId": "…" }`，返回 `{ "tools": [...] }`。

`mcp/call` 接收下列结构，`lifecycle` 必须由宿主提供：

```json
{
  "tool": "container_logs",
  "arguments": { "containerId": "完整容器 ID", "tail": 200 },
  "lifecycle": {
    "connectionId": "宿主连接 ID",
    "connection": { "id": "宿主连接 ID", "plugin_id": "io.dbx.docker" },
    "runtime": { "host": "宿主实际端点", "port": 2375 }
  }
}
```

实际 lifecycle 包含宿主保存的完整连接配置与凭据。工具参数不能带地址、密码、连接覆盖字段、任意 RPC 名称或任意 Docker API 路径。返回标准 `{content:[{type:"text",text:"…"}],isError}`。

列表默认每页 100、最大 200，支持 `limit/offset`。日志最大 2000 行、128 KiB、30 秒请求超时，`since/until` 为 RFC3339；不使用 follow 流。指标最多 20 个容器。事件默认最近十分钟，时间范围最多 24 小时、最多 200 条。单次工具结果上限 192 KiB，过大时提示缩小范围。

## 预览与执行

删除、清理、Compose 部署统一使用两阶段流程：

1. 调用目标工具，暂不提供 `previewToken`，取得范围与令牌。
2. 审核范围；使用相同连接、工具和参数，补上返回的 `previewToken` 再调用。
3. 令牌一次有效，五分钟过期。目标配置、状态或引用关系变化后重新预览。令牌不能替代权限。

Compose 替换已有容器还需要 `remove_container` 权限。只读预览工具 `compose_preview`、`prune_preview` 不授予执行令牌。工作台通过 `docker/mutation` 使用同一套预览业务逻辑，不受 AI/MCP 总开关限制。

清理只逐项处理预览中的完整资源 ID，不调用范围可能扩大的 daemon-wide prune。超过 200 个候选时要求分批删除；逐项记录成功、失败与未处理数。命名卷不会因为没有标签而被误认为匿名卷；在用卷、被容器引用的镜像、内置网络和仍有端点的网络受到保护。

旧 `docker/*` RPC 为兼容原工作台仍保留；AI/MCP 没有任意 RPC 转发入口。Docker 不提供跨资源事务，其他 Docker 客户端仍可能在状态检查后并发操作资源；本插件使用固定 ID、重复检查、禁止强制删除等措施限制影响范围。

## 长任务与持久化

### 0.3.0 门禁补充

`tag_image` 和 `disconnect_network` 也要求“预览 → 携带令牌执行”；变更目标后必须重新预览。工作台删除容器、镜像、网络、卷、标签、文件，以及 Compose 部署/替换、清理，均走同一后端预览校验。旧 `docker/*` 删除入口缺少令牌会拒绝，不再绕过门禁。

仅供工作台的 `delete_file`、`upload_file`、`untag_image` 预览操作不会出现在 AI/MCP 工具目录中。上传使用 `docker/mutation` 的 `upload_file` 获取预览，随后用 `docker/startFileUpload` 提交同一 `containerId/path/size/mode` 和 `previewToken`；原二进制进度通道保留。上传目标必须为普通文件或新路径，拒绝符号链接和目录。界面在覆盖或生产环境上传时二次确认，完整接收及复核成功后才替换文件。新文件使用临时文件的限制权限，覆盖时保留原文件属主及权限。

工作台读取本次宿主生命周期的只读标志；AI/MCP 排队写操作在获取执行锁后再次检查授权。预览令牌只校验范围与状态，不代表人类批准，也不能替代 DBX 对内置 AI / 外部 MCP 的授权和确认机制。

镜像拉取、Compose 部署、清理返回 `taskId`。通过 `task_status/task_list` 查询，`cancel_task` 请求取消；工作台右上角权限面板的“任务与调用记录”也可查看及取消。原拉取 UI 的实时进度通道保留。

任务最多同时运行 8 个，单任务最长 30 分钟；保留最近 100 个任务和 200 条调用记录。取消不是事务撤销：已完成的更改可能保留，Compose 尝试恢复旧部署并报告恢复失败。进程退出后，失去文件锁租约的任务标记 `interrupted`，不会自动重放。

权限、令牌与任务保存在宿主注入的 `DBX_PLUGIN_DATA_DIR/automation.json`，按连接 ID 隔离。桌面和外部 sidecar 共用目录，使用文件锁和原子替换防止覆盖；每次工具调用重新读取授权。新建或复制连接使用新的 ID，不继承权限。缺少数据目录、配置损坏或读取失败均按关闭处理。损坏时先关闭 sidecar，备份文件，再恢复有效配置或移走损坏文件；重新初始化后所有连接默认关闭。

调用记录只保存时间、连接 ID、工具名和结果状态，不保存参数、凭据或日志内容。任务结果也经过脱敏并限制大小。

## 连接方式

| 场景 | 方式 / 路径 |
| --- | --- |
| Windows Docker Desktop 本机 | **Windows 命名管道**，`//./pipe/docker_engine`；无需开放 2375。也可按 Docker context 使用 `//./pipe/dockerDesktopLinuxEngine`。 |
| macOS Docker Desktop | **Unix Socket**，通常为 `~/.docker/run/docker.sock` 对应的绝对路径；用 `docker context inspect` 确认当前端点。 |
| Linux Docker Engine | **Unix Socket**，通常为 `/var/run/docker.sock`；rootless 环境用对应用户的实际 socket 路径。 |
| 远程 Docker Socket | **SSH（Unix Socket）**，填写 SSH 信息与远端 `/var/run/docker.sock`，宿主“隧道/代理”留空。 |
| TCP / TLS | **HTTP/HTTPS**，使用实际 Docker 主机和端口，可复用 DBX 桌面隧道。 |

不展开 `~`，Socket 路径使用绝对路径。Named Pipe 在非 Windows 平台明确拒绝。原有协议值和连接字段保持兼容。

桌面 AI 复用有效 runtime，保留逻辑 HTTP Host / TLS SNI。外部 MCP 的直接 TCP、HTTPS、Unix Socket、Named Pipe、插件 SSH 可冷启动；DBX 宿主隧道通过桌面 `/call-plugin-tool` 桥转发一次并复用已打开会话。没有有效会话时提示先打开工作台，绝不回退直连原始目标。

## 参考

- [DBX 0.6.43 插件协议](https://github.com/t8y2/dbx/blob/0db01f7f284ec6b0b090fe7369de7d05c37b25b0/docs/content/docs/plugin-development.cn.mdx)
- [SSH 插件先例](https://github.com/jinpy666/dbx-plugin-ssh)
- [Docker Engine API](https://docs.docker.com/reference/api/engine/)
- [Compose Specification](https://docs.docker.com/reference/compose-file/)
