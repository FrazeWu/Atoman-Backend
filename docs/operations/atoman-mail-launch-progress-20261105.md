# Atoman 邮件系统上线记录 — 2026-11-05

## 当前状态

Webmail 已部署到 **https://www.atoman.org/mail**，`fazong@atoman.org` 已从预留状态激活并关联既有 Atoman 管理员。继续使用既有网站账号登录；第三方邮件客户端使用邮箱设置中创建的应用专用密码。

主站 API、Stalwart、持久发件 worker 均已实际运行。公网入站和外发回复可达，附件与回复引用经过真实验证。**两封 fazong 外发测试在自有 Gmail 的落点均为 SPAM；Gmail 收件箱落点验收未通过。** SPF、RSA DKIM、DMARC 和 TLS 验证通过，不能据此声称所有邮件会进入收件箱。

**上线后完整邮件 NAS 恢复已通过。** 2026-11-05 14:44:25 UTC，从真实 NAS 读回的新胶囊用 Mac 离线密钥解密，在隔离服务中恢复成功；14:45:40 UTC，独立 v2 守卫再次读取原始源 manifest、全站导出 receipt、胶囊 receipt，校验通过，没有重新修改生产环境。

| 最终恢复证据 | 结果 |
| --- | --- |
| 配对源 / 邮件胶囊 | `20261105T143410Z-xzbrgspt` / `20261105T143410Z-xzbrgsptm` |
| 一致性冻结写入 | 8.636 秒；随后恢复生产服务并继续在线导出 |
| 应用邮件数据 | 全部九张实际 mail 表；Users 42、账户 1、加密凭据 1、发件记录 3；逐表计数与关联一致 |
| Stalwart 数据库 | 29 张表完整恢复，逐表计数一致 |
| 邮件对象与配置 | 两桶全部 10 + 0 个对象，40 份运行文件全部校验 |
| 原合成邮件 | INBOX UID 保留；完整 MIME、正文与 4150 字节附件的 SHA256 一致 |
| NAS 邮件胶囊 | 1,382,936 字节，实际写入、完整读回、摘要与原子发布标记均通过 |
| 全站 NAS 大包 | 仍为 pending；`full_snapshot_nas_verified=false`，不宣称全站已在 NAS 恢复 |

本轮 prod/dev 使用已声明的 `data-and-mail` profile：恢复结构及全部数据、验证逐表计数，完整全站索引恢复证据关联首次全量快照；Stalwart 邮件库本轮仍完整恢复。详细摘要、路径和恢复边界见下方备份记录。


## 已部署版本

