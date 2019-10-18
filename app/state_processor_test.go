package app

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"io/ioutil"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/lianxiangcloud/linkchain/accounts/abi"
	"github.com/lianxiangcloud/linkchain/accounts/keystore"
	common "github.com/lianxiangcloud/linkchain/libs/common"
	"github.com/lianxiangcloud/linkchain/libs/crypto"
	"github.com/lianxiangcloud/linkchain/libs/cryptonote/ringct"
	lktypes "github.com/lianxiangcloud/linkchain/libs/cryptonote/types"
	"github.com/lianxiangcloud/linkchain/libs/cryptonote/xcrypto"
	"github.com/lianxiangcloud/linkchain/libs/log"
	"github.com/lianxiangcloud/linkchain/state"
	types "github.com/lianxiangcloud/linkchain/types"
	"github.com/lianxiangcloud/linkchain/vm/evm"
	"github.com/lianxiangcloud/linkchain/wallet/wallet"
	"github.com/stretchr/testify/assert"
)

var (
	Bank  []*keystore.Key
	State *state.StateDB
	SP    *StateProcessor
	VC    evm.Config
	APP   *LinkApplication
)

type utxoKey struct {
	Sks, Skv lktypes.SecretKey // secret key for spending & viewing
	Pks, Pkv lktypes.PublicKey // public key for spending & viewing
	Addr     lktypes.AccountAddress
	Acc      lktypes.AccountKey
	Keyi     map[lktypes.PublicKey]uint64
}

// without money
var (
	Accs     []*keystore.Key
	UtxoAccs []*utxoKey
)

type MyReader struct {
	I int
}

// TODO: Add a Read([]byte) (int, error) method to MyReader.
func (myR MyReader) Read(b []byte) (int, error) {
	b[0] = 'A' // 65
	return myR.I, nil
}

func init() {
	Bank = accounts
	State = newTestState()
	APP, _ = initApp()
	SP = NewStateProcessor(nil, APP)
	APP.processor = SP
	VC = evm.Config{EnablePreimageRecording: false}
	types.SaveBalanceRecord = true

	LEN := 4
	Accs = make([]*keystore.Key, LEN)
	UtxoAccs = make([]*utxoKey, LEN)
	for i := 0; i < LEN; i++ {
		// gen acc
		s := string(crypto.Keccak512([]byte(string(i))))
		ask, err := ecdsa.GenerateKey(crypto.S256(), strings.NewReader(s))
		if err != nil {
			panic(err)
		}
		aaddr := crypto.PubkeyToAddress(ask.PublicKey)
		Accs[i] = &keystore.Key{
			PrivateKey: ask,
			Address:    aaddr,
		}
		// gen utxo
		sksr := ringct.ScalarmultH(ringct.H)
		for j := 0; j < i; j++ {
			sksr = ringct.ScalarmultH(sksr)
		}
		pksr := ringct.ScalarmultBase(sksr)
		//sksr, pksr := xcrypto.SkpkGen()
		skvr, pkvr := sksr, pksr
		sks, pks, skv, pkv := lktypes.SecretKey(sksr), lktypes.PublicKey(pksr), lktypes.SecretKey(skvr), lktypes.PublicKey(pkvr)
		addr := lktypes.AccountAddress{
			ViewPublicKey:  pkv,
			SpendPublicKey: pks,
		}
		acc := lktypes.AccountKey{
			Addr:      addr,
			SpendSKey: sks,
			ViewSKey:  skv,
			SubIdx:    uint64(0),
		}
		address := wallet.AddressToStr(&acc, uint64(0))
		acc.Address = address
		keyi := make(map[lktypes.PublicKey]uint64)
		keyi[acc.Addr.SpendPublicKey] = 0
		UtxoAccs[i] = &utxoKey{
			Sks:  sks,
			Skv:  skv,
			Pks:  pks,
			Pkv:  pkv,
			Addr: addr,
			Acc:  acc,
			Keyi: keyi,
		}
	}
	log.Debug("00", "a0", Accs[0].PrivateKey, "a1", Accs[1].PrivateKey, "u0", UtxoAccs[0].Sks, "u1", UtxoAccs[0].Sks)
}

