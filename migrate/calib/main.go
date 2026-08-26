// calib 工具：按票数与目标出块时间模拟标定 powLimitBits（ticket 挖矿初始难度）。
//
// 用法：
//
//	calib -tickets 406800 -targetSec 5
//
// 输出候选 bits 及对应出块时间，取最接近 targetSec 的值写入 ycc.go 的 powLimitBits。
// 说明：ticket 挖矿概率被 compact 难度量化，初始出块时间 ≈ 1/(票数×P)；
// 此后难度每 targetTimespan/targetTimePerBlock 块自动调整收敛到 targetTimePerBlock，
// powLimitBits 只影响初始窗口（真实链票数多，务必校准避免初始出块过快/过慢）。
package main

import (
	"flag"
	"fmt"

	"github.com/yccproject/ycc/migrate"
)

func main() {
	tickets := flag.Int("tickets", 1000, "迁移后 ticket 总数（transform 输出的票数）")
	targetSec := flag.Float64("targetSec", 5.0, "目标出块时间（秒）")
	flag.Parse()

	bits, bt := migrate.RecommendPowLimitBits(int64(*tickets), *targetSec)
	fmt.Printf("\n建议 powLimitBits = 0x%08x（预测出块 %.1fs）\n", bits, bt)
}
