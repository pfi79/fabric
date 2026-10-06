/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
)

// The keys under which a channel's database says which version of the state it
// holds. All of them are kept under one prefix, and what is under that prefix is
// the whole of what the database says about the state of the channel. The prefix
// keeps them apart from the tree nodes, which nodedb.go keeps under a prefix of
// its own, and apart from the keys of the state itself. Anything reading the
// layout of such a database, the migrator out of another version of this store
// among it, has one prefix to read and one place to read it from.
const metaKeyPrefix = 'm'

// The names the parts of the metadata are kept under, each of them under the
// prefix above.
const (
	formatKey      = 'f'
	stateRootKey   = 'r'
	savePointKey   = 'm'
	rootHistoryKey = 'h'
	liveNodesKey   = 'l'
)

// metaKey returns the key the piece of the metadata of the given name is kept
// under: the prefix of the metadata and the name of the piece under it.
func metaKey(name byte) []byte {
	return []byte{metaKeyPrefix, name}
}

// dbFormatVersion is the number this version of the store writes under the
// format key of every database it commits to, and the only number it reads.
//
// The layout of the tree in the database, and with it the root the channel is
// committed at, is a matter of the shape of the tree rather than of this store:
// go-ethereum decides it, and decides it the same way for everything that
// holds one. What the number is for is the rest of the database, where this
// store keeps what the tree does not say. A database written by another version
// of this store has its state somewhere this version does not look, and
// reading it as if it were here would hand out a state that is not the one the
// channel was committed to. So a mismatch is refused rather than read.
const dbFormatVersion = "1"

// stateMetadata is everything a channel's database holds about its state
// besides the state itself.
//
// It is a field of its own on the database so that the layout of this metadata
// can grow without the rest of the package having to be told: reading the state
// means asking this, and asking this is the whole of what the rest of the
// package knows about where a version is written down.
type stateMetadata struct {
	handle db.DBHandle
	dbName string
}

// checkFormat returns an error unless the database holds state this version of
// the store wrote. A database that names no format at all is refused as well:
// it is one of a version that wrote no format, and its state cannot be told
// apart from state this version would have written somewhere else.
func (m *stateMetadata) checkFormat() error {
	blob, err := m.handle.Get(metaKey(formatKey))
	if err != nil {
		return err
	}
	if string(blob) == dbFormatVersion {
		return nil
	}
	return fmt.Errorf(
		"the state of channel [%s] is in a format this version of leveldbtrie cannot read: %w",
		m.dbName,
		&dataformat.ErrFormatMismatch{
			DBInfo:         fmt.Sprintf("state leveldb of channel [%s]", m.dbName),
			ExpectedFormat: dbFormatVersion,
			Format:         string(blob),
		},
	)
}

// markFormat writes the format of this version into a database that holds
// nothing. A channel is of the current format from the moment it is opened, so
// that nothing can be committed to it behind the back of the check above.
func (m *stateMetadata) markFormat() error {
	batch := m.handle.NewUpdateBatch()
	writeFormatTo(batch)
	return m.handle.WriteBatch(batch, true)
}

// stateRoot returns the root of the tree the channel is committed at. A channel
// that has never been committed to holds the root of a tree with no entries,
// which is the one root every implementation of the tree agrees on, and which
// leaves nothing in the database to address.
func (m *stateMetadata) stateRoot() (common.Hash, error) {
	blob, err := m.handle.Get(metaKey(stateRootKey))
	if err != nil {
		return common.Hash{}, err
	}
	if len(blob) == 0 {
		return types.EmptyRootHash, nil
	}
	return common.BytesToHash(blob), nil
}

// savePoint returns the height of the highest transaction the state is
// consistent upto, or nil if the channel has never been committed to.
func (m *stateMetadata) savePoint() (*version.Height, error) {
	blob, err := m.handle.Get(metaKey(savePointKey))
	if err != nil {
		return nil, err
	}
	if len(blob) == 0 {
		return nil, nil
	}
	height, _, err := version.NewHeightFromBytes(blob)
	if err != nil {
		return nil, err
	}
	return height, nil
}

// rootHistory returns the roots of the versions the channel has been committed
// at, the newest first, the newest being the root the channel is committed at.
func (m *stateMetadata) rootHistory() ([]common.Hash, error) {
	blob, err := m.handle.Get(metaKey(rootHistoryKey))
	if err != nil {
		return nil, err
	}
	return decodeRootHistory(blob)
}

