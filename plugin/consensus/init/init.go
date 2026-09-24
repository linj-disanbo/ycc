package init

import (
	_ "github.com/33cn/plugin/plugin/consensus/rollup"   //auto gen
	_ "github.com/33cn/plugin/plugin/consensus/ticket"   //auto gen (v1.69.0+ 已适配 eth 地址签名，无需本地 fork)
	_ "github.com/yccproject/ycc/plugin/consensus/pos33" //auto gen
)