func balancesChecker(t *testing.T, beforeBalanceIn, afterBalanceIn, beforeBalanceOut, afterBalanceOut []*big.Int, expectAmount, expectFee, actualFee *big.Int) {
	fmt.Println(beforeBalanceIn, afterBalanceIn, beforeBalanceOut, afterBalanceOut, expectAmount, expectFee, actualFee)
	// Sanity Check
	ins := len(beforeBalanceIn)
	for _, list := range [][]*big.Int{beforeBalanceIn, afterBalanceIn} {
		assert.Equal(t, ins, len(list))
	}
	outs := len(beforeBalanceOut)
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

func resultChecker(t *testing.T, receipts types.Receipts, utxoOutputs []*types.UTXOOutputData, keyImages []*lktypes.Key, expReceiptsLen, expUtxoOutputsLen, expKeyImageLen int) {
	// Sanity Check
	assert.True(t, expReceiptsLen >= 0)
	assert.True(t, expUtxoOutputsLen >= 0)
	assert.True(t, expKeyImageLen >= 0)
	// Length Check
	assert.Equal(t, expReceiptsLen, len(receipts))
	assert.Equal(t, expUtxoOutputsLen, len(utxoOutputs))
	assert.Equal(t, expKeyImageLen, len(keyImages))
}

func othersChecker(t *testing.T, expnonce []uint64, nonce []uint64) {
	// Sanity Check
	assert.True(t, len(nonce) > 0)
	assert.Equal(t, len(nonce), len(expnonce))
	// Length Check
	for ind, n := range nonce {
		expn := expnonce[ind]
		assert.True(t, n >= 0)
		assert.Equal(t, expn, n)
	}
}

func hashChecker(t *testing.T, receiptHash, stateHash, balanceRecordHash common.Hash, exprecipts, expstate, expbalancerecord string) {

	assert.Equal(t, receiptHash, common.HexToHash(exprecipts))
	assert.Equal(t, stateHash, common.HexToHash(expstate))
	assert.Equal(t, balanceRecordHash, common.HexToHash(expbalancerecord))
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

func getBalance(tx *types.UTXOTransaction, skv, sks lktypes.SecretKey) (amount *big.Int, mask lktypes.Key) {
	amount = big.NewInt(-1) // if no input matched, return -1
	//gen acc & kI
	acc := lktypes.AccountKey{
		Addr: lktypes.AccountAddress{
			SpendPublicKey: lktypes.PublicKey(ringct.ScalarmultBase(lktypes.Key(sks))),
			ViewPublicKey:  lktypes.PublicKey(ringct.ScalarmultBase(lktypes.Key(skv))),
		},
		SpendSKey: sks,
		ViewSKey:  skv,
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc, uint64(0))
	acc.Address = address
	keyi := make(map[lktypes.PublicKey]uint64)
	keyi[acc.Addr.SpendPublicKey] = 0
	// output
	outputID := -1
	outputCnt := len(tx.Outputs)
	for i := 0; i < outputCnt; i++ {
		o := tx.Outputs[i]
		switch ro := o.(type) {
		case *types.UTXOOutput:
			outputID++
			keyMaps := make(map[lktypes.KeyDerivation]lktypes.PublicKey, 0)
			derivationKeys := make([]lktypes.KeyDerivation, 0)
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
			ecdh := &lktypes.EcdhTuple{
				Mask:   tx.RCTSig.RctSigBase.EcdhInfo[outputID].Mask,
				Amount: tx.RCTSig.RctSigBase.EcdhInfo[outputID].Amount,
			}
			log.Debug("GenerateKeyDerivation", "derivationKey", realDeriKey, "amount", tx.RCTSig.RctSigBase.EcdhInfo[outputID].Amount)
			scalar, err := xcrypto.DerivationToScalar(realDeriKey, outputID)
			if err != nil {
				log.Error("DerivationToScalar fail", "derivationKey", realDeriKey, "outputID", outputID, "err", err)
				continue
			}
			ok := xcrypto.EcdhDecode(ecdh, lktypes.Key(scalar), false)
			if !ok {
				log.Error("EcdhDecode fail", "err", err)
				continue
			}
			amount = big.NewInt(0).Mul(types.Hash2BigInt(ecdh.Amount), big.NewInt(types.UTXO_COMMITMENT_CHANGE_RATE))
			mask = ecdh.Mask
		default:
		}
	}
	return
}

func calExpectAmount(amounts ...*big.Int) *big.Int {
	sum := big.NewInt(0)
	for _, amount := range amounts {
		sum.Add(sum, amount)
	}
	return sum
}

func calExpectFee(fees ...uint64) *big.Int {
	sum := big.NewInt(0)
	for _, fee := range fees {
		feeI := big.NewInt(0).SetUint64(fee)
		sum.Add(sum, feeI)
	}
	sum.Mul(sum, big.NewInt(types.ParGasPrice))
	return sum
}

//*********** Account Based Transactions Test **********
//tx
func TestAccount2Account(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	rAdd := Accs[0].Address

	amount1 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee1 := types.CalNewAmountGas(amount1, types.EverLiankeFee)
	nonce := uint64(0)
	tx1 := types.NewTransaction(nonce, rAdd, amount1, fee1, gasPrice, nil)
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{state.GetBalance(rAdd)}
	block := genBlock(types.Txs{tx1})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{1}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x9199391959b690a1684c077b1ddddaf4d3a1393bc14ffcc49981c1a943982c97", "0xc9def4039b552cfe968214bdcdfaa0ca19d5710c5c816e4aa849752336b90963", "0x56cef0615cc7e8f6a90ec993ec41fb7e09b29401423ce3130f77f41c5a06ec39")
}

//tx2(to Contract)
func TestAccount2Contract(t *testing.T) {
	state := State.Copy()

	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(sAdd, 1000000, nonce, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	nonce++

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

	amount2 := big.NewInt(0)
	fee2 := uint64(31539)
	tx2 := types.NewTransaction(nonce, tkAdd, big.NewInt(0), fee2, gasPrice, data)
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	log.Debug("SAVER", "balance", state.GetBalance(sAdd))
	block := genBlock(types.Txs{tx1})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	stateHash := state.IntermediateRoot(false)
	return
	state.Commit(false, 1)
	//ADD := receipts[0].ContractAddress
	stateHash = state.IntermediateRoot(false)
	log.Debug("SAVER", "DP", state.JSONDumpKV(), "stateHash", stateHash)

	block = genBlock(types.Txs{tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err = SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	log.Debug("SAVER", "nonce", state.GetNonce(sAdd), "addr", receipts[0].ContractAddress, "codehash", state.GetCodeHash(receipts[0].ContractAddress))
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectAmount := calExpectAmount(amount1, amount2)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash = state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "sh", stateHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0xbc0e06d5460784e4e8828db5de4b686fa108308b475de024bc911513a3db2a16", "0xa07f4b8f34bc8fec02e7daedb5944c135192d90eb068ecd3b001ffeea6ce80e0", "0x8dcdf5c769987bb4ea14d4aa38f73e903806bd2dc24fea38cf6160beaacaacf2")
}

//tx3(to Contract & Value transfer)
func TestAccount2Contract2(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(sAdd, 1000000, nonce, "../test/token/sol/t.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	nonce++

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

	amount2 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee2 := types.CalNewAmountGas(amount2, types.EverContractLiankeFee) + uint64(26596)
	tx2 := types.NewTransaction(nonce, tkAdd, amount2, fee2, gasPrice, data)
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}

	expectAmount := calExpectAmount(amount1, amount2)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x97981d7553106178d0555f4117f2a9d26627629212de3fd7ea755de66c0391c7", "0x1c77efe459c11215af1f99822cf336b9b789c50281ff1585c90f602d2ecc1af8", "0x276aac6012a392499a40d81f66959f9bd009ff607b56b16501ca73295c9968c6")
}

//tx4(to Contract & Value transfer but fail)
func TestAccount2ContractVmerr(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(sAdd, 1000000, nonce, "../test/token/sol/t.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	nonce++

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

	amount2 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee2bf := types.CalNewAmountGas(amount2, types.EverContractLiankeFee) + uint64(26595)
	fee2 := uint64(26595)
	tx2 := types.NewTransaction(nonce, tkAdd, amount2, fee2bf, gasPrice, data)
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x2728f90aafc6c17d468a31ad23b234f593af353e29240a3ebd562d44524b046f", "0x09caa0d1920d500cae85e41e0b64da7e9f592fa0918f52f9793a950e3e2b472b", "0x920d153684014a1b61af6c6a29fcb29a28739e53c7589ce20411b96141e9e0ee")
}

//txt
func TestAccount2AccountToken(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	rAdd := Accs[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(sAdd, 1000000, nonce, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())

	amount2 := big.NewInt(0)
	fee2 := types.CalNewAmountGas(amount2, types.EverLiankeFee)
	tkamount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	tx2 := types.NewTokenTransaction(tkAdd, 1, rAdd, tkamount, fee2, gasPrice, nil)
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{state.GetBalance(rAdd)}
	bftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
	bftkBalanceOut := []*big.Int{state.GetTokenBalance(rAdd, tkAdd)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(rAdd)}
	aftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
	aftkBalanceOut := []*big.Int{state.GetTokenBalance(rAdd, tkAdd)}

	expectAmount := calExpectAmount(amount1, amount2)
	expecttkAmount := calExpectAmount(tkamount)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	balancesChecker(t, bftkBalanceIn, aftkBalanceIn, bftkBalanceOut, aftkBalanceOut, expecttkAmount, big.NewInt(0), big.NewInt(0))
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0xdb1e7456de2767029930cf70815f4aedd593cf7dbb5ee1d9e9d5eb7207605551", "0x56ef2209dd32ce00d23afdab5f731ea942fd5f6f1399ddc20fb5053defa7d0bd", "0xfcea8b6bbec0175da26f99c825e3f833532fb54f64e3053dc4bda5c68e279a3a")
}

//cct
func TestContractCreation(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	nonce := uint64(0)
	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	tx := genContractCreateTx(accounts[0].Address, 1000000, nonce, "../test/token/sol/SimpleToken.bin")
	tx.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{}
	block := genBlock(types.Txs{tx})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{}

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x22f093fd155fd4b933f06ab869b982373b1baa080c0ef5305f690ea0b5c75deb", "0xbf4a82a0a7db313b6a9d1b7d958435cacab3e7cd7e9445368a6c79c945de81af", "0x119a145bf3a570a87f08dfae193f8bd6bee8b036d1aea1bd6e1154b6aba24a8d")
}

//cct2
func TestContractCreation2(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	nonce := uint64(0)
	amount1 := big.NewInt(0).Mul(big.NewInt(100), big.NewInt(1e16))
	fee1 := uint64(0)
	tx1 := genContractCreateTx2(accounts[0].Address, 100000, nonce, "../test/token/sol/a.bin", amount1)
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	//log.Debug("tx", "tx", tx1)

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	block := genBlock(types.Txs{tx1})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}
	log.Debug("add", "tkadd", tkAdd, "acadd", receipts[0].ContractAddress, "receipt", receipts[0])

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x33f317261865283696181f0b763639881016eba587c35f8695e2e7e2024a5fcf", "0xe1bdd90a9ef73d3b3838432bc39f44c1a90a56720561f0b8137265e8ee7c0d13", "0x4789c1287a39f8327e4d710fdff672807a3cc0c48a80da530dfab338267fc27c")
}

//cctbytx
// func TestContractCreationBySendToEmptyAddress(t *testing.T) {
// 		state := State.Copy()
//types.SaveBalanceRecord = true
//types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

// 	sender := Bank[0].PrivateKey
// 	sAdd := Bank[0].Address

// 	var ccode []byte
// 	bin, err := ioutil.ReadFile("../test/token/sol/SimpleToken.bin")
// 	if err != nil {
// 		panic(err)
// 	}
// 	ccode = common.Hex2Bytes(string(bin))

// 	amount1 := big.NewInt(0)
// 	fee1 := uint64(1494617)
// 	nonce := uint64(0)
// 	tx := types.NewContractCreation(nonce, amount1, fee1, gasPrice, ccode)
// 	tx.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(sAdd, tx.Nonce(), tx.Data())
// 	nonce++

// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	bfBalanceOut := []*big.Int{big.NewInt(0)}
// 	block := genBlock(types.Txs{tx})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}
// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	afBalanceOut := []*big.Int{state.GetBalance(tkAdd)}

// 	expectAmount := calExpectAmount(amount1)
// 	expectFee := calExpectFee(fee1)
// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
// 	expectNonce := []uint64{nonce}
// 	actualNonce := []uint64{state.GetNonce(sAdd)}

// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
// 	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
// 	othersChecker(t, expectNonce, actualNonce)
// }

//cut
func TestContractUpdate(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(accounts[0].Address, 1000000, nonce, "../test/token/tcvm/TestToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	contractAddr := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	nonce++

	amount2 := big.NewInt(0)
	fee2 := uint64(0)
	tx2 := genContractUpgradeTx(tx1.FromAddr, contractAddr, nonce, "../test/token/tcvm/TestToken.bin")
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{}

	expectAmount := calExpectAmount(amount1, amount2)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := receipts.Hash()
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash(types.BlockBalanceRecordsInstance.Json())
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0xe4c1699aa0c9bf8cc3aa1fe91eb8221789b24787168738ded0d2e432bbea9ec2", "0x8d95fc235a83b4b9d0427f2df600cfbd917d1bf9df99815884e4eb4117a74c68", "0x7773c71c5273f9e63153f88e46e5573a0cc57aac879a9078c7faa913901f4297")
}

//*********** UTXO Based Transactions Test **********

//A->A
func TestSingleAccount2SingleAccount(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	rAdd := Accs[0].Address

	amount1bf := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee1 := types.CalNewAmountGas(amount1bf, types.EverLiankeFee)
	fee1i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee1), big.NewInt(types.ParGasPrice))
	amount1 = big.NewInt(0).Sub(amount1bf, fee1i)
	nonce := uint64(0)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount1bf,
	}
	aout := types.AccountDestEntry{
		To:     rAdd,
		Amount: amount1,
		Data:   nil,
	}
	tx, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&aout}, common.EmptyAddress, nil)
	if err != nil {
		panic(err)
	}
	tx.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{state.GetBalance(rAdd)}
	block := genBlock(types.Txs{tx})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{state.GetBalance(rAdd)}

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := types.RlpHash("")
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash("")
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421", "0x3b996683504ced74d75cda750a105b987c629bbd5db0cd164cd5ed5cfa0e0bba", "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
}

//A->C
func TestSingleAccount2Contract(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address

	amount1 := big.NewInt(0)
	fee1 := uint64(0)
	nonce := uint64(0)
	tx1 := genContractCreateTx(sAdd, 1000000, nonce, "../test/token/sol/SimpleToken.bin")
	tx1.Sign(types.GlobalSTDSigner, sender)
	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
	nonce++

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
	fee2i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
	amount2 := big.NewInt(0)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  1,
		Amount: fee2i,
	}
	aout := &types.AccountDestEntry{
		To:     tkAdd,
		Amount: big.NewInt(0),
		Data:   data,
	}
	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{aout}, common.EmptyAddress, nil)
	if err != nil {
		panic(err)
	}
	tx2.Sign(types.GlobalSTDSigner, sender)
	nonce++

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{big.NewInt(0)}

	expectAmount := calExpectAmount(amount1, amount2)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 0, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := types.RlpHash("")
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash("")
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421", "0xa07f4b8f34bc8fec02e7daedb5944c135192d90eb068ecd3b001ffeea6ce80e0", "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
}

