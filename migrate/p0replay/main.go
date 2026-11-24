// p0replay：把 txpick 抽出的旧交易样本重放到新链节点，按预期判定通过/失败。
//
// 用法：
//
//	p0replay -samples samples.json -rpc http://127.0.0.1:8801 \
//	         -out replay-results.tsv [-only <class>] [-eth-rpc http://127.0.0.1:8547] [-wait-packed 20s]
//
// 判定规则（来自样本字段，不是硬编码链上状态）：
//
//	pos33                     → 期望拒绝（ErrExecNameNotAllow）
//	expireClass=time          → 期望拒绝（ErrTxExpire）
//	dust=true（coins amount1） → 期望**接受**（本次决策接受的剩余风险，记录为 INFO）
//	willIndex=true            → 期望拒绝（ErrTxDup）
//	其它                       → 仅记录，不判定
//
// 注意：Chain33.SendTransaction 的 jrpc 参数是 {"data": "<hex>"}（rpctypes.RawParm 的 json tag 是 data，
// 不是 tx）；用 "tx" 会被当成空串，解出零值交易并一律返回 ErrTxChainID。
//
// 另外对 rlpForm 样本：rawHex 解出的 evm payload 里 Note 即原始 RLP 交易；若给 -eth-rpc，
// 会再用该 RLP 走 eth_sendRawTransaction（模拟“重新组装提交”），期望被 nonce 拒。
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/33cn/chain33/types"
)

type sample struct {
	Class       string `json:"class"`
	Height      int64  `json:"height"`
	TxHash      string `json:"txhash"`
	RawHex      string `json:"rawHex"`
	Execer      string `json:"execer"`
	SignTy      int32  `json:"sigTy"`
	RLPForm     bool   `json:"rlpForm"`
	Nonce       int64  `json:"nonce"`
	Expire      int64  `json:"expire"`
	ExpireClass string `json:"expireClass"`
	TxHeight    int64  `json:"txHeight"`
	Amount      int64  `json:"amount"`
	Dust        bool   `json:"dust"`
	WillIndex   bool   `json:"willIndex"`
}

type samplesFile struct {
	SnapshotHeight int64    `json:"snapshotHeight"`
	Samples        []sample `json:"samples"`
}

type rpcReq struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  interface{}     `json:"error"`
}

type row struct {
	Class    string
	Height   int64
	TxHash   string
	Expected string
	Actual   string
	Verdict  string // PASS / FAIL / INFO / SKIP
	Note     string
}

func main() {
	samplesPath := flag.String("samples", "samples.json", "txpick 输出的样本 JSON")
	rpc := flag.String("rpc", "http://127.0.0.1:8801", "新链 jrpc 地址")
	ethRPC := flag.String("eth-rpc", "", "新链 eth rpc 地址（可选；用于 RLP 重新提交测试）")
	out := flag.String("out", "replay-results.tsv", "结果输出（TSV）")
	only := flag.String("only", "", "只跑指定 class（逗号分隔多个）")
	waitPacked := flag.Duration("wait-packed", 20*time.Second, "接受后等待打包的时间（0=不等）")
	dry := flag.Bool("dry", false, "只打印不发送")
	flag.Parse()

	b, err := os.ReadFile(*samplesPath)
	if err != nil {
		fatal("读取样本失败: %v", err)
	}
	var sf samplesFile
	if err := json.Unmarshal(b, &sf); err != nil {
		fatal("解析样本失败: %v", err)
	}
	if len(sf.Samples) == 0 {
		fatal("样本为空")
	}
	onlySet := map[string]bool{}
	if *only != "" {
		for _, c := range strings.Split(*only, ",") {
			onlySet[strings.TrimSpace(c)] = true
		}
	}
	fmt.Printf("样本文件: %s（快照高度 %d，%d 笔）\n新链 jrpc: %s\n\n", *samplesPath, sf.SnapshotHeight, len(sf.Samples), *rpc)

	var rows []row
	for _, s := range sf.Samples {
		if len(onlySet) > 0 && !onlySet[s.Class] {
			continue
		}
		exp, kind := expectation(s)
		r := row{Class: s.Class, Height: s.Height, TxHash: s.TxHash, Expected: exp}
		if *dry {
			r.Actual, r.Verdict, r.Note = "-", "SKIP", "dry-run"
			rows = append(rows, r)
			continue
		}
		res, rpcErr := callRPC(*rpc, "Chain33.SendTransaction", []interface{}{map[string]string{"data": s.RawHex}})
		switch {
		case rpcErr != nil || isErrResult(res):
			r.Actual = errText(res, rpcErr)
			r.Verdict = judgeReject(r.Actual, kind)
		default:
			txHash := extractString(res)
			r.Actual = "accepted"
			if kind == "accept" {
				r.Verdict = "INFO"
				r.Note = "被接受（dust 剩余风险的预期表现）"
			} else {
				r.Verdict = "FAIL"
				r.Note = "本应被拒绝"
			}
			if *waitPacked > 0 && txHash != "" {
				if h, ok := waitPackedTx(*rpc, txHash, *waitPacked); ok {
					r.Note += fmt.Sprintf("；已打包 height=%d", h)
				} else {
					r.Note += "；等待打包超时"
				}
			}
		}
		rows = append(rows, r)

		// eth RLP 重新提交（可选）
		if *ethRPC != "" && s.RLPForm {
			rawRLP := extractRLP(s.RawHex)
			if rawRLP == "" {
				rows = append(rows, row{Class: s.Class + "/rlp", Height: s.Height, TxHash: s.TxHash,
					Expected: "nonce 拒绝", Actual: "-", Verdict: "SKIP", Note: "payload 中无 RLP Note"})
			} else {
				res, rpcErr := callRPC(*ethRPC, "eth_sendRawTransaction", []interface{}{rawRLP})
				rr := row{Class: s.Class + "/rlp", Height: s.Height, TxHash: s.TxHash, Expected: "nonce 拒绝"}
				if rpcErr != nil || isErrResult(res) {
					rr.Actual = errText(res, rpcErr)
					if strings.Contains(strings.ToLower(rr.Actual), "nonce") {
						rr.Verdict = "PASS"
					} else {
						rr.Verdict = "INFO"
					}
				} else {
					rr.Actual = "accepted"
					rr.Verdict = "FAIL"
				}
				rows = append(rows, rr)
			}
		}
	}

	if err := writeTSV(*out, rows); err != nil {
		fatal("写结果失败: %v", err)
	}
	summarize(rows, *out)
}

