package app

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"math/big"
	"testing"
	"time"

	"github.com/lianxiangcloud/linkchain/accounts/abi"
	"github.com/lianxiangcloud/linkchain/accounts/keystore"
	common "github.com/lianxiangcloud/linkchain/libs/common"
	"github.com/lianxiangcloud/linkchain/libs/crypto"
	"github.com/lianxiangcloud/linkchain/libs/cryptonote/ringct"
	types2 "github.com/lianxiangcloud/linkchain/libs/cryptonote/types"
	"github.com/lianxiangcloud/linkchain/libs/cryptonote/xcrypto"
	"github.com/lianxiangcloud/linkchain/libs/log"
	"github.com/lianxiangcloud/linkchain/libs/ser"
	"github.com/lianxiangcloud/linkchain/state"
	types "github.com/lianxiangcloud/linkchain/types"
	"github.com/lianxiangcloud/linkchain/vm/evm"
	"github.com/lianxiangcloud/linkchain/wallet/wallet"
	"github.com/stretchr/testify/assert"
)

var (
	Bank  *keystore.Key
	State *state.StateDB
	SP    *StateProcessor
	VC    evm.Config
	APP   *LinkApplication
)

func init() {
	Bank = accounts[0]
	State = newTestState()
	APP, _ = initApp()
	SP = NewStateProcessor(nil, APP)
	APP.processor = SP
	VC = evm.Config{EnablePreimageRecording: false}
	types.SaveBalanceRecord = true
}

func balancesChecker(t *testing.T, beforeBalanceIn, afterBalanceIn, beforeBalanceOut, afterBalanceOut []*big.Int, expectAmount, expectFee, actualFee *big.Int) {
	fmt.Println(beforeBalanceIn, afterBalanceIn, beforeBalanceOut, afterBalanceOut, expectAmount, expectFee, actualFee)
	// Sanity Check
	ins := len(beforeBalanceIn)
	for _, list := range [][]*big.Int{beforeBalanceIn, afterBalanceIn} {
		assert.Equal(t, ins, len(list))
	}
	outs := len(beforeBalanceIn)
	for _, list := range [][]*big.Int{beforeBalanceOut, afterBalanceOut} {
		assert.Equal(t, outs, len(list))
	}
	// Legality Check
	amountlist := make([]*big.Int, 0)
	for _, list := range [][]*big.Int{beforeBalanceIn, afterBalanceIn, beforeBalanceOut, afterBalanceOut} {
		amountlist = append(amountlist, list...)
	}
	for _, item := range []*big.Int{expectAmount, expectFee, actualFee} {
		amountlist = append(amountlist, item)
	}
	for _, amount := range amountlist {
		assert.True(t, amount.Sign() >= 0)
	}
	// Equality Check
	//delta(input) = fee + amount
	sumin := big.NewInt(0)
	for i := 0; i < ins; i++ {
		d := big.NewInt(0).Sub(beforeBalanceIn[i], afterBalanceIn[i])
		sumin = big.NewInt(0).Add(sumin, d)
	}
	assert.Equal(t, big.NewInt(0).Add(expectAmount, expectFee), sumin)
	//delta(output) = amount
	sumout := big.NewInt(0)
	for i := 0; i < outs; i++ {
		d := big.NewInt(0).Sub(afterBalanceOut[i], beforeBalanceOut[i])
		sumout = big.NewInt(0).Add(sumout, d)
	}
	assert.Equal(t, expectAmount, sumout)
	//fee=fee
	assert.Equal(t, expectFee, actualFee)
}

func lengthChecker(t *testing.T, receipts types.Receipts, utxoOutputs []*types.UTXOOutputData, keyImages []*types2.Key, expReceiptsLen, expUtxoOutputsLen, expKeyImageLen int) {
	// Sanity Check
	assert.True(t, expReceiptsLen >= 0)
	assert.True(t, expUtxoOutputsLen >= 0)
	assert.True(t, expKeyImageLen >= 0)
	// Length Check
	assert.Equal(t, expReceiptsLen, len(receipts))
	assert.Equal(t, expUtxoOutputsLen, len(utxoOutputs))
	assert.Equal(t, expKeyImageLen, len(keyImages))
}

func genBlock(txs types.Txs) *types.Block {
	block := &types.Block{
		Header: &types.Header{
			Height:     1,
			Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
			Time:       uint64(time.Now().Unix()),
			NumTxs:     uint64(len(txs)),
			TotalTxs:   uint64(len(txs)),
			ParentHash: common.EmptyHash,
			GasLimit:   1e19,
		},
		Data: &types.Data{
			Txs: txs,
		},
	}
	return block
}