| 部分 | 版本或证据 |
| --- | --- |
| 后端业务版本 | `a257393acf4e4b9c9fa86e5bbbe8147cd788fccb` |
| 后端不可变发布目录 | `/opt/atoman/backend/releases/a257393acf4e-658acb1314fe` |
| 本次运行与运维集成基线 | `b9046f155c22fcd906030acc1397e9d244d2f97d`，正常合并并推送 main |
| 前端生产版本 | `f52ea1d5929eee18ddba3b69b90b1bafd0679f76` |
| Cloudflare Pages | `b6917425-edc6-4c0c-bb09-d89ff35df5d2`，2026-11-05 12:53:50 UTC production success |
| 业务版本 CI | [42033494634](https://github.com/FrazeWu/Atoman-Backend/actions/runs/42033494634)，success |
| 含恢复守卫的 CI | [42042541164](https://github.com/FrazeWu/Atoman-Backend/actions/runs/42042541164)，精确 head `7aabbdff6223af147f8675dfd88bc470f25affc4`，14:22:28 UTC success |

后端运行账户为非 root 的 `atoman`；outbox 和 maintenance 不继承 Stalwart 管理员凭据。JMAP/管理入口只绑定本机回环地址。既有 book-storage、clamav 和 managed-runtime 配置保留。

## 已完成的功能

- 桌面三栏与手机单栏 Webmail；收件箱、线程、搜索、分页、原始邮件及附件下载，操作按账户归属校验。
- 加密草稿、自动保存、版本冲突处理、私有附件、回复/回复全部/转发与正确引用链，To/Cc/Bcc 隐私处理。
- 持久发件队列与服务端五秒撤销；冻结发送内容、请求幂等、发送状态恢复，未知结果不盲目重发。
- 已读、星标、归档、垃圾箱、垃圾邮件、移动与批量操作；五分钟撤销回执；自定义空文件夹管理。
- 签名、偏好、Sieve 过滤规则、阻止地址、自动回复、邀请开通和应用专用密码创建/列出/吊销。
- Stalwart Trash/Junk 保留三十天；应用维护只处理符合条件的过期记录及不再引用的对象，不清理活跃或结果不明的发件。

## 真实生产验证

| 测试 | 实际结果 |
| --- | --- |
| 早期 admin 直投自有 Gmail | INBOX；SPF、RSA DKIM、DMARC pass，TLS 1.3 |
| 早期 admin 经 support 转发至自有 Gmail | INBOX；原始认证头已核验 |
| fazong 自发并 Bcc 自有 Gmail，14:14 UTC | 自身 Inbox 收到；Gmail SPAM；正文、原始邮件与 107 字节附件完整 |
| 自有 Gmail 经公网 MX 发给 fazong，14:22 UTC | 新邮箱 Inbox；真实 From、正文 marker、原始 MIME、41 字节附件全部核验，附件 SHA256 `a11c805bc215baa35bb32801bb56bf57a3560508835b92f18856f214653cd93e` |
| fazong 正常回复自有 Gmail，14:30 UTC | 已送达 Gmail SPAM；唯一收件人、Message-ID、In-Reply-To、References 已实查，Gmail 将其归入原对话 |
| 五秒撤销、归档撤销、草稿冲突、附件与文件夹 | 生产业务验收通过 |
| 应用专用密码 | 实际 IMAPS 登录成功，吊销后登录被拒绝 |

完整生产验收运行 `b6d67d7d-e40d-4352-a029-f369924a559c` 于 14:14:40 UTC 完成。正常回复使用独立持久 request ID 和一次性回执，只入队一次。未知发送结果不会触发重复发送。

切换后另有一封 Gmail→support→同一 Gmail 的测试，Gmail 同一消息显示 SENT 与 INBOX，但返回 MIME 是原发送副本，没有新的 Stalwart Received 头。该观察保留，不把它当成独立的新转发传输头证明；上表的早期 support 转发证据仍成立。

两封 SPAM 邮件的认证头均为 SPF pass、RSA DKIM pass、DMARC pass、TLS 1.3；额外 Ed25519 签名为 `neutral (no key)`。早期 INBOX 邮件有同样的 Ed25519 结果，不能推断它导致垃圾箱分类。没有修改 Gmail 标签、白名单或联系人来制造验收成功。

对照四封邮件未发现可确认的 Date、单例头重复、MIME 边界、CRLF、Base64 换行、回复链或正反向 DNS 错误；不能把发件人/IP 声誉猜测写成已证实根因。Google 的说明也明确：认证本身不保证投递或分类结果。参考 [Gmail 身份验证](https://support.google.com/mail/answer/180707?hl=en) 与 [发件人指南](https://support.google.com/mail/answer/81126?hl=en)。

## 公网切换与客户端

2026-11-05 14:17:25 UTC 添加新主 MX，真实外部入站通过后，于 14:24 UTC 停用原 Cloudflare Email Routing 并移除三条旧 MX。Google 与 Cloudflare 公共解析均已验证 apex 只有 `1 mail.atoman.org`。既有 SPF 授权条目、DMARC、两个新 DKIM 和 Resend/SES 子域记录均保留。

`mail.atoman.org` 指向 `169.58.161.96`，使用有效公共证书。出站 EHLO 为与正反向解析一致的 `vmi3410530.contaboserver.net`。SMTP 25 的未认证外部中继实际被 550 拒绝。

MTA-STS HTTPS 策略及 `_mta-sts` TXT 已发布，当前明确为 **testing**，不是 enforce。证书自动续期 timer 和续期部署 hook 已验证。

| 客户端项目 | 配置 |
| --- | --- |
| 用户名 | `fazong@atoman.org` |
| IMAP | `mail.atoman.org:993`，SSL/TLS |
| SMTP | `mail.atoman.org:465`，SSL/TLS；或 `587`，STARTTLS |
| 验证 | 完整邮箱地址 + 在 Webmail 设置中新建的应用专用密码 |
| 支持别名 | `support@atoman.org` 继续转发至既有自有 Gmail，不是第二个独立登录账户 |

所有合成验收用应用专用密码均已吊销，不在此文档提供密码或密钥。

## 备份、恢复与日常运维

首次上线前完整邮件 NAS 恢复已经通过，源 `20261105T123021Z-cl6v20q0`，胶囊 `20261105T123021Z-cl6v20q0m`。上线后追加源为 `20261105T143410Z-xzbrgspt`，最终恢复结论见本文顶部更新及[完整备份恢复记录](atoman-mail-backup-recovery-20261105.md)。

完整邮件恢复范围包含 Stalwart 数据库、全部实际九张 `mail_*` 表及关联 `Users`、两个私有 R2 桶、配置、证书和运行密钥。恢复 Webmail 入口还需要兼容的完整 Atoman 应用/认证基线；邮件胶囊不是独立全站备份。详见[恢复守卫与基线](atoman-mail-activation-v2-20261105.md)。

| 任务 | 已安装与实际验证 |
| --- | --- |
| 邮件维护 | 每日 02:45 UTC，加最多五分钟随机延迟；手动运行 success |
| 配对备份 | 既有每日 03:30 UTC timer 加随机延迟；现包含 prod/dev/mail 三库、邮件对象和运行文件 |
| 健康检查 | 每三十分钟；backend/Stalwart/outbox、最新同源全站与邮件胶囊发布状态；手动运行 success |
| 失败告警 | systemd/journal；没有配置外部推送收件人 |

完整全站大包的 NAS 到达仍是独立的 pending 项，不能用完整邮件胶囊的成功替代。NAS 既有策略配置为十五天、至少两份；慢速全站传输可能阻挡该轮全局成功状态与清理。现有 NAS 主机脚本、cron、权限未修改。

## 验证范围与私有证据

后端业务 CI、真实 Stalwart 集成、前端 29 项测试、type-check 和构建已通过。真实 Chrome fixture 覆盖桌面交互与 390px 手机布局。公开生产路由进入正常登录页；没有把合成 fixture 记作已登录生产浏览器验收。

Mac 的既有个人代理 fake-IP 路由仍影响原生客户端直连。自动审批拒绝读取无关个人代理配置，因此未读取或修改该配置；公共服务器 TLS/IMAPS/SMTP 验证与托管 Webmail 已分别完成。

生产私有证据位于 `/var/lib/atoman-mail`：`deployment-receipt.json`、`acceptance-webmail.json`、`acceptance-incoming.json`、`acceptance-owned-reply.json`、`acceptance-gmail-production.json`、`dns-cutover-receipt.json`、`ops-installation-receipt.json`。这些文件包含合成验收及安全状态，不提交到 Git。恢复证据在 `/var/backups/atoman/mail-recovery`，私钥始终留在 Mac 离线身份文件中。
