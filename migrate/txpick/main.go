// txpick：从旧链 datadir（快照）里按类别抽取代表性交易样本，输出 JSON（含原始交易字节）。
//
// 用途：新链「防重放」验收（P0 复放实测）需要把旧交易原样重放到新链上，
// 而 txscan 只导出 txhash（没有交易本体）。本工具按类别各抽 N 笔，导出
// rawHex（= types.Encode(tx)，可直接作为 Chain33.SendTransaction 的 tx 参数）。
//
// 用法：
//
//	txpick -datadir <旧链 datadir> -out samples.json [-per-class 3] [-max-height N]
//
// 类别（每类默认 3 笔）：
//
//	coins_time_expire     coins + 时间戳型 expire        → 新链 ErrTxExpire
//	coins_height_expire   coins + TxHeight 型 expire     → 通常即 dust（被 -skip-dust 跳过）
//	coins_zero_expire     coins + expire==0 且非 dust    → 应 ErrTxDup（在索引内）
//	coins_dust_zero       coins + amount==1 + expire==0  → 被 dust 过滤跳过（可重放）
//	pos33                 pos33 执行器                   → ErrExecNameNotAllow（新链停用）
//	eth_sign_evm          eth 签名(ty=8452) 且 execer=evm → evm nonce / 过期 拦
//	other_exec            其它执行器（ticket/token/paracross…）
//	evm_amount_one        evm + amount==1（非 coins dust）→ 仍进索引（若 expire 允许）
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/yccproject/ycc/migrate"

	_ "github.com/33cn/chain33/system/address/eth"
)

const (
	ClsCoinsTimeExpire   = "coins_time_expire"
	ClsCoinsHeightExpire = "coins_height_expire"
	ClsCoinsZeroExpire   = "coins_zero_expire"
	ClsCoinsDustZero     = "coins_dust_zero"
	ClsPos33             = "pos33"
	ClsEthSignEvm        = "eth_sign_evm"
	ClsOtherExec         = "other_exec"
	ClsEvmAmountOne      = "evm_amount_one"
)

var allClasses = []string{
	ClsCoinsTimeExpire, ClsCoinsHeightExpire, ClsCoinsZeroExpire, ClsCoinsDustZero,
	ClsPos33, ClsEthSignEvm, ClsOtherExec, ClsEvmAmountOne,
}

type sample struct {
	Class       string `json:"class"`
	Height      int64  `json:"height"`
	Index       int    `json:"index"`
	TxHash      string `json:"txhash"`
	RawHex      string `json:"rawHex"`
	Execer      string `json:"execer"`
	SignTy      int32  `json:"sigTy"`
	EthSign     bool   `json:"ethSign"`
	RLPForm     bool   `json:"rlpForm"` // payload.Note 非空 = eth 原始 RLP 交易
	Nonce       int64  `json:"nonce"`
	Expire      int64  `json:"expire"`
	ExpireClass string `json:"expireClass"`
	TxHeight    int64  `json:"txHeight,omitempty"`
	Amount      int64  `json:"amount"`
	AmountKind  string `json:"amountKind,omitempty"`
	From        string `json:"from"`
	Dust        bool   `json:"dust"`
	WillIndex   bool   `json:"willIndex"` // 按 txscan(-filter-pos33 -skip-timestamp-expire -skip-dust-amount) 是否收录
	Expect      string `json:"expect"`    // 预期结果（给人看）
}

type result struct {
	SnapshotHeight int64             `json:"snapshotHeight"`
	PerClass       int               `json:"perClass"`
	Criteria       string            `json:"criteria"`
	ScannedBlocks  int64             `json:"scannedBlocks"`
	ScannedTxs     int64             `json:"scannedTxs"`
	Samples        []sample          `json:"samples"`
	ClassCounts    map[string]int    `json:"classCounts"`
	MissingClasses []string          `json:"missingClasses,omitempty"`
	Notes          map[string]string `json:"notes,omitempty"`
}