func getBalance(tx *types.UTXOTransaction, skv, sks types2.SecretKey) (amount *big.Int) {
	amount = big.NewInt(-1) // if no input matched, return -1
	//gen acc & kI
	acc := types2.AccountKey{
		Addr: types2.AccountAddress{
			SpendPublicKey: types2.PublicKey(ringct.ScalarmultBase(types2.Key(sks))),
			ViewPublicKey:  types2.PublicKey(ringct.ScalarmultBase(types2.Key(skv))),
		},
		SpendSKey: sks,
		ViewSKey:  skv,
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc, uint64(0))
	acc.Address = address
	keyi := make(map[types2.PublicKey]uint64)
	keyi[acc.Addr.SpendPublicKey] = 0
	// output
	outputID := -1
	outputCnt := len(tx.Outputs)
	for i := 0; i < outputCnt; i++ {
		o := tx.Outputs[i]
		switch ro := o.(type) {
		case *types.UTXOOutput:
			outputID++
			keyMaps := make(map[types2.KeyDerivation]types2.PublicKey, 0)
			derivationKeys := make([]types2.KeyDerivation, 0)
			derivationKey, err := xcrypto.GenerateKeyDerivation(tx.RKey, skv)
			if err != nil {
				log.Error("GenerateKeyDerivation fail", "rkey", tx.RKey, "err", err)
				continue
			}
			derivationKeys = append(derivationKeys, derivationKey)
			keyMaps[derivationKey] = tx.RKey
			if len(tx.AddKeys) > 0 {
				//we use a addinational key for utxo->account proof, maybe cause err here
				for _, addkey := range tx.AddKeys {
					derivationKey, err = xcrypto.GenerateKeyDerivation(addkey, skv)
					if err != nil {
						log.Info("GenerateKeyDerivation fail", "addkey", addkey, "err", err)
						continue
					}
					derivationKeys = append(derivationKeys, derivationKey)
					keyMaps[derivationKey] = addkey
				}
			}
			recIdx := uint64(outputID)
			realDeriKey, _, err := types.IsOutputBelongToAccount(&acc, keyi, ro.OTAddr, derivationKeys, recIdx)
			if err != nil {
				// trivial error for multi output tx
				//log.Info("IsOutputBelongToAccount fail", "ro.OTAddr", ro.OTAddr, "derivationKey", derivationKey, "recIdx", recIdx, "err", err)
				continue
			}
			ecdh := &types2.EcdhTuple{
				Mask:   tx.RCTSig.RctSigBase.EcdhInfo[outputID].Mask,
				Amount: tx.RCTSig.RctSigBase.EcdhInfo[outputID].Amount,
			}
			log.Debug("GenerateKeyDerivation", "derivationKey", realDeriKey, "amount", tx.RCTSig.RctSigBase.EcdhInfo[outputID].Amount)
			scalar, err := xcrypto.DerivationToScalar(realDeriKey, outputID)
			if err != nil {
				log.Error("DerivationToScalar fail", "derivationKey", realDeriKey, "outputID", outputID, "err", err)
				continue
			}
			ok := xcrypto.EcdhDecode(ecdh, types2.Key(scalar), false)
			if !ok {
				log.Error("EcdhDecode fail", "err", err)
				continue
			}
			amount = big.NewInt(0).Mul(types.Hash2BigInt(ecdh.Amount), big.NewInt(types.GetUtxoCommitmentChangeRate(tx.TokenID)))
		default:
		}
	}
	return
}

//*********** Account Based Transactions Test **********
//tx
func TestAccount2Account(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey
	receiver, _ := crypto.GenerateKey()
	sAdd := crypto.PubkeyToAddress(sender.PublicKey)
	rAdd := crypto.PubkeyToAddress(receiver.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	nonce := state.GetNonce(sAdd)
	tx := types.NewTransaction(nonce, rAdd, amount, fee, gasPrice, nil)
	tx.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
}

//tx2(to Contract)
func TestAccount2Contract(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	amount := big.NewInt(0)
	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	log.Debug("from", "tokenAddress", tkAdd)
	//var cabi abi.ABI
	bin, err := ioutil.ReadFile("../test/token/sol/SimpleToken.abi")
	if err != nil {
		panic(err)
	}
	cabi, err := abi.JSON(bytes.NewReader(bin))
	if err != nil {
		panic(err)
	}
	var data []byte
	method := "transfertokentest"
	data, err = cabi.Pack(method, big.NewInt(0))
	if err != nil {
		panic(err)
	}

	fee := uint64(31539)
	tx2 := types.NewTransaction(1, tkAdd, big.NewInt(0), fee, gasPrice, data)
	tx2.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	assert.Equal(t, uint64(2), state.GetNonce(sAdd))
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	log.Debug("test final", "receipts0", *receipts[0], "receipts1", *receipts[1], "utxoOutputs", utxoOutputs, "keyImages", keyImages, "BAL", *types.BlockBalanceRecordsInstance, "record0", *(*types.BlockBalanceRecordsInstance).TxRecords[0], "record1", *(*types.BlockBalanceRecordsInstance).TxRecords[1])

}

//tx3(to Contract & Value transfer)
func TestAccount2Contract2(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}

	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/t.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())

	//var cabi abi.ABI
	bin, err := ioutil.ReadFile("../test/token/sol/t.abi")
	if err != nil {
		panic(err)
	}
	cabi, err := abi.JSON(bytes.NewReader(bin))
	if err != nil {
		panic(err)
	}
	var data []byte
	method := "set"
	data, err = cabi.Pack(method, big.NewInt(0))
	if err != nil {
		panic(err)
	}
	magic_number := uint64(26596)
	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee := types.CalNewAmountGas(amount, types.EverContractLiankeFee) + magic_number
	tx2 := types.NewTransaction(1, tkAdd, amount, fee, gasPrice, data)
	tx2.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}

	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	fmt.Println(fee, actualFee)
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)

}

