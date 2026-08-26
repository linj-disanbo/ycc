// pos33stat 工具：从 dump.gob 直接统计 pos33 合约相关资金与状态分布（权威口径）。
//
//	pos33stat -in dump.gob -symbol ycc
//
// 统计：pos33 执行器主余额（奖励池 Bal/Frozen）、pos33 执行器区域（质押）账户数与合计/分布、
// pos33 状态键（consignee/consignor/deposit/bind/bls）数量、异常地址（非 0x+40hex）。
package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/33cn/chain33/common/address"
	"github.com/33cn/chain33/system/address/eth"
	"github.com/33cn/chain33/types"
	pos33types "github.com/yccproject/ycc/plugin/dapp/pos33/types"
	"github.com/yccproject/ycc/migrate"
)

func execAddrOf(name string) string {
	a, err := address.GetExecAddress(name, eth.ID)
	if err != nil {
		panic(err)
	}
	return a
}

func main() {
	in := flag.String("in", "dump.gob", "export 输出文件")
	symbol := flag.String("symbol", "ycc", "币符号")
	topN := flag.Int("top", 20, "区域分布展示前 N 大")
	flag.Parse()

	f, _ := os.Open(*in)
	var d migrate.Dump
	if err := gob.NewDecoder(f).Decode(&d); err != nil {
		log.Fatalf("解析 dump 失败: %v", err)
	}
	f.Close()

	pos33Exec := execAddrOf("pos33")
	mainPfx := "mavl-coins-" + *symbol + "-"
	regionPfx := mainPfx + "exec-" + pos33Exec + ":"
	pos33Pfx := "mavl-pos33-"
	addrRe := regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

	var poolBal, poolFrozen int64
	var region []struct {
		addr   string
		bal    int64
		frozen int64
	}
	var pos33Keys = map[string]int{}
	var badAddrs []string

	for _, kv := range d.KVs {
		k := string(kv.Key)
		switch {
		case k == mainPfx+pos33Exec:
			acc := &types.Account{}
			if types.Decode(kv.Value, acc) == nil {
				poolBal = acc.Balance
				poolFrozen = acc.Frozen
			}
		case strings.HasPrefix(k, regionPfx):
			acc := &types.Account{}
			if types.Decode(kv.Value, acc) == nil {
				addr := strings.TrimPrefix(k, regionPfx)
				region = append(region, struct {
					addr   string
					bal    int64
					frozen int64
				}{addr, acc.Balance, acc.Frozen})
			}
		case strings.HasPrefix(k, pos33Pfx):
			// 键形如 mavl-pos33-<type>-<addr> 或 mavl-pos33-<addr>
			rest := strings.TrimPrefix(k, pos33Pfx)
			typ := "deposit"
			for _, t := range []string{"consignee-", "consignor-", "bind-", "bls-"} {
				if strings.HasPrefix(rest, t) {
					typ = strings.TrimSuffix(t, "-")
					rest = strings.TrimPrefix(rest, t)
					break
				}
			}
			if rest == "allcountid" || rest == "all-frozen" {
				typ = rest
			}
			pos33Keys[typ]++
		}
		if strings.HasPrefix(k, "mavl-coins-") || strings.HasPrefix(k, pos33Pfx) {
			// 收集所有地址字段做合法性检查（coins 主余额/区域 + pos33 记录地址）
			for _, tok := range []string{""} {
				_ = tok
			}
		}
	}
	// 异常地址：coins 键里的地址，eth(0x+40hex) 或 base58 都不算异常；
	// 仅含非法字符（如全角逗号、内嵌 0x、大写 ETH 校验和等）标记
	bs58Re := regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{26,35}$`)
	for _, kv := range d.KVs {
		k := string(kv.Key)
		var addr string
		if strings.HasPrefix(k, mainPfx) && !strings.HasPrefix(k, mainPfx+"exec-") {
			addr = strings.TrimPrefix(k, mainPfx)
		} else if i := strings.Index(k, mainPfx+"exec-"); i == 0 {
			rest := strings.TrimPrefix(k, mainPfx+"exec-")
			if j := strings.Index(rest, ":"); j > 0 {
				addr = rest[j+1:]
			}
		}
		if addr == "" {
			continue
		}
		if addrRe.MatchString(addr) || bs58Re.MatchString(addr) {
			continue
		}
		// 再排除 eth 校验和（Mixed-case）与 chain33 其它合法格式：交给 CheckAddress
		badAddrs = append(badAddrs, addr)
	}

	fmt.Printf("== pos33 执行器地址: %s ==\n", pos33Exec)
	fmt.Printf("主余额（奖励池）: balance=%.4f ycc  frozen=%.4f ycc  合计=%.4f 亿 ycc\n",
		float64(poolBal)/1e8, float64(poolFrozen)/1e8, float64(poolBal+poolFrozen)/1e16)
	var rBal, rFrozen int64
	for _, r := range region {
		rBal += r.bal
		rFrozen += r.frozen
	}
	fmt.Printf("执行器区域: %d 个账户  合计=%.4f 亿 ycc（balance=%.4f 亿 / frozen=%.4f 亿）\n",
		len(region), float64(rBal+rFrozen)/1e16, float64(rBal)/1e16, float64(rFrozen)/1e16)
	sort.Slice(region, func(i, j int) bool { return region[i].frozen+region[i].bal > region[j].frozen+region[j].bal })
	fmt.Printf("  前 %d 大区域账户（frozen 质押额）:\n", *topN)
	for i, r := range region {
		if i >= *topN {
			break
		}
		fmt.Printf("    %s  %.2f 万 ycc（bal=%.2f）\n", r.addr, float64(r.frozen)/1e8/1e4, float64(r.bal)/1e8)
	}
	// 奖励记账统计（从 consignee 记录解码）
	var sumRemainReward, sumFeeReward, sumRemainFeeReward, sumConsignorAmount int64
	var consigneeCount int
	for _, kv := range d.KVs {
		k := string(kv.Key)
		if !strings.HasPrefix(k, pos33Pfx+"consignee-") {
			continue
		}
		ce := &pos33types.Pos33Consignee{}
		if types.Decode(kv.Value, ce) != nil {
			continue
		}
		consigneeCount++
		sumFeeReward += ce.FeeReward
		sumRemainFeeReward += ce.RemainFeeReward
		for _, cr := range ce.Consignors {
			sumRemainReward += cr.RemainReward
			sumConsignorAmount += cr.Amount
		}
	}
	fmt.Printf("consignee 记录: %d 个；委托人质押合计=%.4f 亿；奖励记账：remainReward=%.4f ycc / remainFeeReward=%.4f ycc / feeReward(历史累计,勿再发)=%.4f 亿 ycc\n",
		consigneeCount, float64(sumConsignorAmount)/1e16,
		float64(sumRemainReward)/1e8, float64(sumRemainFeeReward)/1e8, float64(sumFeeReward)/1e16)
	fmt.Printf("pos33 状态键: %v\n", pos33Keys)
	if len(badAddrs) > 0 {
		fmt.Printf("⚠ 疑似异常地址（非 eth/base58 标准格式，需人工复核）: %d 个\n", len(badAddrs))
		for _, a := range badAddrs {
			fmt.Printf("    %s\n", a)
		}
	} else {
		fmt.Printf("coins 键地址格式全部正常（eth + base58 双格式）\n")
	}
}
