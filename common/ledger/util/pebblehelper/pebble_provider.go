/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package pebblehelper

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/pkg/errors"
)

const (
	internalDBName = "_"
	maxBatchSize   = 1000000
)

var (
	dbNameKeySep     = []byte{0x00}
	lastKeyIndicator = byte(0x01)
	formatVersionKey = []byte{'f'}
)

// Conf configuration for PebbleProvider.
type Conf struct {
	DBPath         string
	ExpectedFormat string
}

// DataFormatInfo contains the information about the version of the data format.
type DataFormatInfo struct {
	FormatVerison string
	IsDBEmpty     bool
}

// RetrieveDataFormatInfo retrieves the DataFormatInfo for the db at the supplied dbPath.
func RetrieveDataFormatInfo(dbPath string) (*DataFormatInfo, error) {
	db := CreateDB(&Conf{DBPath: dbPath})
	db.Open()
	defer db.Close()

	dbEmpty, err := db.IsEmpty()
	if err != nil {
		return nil, err
	}

	db.dbName = internalDBName
	internalDB := &PebbleDBHandle{
		db:     db,
		dbName: internalDBName,
	}

	formatVersion, err := internalDB.Get(formatVersionKey)
	if err != nil {
		return nil, err
	}

	return &DataFormatInfo{
		IsDBEmpty:     dbEmpty,
		FormatVerison: string(formatVersion),
	}, nil
}

// PebbleProvider enables using a single pebble db as multiple logical databases.
type PebbleProvider struct {
	db *PebbleDB

	mux       sync.Mutex
	dbHandles map[string]*PebbleDBHandle
}

var _ db.Provider = (*PebbleProvider)(nil)

// NewProvider constructs a PebbleProvider.
func NewProvider(conf *Conf) (*PebbleProvider, error) {
	db, err := openDBAndCheckFormat(conf)
	if err != nil {
		return nil, err
	}
	return &PebbleProvider{
		db:        db,
		dbHandles: make(map[string]*PebbleDBHandle),
	}, nil
}

func openDBAndCheckFormat(conf *Conf) (d *PebbleDB, e error) {
	db := CreateDB(conf)
	db.Open()

	defer func() {
		if e != nil {
			db.Close()
		}
	}()

	db.dbName = internalDBName
	internalDB := &PebbleDBHandle{
		db:     db,
		dbName: internalDBName,
	}

	dbEmpty, err := db.IsEmpty()
	if err != nil {
		return nil, err
	}

	if dbEmpty && conf.ExpectedFormat != "" {
		logger.Infof("DB is empty. Setting db format as %s", conf.ExpectedFormat)
		if err := internalDB.Put(formatVersionKey, []byte(conf.ExpectedFormat), true); err != nil {
			return nil, err
		}
		return db, nil
	}

	formatVersion, err := internalDB.Get(formatVersionKey)
	if err != nil {
		return nil, err
	}
	logger.Debugf("Checking for db format at path [%s]", conf.DBPath)

	if !bytes.Equal(formatVersion, []byte(conf.ExpectedFormat)) {
		logger.Errorf("The db at path [%s] contains data in unexpected format. expected data format = [%s] (%#v), data format = [%s] (%#v).",
			conf.DBPath, conf.ExpectedFormat, []byte(conf.ExpectedFormat), formatVersion, formatVersion)
		return nil, &dataformat.ErrFormatMismatch{
			ExpectedFormat: conf.ExpectedFormat,
			Format:         string(formatVersion),
			DBInfo:         fmt.Sprintf("pebble db at [%s]", conf.DBPath),
		}
	}
	logger.Debug("format is latest, nothing to do")
	return db, nil
}

// GetDataFormat returns the format of the data.
func (p *PebbleProvider) GetDataFormat() (string, error) {
	f, err := p.getDBHandle(internalDBName).Get(formatVersionKey)
	return string(f), err
}

// SetDataFormat sets the data format.
func (p *PebbleProvider) SetDataFormat(format string) error {
	db := p.getDBHandle(internalDBName)
	return db.Put(formatVersionKey, []byte(format), true)
}

func (p *PebbleProvider) getDBHandle(dbName string) *PebbleDBHandle {
	p.mux.Lock()
	defer p.mux.Unlock()
	dbHandle := p.dbHandles[dbName]
	if dbHandle == nil {
		closeFunc := func() {
			p.mux.Lock()
			defer p.mux.Unlock()
			delete(p.dbHandles, dbName)
		}
		dbHandle = &PebbleDBHandle{dbName, p.db, closeFunc}
		p.dbHandles[dbName] = dbHandle
	}
	return dbHandle
}

// GetDBHandle returns a handle to a named db.
func (p *PebbleProvider) GetDBHandle(dbName string) db.DBHandle {
	return p.getDBHandle(dbName)
}

// Close closes the underlying pebble db.
func (p *PebbleProvider) Close() {
	p.db.Close()
}

// Drop drops all the data for the given dbName.
func (p *PebbleProvider) Drop(dbName string) error {
	dbHandle := p.getDBHandle(dbName)
	defer dbHandle.Close()
	return dbHandle.deleteAll()
}

// PebbleDBHandle is a handle to a named db.
type PebbleDBHandle struct {
	dbName    string
	db        *PebbleDB
	closeFunc func()
}

