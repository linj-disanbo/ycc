# ycc 链"减少存储重跑"迁移设计（pos33 → ticket，5s 出块，状态保留）

> 2026-08-24 定稿并本地端到端验证通过（go1.18 构建，单节点 ticket 链实测 5s 出块、奖励发放、票状态流转）。2026-09-18 参数定稿。
> 用户明确约束：票面价沿用 ycc（ticketPrice=100000，即 **10 万 ycc/张**）；收益参数按实际评估（**已定稿：coinReward=43 / coinDevFund=26，bty 5:3，总增发与旧链相近，见 REWARD.md**）；**ycc 总量不变**（不增发不销毁）；chainID 必须变更防重放（**已定稿 1000**）。

## 1. 现状

- 线上链 `https://yuan.org/yccApi`：高度 ~146,984,348（~1.47 亿块），blockTime≈1.09s/块，chainID=999，Title="pos33"，genesisBlockTime=1652797628（2022-05），CoinSymbol="ycc"。
- 共识 pos33（ycc 自有 `plugin/consensus/pos33` + `plugin/dapp/pos33`），BLS sortition；`[mver.consensus.pos33] ticketPrice1=10000 / ticketPrice2=100000`。
- 存储 **kvmvcc**（`plugin/store/kvmvcc`）：状态 hash = 每块变更集 sha256（非 merkle 根）；全量状态 = MVCC 版本链（`common/db/mvcc.go`），键布局 `.-mvcc-.d.<key>.<20位版本>` / `.-mvcc-.m.`；所有历史版本不裁剪 → 状态库与历史块同样膨胀，这是"减少存储"的清理对象。
- 账户键：主余额 `mavl-coins-ycc-<addr>`；执行器区域 `mavl-coins-ycc-exec-<execaddr>:<addr>`（注意 ':' 分隔）；pos33 键 `mavl-pos33-*`；ticket 键 `mavl-ticket-*`。

## 2. 迁移目标与架构

1. **全量状态保留**：导出 kvmvcc 最新版本 → pos33→ticket 转换 → 写入新链 datadir（store 版本0 预载 + blockchain.db 的 LODB 本地键）→ 新链创世状态即迁移状态。
2. **共识 5s**：ticket 共识 `[mver.consensus.ticket] targetTimePerBlock=5`（难度自调整收敛到 5s）+ 初始 powLimitBits 按票数校准（见 §5.3）。
3. **pos33 → ticket**：ticket 共识/执行器（plugin v1.68.4 自带；共识 fork 到 ycc 修复 eth 签名，见 §4.3）。
4. **chainID 变更**（§5.1）：旧交易执行时 `tx.ChainID != cfg.GetChainID()` 被拒 → 防重放。

### 2.1 机制要点（零框架改动）

- **导出**：读旧节点 `kvmvcc/store.db`（leveldb），遍历 `.-mvcc-.d.` 前缀，同逻辑键取最大版本值（20 位补零 → 字典序==数值序，直接覆盖保留最后值）。
- **转换**（transform 工具，离线，总量守恒）：见 §4.2。
- **导入**（build 工具，离线）：把最终状态键写成新 store.db 的**版本0 数据键**（`.-mvcc-.d.<key>.00000000000000000000`，不写版本元信息）；把 ticket 本地列表键写入 blockchain.db（`LODB-ticket-tl:<miner>:1:<id>` 等，与区块同库）。
- **新链启动**：创世块无交易 → 空变更集 → kvmvcc 自动注册版本0（stateHash=空变更集哈希，`0xdd0ad3...`），版本0 数据即创世状态；ticket 共识从高度 1 正常出块。
- **本地票列表**：钱包/共识的 TicketList 查询走 localdb（`ticket.GetLocalDB()`），预载后矿工钱包即可发现创世票并挖矿（实测验证）。

## 3. 工具链（migrate/）

| 工具 | 作用 |
|------|------|
| `export` | 读旧 kvmvcc store.db → dump.gob（全量最终状态 + 高度/stateHash） |
| `transform` | dump.gob → state.bin / local.bin / report.txt / summary.json（pos33→ticket 转换 + 总量校验 + powLimitBits 建议） |
| `build` | state.bin/local.bin → 新链 datadir（kvmvcc/store.db + blockchain.db 预载） |
| `calib` | 按票数模拟标定 powLimitBits（目标出块时间；transform 的 summary.json 也会给出建议值） |
| `gendump` | 测试用：生成合成旧状态 dump |

