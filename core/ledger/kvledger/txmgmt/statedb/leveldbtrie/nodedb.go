/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package leveldbtrie holds the world state of a channel in a patricia merkle
// trie whose nodes are kept in LevelDB. The shape of the tree belongs to
// go-ethereum; what belongs to this package is where the nodes of that tree
// live. This file owns one decision and nothing else: a node is kept in the
// LevelDB that already holds the channel, under its own hash. The layout of
// that key is unexported, so that nothing else in the package ever assembles a
// key by hand.
package leveldbtrie

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb/database"
	db "github.com/hyperledger/fabric/common/ledger"
)

// nodeKeyPrefix keeps the trie nodes apart from everything else a channel's
// database holds.
const nodeKeyPrefix = 'n'

// NodeDB is the node store of the tries of one channel.
//
// A node is addressed by its own hash alone; the path takes no part in the
// address, and neither does the owner. Two tries sharing a database therefore
// must not have a node in common, which holds because every channel has a
// database of its own.
type NodeDB struct {
	handle db.DBHandle
}

var _ database.NodeDatabase = (*NodeDB)(nil)

// NewNodeDB returns the node store kept in the given handle.
func NewNodeDB(handle db.DBHandle) *NodeDB {
	return &NodeDB{handle: handle}
}

// NodeReader returns a reader for the nodes of the trie with the given root.
//
// The root node has to be in the database. Opening a channel on a root the
// database does not hold must say so rather than hand out a reader that reads
// nothing, because that reader would present the channel as an empty state
// rather than as a broken one.
func (d *NodeDB) NodeReader(stateRoot common.Hash) (database.NodeReader, error) {
	blob, err := d.handle.Get(nodeKey(stateRoot[:]))
	if err != nil {
		return nil, fmt.Errorf("cannot open the trie of root %#x: %w", stateRoot, err)
	}
	if len(blob) == 0 {
		return nil, fmt.Errorf("cannot open the trie of root %#x: the database holds no node under it", stateRoot)
	}
	return &nodeReader{db: d}, nil
}

// Commit writes the nodes a trie commit collected into the database. The set is
// what names every node by its hash, and is the only way in.
//
// A commit that has more to say about the version of the tree than which nodes
// it is made of writes the nodes with CommitInto instead, so that the nodes and
// what is said of them reach the database in one transaction.
func (d *NodeDB) Commit(set *trienode.NodeSet) error {
	if set == nil {
		return nil
	}
	batch := d.handle.NewUpdateBatch()
	d.CommitInto(batch, set)
	return d.handle.WriteBatch(batch, true)
}

// CommitInto adds the nodes a trie commit collected to the given batch. The
// batch is the caller's to write, and to write together with whatever else
// belongs to the same version of the tree.
//
// A node the set marks as deleted carries no hash to address it by, so it is
// left where it is. Reclaiming the nodes an older version no longer reaches
// is the business of the collector of garbage, which walks the versions and
// not the node sets.
func (d *NodeDB) CommitInto(batch db.Batch, set *trienode.NodeSet) {
	if set == nil {
		return
	}
	for _, node := range set.Nodes {
		if node.IsDeleted() {
			continue
		}
		batch.Put(nodeKey(node.Hash[:]), node.Blob)
	}
}

// nodeReader reads the nodes of one channel out of the database.
type nodeReader struct {
	db *NodeDB
}

var _ database.NodeReader = (*nodeReader)(nil)

// Node returns the blob of the node with the given hash. A node that is not in
// the database is not an error here: the reader of go-ethereum turns an empty
// blob into a missing node error of its own, and that error names the path
// beside the hash, which this method cannot know.
func (r *nodeReader) Node(owner common.Hash, path []byte, hash common.Hash) ([]byte, error) {
	return r.db.handle.Get(nodeKey(hash[:]))
}

// nodeKey returns the key a node with the given hash is kept under. The hash is
// the whole of the address; the prefix is what tells a node from the metadata
// around it.
func nodeKey(hash []byte) []byte {
	return append([]byte{nodeKeyPrefix}, hash...)
}

// nodeHash returns the hash the node kept under the given key is called by. It
// is the other half of nodeKey, and it is here so that nothing outside this file
// has to know how the two are put together: whoever walks the keys of the nodes
// and wants to know which node each of them is asks, rather than counting bytes
// off the front of a key.
//
// A key that is not the key of a node is reported rather than read as the name of
// a node: nothing else is kept under the prefix of a node, and a key there that
// is of no shape this store wrote is a database that is not what it says.
func (d *NodeDB) nodeHash(key []byte) (common.Hash, bool) {
	if len(key) != 1+common.HashLength || key[0] != nodeKeyPrefix {
		return common.Hash{}, false
	}
	return common.BytesToHash(key[1:]), true
}

// nodeKeyRange returns the range of keys every node of the channel is kept
// under. Walking it walks the nodes and nothing else, which is what the
// collector of garbage needs and what nothing else has any business doing.
func nodeKeyRange() (start, end []byte) {
	return []byte{nodeKeyPrefix}, []byte{nodeKeyPrefix + 1}
}