//A->U+
func TestSingleAccount2MulitipleUTXO(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	utxo1, utxo2 := UtxoAccs[0], UtxoAccs[1]

	amount1bf := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := uint64(0)
	fee1 := types.CalNewAmountGas(amount1bf, types.EverLiankeFee)
	fee1i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee1), big.NewInt(types.ParGasPrice))
	amount1 = big.NewInt(0).Sub(amount1bf, fee1i)
	amount1a := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50))
	amount1b := big.NewInt(0).Sub(amount1, amount1a)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount1bf,
	}
	uout1 := types.UTXODestEntry{
		Addr:   utxo1.Addr,
		Amount: amount1a,
	}
	uout2 := types.UTXODestEntry{
		Addr:   utxo2.Addr,
		Amount: amount1b,
	}
	tx1, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, nil)
	if err != nil {
		panic(err)
	}
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++
	balance11, _ := getBalance(tx1, lktypes.SecretKey(utxo1.Skv), lktypes.SecretKey(utxo1.Sks))
	balance12, _ := getBalance(tx1, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	block := genBlock(types.Txs{tx1})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{balance11, balance12}

	expectAmount := calExpectAmount(amount1)
	expectFee := calExpectFee(fee1)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 1, 2, 0)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := types.RlpHash("")
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash("")
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421", "0xa45746b9e991e8c9c07c2ae0db56a5639de3c089eb299b990ab8143ac4217f5e", "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
}

