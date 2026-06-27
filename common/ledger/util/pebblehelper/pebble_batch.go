/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"github.com/cockroachdb/pebble/v2"
	db "github.com/hyperledger/fabric/common/ledger"
)

// PebbleBatch encloses the details of multiple updates.
type PebbleBatch struct {
	batch  *pebble.Batch
	dbName string
	size   int
}

var _ db.Batch = (*PebbleBatch)(nil)

// NewPebbleBatch constructs a new PebbleBatch linked to the given pebble DB.
func NewPebbleBatch(dbName string, pdb *pebble.DB) *PebbleBatch {
	return &PebbleBatch{
		batch:  pdb.NewBatch(),
		dbName: dbName,
	}
}

// Put adds a KV pair to the batch.
func (b *PebbleBatch) Put(key []byte, value []byte) {
	k := constructLevelKey(b.dbName, key)
	b.batch.Set(k, value, pebble.NoSync)
	b.size += len(k) + len(value)
}

// Delete deletes a Key and associated value from the batch.
func (b *PebbleBatch) Delete(key []byte) {
	k := constructLevelKey(b.dbName, key)
	b.batch.Delete(k, pebble.NoSync)
	b.size += len(k)
}

// Len returns the number of records in the batch.
func (b *PebbleBatch) Len() int {
	return int(b.batch.Count())
}

// Size returns the accumulated size of the batch.
func (b *PebbleBatch) Size() int {
	return b.size
}

// Reset resets the batch.
func (b *PebbleBatch) Reset() {
	b.batch.Reset()
	b.size = 0
}