//txt
func TestAccount2AccountToken(t *testing.T) {

	state := State.Copy()
	sender := Bank.PrivateKey
	receiver, _ := crypto.GenerateKey()
	sAdd := crypto.PubkeyToAddress(sender.PublicKey)
	rAdd := crypto.PubkeyToAddress(receiver.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	amount := big.NewInt(0)
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	tx1 := genContractCreateTx(sAdd, 0, 0, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())

	tx2 := types.NewTokenTransaction(tkAdd, 1, rAdd, big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100)), fee, gasPrice, nil)
	tx2.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)

}

//cct
func TestContractCreation(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	amount := big.NewInt(0)
	tx := genContractCreateTx(accounts[0].Address, 1000000, 0, "../test/token/sol/SimpleToken.bin")
	tx.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectFee := big.NewInt(0)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
}

//cct2
// func TestContractCreationBySendToEmptyAddress(t *testing.T) {
// 	state := State.Copy()
// 	sender := Bank.PrivateKey
// 	sAdd := crypto.PubkeyToAddress(sender.PublicKey)
// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	bfBalanceOut := []*big.Int{big.NewInt(0)}
// 	var ccode []byte
// 	bin, err := ioutil.ReadFile("../test/token/sol/SimpleToken.bin")
// 	if err != nil {
// 		panic(err)
// 	}
// 	ccode = common.Hex2Bytes(string(bin))
// 	amount := big.NewInt(0)
// 	fee := uint64(1494617)
// 	tx := types.NewContractCreation(0, amount, fee, gasPrice, ccode)
// 	tx.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(sAdd, tx.Nonce(), tx.Data())
// 	block := genBlock(types.Txs{tx})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}
// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}
// 	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
// 	lengthChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
// }

//cut
func TestContractUpdate(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	amount := big.NewInt(0)

	tx1 := genContractCreateTx(accounts[0].Address, 0, 0, "../test/token/tcvm/TestToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	contractAddr := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	tx2 := genContractUpgradeTx(tx1.FromAddr, contractAddr, tx1.Nonce()+1, "../test/token/tcvm/TestToken.bin")
	tx2.Sign(types.GlobalSTDSigner, sender)

	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectFee := big.NewInt(0)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
}

//*********** UTXO Based Transactions Test **********

//A->A
func TestSingleAccount2SingleAccount(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey
	receiver, _ := crypto.GenerateKey()
	sAdd := crypto.PubkeyToAddress(sender.PublicKey)
	rAdd := crypto.PubkeyToAddress(receiver.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := state.GetNonce(sAdd)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount,
	}
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)

	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0).Sub(amount, expectFee)
	aout := types.AccountDestEntry{
		To:     rAdd,
		Amount: amount,
		Data:   nil,
	}
	tx, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&aout}, common.EmptyAddress, expectFee, nil)
	if err != nil {
		panic(err)
	}
	tx.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
}

func TestSingleAccount2Contract(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}

	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	expectFee1 := big.NewInt(0)
	fmt.Println("#############", tkAdd)
	//var cabi abi.ABI
	bin, err := ioutil.ReadFile("../test/token/sol/SimpleToken.abi")
	if err != nil {
		panic(err)
	}
	cabi, err := abi.JSON(bytes.NewReader(bin))
	if err != nil {
		panic(err)
	}
	var data []byte
	method := "transfertokentest"
	data, err = cabi.Pack(method, big.NewInt(0))
	if err != nil {
		panic(err)
	}

	fee2 := uint64(31539)
	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
	amount := expectFee2

	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  1,
		Amount: amount,
	}

	aout := &types.AccountDestEntry{
		To:     tkAdd,
		Amount: big.NewInt(0),
		Data:   data,
	}

	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{aout}, common.EmptyAddress, expectFee2, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println("#######", tx2)
	tx2.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectFee := big.NewInt(0).Add(expectFee1, expectFee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0)

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
}

//A->U+
func TestSingleAccount2MulitipleUTXO(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := state.GetNonce(sAdd)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount,
	}
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0).Sub(amount, expectFee)
	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50))
	amount2 := big.NewInt(0).Sub(amount, amount1)

	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx, rSeckey, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(rSeckey)
	tx.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	afBalanceOut := []*big.Int{getBalance(tx, types2.SecretKey(skv1), types2.SecretKey(sks1)), getBalance(tx, types2.SecretKey(skv2), types2.SecretKey(sks2))}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 1, 2, 0)
}

