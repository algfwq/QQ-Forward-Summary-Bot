# QQ Forward Summary Bot

一个基于 Go 的 NapCatQQ 私聊机器人。

## 功能

- 作为反向 WebSocket 服务端，等待 NapCat 主动连接
- 从同一条 WebSocket 连接接收私聊消息事件
- 识别私聊中的合并转发消息段 `forward`
- 通过同一条 WebSocket 调用 NapCat 的 `get_forward_msg` 递归展开所有嵌套合并转发
- 将整理后的聊天记录和可访问图片一起发送到 OpenAI 兼容接口做多模态总结
- 通过 `reply` 消息段和 `send_private_msg` 把总结结果回复给原消息发送者
- 将运行日志同时输出到控制台和本地日志文件
- 对 AI 返回结果做纯文本净化，禁止发送 Markdown 格式回复

## 处理范围

- 只处理私聊消息，不处理群聊消息
- 只在消息中检测到合并转发段时触发总结
- 支持递归展开嵌套合并转发
- 支持多模态模型输入：会把转发中能直接访问的图片一并发送给模型
- 如果图片段没有可用的 `url`、`data:` 或 `base64://` 内容，则仍会保留文字占位，但不会把该图片上传给模型
- 回复内容会尽量保持为纯文本，不包含标题、列表、代码块、引用、加粗、反引号等 Markdown 语法

## 依赖

- Go 1.24+
- 已运行的 NapCatQQ，并开启 OneBot 反向 WebSocket
- 一个支持 `/chat/completions` 的 OpenAI 兼容接口
- 如果需要图片分析，所用模型必须支持多模态输入

## 配置

编辑根目录下的 `config.example.json`，记得改名为`config.json`：

- `server.listen`: 本程序监听地址
- `server.ws_path`: 反向 WebSocket 路径
- `server.access_token`: 可选，校验 NapCat 连接时的 `Authorization` 头或 `access_token` 查询参数
- `log.dir`: 日志目录，默认 `logs`
- `log.file_prefix`: 日志文件名前缀
- `openai.base_url`: OpenAI 兼容接口基础地址，例如 `https://api.openai.com/v1`
- `openai.api_key`: OpenAI 兼容接口密钥
- `openai.model`: 模型名
- `openai.request_timeout_seconds`: OpenAI 请求超时秒数
- `openai.temperature`: 模型温度
- `openai.system_prompt`: 总结用系统提示词
- `bot.process_timeout_seconds`: 单次处理私聊转发消息的最大耗时
- `bot.max_forward_depth`: 递归展开嵌套合并转发的最大深度
- `bot.summary_input_limit`: 发给 AI 之前的最大字符数，超出会自动截断
- `bot.max_images`: 最多附带多少张图片给多模态模型

推荐关注这几个字段：

- `log.dir`: 建议保留默认值 `logs`
- `openai.model`: 需要填写实际支持的模型名
- `bot.max_images`: 图片很多时可适当调小，避免请求过大

配置示例：

```json
{
  "server": {
    "listen": ":8080",
    "ws_path": "/ws",
    "access_token": "",
    "read_header_timeout_seconds": 10
  },
  "log": {
    "dir": "logs",
    "file_prefix": "qq-summary-bot"
  },
  "openai": {
    "base_url": "https://your-openai-compatible-host/v1",
    "api_key": "your-api-key",
    "model": "your-multimodal-model",
    "request_timeout_seconds": 60,
    "temperature": 0.2,
    "system_prompt": "你是一个负责整理 QQ 聊天记录的助手。请基于给出的聊天记录用中文输出准确、克制的总结，不要编造未出现的信息。优先提炼主题、结论、待办、时间地点人物，以及仍未解决的问题。若内容杂乱，请分段总结。总长度控制在 400 字以内。"
  },
  "bot": {
    "process_timeout_seconds": 120,
    "max_forward_depth": 8,
    "summary_input_limit": 12000,
    "max_images": 12
  }
}
```

## 启动

```powershell
go run . -config config.json
```

启动后会同时输出到控制台和本地日志文件，例如 `logs/qq-summary-bot-20260405.log`。

## NapCat 侧配置建议

1. 打开 NapCat 的反向 WebSocket。
2. 将反向 WebSocket 地址设置为 `ws://127.0.0.1:8080/ws`。
3. 如果设置了 `server.access_token`，则让 NapCat 连接时带上相同的 Token。
4. 保证 NapCat 能把私聊消息事件通过这条反向 WebSocket 发出来。
5. 保证转发消息中的图片段能提供可访问的图片地址或可用数据，否则只能作为文字占位参与总结。

## 工作流程

1. 用户给机器人发私聊合并转发消息。
2. NapCat 通过反向 WebSocket 把私聊事件发给本程序。
3. 程序从消息段中找到 `forward.id`。
4. 程序通过同一条 WebSocket 调用 `get_forward_msg` 获取合并转发详情，并递归处理其中再次出现的 `forward`。
5. 程序整理文字内容，并收集其中可直接发送给模型的图片。
6. 程序将整理后的记录和图片一并发送给 OpenAI 兼容 API 总结。
7. 程序对模型输出做纯文本净化，去除 Markdown 语法。
8. 程序通过同一条 WebSocket 调用 `send_private_msg`，用 `reply` 段回复总结结果。

## 日志说明

- 日志会写入 `log.dir` 指定目录
- 日志文件按日期命名，例如 `qq-summary-bot-20260405.log`
- 当前会记录监听启动、NapCat 连接断开、收到可处理消息、展开转发、总结输入规模、回复发送结果等信息

## 多模态说明

- 图片会按在合并转发中出现的顺序编号为“图片#1”“图片#2”并与文字整理稿对应
- 当前优先使用图片段中的 `url`
- 如果图片段提供的是 `data:` 地址，也会直接发送给模型
- 如果图片段提供的是 `base64://` 数据，会转换为 `data:` URL 后发送给模型
- 如果图片段只有本地文件名或不可访问路径，则不会直接上传给模型，但整理稿里仍会保留图片占位描述

## 回复格式说明

- 回复消息固定要求为中文纯文本
- 程序会在发送前清理常见 Markdown 语法
- 即使模型返回了标题、列表、代码块或链接包装，程序也会尽量转成普通文本

## 故障排查

- 如果日志里看到 `napcat reverse websocket connected`，说明 NapCat 已经连上本程序
- 如果看到周期性的心跳相关消息但没有私聊处理日志，通常说明连接正常，只是还没有触发目标消息
- 如果没有生成总结回复，先检查日志中是否出现 `received private forward message`、`prepared summary input`、`reply sent`
- 如果 AI 调用失败，优先检查 `openai.base_url`、`openai.api_key`、`openai.model` 是否正确，以及模型是否支持图片输入
- 如果图片未参与分析，优先检查 NapCat 返回的图片段里是否带有可访问图片地址

## 参考接口

- NapCat `get_forward_msg`
- NapCat `send_private_msg`
- OneBot `reply` 消息段
- OneBot `forward` 消息段
