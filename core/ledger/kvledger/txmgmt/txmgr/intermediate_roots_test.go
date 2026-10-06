/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package txmgr

import (
	"maps"
	"slices"
	"testing"

	"github.com/hyperledger/fabric/common/ledger/testutil"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/bookkeeping"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/privacyenabledstate"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/hyperledger/fabric/core/ledger/mock"
	btltestutil "github.com/hyperledger/fabric/core/ledger/pvtdatapolicy/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// rootsEnv is a test environment whose state db hands out the root of the state
// after every transaction of a block, the way a db that keeps the state in a tree
// does, and remembers the roots it handed out.
//
// The state itself is kept by a real leveldb store: only the roots are the work
// of this environment.
type rootsEnv struct {
	bg                 *testutil.BlockGenerator
	testDBEnv          *privacyenabledstate.LevelDBTestEnv
	testBookkeepingEnv *bookkeeping.TestEnv
	store              *privacyenabledstate.RootsStore
	reporter           *privacyenabledstate.RootRecorder
	txmgr              *LockBasedTxMgr
}

func newRootsEnv(t *testing.T, ledgerID string) *rootsEnv {
	env := &rootsEnv{reporter: &privacyenabledstate.RootRecorder{}}

	env.testDBEnv = &privacyenabledstate.LevelDBTestEnv{}
	env.testDBEnv.Init(t)
	t.Cleanup(env.testDBEnv.Cleanup)

	env.testBookkeepingEnv = bookkeeping.NewTestEnv(t, ledger.GoLevelDB)
	t.Cleanup(env.testBookkeepingEnv.Cleanup)

	vdbProvider, err := statekvdb.NewVersionedDBProvider(t.TempDir(), ledger.GoLevelDB)
	require.NoError(t, err)
	t.Cleanup(vdbProvider.Close)

	vdb, err := vdbProvider.GetDBHandle(ledgerID, nil)
	require.NoError(t, err)

	env.store = privacyenabledstate.NewRootsStore(vdb, ledgerID)
	db := env.testDBEnv.WrapDB(ledgerID, env.store)
	db.RootReporter = env.reporter

	txmgr, err := NewLockBasedTxMgr(&Initializer{
		LedgerID:            ledgerID,
		DB:                  db,
		BtlPolicy:           btltestutil.SampleBTLPolicy(map[[2]string]uint64{}),
		BookkeepingProvider: env.testBookkeepingEnv.TestProvider,
		CCInfoProvider:      &mock.DeployedChaincodeInfoProvider{},
		HashFunc:            testHashFunc,
	})
	require.NoError(t, err)
	t.Cleanup(txmgr.Shutdown)
	env.txmgr = txmgr

	env.bg, _ = testutil.NewBlockGenerator(t, ledgerID, false)
	return env
}

// simulateTx simulates a transaction that reads the given keys and writes the
// given ones, and returns the write set of it as it is put into a block.
func simulateTx(t *testing.T, txMgr *LockBasedTxMgr, txid string, reads []string, writes map[string]string) []byte {
	t.Helper()
	simulator, err := txMgr.NewTxSimulator(txid)
	require.NoError(t, err)
	for _, key := range reads {
		_, err := simulator.GetState("ns1", key)
		require.NoError(t, err)
	}
	for _, key := range slices.Sorted(maps.Keys(writes)) {
		require.NoError(t, simulator.SetState("ns1", key, []byte(writes[key])))
	}
	simulator.Done()
	results, err := simulator.GetTxSimulationResults()
	require.NoError(t, err)
	rwSetBytes, err := proto.Marshal(results.PubSimulationResults)
	require.NoError(t, err)
	return rwSetBytes
}

// commitBlockOfTXs commits a block that carries one transaction per given write
// set, the way a block is committed on a peer: validated and prepared, then
// committed to the state db.
func commitBlockOfTXs(t *testing.T, txMgr *LockBasedTxMgr, bg *testutil.BlockGenerator, rwsets ...[]byte) {
	t.Helper()
	block := bg.NextBlock(rwsets)
	_, _, _, err := txMgr.ValidateAndPrepare(&ledger.BlockAndPvtData{Block: block}, true)
	require.NoError(t, err)
	require.NoError(t, txMgr.Commit())
}

func TestBlockOfThreeTransactionsGivesThreeRootsAndTheRootOfTheBlock(t *testing.T) {
	env := newRootsEnv(t, "testIntermediateRootsThree")

	firstTx := simulateTx(t, env.txmgr, "tx1", nil, map[string]string{"key1": "value1"})
	secondTx := simulateTx(t, env.txmgr, "tx2", nil, map[string]string{"key2": "value2"})
	thirdTx := simulateTx(t, env.txmgr, "tx3", nil, map[string]string{"key3": "value3"})

	commitBlockOfTXs(t, env.txmgr, env.bg, firstTx, secondTx, thirdTx)

	// Three transactions, all three of them write something, hence three roots of
	// transactions, named after the transactions they are the root of...
	require.Len(t, env.reporter.TxRoots, 3)
	for txNum := range uint64(3) {
		require.Equal(t, uint64(1), env.reporter.TxRoots[txNum].Height)
		require.Equal(t, txNum, env.reporter.TxRoots[txNum].TxNum)
	}
	// The three are the roots of three different states...
	require.NotEqual(t, env.reporter.TxRoots[0].Root, env.reporter.TxRoots[1].Root)
	require.NotEqual(t, env.reporter.TxRoots[1].Root, env.reporter.TxRoots[2].Root)
	// ...the last of which is the root of the block, which is named after no
	// transaction at all.
	require.Len(t, env.reporter.BlockRoots, 1)
	require.Equal(t, uint64(1), env.reporter.BlockRoots[0].Height)
	require.Equal(t, env.reporter.TxRoots[2].Root, env.reporter.BlockRoots[0].Root)

	// The state the block committed is the state of its three transactions.
	vv, err := env.store.GetState("ns1", "key3")
	require.NoError(t, err)
	require.Equal(t, []byte("value3"), vv.Value)
	require.Equal(t, version.NewHeight(1, 2), vv.Version)
}

func TestInvalidTransactionDoesNotBreakTheSequenceOfRoots(t *testing.T) {
	env := newRootsEnv(t, "testIntermediateRootsInvalidTX")

	// The three transactions are simulated against an empty state, so the second
	// one, which reads the key the first one writes, reads it as absent.
	firstTx := simulateTx(t, env.txmgr, "tx1", nil, map[string]string{"key1": "value1"})
	secondTx := simulateTx(t, env.txmgr, "tx2", []string{"key1"}, map[string]string{"key2": "value2"})
	thirdTx := simulateTx(t, env.txmgr, "tx3", nil, map[string]string{"key3": "value3"})

	commitBlockOfTXs(t, env.txmgr, env.bg, firstTx)
	env.reporter.TxRoots, env.reporter.BlockRoots = nil, nil

	// In the block below the second transaction reads a key that a preceding
	// transaction of that very block has written: it is invalid, and the
	// transaction after it is not.
	firstTxOfSecondBlock := simulateTx(t, env.txmgr, "tx4", nil, map[string]string{"key1": "value1_2"})
	commitBlockOfTXs(t, env.txmgr, env.bg, firstTxOfSecondBlock, secondTx, thirdTx)

	// The invalid transaction changes nothing, hence there is no root of it: the
	// roots of the block are the root of the first transaction and the root of
	// the third one, each named after the transaction it is the root of.
	require.Len(t, env.reporter.TxRoots, 2)
	require.Equal(t, uint64(0), env.reporter.TxRoots[0].TxNum)
	require.Equal(t, uint64(2), env.reporter.TxRoots[1].TxNum)
	require.Len(t, env.reporter.BlockRoots, 1)
	require.Equal(t, env.reporter.TxRoots[1].Root, env.reporter.BlockRoots[0].Root)

	// And what the invalid transaction wrote is nowhere in the state.
	vv, err := env.store.GetState("ns1", "key2")
	require.NoError(t, err)
	require.Nil(t, vv)

	vv, err = env.store.GetState("ns1", "key3")
	require.NoError(t, err)
	require.Equal(t, []byte("value3"), vv.Value)
	require.Equal(t, version.NewHeight(2, 2), vv.Version)

	vv, err = env.store.GetState("ns1", "key1")
	require.NoError(t, err)
	require.Equal(t, []byte("value1_2"), vv.Value)
}

func TestOldBlockPrivateDataGivesNoRootOfTransactions(t *testing.T) {
	env := newRootsEnv(t, "testIntermediateRootsOldBlocks")

	commitBlockOfTXs(t, env.txmgr, env.bg, simulateTx(t, env.txmgr, "tx1", nil, map[string]string{"key1": "value1"}))
	env.reporter.TxRoots, env.reporter.BlockRoots = nil, nil

	// The private data of old blocks is committed with no height at all: there is
	// no block the root of it could be named after, nor a transaction of a block
	// that the boundaries of the block could be made of.
	updates := privacyenabledstate.NewUpdateBatch()
	putPvtUpdates(t, updates, "ns1", "coll1", "pvtkey1", []byte("pvtvalue1"), version.NewHeight(1, 0))
	require.NoError(t, env.txmgr.db.ApplyPrivacyAwareUpdates(updates, nil))

	require.Empty(t, env.reporter.TxRoots)
	require.Empty(t, env.reporter.BlockRoots)

	// The private data is committed all the same.
	vv, err := env.txmgr.db.GetPrivateData("ns1", "coll1", "pvtkey1")
	require.NoError(t, err)
	require.Equal(t, []byte("pvtvalue1"), vv.Value)
}