func TestSingleAccount2MulitipleUTXOToken(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey
	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	amount := big.NewInt(0)
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	tx1 := genContractCreateTx(sAdd, 0, 0, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	bfTkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd), big.NewInt(0)}
	bfTkBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}

	amount = big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(1))
	nonce := uint64(1)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount,
	}
	fee = types.CalNewAmountGas(big.NewInt(0), types.EverLiankeFee)
	expectFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	//amount = big.NewInt(0).Sub(amount, expectFee)
	amount1 := big.NewInt(0).Mul(big.NewInt(1e17), big.NewInt(5))
	amount2 := big.NewInt(0).Sub(amount, amount1)
	//amount2 = big.NewInt(0).Add(amount2, big.NewInt(1))
	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, tkAdd, expectFee, nil)
	if err != nil {
		log.Root().Debug("Gen tx err", "err", err)

	}
	fmt.Println(tx2.String())
	tx2.Sign(types.GlobalSTDSigner, sender)
	block := genBlock(types.Txs{tx1, tx2})

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	afBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	afTkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd), big.NewInt(0)}
	afTkBalanceOut := []*big.Int{getBalance(tx2, types2.SecretKey(skv1), types2.SecretKey(sks1)), getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2))}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, big.NewInt(0), expectFee, actualFee)
	balancesChecker(t, bfTkBalanceIn, afTkBalanceIn, bfTkBalanceOut, afTkBalanceOut, amount, big.NewInt(0), big.NewInt(0))
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 2, 0)
}

//U->A
func TestSingleUTXO2Account(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := state.GetNonce(sAdd)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount,
	}
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	expectFee1 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0).Sub(amount, expectFee1)
	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50))
	amount2 := big.NewInt(0).Sub(amount, amount1)

	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx1, rSeckey, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee1, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(rSeckey)
	tx1.Sign(types.GlobalSTDSigner, sender)
	fmt.Println(tx1)
	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx1.Outputs[0].(*types.UTXOOutput).OTAddr,
			Commit: tx1.Outputs[0].(*types.UTXOOutput).Remark,
		}},
		RingIndex: 0,
		RKey:      tx1.RKey,
		OutIndex:  0,
		Amount:    big.NewInt(0).Set(amount1),
		Mask:      tx1.RCTSig.RctSigBase.EcdhInfo[0].Mask,
	}

	acc1 := types2.AccountKey{
		Addr: types2.AccountAddress{
			SpendPublicKey: types2.PublicKey(pks1),
			ViewPublicKey:  types2.PublicKey(pkv1),
		},
		SpendSKey: types2.SecretKey(sks1),
		ViewSKey:  types2.SecretKey(skv1),
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc1, uint64(0))
	acc1.Address = address
	keyi1 := make(map[types2.PublicKey]uint64)
	keyi1[acc1.Addr.SpendPublicKey] = 0

	amount = amount1
	fee = types.CalNewAmountGas(amount, types.EverLiankeFee)
	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0).Sub(amount, expectFee2)

	aDest := &types.AccountDestEntry{
		To:     sAdd,
		Amount: amount,
	}

	tx2, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest}, common.EmptyAddress, common.EmptyAddress, expectFee2, []byte{})
	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest}, mk)
	if err != nil {
		panic(err)
	}

	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{getBalance(tx1, types2.SecretKey(skv2), types2.SecretKey(sks2))}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	amount = amount2
	expectFee := big.NewInt(0).Add(expectFee1, expectFee2)
	println(bfBalanceIn[0].String(), afBalanceIn[0].String(), bfBalanceOut[0].String(), afBalanceOut[0].String(), amount.String(), expectFee.String(), actualFee.String())
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 2, 1)
}

