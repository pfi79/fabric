/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package validation

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/privacyenabledstate"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/rwsetutil"
)

// block is used to used to hold the information from its proto format to a structure
// that is more suitable/friendly for validation
type block struct {
	num uint64
	txs []*transaction
}

// transaction is used to hold the information from its proto format to a structure
// that is more suitable/friendly for validation
type transaction struct {
	indexInBlock            int
	id                      string
	rwset                   *rwsetutil.TxRwSet
	validationCode          peer.TxValidationCode
	containsPostOrderWrites bool
	// updates holds what this transaction writes, apart from the updates of the
	// block as a whole, which is what the boundaries between the transactions of
	// a block are made of. It is filled in as the transaction is prepared, and it
	// stays nil for a transaction that is invalid or that writes nothing.
	updates *privacyenabledstate.UpdateBatch
}

// updatesForTx returns the batch of the updates of this transaction, and makes
// one if this is the first update written into it.
func (t *transaction) updatesForTx() *privacyenabledstate.UpdateBatch {
	if t.updates == nil {
		t.updates = privacyenabledstate.NewUpdateBatch()
	}
	return t.updates
}

// publicAndHashUpdates encapsulates public and hash updates. The intended use of this to hold the updates
// that are to be applied to the statedb  as a result of the block commit
type publicAndHashUpdates struct {
	publicUpdates *privacyenabledstate.PubUpdateBatch
	hashUpdates   *privacyenabledstate.HashedUpdateBatch
}

// newPubAndHashUpdates constructs an empty PubAndHashUpdates
func newPubAndHashUpdates() *publicAndHashUpdates {
	return &publicAndHashUpdates{
		privacyenabledstate.NewPubUpdateBatch(),
		privacyenabledstate.NewHashedUpdateBatch(),
	}
}

// containsPvtWrites returns true if this transaction is not limited to affecting the public data only
func (t *transaction) containsPvtWrites() bool {
	for _, ns := range t.rwset.NsRwSets {
		for _, coll := range ns.CollHashedRwSets {
			if coll.PvtRwSetHash != nil {
				return true
			}
		}
	}
	return false
}

// retrieveHash returns the hash of the private write-set present
// in the public data for a given namespace-collection
func (t *transaction) retrieveHash(ns string, coll string) []byte {
	if t.rwset == nil {
		return nil
	}
	for _, nsData := range t.rwset.NsRwSets {
		if nsData.NameSpace != ns {
			continue
		}

		for _, collData := range nsData.CollHashedRwSets {
			if collData.CollectionName == coll {
				return collData.PvtRwSetHash
			}
		}
	}
	return nil
}

// applyWriteSet adds (or deletes) the key/values present in the write set to the
// publicAndHashUpdates, and to the given batch as well, which holds what this
// transaction writes and nothing else.
//
// Both are given the very same values: a value is held twice, never copied.
func (u *publicAndHashUpdates) applyWriteSet(
	txRWSet *rwsetutil.TxRwSet,
	txHeight *version.Height,
	db *privacyenabledstate.DB,
	containsPostOrderWrites bool,
	txUpdates *privacyenabledstate.UpdateBatch,
) error {
	u.publicUpdates.ContainsPostOrderWrites =
		u.publicUpdates.ContainsPostOrderWrites || containsPostOrderWrites
	txops, err := prepareTxOps(txRWSet, u, db)
	logger.Debugf("txops=%#v", txops)
	if err != nil {
		return err
	}
	applyTxOps(txops, u.publicUpdates, u.hashUpdates, txHeight)
	txUpdates.PubUpdates.ContainsPostOrderWrites = containsPostOrderWrites
	applyTxOps(txops, txUpdates.PubUpdates, txUpdates.HashUpdates, txHeight)
	return nil
}

// applyTxOps adds (or deletes) the key/values the given key ops stand for to the
// public and the hash updates
func applyTxOps(
	txops txOps,
	pubUpdates *privacyenabledstate.PubUpdateBatch,
	hashUpdates *privacyenabledstate.HashedUpdateBatch,
	txHeight *version.Height,
) {
	for compositeKey, keyops := range txops {
		if compositeKey.coll == "" {
			ns, key := compositeKey.ns, compositeKey.key
			if keyops.isDelete() {
				pubUpdates.Delete(ns, key, txHeight)
			} else {
				pubUpdates.PutValAndMetadata(ns, key, keyops.value, keyops.metadata, txHeight)
			}
		} else {
			ns, coll, keyHash := compositeKey.ns, compositeKey.coll, []byte(compositeKey.key)
			if keyops.isDelete() {
				hashUpdates.Delete(ns, coll, keyHash, txHeight)
			} else {
				hashUpdates.PutValHashAndMetadata(ns, coll, keyHash, keyops.value, keyops.metadata, txHeight)
			}
		}
	}
}