// expectation 依据样本字段给出预期（reject 判定码 / accept）。
func expectation(s sample) (string, string) {
	switch {
	case s.Class == "pos33":
		return "拒绝：ErrExecNameNotAllow", "ErrExecNameNotAllow"
	case s.ExpireClass == "time":
		return "拒绝：ErrTxExpire", "ErrTxExpire"
	case s.Dust:
		return "接受（dust 被跳过，剩余风险）", "accept"
	case s.WillIndex:
		return "拒绝：ErrTxDup", "ErrTxDup"
	}
	return "未知（仅记录）", ""
}

func judgeReject(actual, kind string) string {
	switch kind {
	case "accept":
		return "FAIL" // 期望接受却拒绝了
	case "":
		return "INFO"
	}
	if strings.Contains(strings.ToLower(actual), strings.ToLower(kind)) {
		return "PASS"
	}
	// 兼容不同措辞
	if kind == "ErrTxDup" && strings.Contains(strings.ToLower(actual), "dup") {
		return "PASS"
	}
	return "FAIL"
}

// ---------- RPC ----------

var httpClient = &http.Client{Timeout: 15 * time.Second}

func callRPC(url, method string, params []interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rr rpcResp
	if err := json.Unmarshal(data, &rr); err != nil {
		return nil, fmt.Errorf("响应非 JSON: %s", truncate(string(data), 200))
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("%v", rr.Error)
	}
	return rr.Result, nil
}

func isErrResult(res json.RawMessage) bool {
	return len(res) == 0 || string(res) == "null"
}

func errText(res json.RawMessage, err error) string {
	if err != nil {
		return truncate(err.Error(), 300)
	}
	return "null result"
}

func extractString(res json.RawMessage) string {
	var s string
	if err := json.Unmarshal(res, &s); err == nil {
		return s
	}
	return ""
}

func waitPackedTx(url, txHash string, d time.Duration) (int64, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if res, err := callRPC(url, "Chain33.QueryTransaction", []interface{}{map[string]string{"hash": txHash}}); err == nil && !isErrResult(res) {
			var detail struct {
				Height int64 `json:"height"`
			}
			if json.Unmarshal(res, &detail) == nil && detail.Height >= 0 {
				return detail.Height, true
			}
		}
		time.Sleep(time.Second)
	}
	return 0, false
}

// extractRLP 从 rawHex 解出的 evm payload 里取 Note（原始 RLP 交易 hex）。
func extractRLP(rawHex string) string {
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return ""
	}
	var tx types.Transaction
	if err := types.Decode(raw, &tx); err != nil {
		return ""
	}
	var act types.EVMContractAction4Chain33
	if err := types.Decode(tx.Payload, &act); err != nil {
		return ""
	}
	return act.Note
}

// ---------- 输出 ----------

func writeTSV(path string, rows []row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, "class\theight\ttxhash\texpected\tactual\tverdict\tnote"); err != nil {
		return err
	}
	for _, r := range rows {
		act := strings.ReplaceAll(r.Actual, "\t", " ")
		act = strings.ReplaceAll(act, "\n", " ")
		note := strings.ReplaceAll(r.Note, "\t", " ")
		if _, err := fmt.Fprintf(f, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			r.Class, r.Height, r.TxHash, r.Expected, truncate(act, 200), r.Verdict, note); err != nil {
			return err
		}
	}
	return nil
}

func summarize(rows []row, out string) {
	cnt := map[string]int{}
	for _, r := range rows {
		cnt[r.Verdict]++
	}
	fmt.Printf("%-24s %-6s %s\n", "class", "结果", "说明")
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Class < rows[j].Class })
	for _, r := range rows {
		fmt.Printf("%-24s %-6s %-38s %s\n", r.Class, r.Verdict, truncate(r.Expected, 38), truncate(r.Actual+func() string {
			if r.Note != "" {
				return "；" + r.Note
			}
			return ""
		}(), 70))
	}
	fmt.Printf("\n合计: PASS=%d FAIL=%d INFO=%d SKIP=%d\n", cnt["PASS"], cnt["FAIL"], cnt["INFO"], cnt["SKIP"])
	fmt.Printf("详细结果: %s\n", out)
	if cnt["FAIL"] > 0 {
		os.Exit(2)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
