package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/big"

	//"math/rand"
	"sync"
	"time"

	"github.com/lianxiangcloud/linkchain/libs/crypto"
	"github.com/lianxiangcloud/linkchain/libs/ser"

	"github.com/lianxiangcloud/linkchain/libs/hexutil"

	"github.com/lianxiangcloud/linkchain/accounts/keystore"
	"github.com/lianxiangcloud/linkchain/libs/common"
	flog "github.com/lianxiangcloud/linkchain/libs/log"
	"github.com/lianxiangcloud/linkchain/types"

	lktypes "github.com/lianxiangcloud/linkchain/libs/cryptonote/types"
)

var (
	rpcHttp = flag.String("rpc.fromhttp", "http://127.0.0.1:46005", "from链rpc地址，类似http://127.0.0.1:46005这样的RPC路径")
	// fromZoneID    = flag.Int("zone.fromid", 0, "zoneID选择该链账户作from发送测试交易 (default 0)")
	// toZoneID      = flag.Int("zone.toid", -1, "指定测试交易发往哪条to链 (default -1:可以发往所有zone)")
	// toZonerpcHttp = flag.String("rpc.tohttp", "", "to链rpc地址 类似http://127.0.0.1:8000这样的RPC路径")
	// zoneMax       = flag.Int("zone.max", 2, "zoneMax表示总共多少个链, 配合zoneID划分本链账号和非本链账号")
	localMax = flag.Int("max.local", 4, "最多用多少个本链账号发送测试交易, 这个值越大 并发发出的交易越多(如果所在机器CPU够的话)")
	//remoteMax     = flag.Int("max.remote", 0, "最多用多少个非本链账号发送测试交易, 暂时没用 (default 0)")
	//txMode        = flag.Int("tx.mode", 1, "0: 跨链;  1: 本链;  2: 本链和跨链; （表示to地址是否本链）")
	rawTx  = flag.Bool("tx.raw", true, "是否发送签名好的交易 (默认不是)")
	num    = flag.Uint64("num", 5000, "依照上面max.local选出的账号, 每个账户将发出该值指定的交易数")
	toMode = flag.Bool("to.single", false, "tx.mode不为2时是否只用一个to地址作测试 (默认用多个)")
	//logFix = flag.Uint64("log.fix", 0, "有时日志名冲突, 用这个配置解决")
	//logOk  = flag.Bool("log.ok", false, "是否输出交易发送ok的日志信息 (默认不输出)")
	// inputData     = flag.String("tx.input", "", "input data when calling a contract which can be constructed at geth console by typing: contract_instance.myMethod.getData(123)."+
	// 	"\n\tIt's in the form of \"0xd46300fd\", empty by default.")
	// contractRpcHttp = flag.String("rpc.contracthttp", "", "合约链rpc地址，类似http://127.0.0.1:8000这样的RPC路径")
	// contractAddr    = flag.String("contract.addr", "", "Contract address, when this parameter is presented, only send transactions to the given address.")
)