// //A->U+token
// func TestSingleAccount2MulitipleUTXOToken(t *testing.T) {
// 		state := State.Copy()
//	types.SaveBalanceRecord = true
//types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

// 	sender := Bank[0].PrivateKey
// 	sAdd := Bank[0].Address
// 	utxo1, utxo2 := UtxoAccs[0], UtxoAccs[1]

// 	amount1 := big.NewInt(0)
// 	fee1 := uint64(0)
// 	nonce := uint64(0)
// 	tx1 := genContractCreateTx(sAdd, 100000, nonce, "../test/token/sol/SimpleToken.bin")
// 	tx1.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
// 	nonce++

// 	amount2 := big.NewInt(0)
// 	tkamount2 := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(1))
// 	fee2 := types.CalNewAmountGas(big.NewInt(0), types.EverLiankeFee)
// 	//fee2i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
// 	tkamount2a := big.NewInt(0).Mul(big.NewInt(1e17), big.NewInt(5))
// 	tkamount2b := big.NewInt(0).Sub(tkamount2, tkamount2a)
// 	ain := types.AccountSourceEntry{
// 		From:   sAdd,
// 		Nonce:  nonce,
// 		Amount: tkamount2,
// 	}
// 	uout1 := types.UTXODestEntry{
// 		Addr:   utxo1.Addr,
// 		Amount: tkamount2a,
// 	}
// 	uout2 := types.UTXODestEntry{
// 		Addr:   utxo2.Addr,
// 		Amount: tkamount2b,
// 	}
// 	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, tkAdd, nil)
// 	if err != nil {
// 		panic(err)
// 	}
// 	tx2.Sign(types.GlobalSTDSigner, sender)
// 	nonce++
// 	balance21, _ := getBalance(tx2, lktypes.SecretKey(utxo1.Skv), lktypes.SecretKey(utxo1.Sks))
// 	balance22, _ := getBalance(tx2, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
// 	bftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
// 	bftkBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
// 	block := genBlock(types.Txs{tx1, tx2})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}
// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	afBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
// 	aftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
// 	aftkBalanceOut := []*big.Int{balance21, balance22}

