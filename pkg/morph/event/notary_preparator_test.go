package event

import (
	"testing"

	"github.com/nspcc-dev/neo-go/pkg/vm"

	"github.com/TrueCloudLab/frostfs-node/pkg/morph/client"
	"github.com/nspcc-dev/neo-go/pkg/core/interop/interopnames"
	"github.com/nspcc-dev/neo-go/pkg/core/transaction"
	"github.com/nspcc-dev/neo-go/pkg/crypto/hash"
	"github.com/nspcc-dev/neo-go/pkg/crypto/keys"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/network/payload"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/callflag"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
	"github.com/stretchr/testify/require"
)

var (
	alphaKeys      keys.PublicKeys
	wrongAlphaKeys keys.PublicKeys

	dummyInvocationScript      = append([]byte{byte(opcode.PUSHDATA1), 64}, make([]byte, 64)...)
	wrongDummyInvocationScript = append([]byte{byte(opcode.PUSHDATA1), 64, 1}, make([]byte, 63)...)

	scriptHash util.Uint160
)

func init() {
	privat, _ := keys.NewPrivateKey()
	pub := privat.PublicKey()

	alphaKeys = keys.PublicKeys{pub}

	wrongPrivat, _ := keys.NewPrivateKey()
	wrongPub := wrongPrivat.PublicKey()

	wrongAlphaKeys = keys.PublicKeys{wrongPub}

	scriptHash, _ = util.Uint160DecodeStringLE("21fce15191428e9c2f0e8d0329ff6d3dd14882de")
}

type blockCounter struct {
	epoch uint32
	err   error
}

func (b blockCounter) BlockCount() (res uint32, err error) {
	return b.epoch, b.err
}

func TestPrepare_IncorrectScript(t *testing.T) {
	preparator := notaryPreparator(
		PreparatorPrm{
			alphaKeysSource(),
			blockCounter{100, nil},
		},
	)

	t.Run("not contract call", func(t *testing.T) {
		bw := io.NewBufBinWriter()

		emit.Int(bw.BinWriter, 4)
		emit.String(bw.BinWriter, "test")
		emit.Bytes(bw.BinWriter, scriptHash.BytesBE())
		emit.Syscall(bw.BinWriter, interopnames.SystemContractCallNative) // any != interopnames.SystemContractCall

		nr := correctNR(bw.Bytes(), false)

		_, err := preparator.Prepare(nr)

		require.EqualError(t, err, errNotContractCall.Error())
	})

	t.Run("incorrect ", func(t *testing.T) {
		bw := io.NewBufBinWriter()

		emit.Int(bw.BinWriter, -1)
		emit.String(bw.BinWriter, "test")
		emit.Bytes(bw.BinWriter, scriptHash.BytesBE())
		emit.Syscall(bw.BinWriter, interopnames.SystemContractCall)

		nr := correctNR(bw.Bytes(), false)

		_, err := preparator.Prepare(nr)

		require.EqualError(t, err, errIncorrectCallFlag.Error())
	})
}

