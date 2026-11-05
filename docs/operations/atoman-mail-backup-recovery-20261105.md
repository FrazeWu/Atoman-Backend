# 邮件成套备份、NAS 恢复与日常运维（2026-11-05）

## 范围与已验证结果

邮件恢复点包含专用 PostgreSQL 数据库 `atoman_mail`、应用数据库中的全部实际 `mail_*` 表及关联 `Users`、两个邮件 R2 桶的所有对象、Stalwart 配置、证书和运行密钥。生产仍使用原 PostgreSQL 16 实例 `atoman-dev-postgres-1`；`atoman_dev` 继续纳入原全站备份。

完整网站备份和完整邮件恢复胶囊分别保存。正常本地策略为一份成功配对快照，以及每个导出范围最新一份密文；NAS 既有策略配置为十五天、至少两份。其他备份目录和 NAS 的 `direct` 管道不由本次变更清理。

### 首次上线前真实恢复

2026-11-05 14:06:30.447134 UTC，完整邮件胶囊已经在 NAS 写入并完整读回、使用 Mac 既有离线 age 私钥解密，在隔离 PostgreSQL、MinIO、Stalwart 中恢复成功。原合成邮件 INBOX UID、完整 MIME、正文和 4150 字节附件的 SHA256 全部一致。

| 证据 | 已验证值 |
| --- | --- |
| 配对源快照 | `20261105T123021Z-cl6v20q0` |
| 源快照完成时间 | 2026-11-05 12:53:13.220686 UTC |
| 原始 manifest SHA256 | `f9df735e3b3730163f4b4d08235816977e9a53591d5985753270b1c42002691a` |
| 完整邮件胶囊 ID | `20261105T123021Z-cl6v20q0m` |
| 胶囊密文大小 | 1,270,264 字节 |
| 胶囊密文 SHA256 | `ba6eb563accfb0bb24288cf8bba86cdcb08940c326c28e50f7ee0ed4804e1f83` |
| 胶囊 manifest SHA256 | `4f6b3ccfca7ec7a22b8f694903a0eb09deeb308ab00a8af7f022931a61c9e0d3` |
| NAS 位置 | `/mnt/basic_pool/AtomanBackups/snapshots/20261105T123021Z-cl6v20q0m/backup.tar.age` |
| NAS 完整读回验证时间 | 2026-11-05 13:53:16.011379 UTC |
| Stalwart SQL | 29 张表恢复，逐表计数一致 |
| 两个 R2 桶 | `atoman-mail-prod` 4 个对象、`atoman-mail-pending` 0 个对象，全部恢复 |
| 运行配置 | 24 个文件，全部逐文件校验 |
| 应用 SQL | `Users=42`、`mail_accounts=1`、`mail_access_credentials=0`、`mail_invites=0`，隔离恢复与源计数一致 |
| 成功回执 | `/var/backups/atoman/mail-recovery/20261105T123021Z-cl6v20q0/nas-recovery-receipt.json` |
| 成功回执 SHA256 | `c0753e029384982048587f470acf53ab76f4f112fe2d1f2d961a6cd1c2eb0ed7` |

该源快照创建时，新草稿、上传、outbox、偏好、操作回执、retention 六张表尚未迁移，因此回执明确使用 `pre_activation_mail_capsule`。它证明当时全部真实邮件数据的恢复，不将不存在的应用表标为已验证。上线后的最终快照必须另行包含九张现有邮件表、激活账户关联和 vault 密文。

原合成邮件摘要：

- 完整 MIME：`0b72b378bbee6779a5a48c17c99f2d2a798b094f270d32317c4ed1a50dd5057d`。
- 正文：`58b5960e326b5a41fe366a2b44f1fcf52071656ceed79000c7928db5cbaa736f`。
- 附件：`690120b6c347526e9d09486cd99096c2661f265c5eafdc880a38e05879aeb43e`。

回执不包含邮件正文、对象键、私钥或口令。原 canary 回执保存在私有目录 `/var/backups/atoman/mail-canary`。

### 全站 NAS 范围如实保留为未完成

同一源的完整全站密文为 825,135,992 字节，SHA256 为 `44955ff6907c5d2ca9120adb70a324fa98e544a9cff0c3504722df002e0d2ba7`。其本地完整数据库恢复已经通过，包括所有网站索引；NAS 传输链路实测仅约 10–14 KB/s。

