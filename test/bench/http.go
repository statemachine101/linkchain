package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lianxiangcloud/linkchain/accounts/keystore"
	"github.com/lianxiangcloud/linkchain/rpc/rtypes"
	"github.com/lianxiangcloud/linkchain/libs/common"
)

type Block struct {
	Number    int64
	Timestamp int64
	Txs       int
	CrossTxs  int
}

var (
	gTransport = &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		DisableCompression:    true,
		DisableKeepAlives:     false,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       2 * time.Minute,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
	}

	gHttpClient = &http.Client{
		Transport: gTransport,
	}
)
func init() {
	resp, err := gHttpClient.Post(`http://127.0.0.1:46005`, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":"0","method":"eth_getBalance","params":["0x08085a83232c4a3c2f9065f5bc1d93845fe8a4b5","latest"]}`))
	if err != nil {fmt.Println(err)}
	fmt.Println(resp)
}
func Post(url string, reqBody string) ([]byte, error) {
	resp, err := gHttpClient.Post(url, "application/json", strings.NewReader(reqBody))
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("POST: err=%v", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("StatusCode %d, Resp %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func getBlockByNumber(url string, num int64) *Block {
	ret, err := Post(url, fmt.Sprintf(blockByNumberFmt, num))
	if err != nil {
		panic(err)
	}
	result := &struct {
		Result struct {
			Number       string
			Timestamp    string
			Transactions rtypes.Txs
			LenCrossIn   int
		}
	}{}
	if err := json.Unmarshal(ret, result); err != nil {
		panic(err)
	}
	number, err := strconv.ParseInt(result.Result.Number, 0, 64)
	if err != nil {
		panic(fmt.Errorf("url:%s, %s not a number", url, result.Result.Number))
	}
	timestamp, err := strconv.ParseInt(result.Result.Timestamp, 0, 64)
	if err != nil {
		panic(fmt.Errorf("url:%s, %s not a number", url, result.Result.Timestamp))
	}

	block := &Block{
		Number:    number,
		Timestamp: timestamp,
		Txs:       len(result.Result.Transactions),
		CrossTxs:  int(result.Result.LenCrossIn),
	}
	//log.Info("getBlockByNumber: ", "num",num,"len(Txs)",block.Txs,"len(block.CrossTxs)", block.CrossTxs)
	return block
}

func getBlockNumber(url string) int64 {
	ret, err := Post(url, blockNumberStr)
	if err != nil {
		panic(err)
	}
	result := &struct {
		Result string
	}{}
	if err := json.Unmarshal(ret, result); err != nil {
		panic(err)
	}
	num, err := strconv.ParseInt(result.Result, 0, 64)
	if err != nil {
		panic(fmt.Errorf("%s not a number", result.Result))
	}
	return num
}

func getBalances() []*big.Int {
	balances := make([]*big.Int, 0, len(accountsOfLocal))
	for _, a := range accountsOfLocal {
		balance := getBalance(a)
		balances = append(balances, balance)
	}
	return balances
}

func getBalance(a *keystore.Key) *big.Int {
	ret, err := Post(*rpcHttp, fmt.Sprintf(balanceFmt, a.Address.Hex()))
	if err != nil {
		panic(err)
	}
	result := &struct {
		Result string
	}{}
	if err := json.Unmarshal(ret, result); err != nil {
		panic(err)
	}
	balance, ok := big.NewInt(0).SetString(result.Result, 0)
	if !ok {
		panic(fmt.Errorf("%s not a balance", result.Result))
	}
	log.Info("getBalance: ", "addr", a.Address, "balance", balance)
	return balance
}

func getTkBalance(a *keystore.Key,addr common.Address) *big.Int {
	ret, err := Post(*rpcHttp, fmt.Sprintf(tkbalanceFmt, a.Address.Hex(),addr.Hex()))
	if err != nil {
		panic(err)
	}
	result := &struct {
		Result string
	}{}
	if err := json.Unmarshal(ret, result); err != nil {
		panic(err)
	}
	tkbalance, ok := big.NewInt(0).SetString(result.Result, 0)
	if !ok {
		panic(fmt.Errorf("%s not a balance", result.Result))
	}
	log.Info("getTkBalance: ", "addr", a.Address, "tkbalance", tkbalance)
	return tkbalance
}

func getNonces() []uint64 {
	nonces := make([]uint64, 0, len(accountsOfLocal))
	for _, a := range accountsOfLocal {
		nonce := getNonce(a)
		nonces = append(nonces, nonce)
	}
	return nonces
}

func getNonce(a *keystore.Key) uint64 {
	ret, err := Post(*rpcHttp, fmt.Sprintf(nonceFmt, a.Address.Hex()))
	if err != nil {
		panic(err)
	}
	result := &struct {
		Result string
	}{}
	if err := json.Unmarshal(ret, result); err != nil {
		panic(err)
	}
	nonce, err := strconv.ParseUint(result.Result, 0, 64)
	if err != nil {
		panic(err)
	}
	log.Info("getNonce: ", "addr", a.Address, "nonce", nonce)
	return nonce
}
