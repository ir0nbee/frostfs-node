package morph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-adm/internal/modules/config"
	"github.com/TrueCloudLab/frostfs-node/pkg/innerring"
	morphClient "github.com/TrueCloudLab/frostfs-node/pkg/morph/client"
	"github.com/nspcc-dev/neo-go/pkg/core/native/nativenames"
	"github.com/nspcc-dev/neo-go/pkg/core/state"
	"github.com/nspcc-dev/neo-go/pkg/core/transaction"
	"github.com/nspcc-dev/neo-go/pkg/crypto/keys"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/actor"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/trigger"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm/vmstate"
	"github.com/nspcc-dev/neo-go/pkg/wallet"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type cache struct {
	nnsCs    *state.Contract
	groupKey *keys.PublicKey
}

type initializeContext struct {
	clientContext
	cache
	// CommitteeAcc is used for retrieving the committee address and the verification script.
	CommitteeAcc *wallet.Account
	// ConsensusAcc is used for retrieving the committee address and the verification script.
	ConsensusAcc *wallet.Account
	Wallets      []*wallet.Wallet
	// ContractWallet is a wallet for providing the contract group signature.
	ContractWallet *wallet.Wallet
	// Accounts contains simple signature accounts in the same order as in Wallets.
	Accounts     []*wallet.Account
	Contracts    map[string]*contractState
	Command      *cobra.Command
	ContractPath string
}

func initializeSideChainCmd(cmd *cobra.Command, args []string) error {
	initCtx, err := newInitializeContext(cmd, viper.GetViper())
	if err != nil {
		return fmt.Errorf("initialization error: %w", err)
	}
	defer initCtx.close()

	// 1. Transfer funds to committee accounts.
	cmd.Println("Stage 1: transfer GAS to alphabet nodes.")
	if err := initCtx.transferFunds(); err != nil {
		return err
	}

	cmd.Println("Stage 2: set notary and alphabet nodes in designate contract.")
	if err := initCtx.setNotaryAndAlphabetNodes(); err != nil {
		return err
	}

	// 3. Deploy NNS contract.
	cmd.Println("Stage 3: deploy NNS contract.")
	if err := initCtx.deployNNS(deployMethodName); err != nil {
		return err
	}

	// 4. Deploy NeoFS contracts.
	cmd.Println("Stage 4: deploy NeoFS contracts.")
	if err := initCtx.deployContracts(); err != nil {
		return err
	}

	cmd.Println("Stage 4.1: Transfer GAS to proxy contract.")
	if err := initCtx.transferGASToProxy(); err != nil {
		return err
	}

	cmd.Println("Stage 5: register candidates.")
	if err := initCtx.registerCandidates(); err != nil {
		return err
	}

	cmd.Println("Stage 6: transfer NEO to alphabet contracts.")
	if err := initCtx.transferNEOToAlphabetContracts(); err != nil {
		return err
	}

	cmd.Println("Stage 7: set addresses in NNS.")
	if err := initCtx.setNNS(); err != nil {
		return err
	}

	return nil
}

func (c *initializeContext) close() {
	if local, ok := c.Client.(*localClient); ok {
		err := local.dump()
		if err != nil {
			c.Command.PrintErrf("Can't write dump: %v\n", err)
			os.Exit(1)
		}
	}
}

func newInitializeContext(cmd *cobra.Command, v *viper.Viper) (*initializeContext, error) {
	walletDir := config.ResolveHomePath(viper.GetString(alphabetWalletsFlag))
	wallets, err := openAlphabetWallets(v, walletDir)
	if err != nil {
		return nil, err
	}

	needContracts := cmd.Name() == "update-contracts" || cmd.Name() == "init"

	var w *wallet.Wallet
	if needContracts {
		w, err = openContractWallet(v, cmd, walletDir)
		if err != nil {
			return nil, err
		}
	}

	var c Client
	if v.GetString(localDumpFlag) != "" {
		if v.GetString(endpointFlag) != "" {
			return nil, fmt.Errorf("`%s` and `%s` flags are mutually exclusive", endpointFlag, localDumpFlag)
		}
		c, err = newLocalClient(cmd, v, wallets)
	} else {
		c, err = getN3Client(v)
	}
	if err != nil {
		return nil, fmt.Errorf("can't create N3 client: %w", err)
	}

	committeeAcc, err := getWalletAccount(wallets[0], committeeAccountName)
	if err != nil {
		return nil, fmt.Errorf("can't find committee account: %w", err)
	}

	consensusAcc, err := getWalletAccount(wallets[0], consensusAccountName)
	if err != nil {
		return nil, fmt.Errorf("can't find consensus account: %w", err)
	}

	var ctrPath string
	if cmd.Name() == "init" {
		if viper.GetInt64(epochDurationInitFlag) <= 0 {
			return nil, fmt.Errorf("epoch duration must be positive")
		}

		if viper.GetInt64(maxObjectSizeInitFlag) <= 0 {
			return nil, fmt.Errorf("max object size must be positive")
		}
	}

	if needContracts {
		ctrPath, err = cmd.Flags().GetString(contractsInitFlag)
		if err != nil {
			return nil, fmt.Errorf("invalid contracts path: %w", err)
		}
	}

	if err := checkNotaryEnabled(c); err != nil {
		return nil, err
	}

	accounts := make([]*wallet.Account, len(wallets))
	for i, w := range wallets {
		acc, err := getWalletAccount(w, singleAccountName)
		if err != nil {
			return nil, fmt.Errorf("wallet %s is invalid (no single account): %w", w.Path(), err)
		}
		accounts[i] = acc
	}

	cliCtx, err := defaultClientContext(c, committeeAcc)
	if err != nil {
		return nil, fmt.Errorf("client context: %w", err)
	}

	initCtx := &initializeContext{
		clientContext:  *cliCtx,
		ConsensusAcc:   consensusAcc,
		CommitteeAcc:   committeeAcc,
		ContractWallet: w,
		Wallets:        wallets,
		Accounts:       accounts,
		Command:        cmd,
		Contracts:      make(map[string]*contractState),
		ContractPath:   ctrPath,
	}

	if needContracts {
		err := initCtx.readContracts(fullContractList)
		if err != nil {
			return nil, err
		}
	}

	return initCtx, nil
}