func main() {
	datadir := flag.String("datadir", "", "旧链 datadir（须含 blockchain.db）")
	out := flag.String("out", "samples.json", "输出 JSON 文件")
	perClass := flag.Int("per-class", 3, "每类抽取笔数")
	maxHeight := flag.Int64("max-height", -1, "最多扫描到该高度（-1=链尾）")
	progress := flag.Int64("progress", 5000000, "每扫描 N 笔交易打印进度")
	fullScan := flag.Bool("full-scan", false, "即使各类已抽满也扫完全链（统计用）")
	flag.Parse()

	if *datadir == "" {
		log.Fatal("必须指定 -datadir")
	}
	dbPath := filepath.Join(*datadir, "blockchain.db")
	db, err := leveldb.OpenFile(dbPath, &opt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("打开 %s 失败: %v（节点是否在运行？leveldb 锁互斥）", dbPath, err)
	}
	defer db.Close()

	last, err := migrate.LoadLastHeight(db)
	if err != nil {
		log.Fatalf("读取链高度失败: %v", err)
	}
	top := last
	if *maxHeight >= 0 && *maxHeight < top {
		top = *maxHeight
	}
	log.Printf("旧链高度=%d，扫描 1..%d，每类抽 %d 笔", last, top, *perClass)

	res := &result{
		SnapshotHeight: last,
		PerClass:       *perClass,
		Criteria:       "按 execer/金额/expire 形态各抽 N 笔；rawHex = types.Encode(tx)，可直接 SendTransaction",
		ClassCounts:    make(map[string]int),
		Samples:        []sample{},
		Notes: map[string]string{
			ClsCoinsTimeExpire:   "时间戳型在新链上永久失效（blocktime 单调递增）→ 期望 ErrTxExpire",
			ClsCoinsHeightExpire: "TxHeight 型：新链高度到 [H-200,H+600] 窗口后可打包；若同时是 coins amount==1 则被 -skip-dust-amount 跳过",
			ClsCoinsZeroExpire:   "expire==0 且非 dust → 应在 txhash 索引内 → 期望 ErrTxDup",
			ClsCoinsDustZero:     "coins amount==1 → 被 -skip-dust-amount 跳过（本次决策接受的剩余风险）",
			ClsPos33:             "pos33 已配置停用 → 期望 ErrExecNameNotAllow",
			ClsEthSignEvm:        "eth 签名交易：chain33 形式命中索引(ErrTxDup)；RLP 形式需用 eth_sendRawTransaction 原样提交，靠 evm nonce 拦",
			ClsOtherExec:         "其它执行器：按 expire 形态落在过期或索引拦截",
			ClsEvmAmountOne:      "evm amount==1 不算 dust，仍进索引",
		},
	}
	counts := make(map[string]int)

	var scannedTxs int64
	for h := int64(1); h <= top; h++ {
		txs, _, err := migrate.LoadBlockTxs(db, h)
		if err != nil {
			continue
		}
		for i, tx := range txs {
			scannedTxs++
			cls, s := classify(tx, h, i)
			if cls == "" {
				continue
			}
			if counts[cls] >= *perClass {
				continue
			}
			counts[cls]++
			res.Samples = append(res.Samples, *s)
			if !*fullScan && allFull(counts, *perClass) {
				// 已抽满，提前结束
				goto done
			}
		}
		if *progress > 0 && scannedTxs > 0 && scannedTxs%*progress < int64(len(txs)) {
			log.Printf("进度: 高度 %d/%d，交易 %d，已抽 %v", h, top, scannedTxs, counts)
		}
	}
done:
	res.ScannedBlocks = top
	res.ScannedTxs = scannedTxs
	for _, c := range allClasses {
		res.ClassCounts[c] = counts[c]
		if counts[c] < *perClass {
			res.MissingClasses = append(res.MissingClasses, c)
		}
	}
	sort.Slice(res.Samples, func(i, j int) bool {
		if res.Samples[i].Class != res.Samples[j].Class {
			return res.Samples[i].Class < res.Samples[j].Class
		}
		return res.Samples[i].Height < res.Samples[j].Height
	})

	// 往返校验：rawHex 必须能解码回同一笔交易（否则 P0 无法用它重放）
	for i := range res.Samples {
		raw, err := hex.DecodeString(res.Samples[i].RawHex)
		if err != nil {
			log.Fatalf("样本 %s rawHex 非法: %v", res.Samples[i].TxHash, err)
		}
		var tx types.Transaction
		if err := types.Decode(raw, &tx); err != nil {
			log.Fatalf("样本 %s rawHex 无法解码: %v", res.Samples[i].TxHash, err)
		}
		if hex.EncodeToString(tx.Hash()) != res.Samples[i].TxHash {
			log.Fatalf("样本 %s rawHex 往返后 hash 不一致", res.Samples[i].TxHash)
		}
	}

	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		log.Fatalf("序列化失败: %v", err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatalf("写 %s 失败: %v", *out, err)
	}

	// 控制台汇总
	fmt.Printf("\n===== txpick 汇总 =====\n快照高度=%d 扫描区块=%d 交易=%d\n", res.SnapshotHeight, res.ScannedBlocks, res.ScannedTxs)
	for _, c := range allClasses {
		flagMark := ""
		if counts[c] < *perClass {
			flagMark = "  ← 不足"
		}
		fmt.Printf("  %-20s %3d / %-3d%s\n", c, counts[c], *perClass, flagMark)
	}
	fmt.Printf("输出: %s（%d 笔样本，%.1f KB）\n", *out, len(res.Samples), float64(len(b))/1024)
	if len(res.MissingClasses) > 0 {
		fmt.Printf("缺失类别: %s\n", strings.Join(res.MissingClasses, ", "))
	}
}

