/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package tests

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb/database"
	"github.com/hyperledger/fabric/common/ledger/util/dbfactory"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/kvledger"
	"github.com/stretchr/testify/require"
)

// forEachStateDatabase runs the given check once against every store the world
// state can be kept in. The same operations are run against each of them and
// are expected to come to the same result, so that a test of an operation of the
// ledger says what it says about the trie as well as about the flat store
// without being written twice.
func forEachStateDatabase(t *testing.T, check func(t *testing.T, stateDBType string)) {
	t.Helper()
	for _, stateDBType := range []string{ledger.GoLevelDB, ledger.LevelDBTrie} {
		t.Run(stateDBType, func(t *testing.T) {
			check(t, stateDBType)
		})
	}
}

// TestRollbackOnEveryStateDatabase rolls a populated channel back to an earlier
// block and carries the ledger forward again by resubmitting the blocks that
// were rolled off. The trie has to come to the same height, the same blockchain
// info, and the same state as the flat store, the root of the state being
// recomputed out of the blocks that are submitted again.
func TestRollbackOnEveryStateDatabase(t *testing.T) {
	forEachStateDatabase(t, func(t *testing.T, stateDBType string) {
		env := newEnvWithStateDB(t, stateDBType)
		defer env.cleanup()
		env.initLedgerMgmt()

		dataHelper := newSampleDataHelper(t)
		l := env.createTestLedgerFromGenesisBlk("testLedger")
		dataHelper.populateLedger(l)
		dataHelper.verifyLedgerContent(l)
		bcInfo, err := l.lgr.GetBlockchainInfo()
		require.NoError(t, err)
		env.closeLedgerMgmt()

		rootFS := env.initializer.Config.RootFSPath
		rootBefore := stateRootInStore(t, rootFS, "testLedger")
		requireGuardOfTrieRoot(t, stateDBType, rootBefore)

		targetBlockNum := bcInfo.GetHeight() - 3
		require.NoError(t, kvledger.RollbackKVLedger(rootFS, "testLedger", targetBlockNum, stateDBType))

		rebuildable := rebuildableStatedb | rebuildableBookkeeper | rebuildableConfigHistory | rebuildableHistoryDB
		env.verifyRebuilableDirEmpty(rebuildable)

		env.initLedgerMgmt()
		l = env.openTestLedger("testLedger")
		l.verifyLedgerHeight(targetBlockNum + 1)

		for _, b := range dataHelper.submittedData["testLedger"].Blocks[targetBlockNum:] {
			require.NoError(t, l.lgr.CommitLegacy(b, &ledger.CommitOptions{FetchPvtDataFromLedger: true}))
		}
		actualBcInfo, err := l.lgr.GetBlockchainInfo()
		require.NoError(t, err)
		require.Equal(t, bcInfo, actualBcInfo)
		dataHelper.verifyLedgerContent(l)

		// Section 9: re-committing the blocks that were rolled off brings the
		// state back to the root it was at, because it is the same state.
		if rootBefore != nil {
			env.closeLedgerMgmt()
			require.Equal(t, rootBefore, stateRootInStore(t, rootFS, "testLedger"),
				"the root reached by re-committing the rolled-off blocks is not the root before the rollback")
			env.initLedgerMgmt()
		}
	})
}

