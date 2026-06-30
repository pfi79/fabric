/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"github.com/cockroachdb/pebble/v2"
	"github.com/hyperledger/fabric/common/ledger/util/db"
)

// PebbleIterator wraps a pebble.Iterator and implements db.Iterator.
type PebbleIterator struct {
	dbName  string
	iter    *pebble.Iterator
	started bool
}

var _ db.Iterator = (*PebbleIterator)(nil)

// Next moves the iterator to the next key/value pair.
// The first call to Next positions at the first key, subsequent calls advance.
func (itr *PebbleIterator) Next() bool {
	if !itr.started {
		itr.started = true
		return itr.iter.First()
	}
	return itr.iter.Next()
}

// Key returns the key of the current key/value pair.
func (itr *PebbleIterator) Key() []byte {
	if itr.dbName == "" {
		return itr.iter.Key()
	}
	return retrieveAppKey(itr.iter.Key())
}

// Value returns the value of the current key/value pair.
func (itr *PebbleIterator) Value() []byte {
	return itr.iter.Value()
}

// Error returns any accumulated error.
func (itr *PebbleIterator) Error() error {
	return itr.iter.Error()
}

// Release closes the iterator.
func (itr *PebbleIterator) Release() {
	itr.iter.Close()
}

// Seek moves the iterator to the first key/value pair whose key is greater than or equal to the given key.
// It returns whether such a pair exists.
func (itr *PebbleIterator) Seek(key []byte) bool {
	itr.started = true
	var pebbleKey []byte
	if itr.dbName == "" {
		pebbleKey = key
	} else {
		pebbleKey = constructLevelKey(itr.dbName, key)
	}
	return itr.iter.SeekGE(pebbleKey)
}

// Prev moves the iterator to the prev key/value pair.
// The first call to Prev positions at the last key, subsequent calls advance.
func (itr *PebbleIterator) Prev() bool {
	if !itr.started {
		itr.started = true
		return itr.iter.Last()
	}
	return itr.iter.Prev()
}