// 	expectAmount := calExpectAmount(amount1, amount2)
// 	expecttkAmount := calExpectAmount(tkamount2)
// 	expectFee := calExpectFee(fee1, fee2)
// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
// 	expectNonce := []uint64{nonce}
// 	actualNonce := []uint64{state.GetNonce(sAdd)}

// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
// 	balancesChecker(t, bftkBalanceIn, aftkBalanceIn, bftkBalanceOut, aftkBalanceOut, expecttkAmount, big.NewInt(0), big.NewInt(0))
// 	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 2, 0)
// 	othersChecker(t, expectNonce, actualNonce)

// }

//U->A
func TestSingleUTXO2Account(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	utxo1, utxo2 := UtxoAccs[0], UtxoAccs[1]

	amount1bf := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	nonce := uint64(0)
	fee1 := types.CalNewAmountGas(amount1bf, types.EverLiankeFee)
	fee1i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee1), big.NewInt(types.ParGasPrice))
	amount1 = big.NewInt(0).Sub(amount1bf, fee1i)
	amount1a := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(50))
	amount1b := big.NewInt(0).Sub(amount1, amount1a)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount1bf,
	}
	uout1 := types.UTXODestEntry{
		Addr:   utxo1.Addr,
		Amount: amount1a,
	}
	uout2 := types.UTXODestEntry{
		Addr:   utxo2.Addr,
		Amount: amount1b,
	}
	tx1, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, nil)
	if err != nil {
		panic(err)
	}
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++
	balance11, mask11 := getBalance(tx1, lktypes.SecretKey(utxo1.Skv), lktypes.SecretKey(utxo1.Sks))
	balance12, _ := getBalance(tx1, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

	amount2bf := amount1a
	fee2 := types.CalNewAmountGas(amount2bf, types.EverLiankeFee)
	fee2i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
	amount2 := big.NewInt(0).Sub(amount2bf, fee2i)
	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx1.Outputs[0].(*types.UTXOOutput).OTAddr,
		}},
		RingIndex: 0,
		RKey:      tx1.RKey,
		OutIndex:  0,
		Amount:    balance11,
		Mask:      mask11,
	}
	aDest := &types.AccountDestEntry{
		To:     sAdd,
		Amount: amount2,
	}
	tx2, ie, mk, _, err := types.NewUinTransaction(&utxo1.Acc, utxo1.Keyi, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest}, common.EmptyAddress, common.EmptyAddress, []byte{})
	if err != nil {
		panic(err)
	}
	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest}, mk)
	if err != nil {
		panic(err)
	}

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{balance12}

	expectAmount := calExpectAmount(amount1b)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee) //#1
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 2, 1)
	othersChecker(t, expectNonce, actualNonce)

	for _, rece := range receipts {
		rece.TxHash = common.EmptyHash
	}

	receiptHash := types.RlpHash("")
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash("")
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421", "0xf7d85ce7bfdd37a2d5e6dede947a97576011041a85b2d383e354877a90a39f2c", "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
}

