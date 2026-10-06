/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package privacyenabledstate

import (
	"fmt"
	"math"
	"runtime"
	"testing"

	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/util"
	"github.com/stretchr/testify/require"
)

// rootsTestDB returns a db over a store that hands out the root of the state
// after every transaction of a block, and the reporter it hands those roots to.
func rootsTestDB(t *testing.T, ledgerID string) (*DB, *RootRecorder) {
	env := &LevelDBTestEnv{}
	env.Init(t)
	t.Cleanup(env.Cleanup)
	store := NewRootsStore(env.GetDBHandle(ledgerID).VersionedDB, ledgerID)
	db := env.WrapDB(ledgerID, store)
	reporter := &RootRecorder{}
	db.RootReporter = reporter
	return db, reporter
}

// txOfPubWrite returns the batch of a transaction that writes a single public
// key, as the validation of a block leaves behind.
func txOfPubWrite(ns, key string, value []byte, version *version.Height) *UpdateBatch {
	txUpdates := NewUpdateBatch()
	txUpdates.PubUpdates.Put(ns, key, value, version)
	return txUpdates
}

func TestThreeTransactionsGiveThreeRootsAndTheRootOfTheBlock(t *testing.T) {
	ledgerID := generateLedgerID(t)
	db, reporter := rootsTestDB(t, ledgerID)

	updates := NewUpdateBatch()
	for txNum := range 3 {
		key := fmt.Sprintf("key%d", txNum+1)
		updates.PubUpdates.Put("ns1", key, []byte("value"+key), version.NewHeight(7, uint64(txNum)))
		updates.PerTx = append(updates.PerTx, txOfPubWrite("ns1", key, []byte("value"+key), version.NewHeight(7, uint64(txNum))))
	}

	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(7, 2)))

	// Three transactions, all three of them write something, hence three roots of
	// transactions, named after the transactions they are the root of...
	require.Len(t, reporter.TxRoots, 3)
	for txNum := range uint64(3) {
		require.Equal(t, ledgerID, reporter.TxRoots[txNum].Channel)
		require.Equal(t, uint64(7), reporter.TxRoots[txNum].Height)
		require.Equal(t, txNum, reporter.TxRoots[txNum].TxNum)
	}
	// The three are the roots of three different states...
	require.NotEqual(t, reporter.TxRoots[0].Root, reporter.TxRoots[1].Root)
	require.NotEqual(t, reporter.TxRoots[1].Root, reporter.TxRoots[2].Root)
	// ...the last of which is the root of the block, which is named after no
	// transaction at all.
	require.Len(t, reporter.BlockRoots, 1)
	require.Equal(t, ledgerID, reporter.BlockRoots[0].Channel)
	require.Equal(t, uint64(7), reporter.BlockRoots[0].Height)
	require.Equal(t, reporter.TxRoots[2].Root, reporter.BlockRoots[0].Root)
}

func TestTransactionWithoutWritesReportsNoRoot(t *testing.T) {
	ledgerID := generateLedgerID(t)
	db, reporter := rootsTestDB(t, ledgerID)

	updates := NewUpdateBatch()
	updates.PubUpdates.Put("ns1", "key1", []byte("value1"), version.NewHeight(7, 0))
	updates.PubUpdates.Put("ns1", "key3", []byte("value3"), version.NewHeight(7, 2))
	updates.PerTx = []*UpdateBatch{
		txOfPubWrite("ns1", "key1", []byte("value1"), version.NewHeight(7, 0)),
		NewUpdateBatch(), // the second transaction writes nothing at all
		txOfPubWrite("ns1", "key3", []byte("value3"), version.NewHeight(7, 2)),
	}

	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(7, 2)))

	// The second transaction changes nothing, hence the state after it is the
	// state after the first one: there is no root of the second transaction to
	// report, and the root of the block is the root after the third one.
	require.Len(t, reporter.TxRoots, 2)
	require.Equal(t, uint64(0), reporter.TxRoots[0].TxNum)
	require.Equal(t, uint64(2), reporter.TxRoots[1].TxNum)
	require.Len(t, reporter.BlockRoots, 1)
	require.Equal(t, reporter.TxRoots[1].Root, reporter.BlockRoots[0].Root)
}