用法：
```
export  -store <旧节点>/kvmvcc/store.db -out dump.gob
transform -in dump.gob -out out/ -symbol ycc -ticketPrice 100000
build   -datadir <新datadir> -state out/state.bin -local out/local.bin
```

## 4. 状态转换

### 4.1 pos33 状态键（plugin/dapp/pos33）

- `mavl-pos33-consignee-<addr>` → Pos33Consignee（FeePersent、Consignors[{Address,Amount,Reward,RemainReward}]、Amount、FeeReward、RemainFeeReward）
- `mavl-pos33-consignor-<addr>` → Pos33Consignor；`mavl-pos33-<maddr>` → Pos33DepositMsg；`mavl-pos33-bind-*`、`mavl-pos33-bls-*`、`mavl-pos33-allcountid`、`mavl-pos33-all-frozen`
- 质押币冻结在 pos33 执行器区域：`mavl-coins-ycc-exec-<pos33execaddr>:<持有人>`
- 未发放奖励池 = pos33 执行器**主余额**（`mavl-coins-ycc-<pos33execaddr>`，由挖矿块 `ExecIssueCoins` 增发累积）

### 4.2 转换规则（总量守恒，实测通过；2026-08-26 修正）

> **关键会计事实**（经真实链 pos33stat 验证）：chain33 执行器资金模型是双重记账——
> 用户质押/存款经 `TransferToExec`：用户主余额 −x，执行器【主余额】+x（币实际托管处），
> 执行器【区域】frozen/balance +x（负债明细）。**区域不是币，币在执行器主余额**。
> 因此守恒校验口径 = **coins 主余额合计（不含任何 -exec- 区域键）**。

1. **托管退回**：pos33 执行器【主余额】按区域明细（frozen+balance）退还质押人主余额，删区域键；
   池不足时按比例退并告警（真实链实测池≈区域明细，全额退，差额=增发净额 ~555 ycc 级）。
2. 记账奖励：委托人 RemainReward → 主余额；被委托人 RemainFeeReward → 主余额；
   **FeeReward 是历史累计（已随转账发过），不重复发放**；从执行器主余额剩余扣。
3. 每 (被委托人,委托人) 质押 S：票数 n=floor(S/ticketPrice)，票值=n×ticketPrice；
   主余额扣回票值 → **ticket 执行器【主余额】+票值（票币托管）+ ticket 区域 frozen +票值（明细）**；
   零头留主余额。
4. 生成 n 张票 `mavl-ticket-<miner>:migrate:<%010d>`（Status=Opened, IsGenesis=true, Price=ticketPrice）+ 本地列表键。
5. 删除全部 `mavl-pos33-*` 键；其余状态原样保留。
6. 校验：coins 主余额合计前后相等（实测合成 2000 万 ycc 精确守恒）。

### 4.3 必需代码改动（原 01-ycc-code-changes.patch，已应用并提交到 ycc 仓库 `new-ycc` 分支）

> 该补丁文件已不单独保留（交付物已迁入 ycc 仓库）；需要差分时 `git diff 25310e5..new-ycc`。

1. `plugin/consensus/init/init.go`：注册 ticket 共识（ycc fork 版）。
2. `plugin/dapp/init/init.go`：注册 ticket 执行器（33cn/plugin 原版即可）。
3. **`plugin/consensus/ticket/ticket.go`（新增 fork）**：`createMinerTx` 签名改为 `types.EncodeSignID(types.SECP256K1, eth.ID)`——原版 `tx.Sign(types.SECP256K1, priv)` 的 tx.From() 是 base58 地址（ExtractAddressID 按签名类型映射），在 eth 账户体系下余额为 0 → 挖矿块执行 ErrNoBalance（实测根因，pos33 原实现即 EncodeSignID 编码 ethID）。
4. `ycc.go`（内嵌配置）：consensus name="ticket"、minerExecs=["ticket"]、`[consensus.sub.ticket]`、`[mver.consensus.ticket]`、`[fork.sub.ticket]`、powLimitBits 校准（§5.3）。
   > 配置合并机制限制：内嵌 yccconfig 的标量不能经 ycc.toml 覆盖（checkConfig 报 "rewrite defalut key"）→ 共识切换/链参数必须改内嵌配置随代码发布（与 ycc 现有约定一致："这部分配置随代码发布，不能修改"）。

## 5. 新链配置

### 5.1 chainID / evmChainID

- `ChainID = 1000`（**2026-09-18 定稿**，≠999 防重放；ycc.toml 可配置）
- `[crypto.sub.secp256k1eth] evmChainID = 4000`（**2026-09-18 定稿**，同步更换防 EVM 侧重放）

### 5.2 共识与出块

