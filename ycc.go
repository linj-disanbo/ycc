package main

// ycc 这部分配置随代码发布，不能修改
var yccconfig = `
TxHeight = true
FixTime = false

[log]
# 单个日志文件的最大值（单位：兆）
maxFileSize = 300
# 最多保存的历史日志文件个数
maxBackups = 100
# 最多保存的历史日志消息（单位：天）
maxAge = 28
# 日志文件名是否使用本地事件（否则使用UTC时间）
localTime = true
# 历史日志文件是否压缩（压缩格式为gz）
compress = true
# 是否打印调用源文件和行号
callerFile = true
# 是否打印调用方法
callerFunction = false

[blockchain]
maxFetchBlockNum=128
timeoutSeconds=5     # 2026-11-25 定：与上游参考配置一致（原 1）
batchBlockNum=128
isStrongConsistency=false
onChainTimeout=1
driver="leveldb"
defCacheSize = 128   # 2026-11-25 定：与上游参考配置一致（原 256）

[p2p]
enable=true
msgCacheSize=10240
driver="leveldb"
types = ["dht"]
waitPid = false
dbCache = 16

# 说明（2026-11-25 清理）：原先这里还有 [p2p.sub.gossip] 段（innerBounds/innerSeedEnable/
# isSeed/port/useGithub/serverStart）与 [p2p.sub.dht] maxBroadcastPeers —— 两者在本二进制里都不生效：
#   - gossip 模块未编译进来（plugin/p2p/init 只保留 chain33 内置 dht，types=["dht"]）；
#   - maxBroadcastPeers 字段在 chain33 v1.73.1 中已不存在。
# 故删除以免误导运维。

[p2p.sub.dht]
#dht 版本还不稳定，暂时限定较小的连接数
maxConnectNum = 50
# 是否配置为全节点模式，全节点保存所有分片数据，种子节点应配置为true
# 全节点可以切换为分片节点，暂不支持分片节点切换为全节点
isFullNode = true

[p2p.sub.dht.pubsub]
# gossipsub 拓扑参数（默认 D=6/Dhi=12/Dlo=5、心跳 1000ms、history 5/5）
gossipSubD = 10
gossipSubDhi = 20
gossipSubDlo = 7
gossipSubHeartbeatInterval = 900
gossipSubHistoryGossip = 2
gossipSubHistoryLength = 7
# 2026-11-25：删除 disablePubSubMsgSign=true。pubsub 消息默认用节点私钥签名+验签，
# 关闭等于放弃这层防护（业务层仍有区块/交易签名校验），按上游参考配置保持默认开启。

[p2p.sub.dht.broadcast]
# 2026-11-25 定：与上游参考配置一致——关闭轻区块广播（大区块改走完整广播）。
# 注意：这三项迁移到内嵌配置后，节点 ycc.toml 里不得再重复声明，否则启动 panic: rewrite defalut key
disableLtBlock = true
disableBatchTx = true
minLtBlockSize = 100

[address]
defaultDriver="eth"
[address.enableHeight]
eth=0

[mempool]
# 2026-11-25 定：与上游参考配置一致
# 原值：maxTxFeeRate=10000000(等于默认 1e7，删掉)、isLevelFee=false、maxTxFee=1e8、
#       poolCacheSize 未设(默认 10240)、enableEthCheck=true(已 Deprecated，未使用)
poolCacheSize = 102400
minTxFeeRate = 100000
maxTxFee = 1000000000     # 单笔最大交易费 10 ycc
# 每账户在 mempool 的最大待打包交易数（生效读取点是这里的顶层键，不是 sub.score 里的同名键）。
# 100 = 上游参考配置的默认值（原 ycc.toml 顶层写了 100000，会覆盖此处，已一并删除）
maxTxNumPerAccount = 100
isLevelFee = true
name = "price"

[mempool.sub.score]
poolCacheSize=102400
minTxFee=100000
# 每账户在 mempool 的最大待打包交易数：100（原 100000，压测遗留）
# ⚠ 空投/批量关票脚本单账户单批不得超过该值，否则交易会被拒
maxTxNumPerAccount=100
timeParam=1      #时间占价格比例
priceConstant=1544  #手续费相对于时间的一个合适的常量,取当前unxi时间戳前四位数,排序时手续费高1e-5~=快1s
pricePower=1     #常量比例

[mempool.sub.price]
poolCacheSize = 102400

[consensus]
name="ticket"
minerstart=true
genesisBlockTime=1652797628
# 共识创世地址（测试=地址池 CGEN；正式链须换项目方地址）
genesis="0x1be592c383078b2bb1bb9e8b8fef34c589d00571"
minerExecs=["ticket"]

[consensus.sub.ticket]
genesisBlockTime=1787557086
flushTicketInterval=3600

[consensus.sub.pos33]
onlyVoter = false

[[consensus.sub.pos33.genesis]]
minerAddr="0x991fb09dc31a44b3177673f330c582ac2ea168e0"
returnAddr="0xf39e69a8f2c1041edd7616cf079c7084bb7a5242"
blsAddr="0x6da47c11230e44adb384fed56554bfd55dde1750"
count=1000

[mver.consensus]
addWalletTx = false
# 发展基金（测试=地址池 FUND；正式链换项目方地址）。配套 ForkTicketFundAddrV1=-1：每块 26 ycc
# 冻结在该地址于 ticket 执行器下的子账户，关票时解冻，再用 coins withdraw -e ticket 提到主余额
fundKeyAddr = "0x0750f4a4a9360aa739cd0a169f25419da05fd3df"
# 创世 122,551 票 @5s（calib 解析公式标定；少一票方案 2,551 → 0x1e1b6145；
# 若改多一票 122,552 票则为 0x1e1b6137。旧值 0x1e528e0c 是按 40,645 票）
powLimitBits = "0x1e1b6145"
maxTxNumber = 1500          # 单块最多交易数。必须内嵌（缺失时 MaxTxNumber=0 → 同步区块 ErrManyTx）；
                            # 2026-11-25 定：与上游参考配置一致（原 30000 是压测遗留）

[mver.consensus.pos33]
ticketPrice1=10000
ticketPrice2=100000
minerFeePersent=10 
rewardTransfer=1
blockReward=15
voteRewardPersent=25
mineRewardPersent=11

[mver.consensus.ticket]
coinReward = 43        # 2026-09-18 定稿：每块挖矿奖励 43 ycc（新链 5s 块，总增发 69 ycc/块 ≈ 旧 pos33 13.8 ycc/s；bty 口径 5:3 的 5）
coinDevFund = 26       # 2026-09-18 定稿：每块发展基金 26 ycc（5:3 的 3，增发给 fundKeyAddr；必须是整数——写成 26.0 会被 toml 解析为 float64 → MGInt 返回 0）
ticketPrice = 100000   # 与 ycc pos33 一致（UseEntrust=0 → 全程 ticketPrice2=100000），即 10 万 ycc/张
futureBlockTime = 15      # 2026-11-25 定：与上游参考配置一致（原 16）。CheckBlock 校验 blockTime-now > 15s → ErrFutureBlock
# 以下三个时间参数的【单位是秒】（不是块），且对创世票（IsGenesis=true）不生效：
# 创世票可立即挖、立即关；只约束用户后续新买的票（源码里仅在非创世票时检查）。
# 2026-11-25 定：与上游参考配置保持一致。
ticketFrozenTime = 43200    # 12h：新买的票要等 12h 才可用于出块（防快速倒手）
ticketWithdrawTime = 172800 # 48h：买票后 48h 内不能关票/提取
ticketMinerWaitTime = 7200  # 2h：已挖矿的票要等 2h 才能关闭
targetTimespan = 2304
targetTimePerBlock = 5
retargetAdjustmentFactor = 4

# 账户黑名单：ForkAccountBlacklist/V2 在 [fork.system] 中开启（=0）时，
# 对应的 [mver.blacklist.<fork>] 段必须存在，否则启动 panic；ycc 无存量名单，配空列表
[mver.blacklist.ForkAccountBlacklist]
accountBlacklist=[]

[mver.blacklist.ForkAccountBlacklistV2]
accountBlacklist=[]

[store]
dbCache = 256
driver = "leveldb"
# 与上游参考配置同名：kvmvccmavl = "kvmvcc + mavl" 混合 store，不是另一种数据格式。
# 两者共用同一 MVCC 布局（.-mvcc-.d.<key>.<20位版本>），差别只在 kvmvccmavl 多一层 mavl 写路径：
#   height <  ForkKvmvccmavl → mavl + kvmvcc 双写
#   height >= ForkKvmvccmavl → 只写 kvmvcc（mavl 退役）
# ycc 的 ForkKvmvccmavl=0 → 从创世起就是"纯 kvmvcc"，mavl 层不写；行为与 name="kvmvcc" 等价。
# 这样配置的目的是与上游参考配置的 store 名/代码路径保持一致（减少自研面），数据布局不变。
name = "kvmvccmavl"
# store 库版本（写入空链的 StoreDBMeta）。2026-11-25 定：与上游参考配置一致 = 2.0.0（原未设 → 默认 1.0.0）。
# ⚠ 起链后不可再改：major 位变化会触发 NeedReExec（EnableReExecLocal=true 时会 delAllKeys 清空 localdb，
#   防重放 TX:/STX: 索引随之丢失）。所以必须在建链前定死。
storedbVersion = "2.0.0"

[store.sub.kvmvccmavl]
# 唯一需要显式声明的细项：开启后 kvmvcc 使用 MVCCIter 实现，
# IterateRangeByStateHash 才可用（关闭时该接口直接 panic，对应 RPC Chain33.GetTotalCoins）。
# 其余细项（enableMVCC/enableMavlPrefix/enableMavlPrune/enableMemTree/enableMemVal/
# pruneMVCCHeight/pruneMavlHeight/tkCloseCacheLen）不写即走代码默认值：
#   - ForkKvmvccmavl=0 → mavl 层从创世起不写，mavl 相关项全部惰性；
#   - enableMVCCPrune 默认 false → pruneMVCCHeight 也不生效。
enableMVCCIter = true

[crypto]

[crypto.sub.secp256k1eth]
evmChainID=3999

[wallet]
dbCache = 16
driver = "leveldb"
minFee = 100000
signType = "secp256k1"

[exec]
#disableAddrIndex = true
#disableFeeIndex = true
#disableTxIndex = true
enableMVCC = false
enableStat = false


[exec.sub.coins]
disableAddrReceiver = true
disableCheckTxAmount = true
friendExecer=["evm"]

[exec.sub.evm]
evmGasLimit=5000000
ethMapFromExecutor="coins"
ethMapFromSymbol="ycc"
addressDriver="eth"


[exec.sub.token]
saveTokenTxList = false
#配置一个空值，防止配置文件被覆盖
tokenApprs=[]
# relay 创世地址（测试=地址池 RGEN；正式链须换项目方地址）
[exec.sub.relay]
genesis="0xd670b731a89c9ced148bb8134a6a056febbb8837"

# 超级管理员（测试=地址池 SUPER；正式链须换项目方长期持有的地址——链上权限根）
[exec.sub.manage]
superManager=[
    "0x5beda714eb264b084a537a1a8e1e64fbd55ec01a",
]

[exec.sub.paracross]
nodeGroupFrozenCoins=0
#平行链共识停止后主链等待的高度
paraConsensusStopBlocks=30000

# autonomy 总量地址（测试=地址池 AUTOTOTAL；正式链须换项目方地址）
[exec.sub.autonomy]
total="0x2f17404ef6384e20674f8ee9bc428c6de4a4d84a"
useBalance=false

[mver.autonomy]
#最小委员会数量
minBoards=20
#最大委员会数量
maxBoards=40
#公示一周时间，以区块高度计算
publicPeriod=120960
#单张票价
ticketPrice=3000
#重大项目公示金额阈值
largeProjectAmount=1000000
#创建者消耗金额bty
proposalAmount=500
#董事会成员赞成率，以%计，可修改
boardApproveRatio=51
#全体持票人参与率，以%计
pubAttendRatio=51
#全体持票人赞成率，以%计
pubApproveRatio=51
#全体持票人否决率，以%计
pubOpposeRatio=33
#提案开始结束最小周期高度
startEndBlockPeriod=720
#提案高度 结束高度最大周期 100W
propEndBlockPeriod=1000000
#最小董事会赞成率
minBoardApproveRatio=50
#最大董事会赞成率
maxBoardApproveRatio=66
#最小全体持票人否决率
minPubOpposeRatio=33
#最大全体持票人否决率
maxPubOpposeRatio=50
#可以调整，但是可能需要进行范围的限制：参与率最低设置为 50%， 最高设置为 80%，赞成率，最低 50.1%，最高80%
#不能设置太低和太高，太低就容易作弊，太高则有可能很难达到
#最小全体持票人参与率
minPubAttendRatio=50
#最大全体持票人参与率
maxPubAttendRatio=80
#最小全体持票人赞成率
minPubApproveRatio=50
#最大全体持票人赞成率
maxPubApproveRatio=80
#最小公示周期
minPublicPeriod=120960
#最大公示周期
maxPublicPeriod=241920
#最小重大项目阈值(coin)
minLargeProjectAmount=1000000
#最大重大项目阈值(coin)
maxLargeProjectAmount=3000000
#最小提案金(coin)
minProposalAmount=20
#最大提案金(coin)
maxProposalAmount=2000	
#每个时期董事会审批最大额度300万
maxBoardPeriodAmount =3000000
#时期为一个月
boardPeriod=518400
#4w高度，大概2天 (未生效)
itemWaitBlockNumber=40000 

#系统中所有的fork,默认用chain33的测试网络的
#但是我们可以替换
[fork.system]
ForkChainParamV1=0
ForkCheckTxDup=0
# 区块 hash 算法分叉：新链从创世即用 HashNew（旧链当年在 5,000,000 高度才切；新链无历史不需要切点）
ForkBlockHash=0
ForkMinerTime=0
ForkTransferExec=0
ForkExecKey=0
ForkTxGroup=0
ForkResetTx0=0
ForkWithdraw=0
ForkExecRollback=0
ForkCheckBlockTime=0
ForkTxHeight=0
ForkTxGroupPara=0
ForkChainParamV2=0
ForkMultiSignAddress=0
ForkStateDBSet=0
ForkLocalDBAccess=0
ForkBlockCheck=0
ForkBase58AddressCheck=0
#平行链上使能平行链执行器如user.p.x.coins执行器的注册，缺省为0，对已有的平行链需要设置一个fork高度
ForkEnableParaRegExec=0
ForkCacheDriver=0
ForkTicketFundAddrV1=-1 #fork6.3
#主链和平行链都使用同一个fork高度
ForkRootHash=0 
#eth address key format fork
ForkFormatAddressKey=0
# mempool 打包时对 eth 签名交易按 nonce 升序排序（metamask 交易必需）。
# 旧链是未来高度 36,000,000（≈5.7 年，等于永不开启）；新链从 0 开启。
ForkCheckEthTxSort=0
#chain33 v1.72.0 起要求以下 fork 必须在配置中显式声明（缺失会 panic: system fork X not config）
#取值规则：参考链（chain33 v1.72.x + plugin v1.74.0）已开启的 fork → ycc 新链从高度 0 开启；未开启的 → -1 关闭
ForkEthAddressFormat=0
ForkProxyExec=0
ForkMaxTxFeeV1=0
ForkParaFee=-1
ForkAccountBlacklist=0
ForkAccountBlacklistV2=0
ForkTxChainIDStrict=0
# chain33 v1.73.0 新增 fork（同样必须显式声明，缺失会 panic: system fork X not config）
# ForkEthSignEnvelope：启用后对 eth 签名（RLP）交易按当前高度重建 chain33 信封并逐字段比对，
#   防止签名未覆盖的信封字段（execer/To/fee/payload/有效期等）被第三方改写；新链无历史，从 0 开启。
# ForkGenesisOnlyAtHeightZero：框架在派发 Exec_Genesis 前统一拒绝 height!=0 的创世动作。
#   新链创世交易只存在于高度 0，从 0 开启安全。
ForkEthSignEnvelope=0
ForkGenesisOnlyAtHeightZero=0

[fork.sub.coins]
Enable=0
ForkFriendExecer=0

[fork.sub.pos33]
# -1 = 停用 pos33 执行器（driver 激活高度=MaxHeight → LoadDriver 失败 → 交易在 checkTx 处
# 以 ErrExecNameNotAllow 被拒）。pos33 已在迁移中被 ticket 取代，交易重放依赖此停用 + txhash 索引。
Enable=-1
ForkReward15=0 
ForkFixReward=0
UseEntrust=0

[fork.sub.rollup]
# rollup 执行器激活高度：新链从 0 启用（2026-11-25 定；旧链是未来高度 36,000,000）
Enable=0
ForkSecurityHardening=0

[fork.sub.none]
ForkUseTimeDelay=0

[fork.sub.evmxgo]
Enable=0

[fork.sub.ticket]
Enable=0
ForkTicketId = 0 
ForkTicketVrf = 0
# plugin v1.76.x 新增：关闭重复票、挖矿交易(tx[0])执行器必须为 ticket（均为安全加固，新链从 0 开启）
ForkTicketCloseDup=0
ForkMinerExecerCheck=0

[fork.sub.retrieve]
Enable=0
ForkRetrive=0
ForkRetriveAsset=0

[fork.sub.hashlock]
Enable=0
ForkBadRepeatSecret=0

[fork.sub.manage]
Enable=0
ForkManageExec=0
ForkManageAutonomyEnable=-1

[fork.sub.token]
Enable=0
ForkTokenBlackList=0
ForkBadTokenSymbol=0
ForkTokenPrice=0
ForkTokenSymbolWithNumber=0
ForkTokenCheck=0
ForkTokenEvm=-1

[fork.sub.trade]
Enable=0
ForkTradeBuyLimit=0
ForkTradeAsset=0
ForkTradeID=0
ForkTradeFixAssetDB=0
ForkTradePrice=0

[fork.sub.paracross]
Enable=0
ForkParacrossWithdrawFromParachain=0
ForkParacrossCommitTx=0
ForkLoopCheckCommitTxDone=0
#fork for 6.4
ForkParaAssetTransferRbk=0
ForkParaSelfConsStages=0
#仅平行链适用（主链无影响；与上游参考配置一致：RootHash 关闭、AutonomySuperGroup 开启）
ForkParaFullMinerHeight=-1
ForkParaRootHash=-1
ForkParaSupervision=0
ForkParaAutonomySuperGroup = 0
ForkParaFreeRegister = 0
ForkParaTitleExecMatch=0
ForkParaCheckTx=0

[fork.sub.multisig]
Enable=0

[fork.sub.autonomy]
Enable=0
ForkAutonomyDelRule=0
ForkAutonomyEnableItem=0

[fork.sub.evm]
Enable=0
ForkEVMState=0
ForkEVMABI=0
ForkEVMFrozen=0
ForkEVMKVHash=0
ForkEVMYoloV1=0
ForkEVMTxGroup=0
ForkEVMMixAddress=0
ForkEVMAddressInit=0
ForkIntrinsicGas=0
ForkEvmExecNonce=0
#chain33 v1.72.0/plugin v1.74.0 新增，必须显式声明（默认 0 = 从高度 0 生效）
ForkEvmExecNonceV2=0
#plugin v1.76.x 新增（同样必须声明）：ForkEVMSuicidePayer 默认 0；ForkEVMTransferRevert 默认 MaxHeight→-1
ForkEVMSuicidePayer=0
# 新链从 0 开启：EVM value transfer 可回滚（修复"回滚后虚余额仍可花"）。
# 上游默认 MaxHeight 只是要求链显式 opt-in；参考配置在 48206000 开启。
ForkEVMTransferRevert=0
ForkEVMFixOverflow=0

[fork.sub.unfreeze]
Enable=0
ForkTerminatePart=0
ForkUnfreezeIDX=0

[fork.sub.store-kvmvccmavl]
ForkKvmvccmavl=0

[Fork.sub.exchange]
Enable=0
ForkFix1=0
ForkParamV1 = 0
ForkParamV2 = 0
ForkParamV3 = 0
ForkParamV4 = 0
ForkParamV5 = 0
ForkParamV6 = 0
ForkParamV7 = 0
ForkParamV8 = 0
ForkParamV9 = 0
ForkParamV10 = 0
ForkParamV11 = 0
ForkParamV12 = 0
ForkParamV13 = 0
ForkParamV14 = 0
ForkParamV15 = 0
ForkParamV16 = 0
ForkParamV17 = 0
ForkParamV18 = 0
ForkParamV19 = 0
ForkParamV20 = 0
ForkParamV21 = 0
ForkParamV22 = 0
ForkParamV23 = 0
ForkParamV24 = 0
ForkParamV25 = 0
ForkParamV26 = 0
ForkParamV27 = 0
ForkParamV28 = 0
ForkParamV29 = 0

[health]
checkInterval=1
unSyncMaxTimes=2

[metrics]
#是否使能发送metrics数据的发送
enableMetrics = false
#数据保存模式
dataEmitMode = "influxdb"

[metrics.sub.influxdb]
#以纳秒为单位的发送间隔
database = "chain33metrics"
duration = 1000000000
namespace = ""
password = ""
url = "http://influxdb:8086"
username = ""
`