//U->M
func TestSingleUTXO2Mix(t *testing.T) {
	state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

	sender := Bank[0].PrivateKey
	sAdd := Bank[0].Address
	utxo1, utxo2 := UtxoAccs[0], UtxoAccs[1]

	amount1bf := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(100))
	fee1 := types.CalNewAmountGas(amount1bf, types.EverLiankeFee)
	fee1i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee1), big.NewInt(types.ParGasPrice))
	amount1 := big.NewInt(0).Sub(amount1bf, fee1i)
	amount1a := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(90))
	amount1b := big.NewInt(0).Sub(amount1, amount1a)
	nonce := uint64(0)
	ain := types.AccountSourceEntry{
		From:   sAdd,
		Nonce:  nonce,
		Amount: amount1bf,
	}
	uout1 := types.UTXODestEntry{
		Addr:   utxo1.Addr,
		Amount: amount1a,
	}
	uout2 := types.UTXODestEntry{
		Addr:   utxo2.Addr,
		Amount: amount1b,
	}
	tx1, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, common.EmptyAddress, nil)
	if err != nil {
		panic(err)
	}
	tx1.Sign(types.GlobalSTDSigner, sender)
	nonce++
	balance11, mask11 := getBalance(tx1, lktypes.SecretKey(utxo1.Skv), lktypes.SecretKey(utxo1.Sks))
	balance12, _ := getBalance(tx1, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

	sEntey1 := &types.UTXOSourceEntry{
		Ring: []types.UTXORingEntry{types.UTXORingEntry{
			Index:  0,
			OTAddr: tx1.Outputs[0].(*types.UTXOOutput).OTAddr,
		}},
		RingIndex: 0,
		RKey:      tx1.RKey,
		OutIndex:  0,
		Amount:    balance11,
		Mask:      mask11,
	}

	amount2bf := amount1a
	fee2 := types.CalNewAmountGas(amount2bf, types.EverLiankeFee) + uint64(5e8)
	fee2i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
	amount2 := big.NewInt(0).Sub(amount2bf, fee2i)
	amount2a := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(10))
	amount2b := big.NewInt(0).Sub(amount2, amount2a)
	aDest := &types.AccountDestEntry{
		To:     sAdd,
		Amount: amount2a,
	}
	uDest := &types.UTXODestEntry{
		Addr:   uout2.Addr,
		Amount: amount2b,
	}
	tx2, ie, mk, _, err := types.NewUinTransaction(&utxo1.Acc, utxo1.Keyi, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest, uDest}, common.EmptyAddress, common.EmptyAddress, []byte{})
	if err != nil {
		panic(err)
	}
	err = types.UInTransWithRctSig(tx2, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest, uDest}, mk)
	if err != nil {
		panic(err)
	}
	balance22, _ := getBalance(tx2, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	bfBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
	block := genBlock(types.Txs{tx1, tx2})
	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
	if err != nil {
		panic(err)
	}
	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
	afBalanceOut := []*big.Int{balance12, balance22}

	expectAmount := calExpectAmount(amount1b, amount2b)
	expectFee := calExpectFee(fee1, fee2)
	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
	expectNonce := []uint64{nonce}
	actualNonce := []uint64{state.GetNonce(sAdd)}

	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee) //#1
	resultChecker(t, receipts, utxoOutputs, keyImages, 2, 3, 1)
	othersChecker(t, expectNonce, actualNonce)

	receiptHash := types.RlpHash("")
	stateHash := state.IntermediateRoot(false)
	balanceRecordHash := types.RlpHash("")
	log.Debug("SAVER", "rh", receiptHash.Hex(), "sh", stateHash.Hex(), "brh", balanceRecordHash.Hex())
	hashChecker(t, receiptHash, stateHash, balanceRecordHash, "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421", "0x911304550f7dcf2d6b05f7d1cb86794d076627fa51b799219ef98d18d891ca14", "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
}

// //U->M token
// func TestSingleUTXO2MixToken2(t *testing.T) {
// 		state := State.Copy()
//	types.SaveBalanceRecord = true
//	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()

// 	sender := Bank[0].PrivateKey
// 	sAdd := Bank[0].Address
// 	utxo1, utxo2 := UtxoAccs[0], UtxoAccs[1]

// 	amount1 := big.NewInt(0)
// 	fee1 := uint64(0)
// 	nonce := uint64(0)
// 	tx1 := genContractCreateTx(sAdd, 100000, nonce, "../test/token/sol/SimpleToken.bin")
// 	tx1.Sign(types.GlobalSTDSigner, sender)
// 	tkAdd := crypto.CreateAddress(tx1.FromAddr, tx1.Nonce(), tx1.Data())
// 	nonce++