func openAlphabetWallets(v *viper.Viper, walletDir string) ([]*wallet.Wallet, error) {
	walletFiles, err := os.ReadDir(walletDir)
	if err != nil {
		return nil, fmt.Errorf("can't read alphabet wallets dir: %w", err)
	}

	var size int
loop:
	for i := 0; i < len(walletFiles); i++ {
		name := innerring.GlagoliticLetter(i).String() + ".json"
		for j := range walletFiles {
			if walletFiles[j].Name() == name {
				size++
				continue loop
			}
		}
		break
	}
	if size == 0 {
		return nil, errors.New("alphabet wallets dir is empty (run `generate-alphabet` command first)")
	}

	wallets := make([]*wallet.Wallet, size)
	for i := 0; i < size; i++ {
		letter := innerring.GlagoliticLetter(i).String()
		p := filepath.Join(walletDir, letter+".json")
		w, err := wallet.NewWalletFromFile(p)
		if err != nil {
			return nil, fmt.Errorf("can't open wallet: %w", err)
		}

		password, err := config.GetPassword(v, letter)
		if err != nil {
			return nil, fmt.Errorf("can't fetch password: %w", err)
		}

		for i := range w.Accounts {
			if err := w.Accounts[i].Decrypt(password, keys.NEP2ScryptParams()); err != nil {
				return nil, fmt.Errorf("can't unlock wallet: %w", err)
			}
		}

		wallets[i] = w
	}

	return wallets, nil
}

func (c *initializeContext) awaitTx() error {
	return c.clientContext.awaitTx(c.Command)
}

func (c *initializeContext) nnsContractState() (*state.Contract, error) {
	if c.nnsCs != nil {
		return c.nnsCs, nil
	}

	cs, err := c.Client.GetContractStateByID(1)
	if err != nil {
		return nil, err
	}

	c.nnsCs = cs
	return cs, nil
}

func (c *initializeContext) getSigner(tryGroup bool, acc *wallet.Account) transaction.Signer {
	if tryGroup && c.groupKey != nil {
		return transaction.Signer{
			Account:       acc.Contract.ScriptHash(),
			Scopes:        transaction.CustomGroups,
			AllowedGroups: keys.PublicKeys{c.groupKey},
		}
	}

	signer := transaction.Signer{
		Account: acc.Contract.ScriptHash(),
		Scopes:  transaction.Global, // Scope is important, as we have nested call to container contract.
	}

	if !tryGroup {
		return signer
	}

	nnsCs, err := c.nnsContractState()
	if err != nil {
		return signer
	}

	groupKey, err := nnsResolveKey(c.ReadOnlyInvoker, nnsCs.Hash, morphClient.NNSGroupKeyName)
	if err == nil {
		c.groupKey = groupKey

		signer.Scopes = transaction.CustomGroups
		signer.AllowedGroups = keys.PublicKeys{groupKey}
	}
	return signer
}

func (c *clientContext) awaitTx(cmd *cobra.Command) error {
	if len(c.SentTxs) == 0 {
		return nil
	}

	if local, ok := c.Client.(*localClient); ok {
		if err := local.putTransactions(); err != nil {
			return fmt.Errorf("can't persist transactions: %w", err)
		}
	}

	err := awaitTx(cmd, c.Client, c.SentTxs)
	c.SentTxs = c.SentTxs[:0]

	return err
}

