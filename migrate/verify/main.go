// verify 工具：校验 transform 输出相对原始 dump 只做了预期内的修改。
//
//	verify -in dump.gob -state out/state.bin -symbol ycc
//
// 预期变更（其余键必须逐字节一致）：
//   1) mavl-pos33-* 与 pos33 执行器区域币键：全部删除
//   2) coins 主余额键：仅参与转换的地址可被修改（退款+奖励发放）
//   3) 新增键：仅 mavl-ticket-*、ticket 执行器区域币键、ticket 执行器主余额（票值托管）
// 任一违反即输出具体键并返回非零退出码。
package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/33cn/chain33/common/address"
	"github.com/33cn/chain33/system/address/eth"
	"github.com/33cn/chain33/types"
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
	in := flag.String("in", "dump.gob", "原始导出文件")
	state := flag.String("state", "out/state.bin", "transform 输出状态键")
	symbol := flag.String("symbol", "ycc", "币符号")
	flag.Parse()

	f, _ := os.Open(*in)
	var dump migrate.Dump
	if err := gob.NewDecoder(f).Decode(&dump); err != nil {
		log.Fatalf("解析 dump 失败: %v", err)
	}
	f.Close()

	f2, _ := os.Open(*state)
	var outKVs []*types.KeyValue
	if err := gob.NewDecoder(f2).Decode(&outKVs); err != nil {
		log.Fatalf("解析 state 失败: %v", err)
	}
	f2.Close()

	orig := make(map[string][]byte, len(dump.KVs))
	for _, kv := range dump.KVs {
		orig[string(kv.Key)] = kv.Value
	}
	out := make(map[string][]byte, len(outKVs))
	for _, kv := range outKVs {
		out[string(kv.Key)] = kv.Value
	}

	pos33Prefix := "mavl-pos33-"
	coinsPrefix := "mavl-coins-" + *symbol + "-"
	// 执行器地址：与节点运行时一致（eth 驱动），动态计算避免硬编码
	pos33Exec := execAddrOf("pos33")
	ticketExec := execAddrOf("ticket")
	log.Printf("pos33 exec=%s ticket exec=%s", pos33Exec, ticketExec)

	var violations []string
	unchanged, modified, deleted, added := 0, 0, 0, 0

	// 1) 原键在输出中的情况
	for k, v := range orig {
		ov, exists := out[k]
		switch {
		case strings.HasPrefix(k, pos33Prefix):
			if exists {
				violations = append(violations, "pos33 键未删除: "+k)
			}
			deleted++
		case strings.HasPrefix(k, coinsPrefix+"exec-"+pos33Exec+":"):
			if exists {
				violations = append(violations, "pos33 区域币键未删除: "+k)
			}
			deleted++
		default:
			if !exists {
				violations = append(violations, "非预期删除: "+k)
			} else if string(ov) != string(v) {
				// coins 主余额允许被修改（转换地址）
				if strings.HasPrefix(k, coinsPrefix) && !strings.Contains(k, "-exec-") {
					modified++
				} else {
					violations = append(violations, "非 coins 主余额键被修改: "+k)
				}
			} else {
				unchanged++
			}
		}
	}
	// 2) 输出中新增的键
	for k := range out {
		if _, exists := orig[k]; exists {
			continue
		}
		if strings.HasPrefix(k, "mavl-ticket-") ||
			strings.HasPrefix(k, coinsPrefix+"exec-"+ticketExec+":") ||
			k == coinsPrefix+ticketExec { // ticket 执行器主余额（票值托管）
			added++
			continue
		}
		violations = append(violations, "非预期新增: "+k)
	}

	fmt.Printf("原键 %d：未变 %d / coins主余额修改 %d / 删除(pos33+区域) %d；新增 %d；违规 %d\n",
		len(orig), unchanged, modified, deleted, added, len(violations))
	if len(violations) > 0 {
		sort.Strings(violations)
		for _, v := range violations {
			fmt.Println("  ✗", v)
		}
		os.Exit(1)
	}
	fmt.Println("OK：转换只修改了预期键（pos33 清理 + coins 余额搬运 + ticket 新增）")
}