//U->M
func TestSingleUTXO2Mix(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := state.GetNonce(sAdd)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount,
	}
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	expectFee1 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(90))
	amount2 := big.NewInt(0).Sub(big.NewInt(0).Sub(amount, amount1), expectFee1)

	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx1, rSeckey, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee1, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(rSeckey)
	tx1.Sign(types.GlobalSTDSigner, sender)
	fmt.Println(tx1)
	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx1.Outputs[0].(*types.UTXOOutput).OTAddr,
			Commit: tx1.Outputs[0].(*types.UTXOOutput).Remark,
		}},
		RingIndex: 0,
		RKey:      tx1.RKey,
		OutIndex:  0,
		Amount:    big.NewInt(0).Set(amount1),
		Mask:      tx1.RCTSig.RctSigBase.EcdhInfo[0].Mask,
	}

	acc1 := types2.AccountKey{
		Addr: types2.AccountAddress{
			SpendPublicKey: types2.PublicKey(pks1),
			ViewPublicKey:  types2.PublicKey(pkv1),
		},
		SpendSKey: types2.SecretKey(sks1),
		ViewSKey:  types2.SecretKey(skv1),
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc1, uint64(0))
	acc1.Address = address
	keyi1 := make(map[types2.PublicKey]uint64)
	keyi1[acc1.Addr.SpendPublicKey] = 0

	amount = amount1
	amount3 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(10))
	fee = types.CalNewAmountGas(amount3, types.EverLiankeFee) + uint64(5e8)
	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount4 := big.NewInt(0).Sub(big.NewInt(0).Sub(amount, amount3), expectFee2)
	aDest := &types.AccountDestEntry{
		To:     sAdd,
		Amount: amount3,
	}
	uDest := &types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount4,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx2, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest, uDest}, common.EmptyAddress, common.EmptyAddress, expectFee2, []byte{})
	if err != nil {
		panic(err)
	}
	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest, uDest}, mk)
	if err != nil {
		panic(err)
	}

	block := genBlock(types.Txs{tx1, tx2})
	tx11, _ := ser.EncodeToBytes(tx1)
	tx111 := hex.EncodeToString(tx11)
	tx22, _ := ser.EncodeToBytes(tx2)
	tx222 := hex.EncodeToString(tx22)
	fmt.Println("\n\n", tx111, "\n", tx222)

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	balanceO1 := getBalance(tx1, types2.SecretKey(skv2), types2.SecretKey(sks2))
	balanceO2 := getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2))
	afBalanceOut := []*big.Int{big.NewInt(0).Add(balanceO1, balanceO2)}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	amount = big.NewInt(0).Add(amount2, amount4)
	expectFee := big.NewInt(0).Add(expectFee1, expectFee2)
	println(amount.String(), expectFee1.String(), expectFee2.String(), actualFee.String())
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 2, 3, 1)
	fmt.Println("#####################")
	log.Debug("test final", "receipts0", *receipts[0], "receipts1", *receipts[1], "utxoOutputs", utxoOutputs, "keyImages", keyImages, "BAL", *types.BlockBalanceRecordsInstance, "record0", *(*types.BlockBalanceRecordsInstance).TxRecords[0], "record1", *(*types.BlockBalanceRecordsInstance).TxRecords[1])
}

func TestSingleUTXO2MixToken(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	tx1 := genContractCreateTx(sAdd, 0, 0, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())

	expectFeeSingle := big.NewInt(0).Mul(big.NewInt(0).SetUint64(types.CalNewAmountGas(big.NewInt(0), types.EverLiankeFee)), big.NewInt(types.ParGasPrice))

	amount := big.NewInt(10000)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  1,
		Amount: amount,
	}
	amount1 := big.NewInt(9999)
	amount2 := big.NewInt(1)

	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, tkAdd, expectFeeSingle, nil)
	if err != nil {
		panic(err)
	}
	tx2.Sign(types.GlobalSTDSigner, sender)

	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx2.Outputs[0].(*types.UTXOOutput).OTAddr,
		}},
		RingIndex: 0,
		RKey:      tx2.RKey,
		OutIndex:  0,
		Amount:    big.NewInt(0).Set(amount1),
	}

	acc1 := types2.AccountKey{
		Addr: types2.AccountAddress{
			SpendPublicKey: types2.PublicKey(pks1),
			ViewPublicKey:  types2.PublicKey(pkv1),
		},
		SpendSKey: types2.SecretKey(sks1),
		ViewSKey:  types2.SecretKey(skv1),
		SubIdx:    uint64(0),
	}

	amount3 := big.NewInt(3000)
	amount4 := big.NewInt(0).Sub(amount1, amount3)
	expectFee2 := big.NewInt(0).Add(expectFeeSingle, big.NewInt(0).Mul(big.NewInt(0).SetUint64(5e8), big.NewInt(types.ParGasPrice)))
	aDest := &types.AccountDestEntry{
		To:     sAdd,
		Amount: amount3,
	}
	uDest := &types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount4,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	address := wallet.AddressToStr(&acc1, uint64(0))
	acc1.Address = address
	keyi1 := make(map[types2.PublicKey]uint64)
	keyi1[acc1.Addr.SpendPublicKey] = 0

	tx3, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest, uDest}, tkAdd, common.EmptyAddress, expectFee2, []byte{})
	if err != nil {
		panic(err)
	}
	tx3.Sign(types.GlobalSTDSigner, sender)
	err = types.UInTransWithRctSig(tx3, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest, uDest}, mk)
	if err != nil {
		panic(err)
	}

	block := genBlock(types.Txs{tx1, tx2, tx3})

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	bfTkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd), big.NewInt(0)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	bfTkBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}

	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	afTkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd), big.NewInt(0)}
	afBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	balanceO1 := getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2))
	balanceO2 := getBalance(tx3, types2.SecretKey(skv2), types2.SecretKey(sks2))
	afTkBalanceOut := []*big.Int{balanceO1, balanceO2}

	tkAmount := big.NewInt(0).Add(amount2, amount4)

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectFee := big.NewInt(0).Add(expectFeeSingle, expectFee2)

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, big.NewInt(0), expectFee, actualFee)
	balancesChecker(t, bfTkBalanceIn, afTkBalanceIn, bfTkBalanceOut, afTkBalanceOut, tkAmount, big.NewInt(0), big.NewInt(0))
	lengthChecker(t, receipts, utxoOutputs, keyImages, 3, 3, 1)
	//log.Debug("test final", "receipts0", *receipts[0], "receipts1", *receipts[1], "receipts2", *receipts[2])
	//log.Debug("test final", "utxoOutputs", utxoOutputs, "keyImages", keyImages, "BAL", *types.BlockBalanceRecordsInstance)
	//log.Debug("test final", "record0", *(*types.BlockBalanceRecordsInstance).TxRecords[0], "record1", *(*types.BlockBalanceRecordsInstance).TxRecords[1], "record2", *(*types.BlockBalanceRecordsInstance).TxRecords[2])
}