// 	amount2 := big.NewInt(0)
// 	fee2 := types.CalNewAmountGas(big.NewInt(0), types.EverLiankeFee)
// 	//fee2i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee2), big.NewInt(types.ParGasPrice))
// 	tkamount2 := big.NewInt(10000)
// 	tkamount2a := big.NewInt(9999)
// 	tkamount2b := big.NewInt(0).Sub(tkamount2, tkamount2a)
// 	ain := types.AccountSourceEntry{
// 		From:   sAdd,
// 		Nonce:  nonce,
// 		Amount: tkamount2,
// 	}
// 	uout1 := types.UTXODestEntry{
// 		Addr:   utxo1.Addr,
// 		Amount: tkamount2a,
// 	}
// 	uout2 := types.UTXODestEntry{
// 		Addr:   utxo2.Addr,
// 		Amount: tkamount2b,
// 	}
// 	tx2, _, err := types.NewAinTransaction(&ain, []types.DestEntry{&uout1, &uout2}, tkAdd, nil)
// 	if err != nil {
// 		panic(err)
// 	}
// 	tx2.Sign(types.GlobalSTDSigner, sender)
// 	nonce++
// 	balance21, mask21 := getBalance(tx2, lktypes.SecretKey(utxo1.Skv), lktypes.SecretKey(utxo1.Sks))
// 	balance22, _ := getBalance(tx2, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

// 	amount3 := big.NewInt(0)
// 	fee3 := types.CalNewAmountGas(big.NewInt(0), types.EverLiankeFee) + uint64(5e8)
// 	//fee3i := big.NewInt(0).Mul(big.NewInt(0).SetUint64(fee3), big.NewInt(types.ParGasPrice))
// 	tkamount3 := tkamount2a
// 	tkamount3a := big.NewInt(3000)
// 	tkamount3b := big.NewInt(0).Sub(tkamount3, tkamount3a)
// 	sEntey1 := &types.UTXOSourceEntry{
// 		Ring: []types.UTXORingEntry{types.UTXORingEntry{
// 			Index:  0,
// 			OTAddr: tx2.Outputs[0].(*types.UTXOOutput).OTAddr,
// 		}},
// 		RingIndex: 0,
// 		RKey:      tx2.RKey,
// 		OutIndex:  0,
// 		Amount:    balance21,
// 		Mask:      mask21,
// 	}
// 	aDest := &types.AccountDestEntry{
// 		To:     sAdd,
// 		Amount: tkamount3a,
// 	}
// 	uDest := &types.UTXODestEntry{
// 		Addr:   uout2.Addr,
// 		Amount: tkamount3b,
// 	}
// 	tx3, ie, mk, _, err := types.NewUinTransaction(&utxo1.Acc, utxo1.Keyi, []*types.UTXOSourceEntry{sEntey1}, []types.DestEntry{aDest, uDest}, tkAdd, common.EmptyAddress, nil)
// 	if err != nil {
// 		panic(err)
// 	}
// 	tx3.Sign(types.GlobalSTDSigner, sender)
// 	err = types.UInTransWithRctSig(tx3, []*types.UTXOSourceEntry{sEntey1}, ie, []types.DestEntry{aDest, uDest}, mk)
// 	if err != nil {
// 		panic(err)
// 	}
// 	nonce++
// 	balance32, _ := getBalance(tx3, lktypes.SecretKey(utxo2.Skv), lktypes.SecretKey(utxo2.Sks))

// 	bfBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	bftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
// 	bfBalanceOut := []*big.Int{}
// 	bftkBalanceOut := []*big.Int{big.NewInt(0), big.NewInt(0)}
// 	block := genBlock(types.Txs{tx1, tx2, tx3})
// 	receipts, _, blockGas, _, utxoOutputs, keyImages, err := SP.Process(block, state, VC)
// 	if err != nil {
// 		panic(err)
// 	}
// 	afBalanceIn := []*big.Int{state.GetBalance(sAdd)}
// 	aftkBalanceIn := []*big.Int{state.GetTokenBalance(sAdd, tkAdd)}
// 	afBalanceOut := []*big.Int{}
// 	aftkBalanceOut := []*big.Int{balance22, balance32}

// 	expectAmount := calExpectAmount(amount1, amount2, amount3)
// 	expecttkAmount := calExpectAmount(tkamount2b, tkamount3b)
// 	expectFee := calExpectFee(fee1, fee2, fee3)
// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))
// 	expectNonce := []uint64{nonce}
// 	actualNonce := []uint64{state.GetNonce(sAdd)}

// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, expectAmount, expectFee, actualFee)
// 	balancesChecker(t, bftkBalanceIn, aftkBalanceIn, bftkBalanceOut, aftkBalanceOut, expecttkAmount, big.NewInt(0), big.NewInt(0))
// 	resultChecker(t, receipts, utxoOutputs, keyImages, 3, 3, 1)
// 	othersChecker(t, expectNonce, actualNonce)
// }

// //U->C
// func TestSingleUTXO2Contract(t *testing.T) {
// 		state := State.Copy()
//	types.SaveBalanceRecord = true
//	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()
// 	sender := Bank[0].PrivateKey

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