func allFull(counts map[string]int, per int) bool {
	for _, c := range allClasses {
		if counts[c] < per {
			return false
		}
	}
	return true
}

// classify 判断类别并生成样本（不匹配返回 ""）。
func classify(tx *types.Transaction, height int64, index int) (string, *sample) {
	execer := string(tx.Execer)
	realExec := string(types.GetRealExecName(tx.Execer))
	sigTy := int32(0)
	if tx.Signature != nil {
		sigTy = tx.Signature.Ty
	}
	ethSign := sigTy == migrate.EthSignTy
	amount, kind := migrate.TxAmount(tx)
	expCls := migrate.ExpireClass(tx.Expire)
	dust := migrate.IsDust(tx)
	isCoins := migrate.IsCoinsExecer(tx.Execer)
	isPos33 := migrate.IsPos33Execer(execer)

	cls := ""
	switch {
	case isPos33:
		cls = ClsPos33
	case isCoins && expCls == migrate.ExpireClassTime:
		cls = ClsCoinsTimeExpire
	case isCoins && expCls == migrate.ExpireClassHeight:
		cls = ClsCoinsHeightExpire
	case isCoins && expCls == migrate.ExpireClassZero && dust:
		cls = ClsCoinsDustZero
	case isCoins && expCls == migrate.ExpireClassZero:
		cls = ClsCoinsZeroExpire
	case realExec == "evm" && amount == 1:
		cls = ClsEvmAmountOne
	case ethSign && realExec == "evm":
		cls = ClsEthSignEvm
	case !isCoins && realExec != "evm":
		cls = ClsOtherExec
	}
	if cls == "" {
		return "", nil
	}

	// RLP 形式判定：evm payload 的 Note 非空 = eth 原始 RLP 交易（重新组装的 hash 会变）
	rlpForm := false
	if realExec == "evm" {
		var act types.EVMContractAction4Chain33
		if err := types.Decode(tx.Payload, &act); err == nil && len(act.Note) > 0 {
			rlpForm = true
		}
	}

	willIndex := !isPos33 && expCls != migrate.ExpireClassTime && !dust
	expect := ""
	switch {
	case isPos33:
		expect = "ErrExecNameNotAllow（pos33 已配置停用）"
	case expCls == migrate.ExpireClassTime:
		expect = "ErrTxExpire（时间戳型，新链永久失效）"
	case dust:
		expect = "可重放：coins dust 被 -skip-dust-amount 跳过（决策已接受的剩余风险）"
	case rlpForm:
		expect = "RLP 形式：原样重放已被旧 expire 覆盖；重新组装提交靠 evm nonce 拒（nonce too low/high）"
	default:
		expect = "ErrTxDup（txhash 索引命中）"
	}

	s := &sample{
		Class:       cls,
		Height:      height,
		Index:       index,
		TxHash:      hex.EncodeToString(tx.Hash()),
		RawHex:      hex.EncodeToString(types.Encode(tx)),
		Execer:      execer,
		SignTy:      sigTy,
		EthSign:     ethSign,
		RLPForm:     rlpForm,
		Nonce:       tx.Nonce,
		Expire:      tx.Expire,
		ExpireClass: expCls,
		TxHeight:    migrate.ExpireHeight(tx.Expire),
		Amount:      amount,
		AmountKind:  kind,
		Dust:        dust,
		WillIndex:   willIndex,
		Expect:      expect,
	}
	if tx.Signature != nil {
		s.From = strings.ToLower(tx.From())
	}
	return cls, s
}