// liveNodes returns the number of nodes the tree of the channel was found to
// consist of when it was last walked, or nil if it has never been walked.
func (m *stateMetadata) liveNodes() (*int, error) {
	blob, err := m.handle.Get(metaKey(liveNodesKey))
	if err != nil {
		return nil, err
	}
	if len(blob) == 0 {
		return nil, nil
	}
	count, err := strconv.Atoi(string(blob))
	if err != nil {
		return nil, fmt.Errorf("the database holds [%s] as the number of live nodes of channel [%s]: %w", blob, m.dbName, err)
	}
	return &count, nil
}

// writeTo adds the metadata of the given commit to the batch. A nil height is
// not a savepoint: it denotes the committing of the missing private data of old
// blocks, which moves the state without moving the height the state is
// consistent upto.
//
// The root is written whether it is the root of a tree with entries or the root
// of one with none. A tree emptied of its entries leaves the root of the tree it
// used to be in the database otherwise, and a channel opened on that root would
// read the state it used to have.
func (m *stateMetadata) writeTo(batch db.Batch, root common.Hash, height *version.Height, keepRoots int) error {
	history, err := m.rootHistory()
	if err != nil {
		return err
	}

	writeFormatTo(batch)
	batch.Put(metaKey(stateRootKey), root[:])
	batch.Put(metaKey(rootHistoryKey), encodeRootHistory(trimRoots(append([]common.Hash{root}, history...), keepRoots)))
	if height != nil {
		m.writeSavePointTo(batch, height)
	}
	return nil
}

// writeSavePointTo adds the given savepoint to the batch, and nothing else.
func (m *stateMetadata) writeSavePointTo(batch db.Batch, height *version.Height) {
	batch.Put(metaKey(savePointKey), height.ToBytes())
}

// writeRestoreTo adds what the restore of a snapshot that holds no public state
// leaves in the database: the height the state is consistent upto, and the format
// of this version. The state itself is left alone, but the database is not the
// empty one a channel that was never opened is any more, and it has to say which
// version of the store wrote it. A channel restored as a database of no known
// format is a channel that cannot be opened at all.
func (m *stateMetadata) writeRestoreTo(batch db.Batch, height *version.Height) {
	writeFormatTo(batch)
	m.writeSavePointTo(batch, height)
}

// writeLiveNodesTo adds the number of nodes the tree of the channel consists
// of to the batch, and nothing else.
func (m *stateMetadata) writeLiveNodesTo(batch db.Batch, live int) {
	batch.Put(metaKey(liveNodesKey), []byte(strconv.Itoa(live)))
}

// writeFormatTo adds the format of this version to the batch.
func writeFormatTo(batch db.Batch) {
	batch.Put(metaKey(formatKey), []byte(dbFormatVersion))
}

// trimRoots returns the given roots, the newest first, no more of them than the
// store is to keep. The root itself is never trimmed away: a version the store
// cannot be opened at is of no use to anybody.
func trimRoots(roots []common.Hash, keepRoots int) []common.Hash {
	if keepRoots < 1 {
		keepRoots = 1
	}
	if len(roots) > keepRoots {
		return roots[:keepRoots]
	}
	return roots
}

// encodeRootHistory returns the bytes the given roots are written under, the
// newest first. The roots are written as their hashes, one after another, so
// that the newest is at the front of the blob and needs nothing of the rest to
// be read.
func encodeRootHistory(roots []common.Hash) []byte {
	blob := make([]byte, 0, common.HashLength*len(roots))
	for _, root := range roots {
		blob = append(blob, root[:]...)
	}
	return blob
}

// decodeRootHistory reads back what encodeRootHistory wrote. A blob that is not
// a whole number of hashes is reported rather than read as far as it goes: what
// it holds was not written as a history of roots.
func decodeRootHistory(blob []byte) ([]common.Hash, error) {
	if len(blob)%common.HashLength != 0 {
		return nil, fmt.Errorf("the database holds [%d] bytes of root history, which is not a whole number of roots", len(blob))
	}
	roots := make([]common.Hash, 0, len(blob)/common.HashLength)
	for offset := 0; offset < len(blob); offset += common.HashLength {
		roots = append(roots, common.BytesToHash(blob[offset:offset+common.HashLength]))
	}
	return roots, nil
}