// //U->C
// func TestSingleUTXO2Contract(t *testing.T) {
// 	state := State.Copy()
// 	sender := Bank.PrivateKey

// 	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	bfBalanceOut := []*big.Int{big.NewInt(0)}

// 	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/SimpleToken.bin")
// 	tx1.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
// 	expectFee1 := big.NewInt(0)

// 	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
// 	ain := types.AccountSourceEntry{
// 		From:   sAdd,
// 		Nonce:  1,
// 		Amount: amount,
// 	}
// 	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
// 	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
// 	fee3 := uint64(31539)
// 	expectFee3 := big.NewInt(0).Add(big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee3), big.NewInt(types.ParGasPrice)), big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50)))

// 	amount = big.NewInt(0).Sub(amount, expectFee2)
// 	amount1 := expectFee3 //big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(51))

// 	amount2 := big.NewInt(0).Sub(amount, amount1)

// 	sks1, pks1 := xcrypto.SkpkGen()
// 	skv1, pkv1 := xcrypto.SkpkGen()
// 	sks2, pks2 := xcrypto.SkpkGen()
// 	skv2, pkv2 := xcrypto.SkpkGen()

// 	rAddr1 := types2.AccountAddress{
// 		ViewPublicKey:  types2.PublicKey(pkv1),
// 		SpendPublicKey: types2.PublicKey(pks1),
// 	}
// 	rAddr2 := types2.AccountAddress{
// 		ViewPublicKey:  types2.PublicKey(pkv2),
// 		SpendPublicKey: types2.PublicKey(pks2),
// 	}
// 	var remark [32]byte
// 	uout1 := types.UTXODestEntry{
// 		Addr:         rAddr1,
// 		Amount:       amount1,
// 		IsSubaddress: false,
// 		IsChange:     false,
// 		Remark:       remark,
// 	}
// 	uout2 := types.UTXODestEntry{
// 		Addr:         rAddr2,
// 		Amount:       amount2,
// 		IsSubaddress: false,
// 		IsChange:     false,
// 		Remark:       remark,
// 	}

// 	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee2, nil)
// 	if err != nil {
// 		panic(err)
// 	}
// 	err = tx2.Sign(types.GlobalSTDSigner, sender)
// 	if err != nil {
// 		panic(err)
// 	}
// 	sEntey1 := &types.UTXOSourceEntry{
// 		Ring: []types.UTXORingEntry{types.UTXORingEntry{
// 			Index:  0,
// 			OTAddr: tx2.Outputs[0].(*types.UTXOOutput).OTAddr,
// 			Commit: tx2.Outputs[0].(*types.UTXOOutput).Remark,
// 		}},
// 		RingIndex: 0,
// 		RKey:      tx2.RKey,
// 		OutIndex:  0,
// 		Amount:    big.NewInt(0).Set(amount1),
// 		Mask:      tx2.RCTSig.RctSigBase.EcdhInfo[0].Mask,
// 	}

// 	acc1 := types2.AccountKey{
// 		Addr: types2.AccountAddress{
// 			SpendPublicKey: types2.PublicKey(pks1),
// 			ViewPublicKey:  types2.PublicKey(pkv1),
// 		},
// 		SpendSKey: types2.SecretKey(sks1),
// 		ViewSKey:  types2.SecretKey(skv1),
// 		SubIdx:    uint64(0),
// 	}
// 	address := wallet.AddressToStr(&acc1, uint64(0))
// 	acc1.Address = address
// 	keyi1 := make(map[types2.PublicKey]uint64)
// 	keyi1[acc1.Addr.SpendPublicKey] = 0

// 	//var cabi abi.ABI
// 	bin, err := ioutil.ReadFile("../test/token/sol/SimpleToken.abi")
// 	if err != nil {
// 		panic(err)
// 	}
// 	cabi, err := abi.JSON(bytes.NewReader(bin))
// 	if err != nil {
// 		panic(err)
// 	}
// 	var data []byte
// 	method := "transfertokentest"
// 	data, err = cabi.Pack(method, big.NewInt(0))
// 	if err != nil {
// 		panic(err)
// 	}

// 	aDest := &types.AccountDestEntry{
// 		To:     tkAdd,
// 		Amount: big.NewInt(0),
// 		Data:   data,
// 	}