首次邮件 G5 的实际 NAS 观测为 13:40:06.887899 UTC，全站 `.incoming` 密文 6,266,880 字节，尚无完成标记。回执明确：

- `main_bundle_nas_verified=false`。
- `full_snapshot_nas_verified=false`。
- `all_snapshot_files_sha256_verified=false`。
- `main_bundle_nas.mac_full_ciphertext_readback=false`。

完整邮件胶囊的真实 NAS 恢复证明独立成立；不据此声称全站大包已在 NAS 完整验证或恢复。完整全站源快照、密文和源证据均保留。

## 配对备份与一致性

`scripts/ops/backup-mail.py` 先预取两个邮件 R2 桶，以 ETag 条件读取并校验缓存 SHA256。脚本只读取 `ATOMAN_MAIL_R2_ACCESS_KEY_ID` 与 `ATOMAN_MAIL_R2_SECRET_ACCESS_KEY`，拒绝回退到媒体桶 AWS 凭据；它不删除生产 R2 对象。

进入有界暂停窗口后，依次停止 backend、outbox、Stalwart，打开三个 PostgreSQL 导出快照，再核对全部 R2 对象及运行文件。默认暂停上限 120 秒；异常路径也按相反顺序恢复已经停止的服务。首次实际暂停为 10.18 秒。AIC 操作使用 `deploy:mail-stalwart` 协调锁，脚本使用既有 `/run/lock/atoman-backup.lock`。

服务恢复后继续基于所持有快照执行完整 pg_dump、归档和独立 PostgreSQL 恢复。三个数据库并非数据库级全局事务，因此需要这个短暂共同停止写入的窗口来建立一致性。

SQL 恢复逐表比较源快照计数。`mail-assets.tar` 对所有对象和运行文件校验大小及 SHA256，拒绝重复、符号链接、额外成员和路径穿越。所有缓存、明文和临时目录使用私有权限。只有数据库与资产验证全部成功后才写 `complete=true` 和 `COMPLETE`；失败保留上一份成功快照。

| 路径 | 内容 |
| --- | --- |
| `/var/backups/atoman/mail` | 配对源快照，正常保留一份成功版本 |
| `/var/backups/atoman/mail-cache` | 当前 R2 对象缓存，成功后清理过期缓存条目 |
| `/var/backups/atoman/mail-capsules` | 邮件胶囊密文与无秘密清单，按 mail-complete 范围保留 |
| `/srv/atoman-backup-sftp/exports` | 全站和 mail-complete 两种标准 age 密文导出，各保留最新一份 |
| `/var/backups/atoman/mail-recovery` | 私有恢复工作区与无秘密的验收证据 |
| `/var/backups/atoman/mail-canary` | 合成邮件及预期摘要 |

配对备份捕获 `/etc/atoman/backend.env`、`/etc/atoman-mail`、相关 systemd 单元和 drop-in、tmpfiles 锁配置、部署回执，以及 Let’s Encrypt 的实际 `archive` 目录和 renewal 配置。`live` 是符号链接，不能直接作为运行文件输入。具体完整文件列表以仓库 `scripts/ops/systemd/atoman-backup-mail.conf` 的 ExecStart 为准。

### 上线后追加备份

每日默认 `--verification-profile full`，三个数据库完整恢复，包括全部索引和约束。首次配对快照已经完成此完整检查。

紧接上线后的追加备份使用同一完整配置，并允许：

~~~text
--verification-profile data-and-mail
--reference-full-backup /var/backups/atoman/mail/20261105T123021Z-cl6v20q0
~~~

该模式仍导出完整数据库归档，索引和约束 DDL 仍在归档中；新 prod/dev 只恢复结构及全部数据并校验计数，避免重复耗时的全站二级索引构建。`atoman_mail` 仍完整恢复。新 manifest 记录参考快照、manifest 摘要和每库的实际恢复 profile，不能冒称完整索引重建。

上线后追加必须包含 `mail_accounts`、`mail_invites`、`mail_access_credentials`、`mail_pending_sends`、`mail_drafts`、`mail_uploads`、`mail_preferences`、`mail_action_receipts`、`mail_retention` 及 `Users`。胶囊还会收录实际存在的其他 `mail_*` 表。

首次源的原始 manifest、全站导出 receipt、胶囊 receipt/manifest、应用提取 manifest 与 canary 回执已经逐摘要归档至 `mail-recovery/20261105T123021Z-cl6v20q0/source-evidence`。追加过程可以暂留两份，最终恢复通过后恢复既定本地一份策略。归档前不能让 `--retain 1` 删除还需要引用的首次证明。

