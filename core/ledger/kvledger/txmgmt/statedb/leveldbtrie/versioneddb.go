/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/pkg/errors"
)

// maxDataImportBatchSize is how much of a snapshot is written as one version of
// the tree. It is a variable so that a test can make a snapshot be written in
// as many versions as it likes.
var maxDataImportBatchSize = 4 * 1024 * 1024

// versionedDB holds the world state of one channel in a patricia merkle trie
// whose nodes are kept in the LevelDB of the channel.
//
// A read opens the tree at the root the channel is committed at, and a commit
// writes a new tree beside the old one and moves the root onto it. Nothing here
// knows how a node is addressed; that is nodedb.go's business.
type versionedDB struct {
	handle db.DBHandle
	dbName string
	// dbPath is where the database of the channel is, which is one of many and is
	// what a report about the channel has to name beside the channel itself.
	dbPath   string
	nodeDB   *NodeDB
	metadata *stateMetadata
	conf     *Conf
	reporter statedb.RootReporter

	// root is the root of the tree the channel is committed at, as of the last
	// commit or of the first read that had to ask the database for it. Only a
	// commit moves it, and every commit writes it down.
	root      common.Hash
	rootKnown bool

	// lastGCBlock is the height the channel was committed at the last time the
	// garbage of the past versions was collected.
	lastGCBlock uint64
}

var (
	_ statedb.VersionedDB       = (*versionedDB)(nil)
	_ statedb.IntermediateRoots = (*versionedDB)(nil)
)

// newVersionedDB constructs an instance of VersionedDB over the given handle
func newVersionedDB(handle db.DBHandle, dbName string, dbPath string, conf *Conf, reporter statedb.RootReporter) *versionedDB {
	return &versionedDB{
		handle:   handle,
		dbName:   dbName,
		dbPath:   dbPath,
		nodeDB:   NewNodeDB(handle),
		metadata: &stateMetadata{handle: handle, dbName: dbName},
		conf:     conf,
		reporter: reporter,
	}
}

// Open implements method in VersionedDB interface
//
// Opening a channel is where what its database is made of is found out: whether
// it is of a format this version reads, whether it holds the tree its metadata
// says it holds, and whether the nodes of that tree are what they are said to
// be. A channel that fails any of these must not be opened as an empty state or
// as a state of the past, both of which are states a peer would carry on
// serving.
func (vdb *versionedDB) Open() error {
	if err := vdb.checkFormat(); err != nil {
		return err
	}

	// Opening the tree at the root the metadata names is what finds out whether
	// the database holds the root: a channel whose root node is gone is a broken
	// channel, not an empty one. The report names the channel and the database to
	// look in beside the root that is not there, because a peer holds a database
	// for every channel it serves and the reader of the report is the one who has
	// to go and find it.
	if _, err := vdb.tree(); err != nil {
		return fmt.Errorf(
			"cannot open the state of channel [%s] from the database at [%s]: %w",
			vdb.dbName, vdb.dbPath, err,
		)
	}

	if vdb.conf.VerifyOnOpen {
		if err := vdb.verify(); err != nil {
			return err
		}
	}

	if err := vdb.collectGarbageAndCountFromHere(); err != nil {
		return err
	}
	return nil
}

// checkFormat refuses a database this version of the store did not write, and
// marks a database that holds nothing as one it did.
func (vdb *versionedDB) checkFormat() error {
	empty, err := vdb.handle.IsEmpty()
	if err != nil {
		return err
	}
	if empty {
		return vdb.metadata.markFormat()
	}
	return vdb.metadata.checkFormat()
}

// collectGarbageAndCountFromHere collects the garbage of the versions the
// channel no longer reads, and starts the count of the blocks between two
// collections again at the block the channel is committed at.
func (vdb *versionedDB) collectGarbageAndCountFromHere() error {
	if err := vdb.collectGarbage(vdb.reporter); err != nil {
		return err
	}
	savePoint, err := vdb.metadata.savePoint()
	if err != nil {
		return err
	}
	if savePoint != nil {
		vdb.lastGCBlock = savePoint.BlockNum
	}
	return nil
}

// Close implements method in VersionedDB interface
func (vdb *versionedDB) Close() {
	// do nothing because shared db is used
}

// ValidateKeyValue implements method in VersionedDB interface
func (vdb *versionedDB) ValidateKeyValue(key string, value []byte) error {
	return nil
}