// TestRebuildDBsOnEveryStateDatabase drops the state database of a populated
// channel and opens the ledger again, so that its state is rebuilt out of the
// blocks in the block store. The state the trie is brought back to is the state
// the flat store is brought back to, which is the state the channel was at.
func TestRebuildDBsOnEveryStateDatabase(t *testing.T) {
	forEachStateDatabase(t, func(t *testing.T, stateDBType string) {
		env := newEnvWithStateDB(t, stateDBType)
		defer env.cleanup()
		env.initLedgerMgmt()

		dataHelper := newSampleDataHelper(t)
		l1 := env.createTestLedgerFromGenesisBlk("ledger1")
		l2 := env.createTestLedgerFromGenesisBlk("ledger2")
		dataHelper.populateLedger(l1)
		dataHelper.populateLedger(l2)
		dataHelper.verifyLedgerContent(l1)
		dataHelper.verifyLedgerContent(l2)

		env.closeLedgerMgmt()
		rootFS := env.initializer.Config.RootFSPath
		rootBefore := stateRootInStore(t, rootFS, "ledger1")
		requireGuardOfTrieRoot(t, stateDBType, rootBefore)
		env.initLedgerMgmt()

		env.closeAllLedgersAndRemoveDirContents(rebuildableStatedb)

		l1, l2 = env.openTestLedger("ledger1"), env.openTestLedger("ledger2")
		dataHelper.verifyLedgerContent(l1)
		dataHelper.verifyLedgerContent(l2)

		// Section 9: a state rebuilt from the same blocks has the same root as
		// the state it replaces, because the state did not change.
		if rootBefore != nil {
			env.closeLedgerMgmt()
			require.Equal(t, rootBefore, stateRootInStore(t, rootFS, "ledger1"),
				"the root of the state rebuilt from the blocks is not the root before the state was dropped")
			env.initLedgerMgmt()
		}
	})
}

// TestResetOnEveryStateDatabase resets a populated channel to its genesis block
// and commits the blocks again. The state the trie is left at is the state the
// flat store is left at, which is the state the channel started from, and the
// same blockchain info comes back once the blocks are committed again.
func TestResetOnEveryStateDatabase(t *testing.T) {
	forEachStateDatabase(t, func(t *testing.T, stateDBType string) {
		env := newEnvWithStateDB(t, stateDBType)
		defer env.cleanup()
		env.initLedgerMgmt()

		dataHelper := newSampleDataHelper(t)
		l := env.createTestLedgerFromGenesisBlk("ledger1")
		dataHelper.populateLedger(l)
		dataHelper.verifyLedgerContent(l)
		genesis, err := l.lgr.GetBlockByNumber(0)
		require.NoError(t, err)
		bcInfo, err := l.lgr.GetBlockchainInfo()
		require.NoError(t, err)
		env.closeLedgerMgmt()

		rootFS := env.initializer.Config.RootFSPath
		rootBefore := stateRootInStore(t, rootFS, "ledger1")
		requireGuardOfTrieRoot(t, stateDBType, rootBefore)

		require.NoError(t, kvledger.ResetAllKVLedgers(rootFS, stateDBType))
		rebuildable := rebuildableStatedb | rebuildableBookkeeper | rebuildableConfigHistory | rebuildableHistoryDB | rebuildableBlockIndex
		env.verifyRebuilableDirEmpty(rebuildable)

		env.initLedgerMgmt()
		l = env.openTestLedger("ledger1")
		l.verifyLedgerHeight(1)
		actualGenesis, err := l.lgr.GetBlockByNumber(0)
		require.NoError(t, err)
		require.Equal(t, genesis, actualGenesis)

		for _, b := range dataHelper.submittedData["ledger1"].Blocks {
			require.NoError(t, l.lgr.CommitLegacy(b, &ledger.CommitOptions{}))
		}
		actualBcInfo, err := l.lgr.GetBlockchainInfo()
		require.NoError(t, err)
		require.Equal(t, bcInfo, actualBcInfo)
		dataHelper.verifyLedgerContent(l)

		// Section 9: re-committing every block after a reset brings back the
		// root the state had, because it is the same state.
		if rootBefore != nil {
			env.closeLedgerMgmt()
			require.Equal(t, rootBefore, stateRootInStore(t, rootFS, "ledger1"),
				"the root reached by re-committing every block after a reset is not the root before the reset")
			env.initLedgerMgmt()
		}
	})
}