func TestPrepare_IncorrectNR(t *testing.T) {
	type (
		mTX struct {
			sigs    []transaction.Signer
			scripts []transaction.Witness
			attrs   []transaction.Attribute
		}
		fbTX struct {
			attrs []transaction.Attribute
		}
	)

	setIncorrectFields := func(nr payload.P2PNotaryRequest, m mTX, f fbTX) payload.P2PNotaryRequest {
		if m.sigs != nil {
			nr.MainTransaction.Signers = m.sigs
		}

		if m.scripts != nil {
			nr.MainTransaction.Scripts = m.scripts
		}

		if m.attrs != nil {
			nr.MainTransaction.Attributes = m.attrs
		}

		if f.attrs != nil {
			nr.FallbackTransaction.Attributes = f.attrs
		}

		return nr
	}

	alphaVerificationScript, _ := smartcontract.CreateMultiSigRedeemScript(len(alphaKeys)*2/3+1, alphaKeys)
	wrongAlphaVerificationScript, _ := smartcontract.CreateMultiSigRedeemScript(len(wrongAlphaKeys)*2/3+1, wrongAlphaKeys)

	tests := []struct {
		name   string
		addW   bool // additional witness for non alphabet invocations
		mTX    mTX
		fbTX   fbTX
		expErr error
	}{
		{
			name: "incorrect witness amount",
			addW: false,
			mTX: mTX{
				scripts: []transaction.Witness{{}},
			},
			expErr: errUnexpectedWitnessAmount,
		},
		{
			name: "not dummy invocation script",
			addW: false,
			mTX: mTX{
				scripts: []transaction.Witness{
					{},
					{
						InvocationScript: wrongDummyInvocationScript,
					},
					{},
				},
			},
			expErr: ErrTXAlreadyHandled,
		},
		{
			name: "incorrect main TX signers amount",
			addW: false,
			mTX: mTX{
				sigs: []transaction.Signer{{}},
			},
			expErr: errUnexpectedCosignersAmount,
		},
		{
			name: "incorrect main TX Alphabet signer",
			addW: false,
			mTX: mTX{
				sigs: []transaction.Signer{
					{},
					{
						Account: hash.Hash160(wrongAlphaVerificationScript),
					},
					{},
				},
			},
			expErr: errIncorrectAlphabetSigner,
		},
		{
			name: "incorrect main TX attribute amount",
			addW: false,
			mTX: mTX{
				attrs: []transaction.Attribute{{}, {}},
			},
			expErr: errIncorrectAttributesAmount,
		},
		{
			name: "incorrect main TX attribute",
			addW: false,
			mTX: mTX{
				attrs: []transaction.Attribute{
					{
						Value: &transaction.NotaryAssisted{
							NKeys: uint8(len(alphaKeys) + 1),
						},
					},
				},
			},
			expErr: errIncorrectAttribute,
		},
		{
			name: "incorrect main TX proxy witness",
			addW: false,
			mTX: mTX{
				scripts: []transaction.Witness{
					{
						InvocationScript: make([]byte, 1),
					},
					{
						InvocationScript: dummyInvocationScript,
					},
					{},
				},
			},
			expErr: errIncorrectProxyWitnesses,
		},
		{
			name: "incorrect main TX Alphabet witness",
			addW: false,
			mTX: mTX{
				scripts: []transaction.Witness{
					{},
					{
						VerificationScript: wrongAlphaVerificationScript,
						InvocationScript:   dummyInvocationScript,
					},
					{},
				},
			},
			expErr: errIncorrectAlphabet,
		},
		{
			name: "incorrect main TX Notary witness",
			addW: false,
			mTX: mTX{
				scripts: []transaction.Witness{
					{},
					{
						VerificationScript: alphaVerificationScript,
						InvocationScript:   dummyInvocationScript,
					},
					{
						InvocationScript: wrongDummyInvocationScript,
					},
				},
			},
			expErr: errIncorrectNotaryPlaceholder,
		},
		{
			name: "incorrect fb TX attributes amount",
			addW: false,
			fbTX: fbTX{
				attrs: []transaction.Attribute{{}},
			},
			expErr: errIncorrectFBAttributesAmount,
		},
		{
			name: "incorrect fb TX attributes",
			addW: false,
			fbTX: fbTX{
				attrs: []transaction.Attribute{{}, {}, {}},
			},
			expErr: errIncorrectFBAttributes,
		},
		{
			name: "expired fb TX",
			addW: false,
			fbTX: fbTX{
				[]transaction.Attribute{
					{},
					{
						Type: transaction.NotValidBeforeT,
						Value: &transaction.NotValidBefore{
							Height: 1,
						},
					},
					{},
				},
			},
			expErr: ErrMainTXExpired,
		},
		{
			name: "incorrect invoker TX Alphabet witness",
			addW: true,
			mTX: mTX{
				scripts: []transaction.Witness{
					{},
					{
						VerificationScript: alphaVerificationScript,
						InvocationScript:   dummyInvocationScript,
					},
					{},
					{},
				},
			},
			expErr: errIncorrectInvokerWitnesses,
		},
		{
			name: "incorrect main TX attribute with invoker",
			addW: true,
			mTX: mTX{
				attrs: []transaction.Attribute{
					{
						Value: &transaction.NotaryAssisted{
							NKeys: uint8(len(alphaKeys) + 2),
						},
					},
				},
			},
			expErr: errIncorrectAttribute,
		},
	}

	preparator := notaryPreparator(
		PreparatorPrm{
			alphaKeysSource(),
			blockCounter{100, nil},
		},
	)

	var (
		incorrectNR payload.P2PNotaryRequest
		err         error
	)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correctNR := correctNR(nil, test.addW)
			incorrectNR = setIncorrectFields(*correctNR, test.mTX, test.fbTX)

			_, err = preparator.Prepare(&incorrectNR)

			require.EqualError(t, err, test.expErr.Error())
		})
	}
}