func TestPrivateDataAndItsHashJoinTheRootOfTheirOwnTransaction(t *testing.T) {
	// The root of the state after the first transaction of the two blocks below
	// has to be the same root, whether or not the second transaction carries
	// private data: the private data of a transaction, and the hash of it, belong
	// to that transaction and to no other, and they enter the state with it.
	plainRoot := commitTwoTransactionBlock(t, false)
	withPvtDataRoot := commitTwoTransactionBlock(t, true)

	require.Len(t, plainRoot.TxRoots, 2)
	require.Len(t, withPvtDataRoot.TxRoots, 2)
	require.Equal(t, plainRoot.TxRoots[0].Root, withPvtDataRoot.TxRoots[0].Root)
	require.NotEqual(t, plainRoot.TxRoots[1].Root, withPvtDataRoot.TxRoots[1].Root)
}

// commitTwoTransactionBlock commits a block of two transactions: the first one
// writes a public key, the second one writes a public key and, when withPvtData
// is set, a private key with its hash as well.
func commitTwoTransactionBlock(t *testing.T, withPvtData bool) *RootRecorder {
	t.Helper()
	ledgerID := generateLedgerID(t)
	db, reporter := rootsTestDB(t, ledgerID)

	firstTx := txOfPubWrite("ns1", "key1", []byte("value1"), version.NewHeight(3, 0))
	secondTx := txOfPubWrite("ns1", "key2", []byte("value2"), version.NewHeight(3, 1))
	updates := NewUpdateBatch()
	updates.PubUpdates.Merge(firstTx.PubUpdates.UpdateBatch)
	updates.PubUpdates.Merge(secondTx.PubUpdates.UpdateBatch)
	if withPvtData {
		putPvtUpdates(t, updates, "ns1", "coll1", "pvtkey1", []byte("pvtvalue1"), version.NewHeight(3, 1))
		putPvtUpdates(t, secondTx, "ns1", "coll1", "pvtkey1", []byte("pvtvalue1"), version.NewHeight(3, 1))
	}
	updates.PerTx = []*UpdateBatch{firstTx, secondTx}

	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(3, 1)))

	if withPvtData {
		// The private data and its hash are in the state, under the namespaces
		// that are derived for them out of the namespace and the collection.
		vv, err := db.GetPrivateData("ns1", "coll1", "pvtkey1")
		require.NoError(t, err)
		require.Equal(t, &statedb.VersionedValue{Value: []byte("pvtvalue1"), Version: version.NewHeight(3, 1)}, vv)

		vv, err = db.GetValueHash("ns1", "coll1", util.ComputeStringHash("pvtkey1"))
		require.NoError(t, err)
		require.Equal(t, &statedb.VersionedValue{Value: util.ComputeHash([]byte("pvtvalue1")), Version: version.NewHeight(3, 1)}, vv)
	}
	return reporter
}

// plainStore is a db that cannot hand out the root of the state after every
// transaction of a block, as every db but a trie backed one cannot, and that
// remembers the batches it was committed with.
type plainStore struct {
	statedb.VersionedDB
	applied []string
}

func (s *plainStore) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	for _, ns := range batch.GetUpdatedNamespaces() {
		for key := range batch.GetUpdates(ns) {
			s.applied = append(s.applied, ns+"/"+key)
		}
	}
	return s.VersionedDB.ApplyUpdates(batch, height)
}

