/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"bytes"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto/keccak"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
)

// maxGCBatchSize is how much of the garbage is written to the database at once.
// The garbage of a big state is a great many keys to hold in memory at once, so
// it goes in as many transactions as it takes; the state itself is the one thing
// that has to be written whole, and it is written where it is made.
const maxGCBatchSize = 16 * 1024 * 1024

// collectGarbage reclaims the nodes of the versions of the tree the channel no
// longer reads. Every commit writes the nodes of the tree as it is and leaves
// the nodes of the tree as it was where they are, so a channel that has been
// committed to a thousand times holds the nodes of a thousand versions, of
// which the last one is the only one anything reads.
//
// The nodes of the version the channel is at are found by walking its tree from
// its root. Everything else the database holds under the prefix of a node
// belongs to a version nothing reaches, and is deleted.
//
// What was collected is reported to the given reporter: the one of the provider
// for a collection of the opening of a channel, and the one the commit that has
// just been written was given for a collection that follows a commit. A
// collection is a measurement of the version it collected the garbage of, and
// the reporter of that version is the one it has to reach.
func (vdb *versionedDB) collectGarbage(reporter statedb.RootReporter) error {
	start := time.Now()

	tree, err := vdb.tree()
	if err != nil {
		return err
	}
	live := map[common.Hash]struct{}{}
	if err := walkNodes(tree, func(hash common.Hash, _ []byte) error {
		live[hash] = struct{}{}
		return nil
	}); err != nil {
		return err
	}

	reclaimed, err := vdb.deleteUnreachable(live)
	if err != nil {
		return err
	}

	batch := vdb.handle.NewUpdateBatch()
	vdb.metadata.writeLiveNodesTo(batch, len(live))
	if err := vdb.handle.WriteBatch(batch, true); err != nil {
		return err
	}

	elapsed := time.Since(start)
	logger.Debugf("Channel [%s]: collected %d nodes, %d of them live in %s", vdb.dbName, reclaimed, len(live), elapsed)
	if reporter != nil {
		reporter.GC(vdb.dbName, len(live), reclaimed, float64(elapsed)/float64(time.Millisecond))
	}
	return nil
}

// deleteUnreachable deletes every node of the database that is not one of the
// given nodes, and returns how many nodes it deleted.
func (vdb *versionedDB) deleteUnreachable(live map[common.Hash]struct{}) (int, error) {
	start, end := nodeKeyRange()
	itr, err := vdb.handle.GetIterator(start, end)
	if err != nil {
		return 0, err
	}
	defer itr.Release()

	reclaimed := 0
	batch := vdb.handle.NewUpdateBatch()
	for itr.Next() {
		key := itr.Key()
		hash, ok := vdb.nodeDB.nodeHash(key)
		if !ok {
			return reclaimed, fmt.Errorf(
				"the database of channel [%s] holds [%#x] among the keys of the nodes of its tree",
				vdb.dbName, key,
			)
		}
		if _, ok := live[hash]; ok {
			continue
		}
		batch.Delete(key)
		reclaimed++
		if batch.Size() >= maxGCBatchSize {
			if err := vdb.handle.WriteBatch(batch, true); err != nil {
				return reclaimed, err
			}
			batch.Reset()
		}
	}
	if err := itr.Error(); err != nil {
		return reclaimed, err
	}
	if err := vdb.handle.WriteBatch(batch, true); err != nil {
		return reclaimed, err
	}
	return reclaimed, nil
}

// collectGarbageIfDue collects the garbage of the versions the channel has been
// committed to since the last time it ran. A channel is collected when it is
// opened, so an interval of no blocks at all leaves that one collection to do
// the work of every one of them.
//
// The collection is the work of the commit that is due to bring it about, and it
// is reported to the reporter of that commit.
func (vdb *versionedDB) collectGarbageIfDue(height *version.Height, reporter statedb.RootReporter) error {
	if vdb.conf.GCIntervalBlocks <= 0 || height == nil {
		return nil
	}
	if height.BlockNum < vdb.lastGCBlock+uint64(vdb.conf.GCIntervalBlocks) {
		return nil
	}
	if err := vdb.collectGarbage(reporter); err != nil {
		return err
	}
	vdb.lastGCBlock = height.BlockNum
	return nil
}

// verify walks the whole tree of the channel, and finds out whether every node
// of it is what the hash it is kept under says it is.
//
// The walk is what finds out, because a read reaches only the nodes on the path
// of the key it is about, and a node nothing read yet is a node nothing has
// found out. Of the two things that can be wrong with a node, a node that is
// missing or cannot be decoded stops the walk, and a node that is a node of
// some tree but not of this one stops nothing: reads through it answer with the
// values of the tree it belongs to, which is the one way a state can be wrong
// without anything about it looking wrong.
func (vdb *versionedDB) verify() error {
	tree, err := vdb.tree()
	if err != nil {
		return err
	}
	hasher := keccak.NewLegacyKeccak256()
	return walkNodes(tree, func(hash common.Hash, blob []byte) error {
		if len(blob) == 0 {
			return fmt.Errorf("the node %#x of the tree of channel [%s] is not in the database", hash, vdb.dbName)
		}
		hasher.Reset()
		hasher.Write(blob)
		if !bytes.Equal(hasher.Sum(nil), hash[:]) {
			return fmt.Errorf("the node %#x of the tree of channel [%s] is not the node that is called by that name", hash, vdb.dbName)
		}
		return nil
	})
}

// walkNodes calls the given function for every node the tree is made of, from
// the root down, and returns the error of the first call that fails.
//
// A node kept inside its parent is not a node of the database and has no hash
// of its own, and is passed over.
func walkNodes(tree *trie.Trie, visit func(hash common.Hash, blob []byte) error) error {
	nodeIt, err := iteratorOfGeth(tree)
	if err != nil {
		return err
	}
	for {
		var (
			hash common.Hash
			blob []byte
			ok   bool
		)
		if err := catchingThePanicOfGeth(func() {
			if ok = nodeIt.Next(true); ok {
				hash, blob = nodeIt.Hash(), nodeIt.NodeBlob()
			}
		}); err != nil {
			return err
		}
		if !ok {
			return nodeIt.Error()
		}
		if hash == (common.Hash{}) {
			continue
		}
		if err := visit(hash, blob); err != nil {
			return err
		}
	}
}

// iteratorOfGeth returns the iterator over the nodes of the tree, catching what
// the walk of go-ethereum panics over.
func iteratorOfGeth(tree *trie.Trie) (nodeIt trie.NodeIterator, err error) {
	if err := catchingThePanicOfGeth(func() {
		nodeIt, err = tree.NodeIterator(nil)
	}); err != nil {
		return nil, err
	}
	return nodeIt, err
}

// catchingThePanicOfGeth runs one step of the walk of go-ethereum over the
// nodes of a tree and turns a panic of it into an error. A walk of the state of
// a channel has to be able to report what it found, and the walk of go-ethereum
// stops in a panic rather than in an error when a node of the tree is not in the
// database, or is in it as something that is not a node of any tree.
//
// Only the walk of go-ethereum is run under it. A panic of the caller is the
// caller's own and is left to it: the caller here is a check of the state, and a
// check of its own going wrong says nothing whatever about the state, while
// reported as corruption it sends an operator to a database that is whole.
func catchingThePanicOfGeth(step func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("the tree of the channel is not the tree its nodes are said to be: %v", recovered)
		}
	}()
	step()
	return nil
}