// TestSnapshotImportOnEveryStateDatabase takes a snapshot of a channel, bootstraps
// a second ledger out of it, and has that ledger carry the state forward. The
// trie hands the snapshot out in the same byte order as the flat store, so the
// ledger that is bootstrapped reads the same state and continues from the same
// block.
func TestSnapshotImportOnEveryStateDatabase(t *testing.T) {
	forEachStateDatabase(t, func(t *testing.T, stateDBType string) {
		originalEnv := newEnvWithStateDB(t, stateDBType)
		defer originalEnv.cleanup()
		originalEnv.initLedgerMgmt()

		original := originalEnv.createTestLedgerFromGenesisBlk("ledger1")
		original.simulateDeployTx("cc1", nil)
		original.cutBlockAndCommitLegacy()
		original.simulateDataTx("tx1", func(s *simulator) {
			s.setState("cc1", "key1", "value1")
			s.setState("cc1", "key2", "value2")
		})
		original.cutBlockAndCommitLegacy()
		original.simulateDataTx("tx2", func(s *simulator) {
			require.NoError(t, s.DeleteState("cc1", "key2"))
		})
		original.cutBlockAndCommitLegacy()
		original.verifyPubState("cc1", "key1", "value1")
		original.verifyPubState("cc1", "key2", "")
		originalBcInfo, err := original.lgr.GetBlockchainInfo()
		require.NoError(t, err)

		snapshotDir := original.generateSnapshot()
		originalEnv.closeLedgerMgmt()

		bootEnv := newEnvWithStateDB(t, stateDBType)
		defer bootEnv.cleanup()
		bootEnv.initLedgerMgmt()
		booted := bootEnv.createTestLedgerFromSnapshot(snapshotDir)

		booted.verifyLedgerHeight(originalBcInfo.GetHeight())
		booted.verifyPubState("cc1", "key1", "value1")
		booted.verifyPubState("cc1", "key2", "")

		booted.simulateDataTx("tx3", func(s *simulator) {
			s.setState("cc1", "key3", "value3")
		})
		booted.cutBlockAndCommitLegacy()
		booted.verifyPubState("cc1", "key3", "value3")
		booted.verifyPubState("cc1", "key1", "value1")
	})
}

// TestUnjoinOnEveryStateDatabase takes a channel out of a peer and opens the
// peer again. The ledger of the channel is gone whichever store kept its state:
// the state of the trie is dropped along with the database of the channel, not
// left to be found by a peer that no longer serves it.
func TestUnjoinOnEveryStateDatabase(t *testing.T) {
	forEachStateDatabase(t, func(t *testing.T, stateDBType string) {
		env := newEnvWithStateDB(t, stateDBType)
		defer env.cleanup()
		env.initLedgerMgmt()

		dataHelper := newSampleDataHelper(t)
		l := env.createTestLedgerFromGenesisBlk("ledger1")
		dataHelper.populateLedger(l)
		dataHelper.verifyLedgerContent(l)
		env.closeLedgerMgmt()

		require.NoError(t, kvledger.UnjoinChannel(env.initializer.Config, "ledger1"))

		// The nodes and the metadata of the channel's state are gone with the
		// channel, not left in the database for a peer that no longer serves it.
		for _, appKey := range channelKeysInStore(t, env.initializer.Config.RootFSPath, "ledger1") {
			require.NotEqual(t, byte('n'), appKey[0], "a node of the state of the channel was left behind: %q", appKey)
			require.NotEqual(t, byte('m'), appKey[0], "metadata of the state of the channel was left behind: %q", appKey)
		}

		env.initLedgerMgmt()
		_, err := env.ledgerMgr.OpenLedger("ledger1")
		require.EqualError(t, err, "cannot open ledger [ledger1], ledger does not exist")
	})
}

// The store keeps the root of the state of a channel under the name of the
// channel and the metadata of the store: a separator, the metadata prefix 'm',
// and the name of the root under it. Reading it out of the LevelDB the store
// wrote is what lets a test ask which root was committed without asking the
// store itself.
func trieRootKey(channel string) []byte {
	return append(append([]byte(channel), 0x00), 'm', 'r')
}

// requireGuardOfTrieRoot fails when the store under test is the trie and it
// holds no root for the channel, so that a root check does not pass by being
// skipped.
func requireGuardOfTrieRoot(t *testing.T, stateDBType string, root []byte) {
	t.Helper()
	if stateDBType == ledger.LevelDBTrie {
		require.NotNil(t, root, "the state database of the trie holds no root for the channel")
	}
}