```
[consensus]  name="ticket"  minerstart=true  minerExecs=["ticket"]
[consensus.sub.ticket]  genesisBlockTime=<迁移时刻>  flushTicketInterval=3600   # 无 genesis 票：状态预载
[mver.consensus.ticket]
  coinReward=43  coinDevFund=26  ticketPrice=100000  futureBlockTime=16
  ticketFrozenTime=5  ticketWithdrawTime=10  ticketMinerWaitTime=2
  targetTimespan=2304  targetTimePerBlock=5  retargetAdjustmentFactor=4
```
> **2026-09-18 参数定稿**（推导见 REWARD.md）：每块增发 = coinReward 43（挖矿奖励，进票的 ReturnAddress）+ coinDevFund 26（发展基金，`ForkTicketFundAddrV1=-1` 恒启用 → ExecIssueCoins 增发给 fundKeyAddr）= **69 ycc/5s ≈ 13.8 ycc/s**，与旧链 pos33（15 ycc/~1.09s ≈ 13.8 ycc/s、4.34 亿/年）相近；比例 43:26 ≈ bty 口径 **5:3**。ticketPrice=100000（10 万 ycc/张，与 pos33 `ticketPrice2` 一致）。
> 注意两个参数必须为**整数**：TOML 写 `43.0` 会被解析成 float64，`MGInt`/`parseInt` 只认 int64 → 静默取 0（实测 `coinDevFund=100.0` 解析为 0）。

### 5.3 powLimitBits 校准（重要，解析公式，已实测验证）

ticket 挖矿 currentdiff=CompactToBig(BigToCompact(sha256))，按哈希字节长度 b 分档：
b 字节哈希 → currentdiff ∈ [2^(8b-8), 2^(8b))，P(字节长 b)=(1/256)^(32-b)×255/256。
对目标 target（指数 e、mantissa m）：
  P = Σ_{b<e} P(b) + P(b=e)×clamp((m-2^16)/(2^24-2^16), 0, 1)
初始出块时间 ≈ 1/(票数×P)。**transform/calib 用该解析公式按票数反解 (e,m) 精确给出 powLimitBits**：
- 1000 票 → 0x1f0d1b71（实测 3~5s/块 ✅）
- 40.68 万票（真实链规模）→ 0x1e083f96
- 100 万票 → 0x1e035a22；500 万票 → 0x1d7c0000
> 此后难度每 460 块（targetTimespan/targetTimePerBlock）自动调整收敛到 5s，powLimitBits 只影响初始窗口。
> 早期用蒙特卡洛模拟标定，低 P 区间（P<1e-5）试次不足测不准——已弃用，统一用解析公式。

## 6. 本地验证结果（2026-08-24，单节点；含干净仓库全流程复现）

> 注：以下为 2026-08-24 验证时的记录，当时配置 coinReward=75；**2026-09-18 定稿 coinReward=43 / coinDevFund=26**（验证结论不受影响，机制相同）。

- 合成旧状态（3 账户 + pos33 质押 1000 万 ycc + 奖励池）→ 转换 → 构建 → 新链启动：
  - 余额精确：矿工 595 / 委托人 5120 / 用户 3000 / pos33 池 265 / ticket 区域冻结 20000（ycc 单位，总量 28980 前后守恒）
  - 创世块：空交易，stateHash=0xdd0ad3...（空变更集哈希），高度 0
  - 挖矿：钱包导入矿工私钥（addressID=2 eth）+ unlock(walletOrTicket=true) 后开始出块；票状态 Opened→Mined（minerValue=75 ycc/块 正确）；委托人 ticket 区域冻结随奖励增长（1000万+525 ycc @7块）
  - 出块间隔：powLimitBits 校准（transform/calib 自动建议，如 1000 票 → 0x1f0a0000）→ ~2-5s
  - pos33 状态查询 ErrNotFound（已清理）
- **压力测试**：100 万张票（200 矿工×5 委托人）转换 8.6s 完成、守恒 OK；解析公式对 1000~500 万票
  全域给出 5.0s 预测（蒙特卡洛交叉验证 1000 票实测 5.2s 吻合）。
- **干净复现**：从补丁后全新克隆仓库（patch + 工具 + ticket fork）重建节点与全部工具，重跑
  gendump→transform（守恒 OK）→build→起链→挖矿：25 块已挖、委托人 ticket 区域
  10,001,875 ycc（票面 1000 万 + 25×75 奖励）、矿工余额正常（扣 tx 费）、pos33 已清理。

### 6.1 双节点部署验证（2026-08-24 补充）

