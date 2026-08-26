// Package migrate 提供 ycc 链"减少存储重跑"迁移工具库。
// 迁移架构（2026-08-24 定稿）：
//   1) export    : 读旧节点 kvmvcc store.db(leveldb)，取每个逻辑键最新版本 → 全量状态 dump
//   2) transform : pos33 → ticket 转换（质押折票、奖励返还、币账户重排、ticket 状态与本地票列表生成）
//   3) build     : 把转换后的状态写入新 datadir（kvmvcc store 版本0 数据键 + blockchain.db 的 LODB 本地键）
//   新链创世块无交易：store 预载版本0 数据，创世执行（空变更集）自动注册版本0 元信息并得到 stateHash。
//   参考：chain33 kvmvcc store（plugin/store/kvmvcc）、common/db/mvcc.go。
package migrate

import (
	"fmt"

	"github.com/33cn/chain33/types"
)

const (
	// MVCC 键布局（chain33 common/db/mvcc.go）
	mvccPrefix = ".-mvcc-."
	mvccData   = mvccPrefix + "d."
	mvccMeta   = mvccPrefix + "m."
	mvccLast   = mvccPrefix + "l."
	// 版本号 20 位补零
	zeroVersion = "00000000000000000000"
)

// MVCCDataKey 构造存储键：.-mvcc-.d.<key>.<20位版本>
func MVCCDataKey(key []byte, version int64) []byte {
	return []byte(fmt.Sprintf("%s%s.%020d", mvccData, key, version))
}

// MetaPrefix 返回 .-mvcc-.m. 前缀（版本元信息区）
func MetaPrefix() string { return mvccMeta }

// ParseMVCCKey 解析 .-mvcc-.d.<key>.<version> → (逻辑键, 版本号)；非数据键返回 ok=false
func ParseMVCCKey(k []byte) (logical []byte, version int64, ok bool) {
	prefix := []byte(mvccData)
	if len(k) <= len(prefix)+1 {
		return nil, 0, false
	}
	if string(k[:len(prefix)]) != mvccData {
		return nil, 0, false
	}
	rest := k[len(prefix):]
	// 找最后一个 '.'
	lastDot := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '.' {
			lastDot = i
			break
		}
	}
	if lastDot < 0 {
		return nil, 0, false
	}
	vs := string(rest[lastDot+1:])
	if len(vs) != 20 {
		return nil, 0, false
	}
	var v int64
	for _, c := range vs {
		if c < '0' || c > '9' {
			return nil, 0, false
		}
		v = v*10 + int64(c-'0')
	}
	logical = make([]byte, lastDot)
	copy(logical, rest[:lastDot])
	return logical, v, true
}

// Dump 迁移状态导出文件格式（gob 序列化）
type Dump struct {
	// 旧链元数据（用于校验锚点与报告）
	Height    int64  `json:"height"`
	StateHash []byte `json:"stateHash,omitempty"`
	BlockTime int64  `json:"blockTime,omitempty"`
	ChainID   int32  `json:"chainId,omitempty"`
	// 全量最终状态（逻辑键 → 值；value=nil 表示该键无值/已删除）
	KVs []*types.KeyValue `json:"kvs,omitempty"`
}

// OutSet 转换输出：新链 store 状态键 + 本地 db 键
type OutSet struct {
	StateKVs   []*types.KeyValue `json:"stateKvs,omitempty"`
	LocalKVs   []*types.KeyValue `json:"localKvs,omitempty"`
	ReportText string            `json:"-"`
}

// RecommendPowLimitBits 按票数与目标出块时间解析计算 ticket 初始难度（精确公式，非模拟）。
//
// ticket 挖矿：currentdiff = CompactToBig(BigToCompact(sha256))，按哈希字节长度 b 分档：
//   b 字节哈希 → currentdiff ∈ [2^(8b-8), 2^(8b))；P(哈希字节长 b) = (1/256)^(32-b) × 255/256。
// target = CompactToBig(bits)（指数 e、mantissa m → target = m×2^(8(e-3))）：
//   P = Σ_{b<e} P(b) + P(b=e)×clamp((m-2^16)/(2^24-2^16), 0, 1)
// 出块时间 ≈ 1/(票数 × P)，反解 (e, m) 中最接近 targetSec 的。
func RecommendPowLimitBits(tickets int64, targetSec float64) (uint32, float64) {
	if tickets <= 0 {
		return 0x1f00ffff, 0
	}
	wantP := 1.0 / (float64(tickets) * targetSec)
	best := uint32(0)
	bestErr := 1e18
	bestBT := 0.0
	P := func(b uint32) float64 {
		p := 255.0 / 256.0
		for k := b + 1; k <= 32; k++ {
			p /= 256.0
		}
		return p
	}
	for e := uint32(31); e >= 20; e-- {
		pBelow := 0.0
		for b := uint32(0); b < e; b++ {
			pBelow += P(b)
		}
		pe := P(e)
		need := wantP - pBelow
		if need <= 0 {
			continue
		}
		frac := need / pe
		if frac > 1 {
			continue
		}
		m := uint32(0x010000) + uint32(frac*(0xff0000))
		if m > 0x7fffff {
			m = 0x7c0000
		}
		bits := (e << 24) | m
		pTotal := pBelow + pe*frac
		bt := 1.0 / (float64(tickets) * pTotal)
		err := bt - targetSec
		if err < 0 {
			err = -err
		}
		if err < bestErr {
			bestErr = err
			best = bits
			bestBT = bt
		}
	}
	if best == 0 {
		best = 0x1f00ffff
		bestBT = 1.0 / (float64(tickets) * 1.5e-5)
	}
	return best, bestBT
}