// BytesKeySupported implements method in VersionedDB interface
func (vdb *versionedDB) BytesKeySupported() bool {
	return true
}

// IsEmpty returns true if the statedb does not have any content
func (vdb *versionedDB) IsEmpty() (bool, error) {
	return vdb.handle.IsEmpty()
}

// stateRoot returns the root of the tree the channel is committed at. It is
// read out of the metadata the first time it is asked for, and out of memory
// after that; only a commit moves it, and a commit writes it down.
func (vdb *versionedDB) stateRoot() (common.Hash, error) {
	if !vdb.rootKnown {
		root, err := vdb.metadata.stateRoot()
		if err != nil {
			return common.Hash{}, err
		}
		vdb.root, vdb.rootKnown = root, true
	}
	return vdb.root, nil
}

// tree opens the tree the channel is committed at, ready to be read or written
// to.
func (vdb *versionedDB) tree() (*trie.Trie, error) {
	root, err := vdb.stateRoot()
	if err != nil {
		return nil, err
	}
	if root == types.EmptyRootHash {
		return trie.NewEmpty(vdb.nodeDB), nil
	}
	return trie.New(trie.TrieID(root), vdb.nodeDB)
}

// GetState implements method in VersionedDB interface
func (vdb *versionedDB) GetState(namespace string, key string) (*statedb.VersionedValue, error) {
	logger.Debugf("GetState(). ns=%s, key=%s", namespace, key)
	tree, err := vdb.tree()
	if err != nil {
		return nil, err
	}
	return vdb.getState(tree, namespace, key)
}

// GetVersion implements method in VersionedDB interface
func (vdb *versionedDB) GetVersion(namespace string, key string) (*version.Height, error) {
	versionedValue, err := vdb.GetState(namespace, key)
	if err != nil {
		return nil, err
	}
	if versionedValue == nil {
		return nil, nil
	}
	return versionedValue.Version, nil
}

// GetStateMultipleKeys implements method in VersionedDB interface
func (vdb *versionedDB) GetStateMultipleKeys(namespace string, keys []string) ([]*statedb.VersionedValue, error) {
	tree, err := vdb.tree()
	if err != nil {
		return nil, err
	}
	vals := make([]*statedb.VersionedValue, len(keys))
	for i, key := range keys {
		val, err := vdb.getState(tree, namespace, key)
		if err != nil {
			return nil, err
		}
		vals[i] = val
	}
	return vals, nil
}

// getState is GetState over a tree that has already been opened.
func (vdb *versionedDB) getState(tree *trie.Trie, namespace string, key string) (*statedb.VersionedValue, error) {
	treeVal, err := tree.Get(encodeTreeKey(namespace, key))
	if err != nil {
		return nil, err
	}
	if treeVal == nil {
		return nil, nil
	}
	return statekvdb.DecodeValue(treeVal)
}

// ExecuteQuery implements method in VersionedDB interface
func (vdb *versionedDB) ExecuteQuery(namespace, query string) (statedb.ResultsIterator, error) {
	return nil, errors.New("ExecuteQuery not supported for leveldbtrie")
}

// ExecuteQueryWithPagination implements method in VersionedDB interface
func (vdb *versionedDB) ExecuteQueryWithPagination(namespace, query, bookmark string, pageSize int32) (statedb.QueryResultsIterator, error) {
	return nil, errors.New("ExecuteQueryWithMetadata not supported for leveldbtrie")
}

// ApplyUpdates applies the batch to the underlying db.
// height is the height of the highest transaction in the Batch that
// a state db implementation is expected to ues as a save point
func (vdb *versionedDB) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	tree, err := vdb.tree()
	if err != nil {
		return err
	}
	if err := vdb.applyBatch(tree, batch); err != nil {
		return err
	}
	return vdb.commit(tree, height, vdb.reporter)
}