## 两种加密导出与自动发布

`prepare-encrypted-backup.py` 生成完整全站 age 密文。它绑定源 manifest、三个数据库、角色文件和邮件资产；每个标准 `receipt.json` 记录密文大小与 SHA256。

`prepare-mail-recovery-capsule.py` 从同一不可变成功源生成完整邮件胶囊：

1. 核验源 manifest、全站密文回执、邮件数据库和资产摘要。
2. 用 `mail-capsule-application.py` 读取原 `atoman_prod.dump` 的 PostgreSQL TOC 依赖图，选择 `Users`、所有实际 `mail_*` 及必需类型、序列、索引、约束和扩展。不得从当前在线数据库另取数据来冒充同一源。
3. 将完整选择的 schema/data 生成 `application-mail.sql` 与清单；记录源缺失表，不自动补造。
4. 加密包含源 manifest、完整 atoman_mail dump、全部 mail-assets、应用 SQL 和应用清单、胶囊清单的六个有效载荷文件。
5. 胶囊 ID 为原源 ID 加 `m`，标准 receipt 明确 `backup_scope=mail-complete`、`paired_source_snapshot_id` 和 pre/post-activation scope。

`publish-mail-backups.py` 在既有 backup/offsite 锁下确保“全站导出 → 胶囊构建 → 两者发布”完成；即使全站密文已存在也继续检查胶囊。它按 full/mail-complete 各保留最新一份，不能让胶囊误剪全站包。私有胶囊缓存只清理精确已知五文件、且清单与密文均有效的目录，不触碰源快照、NAS 或外来目录。

默认日常调用：

~~~bash
python3 /usr/local/libexec/atoman/publish-mail-backups.py \
  --backup-root /var/backups/atoman/mail \
  --recipient age1rfgu75985jpk7aml06uvdzm0ewa4p7j9ua7l8vut3ek5eueddgfs542fe8 \
  --keep 1
~~~

恢复私钥始终只留在 Mac：`/Users/fafa/.config/atoman-backup/age-recovery-identity.txt`。VPS 使用现有 age 公钥。不得将私钥传给 VPS、NAS、代码库或日志。

## 实际 NAS 协议与保留策略状态

既有正确数据账户为 `fa@172.16.10.201`，数据集 `/mnt/basic_pool/AtomanBackups` 私有归 fa 所有。Mac 专用已有身份文件 `/Users/fafa/.ssh/atoman_nas_setup_20261028` 仅交给 SFTP 客户端，固定 `IdentitiesOnly`、`BatchMode`、`StrictHostKeyChecking`。不新建 SMB、改 ACL、使用无权限账户或运行远程控制 shell。

已读取实际 NAS 上的 `bin/nas-pull-encrypted-backups.py`，而非用仓库 fallback 推断：

- cron 2 每小时 :30 运行。
- 按合法导出 ID 倒序拉取，因此同源 ID 加 `m` 的小胶囊排在全站包前。
- 每次 SFTP get/reget 最多 3600 秒；失败保留 `.incoming` 文件，下轮续传。
- 下载后核验完整 SHA256 与长度，才原子发布 `snapshots/<id>` 和 `LOCAL_COMPLETE.json`。
- 每个完成目录保留标准三文件：密文、receipt、完成标记；额外 receipt 字段兼容。
- **当前实际实现必须先完成整个 pull 与 freshness 检查，才执行十五天/minimum-kept=2 的清理并写全局成功状态。全站慢速大包的超时会使全局状态与本轮清理尚未执行。**

12:30 UTC 历史实际状态为 ok、retention_days=15、minimum_kept=2、retained=6；它不证明后续大包和清理已完成。当前可以陈述“配置十五天、至少两份”，不能声称本轮已验证按十五天删除。此次没有覆盖 NAS 脚本、cron、权限或其他备份。

即时邮件 G5 使用已有数据通道：

~~~bash
python3 stage-mail-backup-to-nas.py \
  --snapshot SOURCE_SNAPSHOT_IDm \
  --vps-root /var/backups/atoman/mail-capsules \
  --workspace /Users/fafa/.config/atoman-backup/mail-recovery-20261105 \
  --nas-identity /Users/fafa/.ssh/atoman_nas_setup_20261028 \
  --known-hosts /Users/fafa/.ssh/known_hosts