// stateRootInStore returns the root the store keeps for the channel, read out of
// the state database on disk. The ledger has to be closed before it is read,
// because a LevelDB holds a write lock while it is open. A store that keeps no
// root for the channel, such as the flat store, yields nil.
func stateRootInStore(t *testing.T, rootFS, channel string) []byte {
	t.Helper()
	handle := dbfactory.CreateDB(ledger.GoLevelDB, kvledger.StateDBPath(rootFS), "")
	handle.Open()
	defer handle.Close()

	blob, err := handle.Get(trieRootKey(channel))
	require.NoError(t, err)
	if len(blob) == 0 {
		return nil
	}
	return append([]byte(nil), blob...)
}

// channelKeysInStore returns the application keys the state database holds for
// the channel. The ledger has to be closed before they are read.
func channelKeysInStore(t *testing.T, rootFS, channel string) [][]byte {
	t.Helper()
	handle := dbfactory.CreateDB(ledger.GoLevelDB, kvledger.StateDBPath(rootFS), "")
	handle.Open()
	defer handle.Close()

	channelPrefix := append([]byte(channel), 0x00)
	itr, err := handle.GetIterator(channelPrefix, append(append([]byte{}, channel...), 0x01))
	require.NoError(t, err)
	defer itr.Release()

	var keys [][]byte
	for itr.Next() {
		keys = append(keys, append([]byte(nil), itr.Key()[len(channelPrefix):]...))
	}
	require.NoError(t, itr.Error())
	return keys
}

// stateRootOfPairs returns the root go-ethereum computes for a tree built from
// nothing out of the given pairs. The pairs are keyed the way the trie holds the
// state. Computing the root this way is a path of its own, beside the commit of
// a ledger, so that a test comparing the two compares two computations rather
// than one computation with itself.
func stateRootOfPairs(t *testing.T, pairs map[string][]byte) []byte {
	t.Helper()
	tree := trie.NewEmpty(inMemoryNodeDatabase{})
	for treeKey, encodedValue := range pairs {
		require.NoError(t, tree.Update([]byte(treeKey), encodedValue))
	}
	return tree.Hash().Bytes()
}

// inMemoryNodeDatabase is a node store that holds nothing, which is all a tree
// built from nothing and only hashed needs.
type inMemoryNodeDatabase struct{}

func (inMemoryNodeDatabase) NodeReader(common.Hash) (database.NodeReader, error) {
	return inMemoryNodeReader{}, nil
}

type inMemoryNodeReader struct{}

func (inMemoryNodeReader) Node(common.Hash, []byte, common.Hash) ([]byte, error) {
	return nil, nil
}

// flatStatePairs reads the state a flat store holds out of its LevelDB and
// returns it keyed the way the trie holds the same state. Reading the flat store
// directly is what keeps the root computed over these pairs independent of the
// trie the state is compared against.
func flatStatePairs(t *testing.T, dbPath, channel string) map[string][]byte {
	t.Helper()
	handle := dbfactory.CreateDB(ledger.GoLevelDB, dbPath, "")
	handle.Open()
	defer handle.Close()

	channelPrefix := append([]byte(channel), 0x00)
	itr, err := handle.GetIterator(channelPrefix, append(append([]byte{}, channel...), 0x01))
	require.NoError(t, err)
	defer itr.Release()

	pairs := map[string][]byte{}
	for itr.Next() {
		appKey := itr.Key()[len(channelPrefix):]
		if len(appKey) == 0 || appKey[0] != 'd' {
			continue
		}
		nsAndKey := appKey[1:]
		sep := bytes.IndexByte(nsAndKey, 0x00)
		require.Positive(t, sep, "a key of the flat state names no namespace")
		treeKey := append(append([]byte(nil), nsAndKey[:sep]...), 0x00)
		treeKey = append(treeKey, nsAndKey[sep+1:]...)
		pairs[string(treeKey)] = append([]byte(nil), itr.Value()...)
	}
	require.NoError(t, itr.Error())
	return pairs
}