var _ db.DBHandle = (*PebbleDBHandle)(nil)

// Get returns the value for the given key.
func (h *PebbleDBHandle) Get(key []byte) ([]byte, error) {
	return h.db.Get(constructLevelKey(h.dbName, key))
}

// Put saves the key/value.
func (h *PebbleDBHandle) Put(key []byte, value []byte, sync bool) error {
	return h.db.Put(constructLevelKey(h.dbName, key), value, sync)
}

// Delete deletes the given key.
func (h *PebbleDBHandle) Delete(key []byte, sync bool) error {
	return h.db.Delete(constructLevelKey(h.dbName, key), sync)
}

func (h *PebbleDBHandle) isDBOpen() bool {
	return h.db.db != nil && h.db.dbState == opened
}

// deleteAll deletes all the keys that belong to the channel (dbName).
func (h *PebbleDBHandle) deleteAll() error {
	if !h.isDBOpen() {
		return errors.Errorf("pebble db at path [%s] is closed", h.db.conf.DBPath)
	}

	sKey := constructLevelKey(h.dbName, nil)
	eKey := constructLevelKey(h.dbName, nil)
	if h.dbName != "" {
		eKey[len(eKey)-1] = lastKeyIndicator
	}

	db := h.db.db
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: sKey,
		UpperBound: eKey,
	})
	if err != nil {
		return errors.Wrap(err, "internal pebble error while obtaining db iterator")
	}
	defer iter.Close()

	numKeys := 0
	batchSize := 0
	batch := db.NewBatch()
	for iter.First(); iter.Valid(); iter.Next() {
		if err = iter.Error(); err != nil {
			return errors.Wrap(err, "internal pebble error while retrieving data from db iterator")
		}
		key := iter.Key()
		numKeys++
		batchSize = batchSize + len(key)
		batch.Delete(key, pebble.NoSync)
		if batchSize >= maxBatchSize {
			if err = batch.Commit(pebble.Sync); err != nil {
				return err
			}
			logger.Infof("Have removed %d entries for channel %s in pebble db %s", numKeys, h.dbName, h.db.conf.DBPath)
			batchSize = 0
			batch.Reset()
		}
	}
	if batch.Len() > 0 {
		return batch.Commit(pebble.Sync)
	}
	return nil
}

// IsEmpty returns true if no data exists for the DBHandle.
func (h *PebbleDBHandle) IsEmpty() (bool, error) {
	if !h.isDBOpen() {
		return false, errors.Errorf("pebble db at path [%s] is closed", h.db.conf.DBPath)
	}

	sKey := constructLevelKey(h.dbName, nil)
	eKey := constructLevelKey(h.dbName, nil)
	if h.dbName != "" {
		eKey[len(eKey)-1] = lastKeyIndicator
	}

	db := h.db.db
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: sKey,
		UpperBound: eKey,
	})
	if err != nil {
		return false, errors.Wrap(err, "internal pebble error while obtaining db iterator")
	}
	defer iter.Close()

	return !iter.First(), nil
}

// NewUpdateBatch returns a new PebbleBatch that can be used to update the db.
func (h *PebbleDBHandle) NewUpdateBatch() db.Batch {
	return &PebbleBatch{
		dbName: h.dbName,
		batch:  h.db.db.NewBatch(),
	}
}

// WriteBatch writes a batch in an atomic way.
func (h *PebbleDBHandle) WriteBatch(batch db.Batch, sync bool) error {
	pb, ok := batch.(*PebbleBatch)
	if !ok || pb == nil || pb.batch.Len() == 0 {
		return nil
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	return pb.batch.Commit(opts)
}

// GetIterator gets a handle to an iterator. The iterator should be released after the use.
// The resultset contains all the keys that are present in the db between the startKey (inclusive) and the endKey (exclusive).
// A nil startKey represents the first available key and a nil endKey represents a logical key after the last available key.
func (h *PebbleDBHandle) GetIterator(startKey []byte, endKey []byte) (db.Iterator, error) {
	if !h.isDBOpen() {
		return nil, errors.Errorf("pebble db at path [%s] is closed", h.db.conf.DBPath)
	}

	sKey := constructLevelKey(h.dbName, startKey)
	eKey := constructLevelKey(h.dbName, endKey)
	if endKey == nil && h.dbName != "" {
		eKey[len(eKey)-1] = lastKeyIndicator
	}
	logger.Debugf("Getting iterator for range [%#v] - [%#v]", sKey, eKey)
	iter, err := h.db.db.NewIter(&pebble.IterOptions{
		LowerBound: sKey,
		UpperBound: eKey,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "internal pebble error while obtaining db iterator")
	}
	return &PebbleIterator{dbName: h.dbName, iter: iter}, nil
}

// Close closes the DBHandle after its db data have been deleted.
func (h *PebbleDBHandle) Close() {
	if h.closeFunc != nil {
		h.closeFunc()
	}
}

func constructLevelKey(dbName string, key []byte) []byte {
	if len(dbName) == 0 {
		if key == nil {
			return nil
		}
		k := make([]byte, len(key))
		copy(k, key)
		return k
	}
	return append(append([]byte(dbName), dbNameKeySep...), key...)
}

func retrieveAppKey(levelKey []byte) []byte {
	return bytes.SplitN(levelKey, dbNameKeySep, 2)[1]
}