~~~

工具从 VPS 取标准密文，在 NAS 自有暂存目录上传，再从 NAS 完整读回并重算摘要，随后写标准完成标记、原子重命名并读回标记。它只处理本次目录，不清理其他 NAS 备份。`nas-transfer-receipt.json` 本身只证明传输；邮箱恢复还须下一步。

## 从 NAS 胶囊恢复与 G5 v2

`verify-mail-capsule-recovery.py` 要求实际 NAS 读回密文、该目录 receipt、传输回执、独立全站 NAS 状态见证以及本机离线 identity：

~~~bash
python3 verify-mail-capsule-recovery.py \
  --ciphertext WORKSPACE/SOURCE_IDm/nas-readback/backup.tar.age \
  --receipt WORKSPACE/SOURCE_IDm/nas-readback/receipt.json \
  --nas-transfer-receipt WORKSPACE/SOURCE_IDm/nas-transfer-receipt.json \
  --main-nas-witness WORKSPACE/SOURCE_ID/main-nas-witness.json \
  --identity /Users/fafa/.config/atoman-backup/age-recovery-identity.txt \
  --workspace WORKSPACE/isolated \
  --report WORKSPACE/SOURCE_ID/nas-mail-recovery-report-v2.json
~~~

恢复步骤：

1. 独立绑定 NAS ciphertext/receipt/完成标记、源快照/全站密文摘要与胶囊 canonical manifest。核验全部六个 payload 文件。
2. 在 `--network none` 的 PostgreSQL 容器恢复全部应用邮件 SQL，比较逐表计数以及用户、账户、credential、draft 关联。源中缺失新表只能在 pre-activation 范围明确记录。
3. 在 Docker internal 网络恢复完整 Stalwart PostgreSQL 与两个 MinIO 桶，所有对象和运行文件校验完整。只给克隆随机一次性 PostgreSQL/MinIO 凭据，绝不注入生产 R2 凭据。
4. 两次短生命周期 Python 客户端使用 `--network container:<restored-mail>`，只访问该隔离 namespace 的 127.0.0.1:8080/993。完全不发布宿主端口。客户端禁用代理和重定向，以 `/jmap/session` 检查就绪，以 `/jmap` 调用管理 API；先改克隆端点，退出客户端，重启克隆，再新建客户端验证 IMAPS。
5. IMAPS 使用 `mail.atoman.org` 的正常证书链及主机名校验，读取原 INBOX UID，对比完整 MIME、正文和附件 SHA256。
6. 所有检查通过后才生成成功回执；最后清理自有临时明文、容器、卷和网络。失败诊断仅写私有工作区，不输出凭据。

OrbStack 实测 internal 网络不会提供可用的宿主随机端口，因此不能以 `docker port` 返回值假定可达。namespace 客户端保留网络隔离和 loopback 限制，解决了这一运行差异。

固定摘要镜像：

| 组件 | 镜像 |
| --- | --- |
| Stalwart | `stalwartlabs/stalwart@sha256:154e55adecadfcc4a67044b9b94d482a20286e1101c97a2b322224bb596053d5` |
| PostgreSQL 16 | `postgres@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777` |
| MinIO | `minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e` |
| MinIO client | `minio/mc@sha256:a7fe349ef4bd8521fb8497f55c6042871b2ae640607cf99d9bede5e9bdf11727` |
| Python protocol client | `python@sha256:eb2c62658b8d51bff0acc408504b76521d02030de56a8a3c1e830083e89b89d2` |