func awaitTx(cmd *cobra.Command, c Client, txs []hashVUBPair) error {
	cmd.Println("Waiting for transactions to persist...")

	const pollInterval = time.Second

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	at := trigger.Application

	var retErr error

	currBlock, err := c.GetBlockCount()
	if err != nil {
		return fmt.Errorf("can't fetch current block height: %w", err)
	}

loop:
	for i := range txs {
		res, err := c.GetApplicationLog(txs[i].hash, &at)
		if err == nil {
			if retErr == nil && len(res.Executions) > 0 && res.Executions[0].VMState != vmstate.Halt {
				retErr = fmt.Errorf("tx %d persisted in %s state: %s",
					i, res.Executions[0].VMState, res.Executions[0].FaultException)
			}
			continue loop
		}
		if txs[i].vub < currBlock {
			return fmt.Errorf("tx was not persisted: vub=%d, height=%d", txs[i].vub, currBlock)
		}
		for range tick.C {
			// We must fetch current height before application log, to avoid race condition.
			currBlock, err = c.GetBlockCount()
			if err != nil {
				return fmt.Errorf("can't fetch current block height: %w", err)
			}
			res, err := c.GetApplicationLog(txs[i].hash, &at)
			if err == nil {
				if retErr == nil && len(res.Executions) > 0 && res.Executions[0].VMState != vmstate.Halt {
					retErr = fmt.Errorf("tx %d persisted in %s state: %s",
						i, res.Executions[0].VMState, res.Executions[0].FaultException)
				}
				continue loop
			}
			if txs[i].vub < currBlock {
				return fmt.Errorf("tx was not persisted: vub=%d, height=%d", txs[i].vub, currBlock)
			}
		}
	}

	return retErr
}

// sendCommitteeTx creates transaction from script, signs it by committee nodes and sends it to RPC.
// If tryGroup is false, global scope is used for the signer (useful when
// working with native contracts).
func (c *initializeContext) sendCommitteeTx(script []byte, tryGroup bool) error {
	return c.sendMultiTx(script, tryGroup, false)
}

// sendConsensusTx creates transaction from script, signs it by alphabet nodes and sends it to RPC.
// Not that because this is used only after the contracts were initialized and deployed,
// we always try to have a group scope.
func (c *initializeContext) sendConsensusTx(script []byte) error {
	return c.sendMultiTx(script, true, true)
}

func (c *initializeContext) sendMultiTx(script []byte, tryGroup bool, withConsensus bool) error {
	var act *actor.Actor
	var err error

	withConsensus = withConsensus && !c.ConsensusAcc.Contract.ScriptHash().Equals(c.CommitteeAcc.ScriptHash())
	if tryGroup {
		// Even for consensus signatures we need the committee to pay.
		signers := make([]actor.SignerAccount, 1, 2)
		signers[0] = actor.SignerAccount{
			Signer:  c.getSigner(tryGroup, c.CommitteeAcc),
			Account: c.CommitteeAcc,
		}
		if withConsensus {
			signers = append(signers, actor.SignerAccount{
				Signer:  c.getSigner(tryGroup, c.ConsensusAcc),
				Account: c.ConsensusAcc,
			})
		}
		act, err = actor.New(c.Client, signers)
	} else {
		if withConsensus {
			panic("BUG: should never happen")
		}
		act, err = c.CommitteeAct, nil
	}
	if err != nil {
		return fmt.Errorf("could not create actor: %w", err)
	}

	tx, err := act.MakeUnsignedRun(script, []transaction.Attribute{{Type: transaction.HighPriority}})
	if err != nil {
		return fmt.Errorf("could not perform test invocation: %w", err)
	}

	if err := c.multiSign(tx, committeeAccountName); err != nil {
		return err
	}
	if withConsensus {
		if err := c.multiSign(tx, consensusAccountName); err != nil {
			return err
		}
	}

	return c.sendTx(tx, c.Command, false)
}

func getWalletAccount(w *wallet.Wallet, typ string) (*wallet.Account, error) {
	for i := range w.Accounts {
		if w.Accounts[i].Label == typ {
			return w.Accounts[i], nil
		}
	}
	return nil, fmt.Errorf("account for '%s' not found", typ)
}

func checkNotaryEnabled(c Client) error {
	ns, err := c.GetNativeContracts()
	if err != nil {
		return fmt.Errorf("can't get native contract hashes: %w", err)
	}

	notaryEnabled := false
	nativeHashes := make(map[string]util.Uint160, len(ns))
	for i := range ns {
		if ns[i].Manifest.Name == nativenames.Notary {
			notaryEnabled = len(ns[i].UpdateHistory) > 0
		}
		nativeHashes[ns[i].Manifest.Name] = ns[i].Hash
	}
	if !notaryEnabled {
		return errors.New("notary contract must be enabled")
	}
	return nil
}