// ApplyUpdatesWithRoots applies the updates of a block one transaction at a
// time, and hands the root of the state after every transaction to the reporter
// beside the root of the block as a whole.
//
// The batches are what the boundaries between the transactions of a block are.
// A transaction that changes nothing does not move the root, and a root that did
// not move is not a root to report. A block with no per-transaction batches is
// one transaction as far as this store is concerned, which is the whole of the
// block as far as it is concerned anyway.
//
// The root of a transaction is reported under its number in the block, counted
// from zero, which is the number the transaction carries in the block itself.
// The reporter is optional throughout: without one, nothing is reported and the
// state is committed exactly as ApplyUpdates commits it.
func (vdb *versionedDB) ApplyUpdatesWithRoots(
	batch *statedb.UpdateBatch,
	height *version.Height,
	perTx []*statedb.UpdateBatch,
	reporter statedb.RootReporter,
) error {
	tree, err := vdb.tree()
	if err != nil {
		return err
	}

	if len(perTx) == 0 {
		if err := vdb.applyBatch(tree, batch); err != nil {
			return err
		}
		return vdb.commit(tree, height, reporter)
	}

	root := tree.Hash()
	for txNum, txBatch := range perTx {
		if err := vdb.applyBatch(tree, txBatch); err != nil {
			return err
		}
		if reporter == nil {
			continue
		}
		txRoot := tree.Hash()
		if txRoot == root {
			continue
		}
		root = txRoot
		if height != nil {
			reporter.TxRoot(vdb.dbName, height.BlockNum, uint64(txNum), txRoot[:])
		}
	}
	return vdb.commit(tree, height, reporter)
}

// applyBatch applies the updates of the given batch to the tree.
func (vdb *versionedDB) applyBatch(tree *trie.Trie, batch *statedb.UpdateBatch) error {
	if batch == nil {
		return nil
	}
	for _, ns := range batch.GetUpdatedNamespaces() {
		for k, vv := range batch.GetUpdates(ns) {
			treeKey := encodeTreeKey(ns, k)
			logger.Debugf("Channel [%s]: Applying key(string)=[%s] key(bytes)=[%#v]", vdb.dbName, string(treeKey), treeKey)

			if vv.Value == nil {
				if err := tree.Delete(treeKey); err != nil {
					return err
				}
				continue
			}
			encodedVal, err := statekvdb.EncodeValue(vv)
			if err != nil {
				return err
			}
			if err := tree.Update(treeKey, encodedVal); err != nil {
				return err
			}
		}
	}
	return nil
}

// commit writes the nodes of the given tree and the metadata that says the
// channel is committed at the root of it, and does both in one transaction of
// the database.
//
// The two cannot be told apart afterwards, and must not be: a database holding
// the nodes of a version its metadata does not name is a version nothing reads,
// and one holding a root it has no nodes for is a channel that opens as an empty
// state whatever it was committed to. A write that stops halfway through
// therefore leaves the channel at the version it was at, whole.
func (vdb *versionedDB) commit(tree *trie.Trie, height *version.Height, reporter statedb.RootReporter) error {
	// The leaves are collected along with the branches, the way a version of a
	// tree is committed rather than merely hashed. The tree of go-ethereum keeps
	// a leaf inside its parent unless it is told to collect them, and the leaves
	// are what the values of the state are read through.
	root, nodes := tree.Commit(true)

	batch := vdb.handle.NewUpdateBatch()
	vdb.nodeDB.CommitInto(batch, nodes)
	if err := vdb.metadata.writeTo(batch, root, height, vdb.conf.KeepRoots); err != nil {
		return err
	}
	if err := vdb.handle.WriteBatch(batch, true); err != nil {
		return err
	}

	vdb.root, vdb.rootKnown = root, true
	vdb.reportBlockRoot(root, height, reporter)

	if err := vdb.collectGarbageIfDue(height, reporter); err != nil {
		// The state is written and the root with it. A version that cannot be
		// collected costs what it costs until the next collection, which is no
		// reason to lose the one just committed to.
		logger.Warningf("Channel [%s]: committed block [%v] and collected no garbage: %v", vdb.dbName, height, err)
	}
	return nil
}

// reportBlockRoot hands the root of the version that was committed to the
// reporter of the peer. The reporter is the one the commit was given, which is
// the one the peer itself for a commit made on its own account, and the one the
// caller handed over for the roots of a block and its transactions.
//
// A commit with no height is the committing of the missing private data of old
// blocks. It moves the state, but there is no block the root of it could be
// named after, and a metric named after a block would then be naming the wrong
// one.
func (vdb *versionedDB) reportBlockRoot(root common.Hash, height *version.Height, reporter statedb.RootReporter) {
	if reporter == nil || height == nil {
		return
	}
	reporter.BlockRoot(vdb.dbName, height.BlockNum, root[:])
}

