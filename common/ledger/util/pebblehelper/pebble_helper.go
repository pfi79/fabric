/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric/common/ledger/util/db"
	"github.com/hyperledger/fabric/internal/fileutil"
	"github.com/pkg/errors"
)

var logger = flogging.MustGetLogger("pebblehelper")

type dbState int32

const (
	closed dbState = iota
	opened
)

// PebbleDB is a wrapper on an actual pebble store implementing db.DB.
type PebbleDB struct {
	conf    *Conf
	db      *pebble.DB
	dbName  string
	dbState dbState
	mutex   sync.RWMutex
}

var _ db.DB = (*PebbleDB)(nil)

// CreateDB constructs a PebbleDB.
func CreateDB(conf *Conf) *PebbleDB {
	return &PebbleDB{
		conf:    conf,
		dbState: closed,
	}
}

// Open opens the underlying pebble db.
func (dbInst *PebbleDB) Open() {
	dbInst.mutex.Lock()
	defer dbInst.mutex.Unlock()
	if dbInst.dbState == opened {
		return
	}
	if _, err := fileutil.CreateDirIfMissing(dbInst.conf.DBPath); err != nil {
		panic(fmt.Sprintf("Error creating dir if missing: %s", err))
	}
	var err error
	dbInst.db, err = pebble.Open(dbInst.conf.DBPath, &pebble.Options{})
	if err != nil {
		panic(fmt.Sprintf("Error opening pebble db: %s", err))
	}
	dbInst.dbState = opened
}

// IsEmpty returns whether or not a database is empty.
func (dbInst *PebbleDB) IsEmpty() (bool, error) {
	dbInst.mutex.RLock()
	defer dbInst.mutex.RUnlock()
	if dbInst.db != nil && dbInst.dbState != opened {
		return false, errors.Errorf("pebble db at path [%s] is closed", dbInst.conf.DBPath)
	}
	iter, err := dbInst.db.NewIter(nil)
	if err != nil {
		return false, err
	}
	defer iter.Close()
	hasItems := iter.First()
	return !hasItems,
		errors.Wrapf(iter.Error(), "error while trying to see if the pebble db at path [%s] is empty", dbInst.conf.DBPath)
}

// Close closes the underlying pebble db.
func (dbInst *PebbleDB) Close() {
	dbInst.mutex.Lock()
	defer dbInst.mutex.Unlock()
	if dbInst.dbState == closed {
		return
	}
	if err := dbInst.db.Close(); err != nil {
		logger.Errorf("Error closing pebble db: %s", err)
	}
	dbInst.dbState = closed
}

// Get returns the value for the given key.
func (dbInst *PebbleDB) Get(key []byte) ([]byte, error) {
	dbInst.mutex.RLock()
	defer dbInst.mutex.RUnlock()
	if dbInst.db != nil && dbInst.dbState != opened {
		return nil, errors.Errorf("pebble db at path [%s] is closed", dbInst.conf.DBPath)
	}
	value, closer, err := dbInst.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		logger.Errorf("Error retrieving pebble key [%#v]: %s", key, err)
		return nil, errors.Wrapf(err, "error retrieving pebble key [%#v]", key)
	}
	defer closer.Close()
	if value == nil {
		return []byte{}, nil
	}
	val := make([]byte, len(value))
	copy(val, value)
	return val, nil
}

// Put saves the key/value.
func (dbInst *PebbleDB) Put(key []byte, value []byte, sync bool) error {
	dbInst.mutex.RLock()
	defer dbInst.mutex.RUnlock()
	if dbInst.db != nil && dbInst.dbState != opened {
		return errors.Errorf("pebble db at path [%s] is closed", dbInst.conf.DBPath)
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	err := dbInst.db.Set(key, value, opts)
	if err != nil {
		logger.Errorf("Error writing pebble key [%#v]", key)
		return errors.Wrapf(err, "error writing pebble key [%#v]", key)
	}
	return nil
}

// Delete deletes the given key.
func (dbInst *PebbleDB) Delete(key []byte, sync bool) error {
	dbInst.mutex.RLock()
	defer dbInst.mutex.RUnlock()
	if dbInst.db != nil && dbInst.dbState != opened {
		return errors.Errorf("pebble db at path [%s] is closed", dbInst.conf.DBPath)
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	err := dbInst.db.Delete(key, opts)
	if err != nil {
		logger.Errorf("Error deleting pebble key [%#v]", key)
		return errors.Wrapf(err, "error deleting pebble key [%#v]", key)
	}
	return nil
}

// GetIterator returns an iterator over key-value store. The iterator should be released after the use.
// The resultset contains all the keys that are present in the db between the startKey (inclusive) and the endKey (exclusive).
// A nil startKey represents the first available key and a nil endKey represents a logical key after the last available key.
func (dbInst *PebbleDB) GetIterator(startKey []byte, endKey []byte) (db.Iterator, error) {
	dbInst.mutex.RLock()
	defer dbInst.mutex.RUnlock()
	if dbInst.db != nil && dbInst.dbState != opened {
		return nil, errors.Errorf("pebble db at path [%s] is closed", dbInst.conf.DBPath)
	}
	iter, err := dbInst.db.NewIter(&pebble.IterOptions{
		LowerBound: startKey,
		UpperBound: endKey,
	})
	if err != nil {
		return nil, err
	}
	return &PebbleIterator{iter: iter}, nil
}

// NewUpdateBatch creates a new batch for the DB.
func (dbInst *PebbleDB) NewUpdateBatch() db.Batch {
	return &PebbleBatch{
		dbName: dbInst.dbName,
		batch:  dbInst.db.NewBatch(),
	}
}

// WriteBatch writes a batch.
func (dbInst *PebbleDB) WriteBatch(batch db.Batch, sync bool) error {
	pb, ok := batch.(*PebbleBatch)
	if !ok {
		return errors.Errorf("expected *PebbleBatch, got %T", batch)
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	return pb.batch.Commit(opts)
}