// 	tx3, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest}, common.EmptyAddress, sAdd, expectFee3, []byte{})
// 	tx3.Sign(types.GlobalSTDSigner, sender)
// 	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest}, mk)
// 	if err != nil {
// 		panic(err)
// 	}
// 	if err != nil {
// 		panic(err)
// 	}

// 	block := genBlock(types.Txs{tx1, tx2, tx3})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}

// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	afBalanceOut := []*big.Int{getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2))}

// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

// 	amount = amount2
// 	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
// 	println(bfBalanceIn[0].String(), afBalanceIn[0].String(), bfBalanceOut[0].String(), afBalanceOut[0].String(), amount.String(), expectFee.String(), actualFee.String())
// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
// 	lengthChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)
// }

// //U->C2 (value transfer)
// func TestSingleUTXO2Contract2(t *testing.T) {
// 	state := State.Copy()
// 	sender := Bank.PrivateKey

// 	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
// 	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}

// 	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/t.bin")
// 	tx1.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
// 	expectFee1 := big.NewInt(0)

// 	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(1000))
// 	ain := types.AccountSourceEntry{
// 		From:   sAdd,
// 		Nonce:  1,
// 		Amount: amount,
// 	}
// 	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
// 	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
// 	amount = big.NewInt(0).Sub(amount, expectFee2)
// 	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(500))
// 	amount2 := big.NewInt(0).Sub(amount, amount1)

// 	sks1, pks1 := xcrypto.SkpkGen()
// 	skv1, pkv1 := xcrypto.SkpkGen()
// 	sks2, pks2 := xcrypto.SkpkGen()
// 	skv2, pkv2 := xcrypto.SkpkGen()

// 	rAddr1 := types2.AccountAddress{
// 		ViewPublicKey:  types2.PublicKey(pkv1),
// 		SpendPublicKey: types2.PublicKey(pks1),
// 	}
// 	rAddr2 := types2.AccountAddress{
// 		ViewPublicKey:  types2.PublicKey(pkv2),
// 		SpendPublicKey: types2.PublicKey(pks2),
// 	}
// 	var remark [32]byte
// 	uout1 := types.UTXODestEntry{
// 		Addr:         rAddr1,
// 		Amount:       amount1,
// 		IsSubaddress: false,
// 		IsChange:     false,
// 		Remark:       remark,
// 	}
// 	uout2 := types.UTXODestEntry{
// 		Addr:         rAddr2,
// 		Amount:       amount2,
// 		IsSubaddress: false,
// 		IsChange:     false,
// 		Remark:       remark,
// 	}

// 	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee2, nil)
// 	if err != nil {
// 		panic(err)
// 	}
// 	err = tx2.Sign(types.GlobalSTDSigner, sender)
// 	if err != nil {
// 		panic(err)
// 	}
// 	sEntey1 := &types.UTXOSourceEntry{
// 		Ring: []types.UTXORingEntry{types.UTXORingEntry{
// 			Index:  0,
// 			OTAddr: tx2.Outputs[0].(*types.UTXOOutput).OTAddr,
// 			Commit: tx2.Outputs[0].(*types.UTXOOutput).Remark,
// 		}},
// 		RingIndex: 0,
// 		RKey:      tx2.RKey,
// 		OutIndex:  0,
// 		Amount:    big.NewInt(0).Set(amount1),
// 		Mask:      tx2.RCTSig.RctSigBase.EcdhInfo[0].Mask,
// 	}

// 	acc1 := types2.AccountKey{
// 		Addr: types2.AccountAddress{
// 			SpendPublicKey: types2.PublicKey(pks1),
// 			ViewPublicKey:  types2.PublicKey(pkv1),
// 		},
// 		SpendSKey: types2.SecretKey(sks1),
// 		ViewSKey:  types2.SecretKey(skv1),
// 		SubIdx:    uint64(0),
// 	}
// 	address := wallet.AddressToStr(&acc1, uint64(0))
// 	acc1.Address = address
// 	keyi1 := make(map[types2.PublicKey]uint64)
// 	keyi1[acc1.Addr.SpendPublicKey] = 0

// 	//var cabi abi.ABI
// 	bin, err := ioutil.ReadFile("../test/token/sol/t.abi")
// 	if err != nil {
// 		panic(err)
// 	}
// 	cabi, err := abi.JSON(bytes.NewReader(bin))
// 	if err != nil {
// 		panic(err)
// 	}
// 	var data []byte
// 	method := "set"
// 	data, err = cabi.Pack(method, big.NewInt(0))
// 	if err != nil {
// 		panic(err)
// 	}
// 	amount3 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
// 	aDest := &types.AccountDestEntry{
// 		To:     tkAdd,
// 		Amount: amount3,
// 		Data:   data,
// 	}
// 	fee3 := uint64(26596)
// 	expectFee3 := big.NewInt(0).Add(big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee3), big.NewInt(types.ParGasPrice)), big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50)))

// 	txfee3 := big.NewInt(0).Sub(amount1, amount3)
// 	tx3, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest}, common.EmptyAddress, sAdd, txfee3, []byte{})
// 	tx3.Sign(types.GlobalSTDSigner, sender)
// 	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest}, mk)
// 	if err != nil {
// 		panic(err)
// 	}
// 	if err != nil {
// 		panic(err)
// 	}