// GetLatestSavePoint implements method in VersionedDB interface
func (vdb *versionedDB) GetLatestSavePoint() (*version.Height, error) {
	return vdb.metadata.savePoint()
}

// GetStateRangeScanIterator implements method in VersionedDB interface
// startKey is inclusive
// endKey is exclusive
func (vdb *versionedDB) GetStateRangeScanIterator(namespace string, startKey string, endKey string) (statedb.ResultsIterator, error) {
	// pageSize = 0 denotes unlimited page size
	return vdb.GetStateRangeScanIteratorWithPagination(namespace, startKey, endKey, 0)
}

// GetStateRangeScanIteratorWithPagination implements method in VersionedDB interface
// startKey is inclusive
// endKey is exclusive
// pageSize parameter limits the number of returned results
// The returned QueryResultsIterator contains results of type *VersionedKV
func (vdb *versionedDB) GetStateRangeScanIteratorWithPagination(namespace string, startKey string, endKey string, pageSize int32) (statedb.QueryResultsIterator, error) {
	tree, err := vdb.tree()
	if err != nil {
		return nil, err
	}

	start := encodeTreeKey(namespace, startKey)
	end := namespaceEnd(namespace)
	if endKey != "" {
		end = encodeTreeKey(namespace, endKey)
	}

	nodeIt, err := tree.NodeIteratorWithRange(start, end)
	if err != nil {
		return nil, err
	}
	return newKVScanner(namespace, trie.NewIterator(nodeIt), pageSize), nil
}

// GetFullScanIterator implements method in VersionedDB interface. This function
// returns a FullScanIterator that can be used to iterate over entire data in the
// statedb for a channel. `skipNamespace` parameter can be used to control if
// the consumer wants the FullScanIterator to skip one or more namespaces from
// the returned results. The intended use of this iterator is to generate the
// snapshot files for this statedb.
func (vdb *versionedDB) GetFullScanIterator(skipNamespace func(string) bool) (statedb.FullScanIterator, error) {
	tree, err := vdb.tree()
	if err != nil {
		return nil, err
	}
	nodeIt, err := tree.NodeIterator(nil)
	if err != nil {
		return nil, err
	}
	return newFullDBScanner(trie.NewIterator(nodeIt), skipNamespace), nil
}

// importState loads the state from a snapshot taken with the FullScanIterator
// of another instance of this store. The parameter itr provides access to the
// snapshotted state.
//
// A snapshot is written as one version of the tree per batch of it, so that a
// snapshot too big to be written at once does not have to be held in memory at
// once. The root is that of the last of those versions, and there are no roots
// of the versions before it: nothing ever reads them.
func (vdb *versionedDB) importState(itr statedb.FullScanIterator, savepoint *version.Height) error {
	if itr == nil {
		// A snapshot that holds no public state moves the height the state is
		// consistent upto and leaves the state itself alone. What it leaves is
		// still a write: the database of the channel is no longer the empty one a
		// channel that was never opened is, and it is opened as a database of a
		// format named or not.
		batch := vdb.handle.NewUpdateBatch()
		vdb.metadata.writeRestoreTo(batch, savepoint)
		return vdb.handle.WriteBatch(batch, true)
	}

	tree, err := vdb.tree()
	if err != nil {
		return err
	}

	batchSize := 0
	for {
		versionedKV, err := itr.Next()
		if err != nil {
			return err
		}
		if versionedKV == nil {
			break
		}
		treeKey := encodeTreeKey(versionedKV.Namespace, versionedKV.Key)
		treeValue, err := statekvdb.EncodeValue(versionedKV.VersionedValue)
		if err != nil {
			return err
		}
		batchSize += len(treeKey) + len(treeValue)
		if err := tree.Update(treeKey, treeValue); err != nil {
			return err
		}
		if batchSize >= maxDataImportBatchSize {
			if err := vdb.commit(tree, nil, vdb.reporter); err != nil {
				return err
			}
			batchSize = 0
			tree, err = vdb.tree()
			if err != nil {
				return err
			}
		}
	}
	if err := vdb.commit(tree, savepoint, vdb.reporter); err != nil {
		return err
	}

	// The versions in between are of no use to anybody, and there may be a great
	// many of them.
	return vdb.collectGarbageAndCountFromHere()
}