- 节点 A（矿工）+ 节点 B（同步节点，同预载 datadir、独立端口、seed 指向 A）：
  - 两节点创世块 stateHash 完全一致（0xdd0ad3...，预载状态确定性一致）✅
  - B 连接 A（peers=2）→ 下载区块 → isCaughtUp=true → **高度/stateHash 与 A 完全一致**（h61 sh 0x5c072efa）✅
- **发现并修复的真实部署缺陷**：`mver.consensus.maxTxNumber` 必须写入**内嵌 ycc.go** 配置
  （旧链在 ycc.toml，但新链同步节点可能只带最小 toml）——缺失时 MaxTxNumber=0，
  同步的区块全部被拒 `ErrManyTx`（实测）。已内嵌 maxTxNumber=30000。
- 注：本地双节点测试中节点曾因测试环境进程组信号误杀而关闭，非链自身问题。

### 6.2 真实链备份验证（2026-08-26，完整通过）

备份 datadir（高度 143,872,889）完整跑通 export → transform → verify → build → 起链：

- **export**：11.95 亿条 MVCC 变更 → 293.3 万最终键，445MB dump，stateHash 716f8076...
- **transform（修正版）**：票 40,645（票值 40.645 亿）、退款 83,000 ycc（质押零头）、奖励 280.19 ycc（记账未发）、
  pos33 留存 274.92 ycc（增发净额）、ticket 执行器主余额 40.645 亿（票值托管）
- **verify**：0 违规；新增 40,884 = 40,645 票 + 238 ticket 区域 + 1 主余额（票 ID 唯一）
- **守恒（主余额口径）**：state.bin 实测 122.568447 亿 ycc == report 报告值，精确一致
- **起链验证**：新链高度 0 正常；质押人 ticket 区域 frozen = 折票额、主余额含退款；
  矿工 0xe887fa 名下 14,121 张可挖票；出块机制此前合成链已验证（5s）

**真实链数据揭示并修复的三个 bug**（经用户质疑发现）：
1. **质押托管在 pos33 执行器主余额**（TransferToExec 双重记账：主余额=资产、区域=负债明细）——
   原实现把区域明细当资产退回、主余额托管锁死（40 亿）；修正为按区域明细从主余额退还
2. **FeeReward 是历史累计**（已随转账发过）——原实现重复发放（1.22 亿）；修正为只发 RemainReward+RemainFeeReward
3. **票 ID 按矿工全局累计**——原实现按（委托人,矿工）对从 0 编号，同矿工多委托人 ID 冲突覆盖
   （40,645 张票丢 5,803）；修正后票键数=票数=40,645
- 守恒口径：coins 主余额合计（**不含 -exec- 区域键**，区域是负债明细非币）

## 7. 真实迁移操作手册（待用户提供旧节点访问方式后执行）

1. **冻结/快照**：在旧节点服务器运行 `export -store <datadir>/kvmvcc/store.db -out dump.gob`（需停写或接受滚动状态；建议记录导出时高度与 stateHash 作锚点）。
2. **转换**：`transform -in dump.gob -out out/ -symbol ycc -ticketPrice 100000` → 检查 report.txt 总量守恒与票数。
3. **标定**：`calib -tickets <票数> -targetSec 5` → 得 powLimitBits，写入 ycc.go。
4. **构建**：`build -datadir <新节点datadir> -state out/state.bin -local out/local.bin`。
5. **部署**：所有节点升级到新二进制（含 §4.3 改动）+ 新 ycc.toml（chainID=1000 等）+ 全新 datadir；矿工导入旧私钥（addressID=2）、unlock 含 walletOrTicket=true。
6. **验证**：余额抽样对照旧链 RPC；出块间隔 ~5s；总量守恒；旧交易重放被拒（chainID）。

## 8. 遗留/注意

- **矿工收益模式变化**：ticket 挖矿奖励进 returnAddr（委托人）的 ticket 区域，矿工（被委托人）无直接块奖励分成（pos33 有 fee_persent）——ticket 模型的固有设计（bityuan 同）；如需矿工分成需另行设计。
- 初始出块时间依赖票数×powLimitBits（§5.3）；真实链 40 万票需 calib 标定。
- 新链无历史区块 → 交易历史/地址索引查询为空（"只保留状态"的固有结果）。
- 旧钱包 DB 不可复用（需重新导入私钥）；dht/airdrop 自动账户为钱包自生成。
- 验证中实测：钱包 SaveSeed 后需先普通 unlock 再 importprivkey（addressID=2），最后 unlock(walletOrTicket=true) 触发挖矿。