var (
	generalRawTxFmt  = `{"jsonrpc":"2.0","method":"eth_sendRawTx","params": ["%s", "%s"],"id":0}`
	txFmt            = `{"jsonrpc":"2.0","method":"eth_sendTransaction","params":[{"from": "%s", "to": "%s", "value": "0x%x", "nonce": "0x%x", "data": "%s"}],"id":1}`
	rawFmt           = `{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params": ["%s"],"id":0}`
	rawUTXOFmt       = `{"jsonrpc":"2.0","method":"eth_sendRawUTXOTransaction","params": ["%s"],"id":0}`
	nonceFmt         = `{"jsonrpc":"2.0","method":"eth_getTransactionCount","params": ["%s","latest"],"id":1}`
	balanceFmt       = `{"jsonrpc":"2.0","method":"eth_getBalance","params": ["%s","latest"],"id":1}`
	tkbalanceFmt     = `{"jsonrpc":"2.0","method":"eth_getTokenBalance","params": ["%s","latest","%s"],"id":1}`
	blockByNumberFmt = `{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x%x", false],"id":1}`
	blockNumberStr   = `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
	unlockFmt        = `{"jsonrpc":"2.0","method":"personal_unlockAccount","params":["%s","%s",%d],"id": 1}` //addr,pwd,time

	unlockDur   = 32 * 24 * 60 * 60
	gasPrice    = big.NewInt(1e11)
	gasLimit    = uint64(1e5)
	zeroAddr    = common.EmptyAddress
	amount1     = big.NewInt(1)
	amount1UTXO = big.NewInt(1000000e11)
	amount28, _ = big.NewInt(0).SetString("1000000000000000000000000000", 0)
	loopSum     = 0

	log = flog.Root()
)

type txInfo struct {
	from  string
	to    string
	nonce uint64
	tx    string
	raw   string
}

func main() {

	// Parse flags
	flag.Parse()

	// Generate Txs to channel
	if *rawTx {
		log.Info("sendRawTransaction")
		go buildRawTxAndSend(int(*num))
	} else {
		log.Info("sendTransaction") // not implement
		go buildRawTxAndSend(int(*num))
	}

	//startBlockNumber := getBlockNumber(*rpcHttp)
	for {
		endBlockNumber := getBlockNumber(*rpcHttp)
		startBlockNumber := endBlockNumber - 10
		if startBlockNumber < 1 {
			startBlockNumber = 1
		}
		countBlock(*rpcHttp, startBlockNumber, endBlockNumber) //统计from链所有交易发送后入链的时间和入链交易个数
		//startBlockNumber = endBlockNumber + 1
		time.Sleep(10 * time.Second)
	}
}

func genEtx(from *keystore.Key, to common.Address, nonce uint64, amount *big.Int, data []byte) (*types.Transaction, error) {
	tx := types.NewTransaction(nonce, to, amount, types.CalNewAmountGas(amount, types.EverLiankeFee), gasPrice, data)
	if err := tx.Sign(types.GlobalSTDSigner, from.PrivateKey); err != nil {
		return nil, err
	}
	return tx, nil
}

func genUTXOTx(from *keystore.Key, nonce uint64, amount *big.Int) (*types.UTXOTransaction, error) {
	skey := from.PrivateKey
	addr := crypto.PubkeyToAddress(skey.PublicKey)
	accountSource := &types.AccountSourceEntry{
		From:   addr,
		Nonce:  nonce,
		Amount: amount,
	}
	transferGas := types.CalNewAmountGas(amount, types.EverLiankeFee)
	transferFee := big.NewInt(0).Mul(big.NewInt(types.ParGasPrice), big.NewInt(0).SetUint64(transferGas))
	utxoDest := &types.UTXODestEntry{
		Addr:   lktypes.AccountAddress{},
		Amount: big.NewInt(0).Sub(amount, transferFee),
	}
	dest := []types.DestEntry{utxoDest}

	utxoTx, _, err := types.NewAinTransaction(accountSource, dest, common.EmptyAddress, transferFee, nil)
	if err != nil {
		return nil, err
	}

	err = utxoTx.Sign(types.GlobalSTDSigner, skey)
	if err != nil {
		return nil, err
	}

	return utxoTx, nil
}

func genUTXOTkTx(from *keystore.Key, nonce uint64, amount *big.Int, tkAddr common.Address) (*types.UTXOTransaction, error) {
	skey := from.PrivateKey
	addr := crypto.PubkeyToAddress(skey.PublicKey)
	accountSource := &types.AccountSourceEntry{
		From:   addr,
		Nonce:  nonce,
		Amount: amount,
	}
	utxoDest := &types.UTXODestEntry{
		Addr:   lktypes.AccountAddress{},
		Amount: amount,
	}
	dest := []types.DestEntry{utxoDest}

	utxoTx, _, err := types.NewAinTransaction(accountSource, dest, tkAddr, transferFee, nil)
	if err != nil {
		return nil, err
	}
	err = utxoTx.Sign(types.GlobalSTDSigner, skey)
	if err != nil {
		return nil, err
	}
	return utxoTx, nil
}

func genCreateContractTx(from *keystore.Key, nonce uint64) (*types.ContractCreateTx, common.Address, error) {

	bin, err := ioutil.ReadFile("./SimpleToken.bin")
	if err != nil {
		return nil, common.EmptyAddress, err
	}
	ccode := common.Hex2Bytes(string(bin))

	ccMainInfo := &types.ContractCreateMainInfo{
		FromAddr:     from.Address,
		AccountNonce: nonce,
		Amount:       big.NewInt(0),
		Payload:      ccode,
	}
	tx := types.CreateContractTx(ccMainInfo, nil)
	tx.Sign(types.GlobalSTDSigner, from.PrivateKey)
	addr := crypto.CreateAddress(tx.FromAddr, tx.Nonce(), tx.Data())
	return tx, addr, nil
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// A value of this type can a JSON-RPC request, notification, successful response or
// error response. Which one it is depends on the fields.
type jsonrpcMessage struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *jsonError      `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func buildRawTxAndSend(txNum int) {

	loopSum = len(accountsOfLocal) * txNum
	log.Info("buildRawTx: start", "accounts", len(accountsOfLocal), "totalTxs: ", loopSum)
	sTime := time.Now()

	for i := 0; i < len(accountsOfLocal); i++ {
		from := accountsOfLocal[i]
		fromHex := from.Address.Hex()
		log.Info("buildRawTx: addr", "from", fromHex, "idx", i)
	}
	toKss := buildToKss()
	var inputDataBytes = []byte{}

	var wg sync.WaitGroup
	for i := 0; i < len(accountsOfLocal); i++ {
		wg.Add(1)
		go func(idx int) {
			var addr common.Address
			from := accountsOfLocal[idx]
			start := uint64(0) //这里要保证accountsOfLocal里没有重复的账号
			end := start + uint64(txNum)
			for nonce := start; nonce < end; nonce++ {
				to := toKss[int(nonce)%len(toKss)]
				var etx types.Tx
				var err error
				if nonce == 0 {
					etx, addr, err = genCreateContractTx(from, nonce)
				} else {
					switch idx {
					case 0:
						etx, err = genUTXOTx(from, nonce, amount1UTXO)
					case 1:
						etx, err = genUTXOTkTx(from, nonce, amount1, addr)
					default:
						etx, err = genEtx(from, to.Address, nonce, amount1, inputDataBytes)
					}
				}
				if err != nil {
					log.Error("buildRawTx: genTx failed", "idx", idx, "err", err)
				}
			Resend:
				jsonErr, err := sendTx(etx)
				if jsonErr != nil {
					if jsonErr.Code == -3014 {
						//log.Warn("benchSend: full", "nonce", nonce, "code", jsonErr.Code, "msg", jsonErr.Message)
						time.Sleep(5 * time.Second)
						goto Resend
					} else {
						log.Warn("benchSend: careful", "nonce", nonce, "code", jsonErr.Code, "msg", jsonErr.Message)
						time.Sleep(1 * time.Second)
						goto Resend
					}
				}
				if nonce == 0 {
					time.Sleep(15 * time.Second) // cct in block
				}
				if idx == 1 {
					tkb := getTkBalance(from, addr)
					log.Debug("tkBalance", "balance", tkb, "addr", addr)
				}
			}
			wg.Done()
		}(i)
	}
	wg.Wait()
	used := time.Now().Sub(sTime)
	nsop := float64(used.Nanoseconds()) / float64(loopSum)
	log.Info("buildRawTx: done", "used", used, "nsop", nsop, "ops", float64(time.Second.Nanoseconds())/nsop)
}

func buildToKss() []ks {
	for i := 0; i < len(toAddrOfLocal); i++ {
		log.Info("buildTx: addr", "to", toAddrOfLocal[i].addr, "idx", i)
	}
	return toAddrOfLocal
}

func countBlock(url string, start int64, end int64) int64 {
	blocks := make([]*Block, 0)
	txs := 0
	startBlockNumber := int64(0)
	endBlockNumber := int64(0)
	startTime := 0
	endTime := 0
	for i := start; i <= end; i++ {
		b := getBlockByNumber(url, i)
		txs += b.Txs + b.CrossTxs
		if b.Txs != 0 || b.CrossTxs != 0 {
			endBlockNumber = i
			endTime = int(b.Timestamp)
			if startBlockNumber == 0 {
				startBlockNumber = i
				startTime = int(b.Timestamp)
			}
		}
		blocks = append(blocks, b)
	}
	second := endTime - startTime
	log.Info("countBlock: ", "url", url, "start", startBlockNumber, "end", endBlockNumber, "seconds", second, "txs", txs, "speed", float64(txs)/float64(second))
	return endBlockNumber
}

func sendTx(tx types.Tx) (jsonErr *jsonError, err error) {
	buf := bytes.NewBuffer(nil)
	err = ser.Encode(buf, tx)
	if err != nil {
		log.Error("sendTx: EncodeRLP failed", "tx", tx, "err", err)
		return
	}
	raw := hexutil.Encode(buf.Bytes())
	txtype := tx.TypeName()
	fmtTx := fmt.Sprintf(generalRawTxFmt, raw, txtype)
	ret, err := Post(*rpcHttp, fmtTx)
	if err != nil {
		log.Warn("sendTx: Post failed", "tx", tx, "err", err)
		return
	}
	var respmsg jsonrpcMessage
	if err = json.Unmarshal(ret, &respmsg); err != nil {
		log.Error("sendTx: Unmarshal failed", "tx", tx, "err", err)
		return

	}
	jsonErr = respmsg.Error
	return

}