func TestStoreWithoutIntermediateRootsIsCommittedAsOneBatch(t *testing.T) {
	ledgerID := generateLedgerID(t)
	env := &LevelDBTestEnv{}
	env.Init(t)
	defer env.Cleanup()
	store := &plainStore{VersionedDB: env.GetDBHandle(ledgerID).VersionedDB}
	db := env.WrapDB(ledgerID, store)
	reporter := &RootRecorder{}
	db.RootReporter = reporter

	updates := NewUpdateBatch()
	firstTx := txOfPubWrite("ns1", "key1", []byte("value1"), version.NewHeight(5, 0))
	secondTx := txOfPubWrite("ns1", "key2", []byte("value2"), version.NewHeight(5, 1))
	updates.PubUpdates.Merge(firstTx.PubUpdates.UpdateBatch)
	updates.PubUpdates.Merge(secondTx.PubUpdates.UpdateBatch)
	putPvtUpdates(t, updates, "ns1", "coll1", "pvtkey1", []byte("pvtvalue1"), version.NewHeight(5, 1))
	putPvtUpdates(t, secondTx, "ns1", "coll1", "pvtkey1", []byte("pvtvalue1"), version.NewHeight(5, 1))
	updates.PerTx = []*UpdateBatch{firstTx, secondTx}

	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(5, 1)))

	// The store is given the block as the one batch it has always been given,
	// with the private data and the hashes merged into the namespaces derived
	// for them, and it is given it once.
	require.ElementsMatch(t, []string{
		"ns1/key1",
		"ns1/key2",
		"ns1" + nsJoiner + pvtDataPrefix + "coll1/pvtkey1",
		"ns1" + nsJoiner + hashDataPrefix + "coll1/" + string(util.ComputeStringHash("pvtkey1")),
	}, store.applied)

	// And the block is committed all the same.
	vv, err := db.GetState("ns1", "key2")
	require.NoError(t, err)
	require.Equal(t, &statedb.VersionedValue{Value: []byte("value2"), Version: version.NewHeight(5, 1)}, vv)

	vv, err = db.GetPrivateData("ns1", "coll1", "pvtkey1")
	require.NoError(t, err)
	require.Equal(t, &statedb.VersionedValue{Value: []byte("pvtvalue1"), Version: version.NewHeight(5, 1)}, vv)

	require.Empty(t, reporter.TxRoots)
	require.Empty(t, reporter.BlockRoots)
}

func TestWritesNoTransactionAccountsForAreCommittedWithTheBlock(t *testing.T) {
	ledgerID := generateLedgerID(t)
	db, reporter := rootsTestDB(t, ledgerID)

	// A block of its own puts the key that the block below expires.
	seedBlock := NewUpdateBatch()
	seedBlock.PubUpdates.Put("ns1", "expiringKey", []byte("expiringvalue"), version.NewHeight(8, 0))
	require.NoError(t, db.ApplyPrivacyAwareUpdates(seedBlock, version.NewHeight(8, 0)))

	updates := NewUpdateBatch()
	firstTx := txOfPubWrite("ns1", "key1", []byte("value1"), version.NewHeight(9, 0))
	secondTx := txOfPubWrite("ns1", "key2", []byte("value2"), version.NewHeight(9, 1))
	updates.PubUpdates.Merge(firstTx.PubUpdates.UpdateBatch)
	updates.PubUpdates.Merge(secondTx.PubUpdates.UpdateBatch)
	updates.PerTx = []*UpdateBatch{firstTx, secondTx}
	// Two writes of this block that no transaction of it accounts for: an expiry
	// marker of the purge manager, and the version a private value has to be
	// re-stamped with because a metadata only transaction moved the version of
	// its hash. The second one re-writes the key of the second transaction, at the
	// height the block expires at.
	expiringHeight := version.NewHeight(8, math.MaxUint64)
	updates.PubUpdates.Delete("ns1", "expiringKey", expiringHeight)
	updates.PubUpdates.Put("ns1", "key2", []byte("value2"), expiringHeight)

	reporter.TxRoots, reporter.BlockRoots = nil, nil
	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(9, 1)))

	// The bookkeeping of the block rides along with the last transaction that
	// writes something, hence the root of the block is the root after it.
	require.Len(t, reporter.TxRoots, 2)
	require.Len(t, reporter.BlockRoots, 1)
	require.Equal(t, reporter.TxRoots[1].Root, reporter.BlockRoots[0].Root)

	// The version the block ends up at for the second key is the bookkeeping one,
	// not the one the second transaction wrote it at.
	vv, err := db.GetState("ns1", "key2")
	require.NoError(t, err)
	require.Equal(t, &statedb.VersionedValue{Value: []byte("value2"), Version: expiringHeight}, vv)

	// And the expiry marker has taken the key it marks out of the state.
	vv, err = db.GetState("ns1", "expiringKey")
	require.NoError(t, err)
	require.Nil(t, vv)
}

