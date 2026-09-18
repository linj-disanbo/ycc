// ticketdist 工具：按持票地址（ReturnAddress）统计票数，列出票最多的 N 个地址及其委托挖矿地址。
//
//	ticketdist -state <state.bin> -top 100 -out top100-tickets.txt
package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/33cn/chain33/types"
	tickettypes "github.com/33cn/plugin/plugin/dapp/ticket/types"
)

type holder struct {
	addr    string
	count   int
	miners  map[string]bool // 委托挖矿地址（MinerAddress）去重
	value   int64           // 票值合计（raw）
}

func main() {
	state := flag.String("state", "state.bin", "transform 输出的 state.bin")
	top := flag.Int("top", 100, "输出前 N 名")
	out := flag.String("out", "top100-tickets.txt", "输出文件")
	flag.Parse()

	f, err := os.Open(*state)
	if err != nil {
		log.Fatalf("打开 state.bin 失败: %v", err)
	}
	defer f.Close()
	var kvs []*types.KeyValue
	if err := gob.NewDecoder(f).Decode(&kvs); err != nil {
		log.Fatalf("解析 state.bin 失败: %v", err)
	}

	holders := make(map[string]*holder)
	var totalTickets int
	for _, kv := range kvs {
		k := string(kv.Key)
		if !strings.HasPrefix(k, "mavl-ticket-") {
			continue
		}
		t := &tickettypes.Ticket{}
		if err := types.Decode(kv.Value, t); err != nil {
			continue
		}
		totalTickets++
		h := holders[t.ReturnAddress]
		if h == nil {
			h = &holder{addr: t.ReturnAddress, miners: make(map[string]bool)}
			holders[t.ReturnAddress] = h
		}
		h.count++
		h.miners[t.MinerAddress] = true
		h.value += t.Price
	}
	list := make([]*holder, 0, len(holders))
	for _, h := range holders {
		list = append(list, h)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].addr < list[j].addr
	})

	// 输出
	lines := []string{fmt.Sprintf("# ycc 迁移后持票地址 Top%d（按票数降序）", *top)}
	lines = append(lines, fmt.Sprintf("# 共 %d 个持票地址，总票数 %d（含全链）", len(list), totalTickets))
	lines = append(lines, "# 排名\t持票地址(ReturnAddress)\t票数\t票值(ycc)\t委托挖矿地址(MinerAddress)")
	for i, h := range list {
		if i >= *top {
			break
		}
		miners := make([]string, 0, len(h.miners))
		for m := range h.miners {
			miners = append(miners, m)
		}
		sort.Strings(miners)
		lines = append(lines, fmt.Sprintf("%d\t%s\t%d\t%d\t%s", i+1, h.addr, h.count, h.value/1e8, strings.Join(miners, ",")))
	}

	of, err := os.Create(*out)
	if err != nil {
		log.Fatalf("创建输出文件失败: %v", err)
	}
	defer of.Close()
	for _, ln := range lines {
		fmt.Fprintln(of, ln)
	}
	log.Printf("完成: %s（%d 个持票地址，总票数 %d）", *out, len(list), totalTickets)
}