// 	block := genBlock(types.Txs{tx1, tx2, tx3})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}

// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
// 	afBalanceOut := []*big.Int{getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2)), state.GetBalance(tkAdd)}

// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

// 	amount = big.NewInt(0).Add(amount2, amount3)
// 	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
// 	println(expectFee1.String(), expectFee2.String(), expectFee3.String(), actualFee.String())
// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
// 	lengthChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)

// }

//U->C2E (value transfer gas usage test) this test is designated to throw err
/*
func TestSingleUTXO2Contract3(t *testing.T) {
	state := State.Copy()
	sender := Bank.PrivateKey

	sAdd := crypto.PubkeyToAddress(sender.PublicKey)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}

	tx1 := genContractCreateTx(sAdd, 1000000, 0, "../test/token/sol/t.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	expectFee1 := big.NewInt(0)

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(1000))
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  1,
		Amount: amount,
	}
	fee := types.CalNewAmountGas(amount, types.EverLiankeFee)
	expectFee2 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee), big.NewInt(types.ParGasPrice))
	amount = big.NewInt(0).Sub(amount, expectFee2)
	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(500))
	amount2 := big.NewInt(0).Sub(amount, amount1)

	sks1, pks1 := xcrypto.SkpkGen()
	skv1, pkv1 := xcrypto.SkpkGen()
	sks2, pks2 := xcrypto.SkpkGen()
	skv2, pkv2 := xcrypto.SkpkGen()

	rAddr1 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv1),
		SpendPublicKey: types2.PublicKey(pks1),
	}
	rAddr2 := types2.AccountAddress{
		ViewPublicKey:  types2.PublicKey(pkv2),
		SpendPublicKey: types2.PublicKey(pks2),
	}
	var remark [32]byte
	uout1 := types.UTXODestEntry{
		Addr:         rAddr1,
		Amount:       amount1,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}
	uout2 := types.UTXODestEntry{
		Addr:         rAddr2,
		Amount:       amount2,
		IsSubaddress: false,
		IsChange:     false,
		Remark:       remark,
	}

	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, expectFee2, nil)
	if err != nil {
		panic(err)
	}
	err = tx2.Sign(types.GlobalSTDSigner, sender)
	if err != nil {
		panic(err)
	}
	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx2.Outputs[0].(*types.UTXOOutput).OTAddr,
			Commit: tx2.Outputs[0].(*types.UTXOOutput).Remark,
		}},
		RingIndex: 0,
		RKey:      tx2.RKey,
		OutIndex:  0,
		Amount:    big.NewInt(0).Set(amount1),
		Mask:      tx2.RCTSig.RctSigBase.EcdhInfo[0].Mask,
	}

	acc1 := types2.AccountKey{
		Addr: types2.AccountAddress{
			SpendPublicKey: types2.PublicKey(pks1),
			ViewPublicKey:  types2.PublicKey(pkv1),
		},
		SpendSKey: types2.SecretKey(sks1),
		ViewSKey:  types2.SecretKey(skv1),
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc1, uint64(0))
	acc1.Address = address
	keyi1 := make(map[types2.PublicKey]uint64)
	keyi1[acc1.Addr.SpendPublicKey] = 0

	//var cabi abi.ABI
	bin, err := ioutil.ReadFile("../test/token/sol/t.abi")
	if err != nil {
		panic(err)
	}
	cabi, err := abi.JSON(bytes.NewReader(bin))
	if err != nil {
		panic(err)
	}
	var data []byte
	method := "set"
	data, err = cabi.Pack(method, big.NewInt(0))
	if err != nil {
		panic(err)
	}
	fee3 := uint64(26596) + uint64(5e8)
	expectFee3 := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee3), big.NewInt(types.ParGasPrice))
	amount3 := big.NewInt(0).Sub(amount1, big.NewInt(0).Sub(expectFee3, big.NewInt(1e11))) //big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	aDest := &types.AccountDestEntry{
		To:     tkAdd,
		Amount: amount3,
		Data:   data,
	}

	tx3, ie, mk, _, err := types.NewUinTransaction(&acc1, keyi1, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest}, common.EmptyAddress, sAdd, expectFee3, []byte{})
	tx3.Sign(types.GlobalSTDSigner, sender)
	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest}, mk)
	if err != nil {
		panic(err)
	}
	if err != nil {
		panic(err)
	}

	block := genBlock(types.Txs{tx1, tx2, tx3})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}

	afBalanceIn := []*big.Int{state.GetBalance(sAdd), big.NewInt(0)}
	afBalanceOut := []*big.Int{getBalance(tx2, types2.SecretKey(skv2), types2.SecretKey(sks2)), state.GetBalance(tkAdd)}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	amount = big.NewInt(0).Add(amount2, amount3)
	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
	println(expectFee1.String(), expectFee2.String(), expectFee3.String(), actualFee.String())
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	lengthChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)
}
*/
