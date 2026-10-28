# Atoman Papers — P0 持久 Worker 轮询调度子项

日期：2026-10-28；分支：feat/papers-worker-daemon-20261028。

## 本次实现
- `internal/platform/papersdaemon`：常驻、**单执行流**轮询器，任务完成后立即轮询，空闲按配置周期等待；数据库错误/Launcher 不确定状态/队列阻断后指数退避，并支持 Context 退出与有界内存状态快照。
- `cmd/papers_p0_unix`：新增 **仅限已有一次性 PostgreSQL 与 UID/GID 65534 测试身份** 的 `daemon` 模式，处理 SIGTERM/SIGINT，沿用原来的 PostgreSQL lease/fencing 和 Unix Socket Launcher，不引入任何可直连宿主机的执行参数。
- 单元回归覆盖参数拒绝、单执行流、不确定状态退避、长时间空闲与终止、数据库失联。
- 完整 `go build ./...` 与新增模块的 `go test -race` 验证通过。

## 尚未实现／绝不能宣称完成
- P0 daemon 目前不作为正式 systemd unit 启动；尚未验证长时间进程服务、运维监控、真实 SIGTERM 与重启后的连通性。
- 私有 S3 对象存储发布：当前仓库使用 AWS SDK Go v1，PutObjectInput 不提供可靠的原子 If-None-Match 条件；没有通过只读查询+普通 Put 替代。正式可验证的不可变对象写入、桶权限、防公共读、并发重试与源/归档生命周期仍为 **No-Go**。
- 生产 Worker 独立账户、正式可信 Launcher、凭证管理、独立对象存储环境注册表和公网 API 均未配置或部署。
- 本分支基于历史 Papers 专题分支，不是直接来自现有 main；集成必须先验证整个祖先提交链和 main 差异，不能不经审查将 P0 原型公开。

**结论：仅持久轮询调度子项完成；Papers 总体生产上线仍然 No-Go。**