func TestBlockWriteAtTheVersionOfATransactionIsNotLost(t *testing.T) {
	ledgerID := generateLedgerID(t)
	db, _ := rootsTestDB(t, ledgerID)

	// The block writes a key at a version, and the transaction of the block
	// writes the same key at the same version but another value. The version
	// alone cannot tell the two writes apart: the value has to be compared, or
	// the write of the block is dropped and the state keeps the value of the
	// transaction, which no store without intermediate roots would.
	sameVersion := version.NewHeight(4, 0)
	firstTx := txOfPubWrite("ns1", "key1", []byte("valueOfTheTransaction"), sameVersion)
	updates := NewUpdateBatch()
	updates.PubUpdates.Put("ns1", "key1", []byte("valueOfTheBlock"), sameVersion)
	updates.PerTx = []*UpdateBatch{firstTx}

	require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, sameVersion))

	vv, err := db.GetState("ns1", "key1")
	require.NoError(t, err)
	require.Equal(t, &statedb.VersionedValue{Value: []byte("valueOfTheBlock"), Version: sameVersion}, vv)
}

// inertStore is a db that commits nothing, so that what a commit costs can be
// measured above the db rather than inside it.
type inertStore struct {
	statedb.VersionedDB
}

func (s *inertStore) BytesKeySupported() bool { return true }

func (s *inertStore) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	return nil
}

func (s *inertStore) ApplyUpdatesWithRoots(
	batch *statedb.UpdateBatch,
	height *version.Height,
	perTx []*statedb.UpdateBatch,
	reporter statedb.RootReporter,
) error {
	return nil
}

func TestCommittingABlockCopiesNoValueBytes(t *testing.T) {
	// The records of the block are of the same number either way and their values
	// are a hundred times as big the second time. A commit that went over the
	// bytes of the values would cost a hundred times as much the second time; a
	// commit that holds pointers to them costs what the number of the records
	// costs, and the size of a value is none of its business.
	smallValues := bytesAllocatedByCommit(t, 200, 4*1024)
	bigValues := bytesAllocatedByCommit(t, 200, 400*1024)

	require.Less(t, bigValues, 3*smallValues,
		"committing %d records of 400KB allocated %d bytes, %d records of 4KB allocated %d bytes",
		200, bigValues, 200, smallValues)
}

// bytesAllocatedByCommit returns the bytes a commit of a block of the given
// number of records, each of a value of the given size, allocates.
func bytesAllocatedByCommit(t *testing.T, records int, valueSize int) uint64 {
	t.Helper()
	env := &LevelDBTestEnv{}
	env.Init(t)
	defer env.Cleanup()
	db := env.WrapDB(generateLedgerID(t), &inertStore{})
	db.RootReporter = &RootRecorder{}

	value := make([]byte, valueSize)
	updates := NewUpdateBatch()
	for txNum := range records / 10 {
		txUpdates := NewUpdateBatch()
		for record := range 10 {
			key := fmt.Sprintf("key-%d-%d", txNum, record)
			updates.PubUpdates.Put("ns1", key, value, version.NewHeight(11, uint64(txNum)))
			txUpdates.PubUpdates.Put("ns1", key, value, version.NewHeight(11, uint64(txNum)))
			// The private data of the transaction, and the hash of it, are
			// merged into the batch of that transaction like the public data is.
			putPvtUpdates(t, updates, "ns1", "coll1", key, value, version.NewHeight(11, uint64(txNum)))
			putPvtUpdates(t, txUpdates, "ns1", "coll1", key, value, version.NewHeight(11, uint64(txNum)))
		}
		updates.PerTx = append(updates.PerTx, txUpdates)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 5 {
		require.NoError(t, db.ApplyPrivacyAwareUpdates(updates, version.NewHeight(11, uint64(records/10-1))))
	}
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