Stalwart 官方对象参考：[BlobStore](https://stalw.art/docs/ref/object/blob-store/)。本版本 BlobStore/DataStore 使用 `singleton` ID；实际协议使用 `urn:ietf:params:jmap:core` 与 `urn:stalwart:jmap`。

### 成功回执合同

成功回执为 `atoman-mail-nas-recovery-v2`。启用守卫另行读取三个独立源文件：原始 source manifest、原始全站 export receipt、原始 capsule receipt，不能仅信回执自报摘要。

它要求 `status=ok`、`restore_succeeded=true`、正确 source/capsule ID/完成时间/大小/摘要，以及：

- `capsule_nas_roundtrip_verified`、`all_mail_payload_files_verified`、`owner_offline_identity_verified` 为 true。
- 嵌入 capsule manifest 的 canonical SHA256 与独立 capsule receipt 一致，完整绑定应用 dump、邮件 dump、资产、两个桶清单和运行文件集合。
- 应用所有实际表及源计数、Users 关联、credential 账户关联、draft owner 关联真实恢复通过。
- NAS 的原始 receipt、完成标记、downloaded_at 与本次实际读回摘要齐全。
- 隔离恢复、全部 R2 对象、全部运行文件、29 张邮件表及三个 canary 摘要一致。
- `production_data_modified=false`，实际验证时间足够新鲜。

若全站仍 pending，`main_bundle_nas_verified` 与 `full_snapshot_nas_verified` 必须同时为 false；pending 见证只记录 source/hash/size/observed_at/实际 partial_bytes，不携带伪造成功 receipt。旧 v1 的 `all_snapshot_files_sha256_verified` 不能被设为 true 来代指胶囊。

## 定时任务、健康与告警

| 仓库模板 | 安装位置 / 行为 |
| --- | --- |
| `atoman-backup-mail.conf` | `/etc/systemd/system/atoman-backup.service.d/zz-mail-paired.conf` |
| `atoman-mail-locks.conf` | `/etc/tmpfiles.d/atoman-mail-locks.conf`，锁文件 root:atoman 0660 |
| `atoman-mail-maintenance.service/.timer` | 02:45 UTC 加最多五分钟延迟；最长二十分钟 |
| `atoman-mail-backup-health.service/.timer` | 开机十五分钟后，之后每三十分钟检查 |
| `atoman-mail-ops-alert.service` | 失败时写 daemon.err 级别 journal 事件 |

必须使用 **zz-mail-paired.conf**，因为已存在 `offsite-sftp.conf`、`retention.conf`；99 名称排序过早，会被后者重置为旧两库任务。保留旧 drop-in 便于回滚，安装后用 `systemctl show` 核对实际三个数据库 ExecStart 和新的唯一 publish-mail-backups ExecStartPost。

原 `atoman-backup.timer` 实际 enabled/active，保留每天 03:30 UTC 加随机延迟。maintenance 使用 `/opt/atoman/backend/current/mail_maintenance_worker`、两个实际 env，显式移除 Stalwart 管理员两个环境变量；`ATOMAN_MAIL_CLEANUP_ENABLED=1`。它通过共享 flock 与备份协调，冲突时以 75 正常跳过。启用前运行 `systemd-tmpfiles --create`。

健康检查要求最新成套快照不超过 48 小时、三库恢复记录和资产完整，且**相同源的全站导出及 mail-complete 胶囊都有效发布**。它绑定实际 source manifest 字节 SHA、全站 receipt 大小/摘要并重新计算小胶囊摘要；缺胶囊或源不匹配即失败。核心服务为 backend、Stalwart、outbox。结果写 `/var/lib/atoman/mail-backup-health.json`，明确 `offsite_transfer_confirmed=false`；本地发布健康不等同 NAS 到达。

失败告警目前为本机 systemd/journal 通道，没有新增外部通知收件人或消息凭据。安装顺序为工作进程二进制、Python 模块与单位、tmpfiles、systemd 验证、daemon-reload、启用 timer、实际健康检查。首次 G5 和上线后追加 NAS 恢复证明均须保留。

## 生产版本与密钥恢复

后端运行在 `/opt/atoman/backend/releases` 与 `current`，使用 `scripts/ops/activate-runtime.py`。旧 `scripts/deploy-production.sh` 不应覆盖受控部署方式。主线负责迁移、五个同版本二进制和激活；邮件 env drop-in 排在 `90-managed-runtime.conf` 后。

恢复时先恢复对应版本的数据库、对象及原 vault/outbox 密钥，再启用工作进程。不能用新生成密钥替代快照密钥。应用胶囊验证完整邮件所有权关联，不声称仅凭 Users 子集即可恢复全站全部登录依赖。

临时 R2 密文中转对象已于 13:22:41.828998 UTC 删除，精确 HEAD 404 与无剩余自有 multipart 已验证；它不会被下一次配对备份递归收录。对应私有回执为首次恢复目录的 `r2-transport-cleanup.json`。

## 上线后最终验收

待主线激活、真实账户收发验收和最终运行配置安装完成后，在协调窗口执行最后一次配对备份、post-activation 完整邮件胶囊发布、真实 NAS 往返与同样隔离恢复。此节仅在该实际结果完成后填写，不能沿用 pre-activation 成功冒充已激活账户恢复。
