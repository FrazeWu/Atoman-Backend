# 日志双写与文件拆分设计

## 背景

生产环境采用全 Docker 部署，需要保留终端输出以支持 `docker logs`，同时把日志持久化到可映射的文件目录。

## 目标

- 默认本地日志目录为 `./log`。
- 生产可通过 `LOG_DIR=/app/logs` 覆盖日志目录，并由 Compose 映射到宿主机。
- 终端输出保持不变。
- 日志文件按入口拆分，便于排查请求、应用过程和错误。

## 配置

- `LOG_DIR`：日志目录，默认 `./log`。
- 日志文件名固定：
  - `access.log`
  - `app.log`
  - `error.log`

## 文件拆分

- `access.log`：Gin 正常请求访问日志。
- `app.log`：标准库 `log.Print*` / `log.Println` / `log.Printf` 输出，包括启动、迁移、后台任务和现有 `WARN:` 文本日志。
- `error.log`：Gin error writer 输出，以及 `log.Fatal*` 这类退出错误。

本设计不做 `WARN:` 文本自动识别；warning 是否拆出独立文件留给后续结构化日志改造。

## 输出方式

启动早期初始化日志输出：

- 标准库 logger 写入 `stdout + app.log`。
- Gin access writer 写入 `stdout + access.log`。
- Gin error writer 写入 `stderr + error.log`。

为了让 `log.Fatal*` 同时进入 `error.log`，启动入口使用一个小的 fatal logger 替代直接调用标准库 `log.Fatal*`，其输出目标为 `stderr + error.log`，行为仍为打印后退出。

## 失败策略

启动时创建日志目录或打开日志文件失败，服务直接退出。这样能避免生产环境以为日志已落盘但实际没有持久化。

## Docker 映射

生产 Compose 应设置：

```yaml
environment:
  LOG_DIR: /app/logs
volumes:
  - ./log:/app/logs
```

如果生产 Compose 文件位于仓库上级目录，应在该文件的 backend 服务中添加上述配置。

## 测试

- 单元测试日志初始化逻辑：默认目录、环境变量覆盖、writer 双写、失败路径。
- 构建验证：`go build ./...`。
- 如存在 backend Compose 服务，检查 `LOG_DIR` 和卷映射是否正确。