// 	rAddr1 := lktypes.AccountAddress{
// 		ViewPublicKey:  lktypes.PublicKey(pkv1),
// 		SpendPublicKey: lktypes.PublicKey(pks1),
// 	}
// 	rAddr2 := lktypes.AccountAddress{
// 		ViewPublicKey:  lktypes.PublicKey(pkv2),
// 		SpendPublicKey: lktypes.PublicKey(pks2),
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

// 	acc1 := lktypes.AccountKey{
// 		Addr: lktypes.AccountAddress{
// 			SpendPublicKey: lktypes.PublicKey(pks1),
// 			ViewPublicKey:  lktypes.PublicKey(pkv1),
// 		},
// 		SpendSKey: lktypes.SecretKey(sks1),
// 		ViewSKey:  lktypes.SecretKey(skv1),
// 		SubIdx:    uint64(0),
// 	}
// 	address := wallet.AddressToStr(&acc1, uint64(0))
// 	acc1.Address = address
// 	keyi1 := make(map[lktypes.PublicKey]uint64)
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
// 	afBalanceOut := []*big.Int{getBalance(tx2, lktypes.SecretKey(skv2), lktypes.SecretKey(sks2))}

// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

// 	amount = amount2
// 	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
// 	println(bfBalanceIn[0].String(), afBalanceIn[0].String(), bfBalanceOut[0].String(), afBalanceOut[0].String(), amount.String(), expectFee.String(), actualFee.String())
// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
// 	resultChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)
// }

// //U->C2 (value transfer)
// func TestSingleUTXO2Contract2(t *testing.T) {
// 		state := State.Copy()
//	types.SaveBalanceRecord = true
//	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()
// 	sender := Bank[0].PrivateKey

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

// 	rAddr1 := lktypes.AccountAddress{
// 		ViewPublicKey:  lktypes.PublicKey(pkv1),
// 		SpendPublicKey: lktypes.PublicKey(pks1),
// 	}
// 	rAddr2 := lktypes.AccountAddress{
// 		ViewPublicKey:  lktypes.PublicKey(pkv2),
// 		SpendPublicKey: lktypes.PublicKey(pks2),
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

// 	acc1 := lktypes.AccountKey{
// 		Addr: lktypes.AccountAddress{
// 			SpendPublicKey: lktypes.PublicKey(pks1),
// 			ViewPublicKey:  lktypes.PublicKey(pkv1),
// 		},
// 		SpendSKey: lktypes.SecretKey(sks1),
// 		ViewSKey:  lktypes.SecretKey(skv1),
// 		SubIdx:    uint64(0),
// 	}
// 	address := wallet.AddressToStr(&acc1, uint64(0))
// 	acc1.Address = address
// 	keyi1 := make(map[lktypes.PublicKey]uint64)
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
// 	afBalanceOut := []*big.Int{getBalance(tx2, lktypes.SecretKey(skv2), lktypes.SecretKey(sks2)), state.GetBalance(tkAdd)}

// 	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

// 	amount = big.NewInt(0).Add(amount2, amount3)
// 	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
// 	println(expectFee1.String(), expectFee2.String(), expectFee3.String(), actualFee.String())
// 	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
// 	resultChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)

// }

//U->C2E (value transfer gas usage test) this test is designated to throw err
/*
func TestSingleUTXO2Contract3(t *testing.T) {
		state := State.Copy()
	types.SaveBalanceRecord = true
	types.BlockBalanceRecordsInstance = types.NewBlockBalanceRecords()
	sender := Bank[0].PrivateKey

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

	rAddr1 := lktypes.AccountAddress{
		ViewPublicKey:  lktypes.PublicKey(pkv1),
		SpendPublicKey: lktypes.PublicKey(pks1),
	}
	rAddr2 := lktypes.AccountAddress{
		ViewPublicKey:  lktypes.PublicKey(pkv2),
		SpendPublicKey: lktypes.PublicKey(pks2),
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

	acc1 := lktypes.AccountKey{
		Addr: lktypes.AccountAddress{
			SpendPublicKey: lktypes.PublicKey(pks1),
			ViewPublicKey:  lktypes.PublicKey(pkv1),
		},
		SpendSKey: lktypes.SecretKey(sks1),
		ViewSKey:  lktypes.SecretKey(skv1),
		SubIdx:    uint64(0),
	}
	address := wallet.AddressToStr(&acc1, uint64(0))
	acc1.Address = address
	keyi1 := make(map[lktypes.PublicKey]uint64)
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
	afBalanceOut := []*big.Int{getBalance(tx2, lktypes.SecretKey(skv2), lktypes.SecretKey(sks2)), state.GetBalance(tkAdd)}

	actualFee := big.NewInt(0).Mul(big.NewInt(0).SetUint64(blockGas), big.NewInt(types.ParGasPrice))

	amount = big.NewInt(0).Add(amount2, amount3)
	expectFee := big.NewInt(0).Add(big.NewInt(0).Add(expectFee1, expectFee2), expectFee3)
	println(expectFee1.String(), expectFee2.String(), expectFee3.String(), actualFee.String())
	balancesChecker(t, bfBalanceIn, afBalanceIn, bfBalanceOut, afBalanceOut, amount, expectFee, actualFee)
	resultChecker(t, receipts, utxoOutputs, keyImages, 3, 2, 1)
}
*/