func TestPrepare_CorrectNR(t *testing.T) {
	tests := []struct {
		hash   util.Uint160
		method string
		args   []any
	}{
		{
			scriptHash,
			"test1",
			nil,
		},
		{
			scriptHash,
			"test2",
			[]any{
				int64(4),
				"test",
				[]any{
					int64(4),
					false,
					true,
				},
			},
		},
	}

	preparator := notaryPreparator(
		PreparatorPrm{
			alphaKeysSource(),
			blockCounter{100, nil},
		},
	)

	for _, test := range tests {
		for i := 0; i < 1; i++ { // run tests against 3 and 4 witness NR
			additionalWitness := i == 0
			nr := correctNR(script(test.hash, test.method, test.args...), additionalWitness)

			event, err := preparator.Prepare(nr)

			require.NoError(t, err)
			require.Equal(t, test.method, event.Type().String())
			require.Equal(t, test.hash.StringLE(), event.ScriptHash().StringLE())

			// check args parsing
			bw := io.NewBufBinWriter()
			emit.Array(bw.BinWriter, test.args...)

			ctx := vm.NewContext(bw.Bytes())

			opCode, param, err := ctx.Next()
			require.NoError(t, err)

			for _, opGot := range event.Params() {
				require.Equal(t, opCode, opGot.code)
				require.Equal(t, param, opGot.param)

				opCode, param, err = ctx.Next()
				require.NoError(t, err)
			}

			_, _, err = ctx.Next() //  PACK opcode
			require.NoError(t, err)
			_, _, err = ctx.Next() //  packing len opcode
			require.NoError(t, err)

			opCode, _, err = ctx.Next()
			require.NoError(t, err)
			require.Equal(t, opcode.RET, opCode)
		}
	}
}

func alphaKeysSource() client.AlphabetKeys {
	return func() (keys.PublicKeys, error) {
		return alphaKeys, nil
	}
}

func script(hash util.Uint160, method string, args ...any) []byte {
	bw := io.NewBufBinWriter()

	if len(args) > 0 {
		emit.AppCall(bw.BinWriter, hash, method, callflag.All, args)
	} else {
		emit.AppCallNoArgs(bw.BinWriter, hash, method, callflag.All)
	}

	return bw.Bytes()
}

func correctNR(script []byte, additionalWitness bool) *payload.P2PNotaryRequest {
	alphaVerificationScript, _ := smartcontract.CreateMultiSigRedeemScript(len(alphaKeys)*2/3+1, alphaKeys)

	signers := []transaction.Signer{
		{},
		{
			Account: hash.Hash160(alphaVerificationScript),
		},
		{},
	}
	if additionalWitness { // insert on element with index 2
		signers = append(signers[:2+1], signers[2:]...)
		signers[2] = transaction.Signer{Account: hash.Hash160(alphaVerificationScript)}
	}

	scripts := []transaction.Witness{
		{},
		{
			InvocationScript:   dummyInvocationScript,
			VerificationScript: alphaVerificationScript,
		},
		{
			InvocationScript: dummyInvocationScript,
		},
	}
	if additionalWitness { // insert on element with index 2
		scripts = append(scripts[:2+1], scripts[2:]...)
		scripts[2] = transaction.Witness{
			InvocationScript:   dummyInvocationScript,
			VerificationScript: alphaVerificationScript,
		}
	}

	nKeys := uint8(len(alphaKeys))
	if additionalWitness {
		nKeys++
	}

	return &payload.P2PNotaryRequest{
		MainTransaction: &transaction.Transaction{
			Signers: signers,
			Scripts: scripts,
			Attributes: []transaction.Attribute{
				{
					Value: &transaction.NotaryAssisted{
						NKeys: nKeys,
					},
				},
			},
			Script: script,
		},
		FallbackTransaction: &transaction.Transaction{
			Attributes: []transaction.Attribute{
				{},
				{
					Type: transaction.NotValidBeforeT,
					Value: &transaction.NotValidBefore{
						Height: 1000,
					},
				},
				{},
			},
		},
	}
}
