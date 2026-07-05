/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package statepebbledb

import (
	"bytes"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	db "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/dataformat"
	"github.com/hyperledger/fabric/common/ledger/util/pebblehelper"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/pkg/errors"
)

var logger = flogging.MustGetLogger("statepebbledb")

var (
	dataKeyPrefix          = []byte{'d'}
	dataKeyStopper         = []byte{'e'}
	nsKeySep               = []byte{0x00}
	lastKeyIndicator       = byte(0x01)
	savePointKey           = []byte{'s'}
	maxDataImportBatchSize = 4 * 1024 * 1024
)

// VersionedDBProvider implements interface VersionedDBProvider
type VersionedDBProvider struct {
	dbProvider *pebblehelper.PebbleProvider
}

// NewVersionedDBProvider instantiates VersionedDBProvider
func NewVersionedDBProvider(dbPath string) (*VersionedDBProvider, error) {
	logger.Debugf("constructing VersionedDBProvider dbPath=%s", dbPath)
	dbProvider, err := pebblehelper.NewProvider(
		&pebblehelper.Conf{
			DBPath:         dbPath,
			ExpectedFormat: dataformat.CurrentFormat,
		},
	)
	if err != nil {
		return nil, err
	}
	return &VersionedDBProvider{dbProvider}, nil
}

// GetDBHandle gets the handle to a named database
func (provider *VersionedDBProvider) GetDBHandle(dbName string, namespaceProvider statedb.NamespaceProvider) (statedb.VersionedDB, error) {
	return newVersionedDB(provider.dbProvider.GetDBHandle(dbName), dbName), nil
}

// ImportFromSnapshot loads the public state and pvtdata hashes from the snapshot files previously generated
func (provider *VersionedDBProvider) ImportFromSnapshot(
	dbName string,
	savepoint *version.Height,
	itr statedb.FullScanIterator,
) error {
	vdb := newVersionedDB(provider.dbProvider.GetDBHandle(dbName), dbName)
	return vdb.importState(itr, savepoint)
}

// BytesKeySupported returns true if a db created supports bytes as a key
func (provider *VersionedDBProvider) BytesKeySupported() bool {
	return true
}

// Close closes the underlying db
func (provider *VersionedDBProvider) Close() {
	provider.dbProvider.Close()
}

// Drop drops channel-specific data from the state pebble db.
// It is not an error if a database does not exist.
func (provider *VersionedDBProvider) Drop(dbName string) error {
	return provider.dbProvider.Drop(dbName)
}

// VersionedDB implements VersionedDB interface
type versionedDB struct {
	db     db.DBHandle
	dbName string
}

// newVersionedDB constructs an instance of VersionedDB
func newVersionedDB(db db.DBHandle, dbName string) *versionedDB {
	return &versionedDB{db, dbName}
}

// Open implements method in VersionedDB interface
func (vdb *versionedDB) Open() error {
	// do nothing because shared db is used
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

// GetState implements method in VersionedDB interface
func (vdb *versionedDB) GetState(namespace string, key string) (*statedb.VersionedValue, error) {
	logger.Debugf("GetState(). ns=%s, key=%s", namespace, key)
	dbVal, err := vdb.db.Get(encodeDataKey(namespace, key))
	if err != nil {
		return nil, err
	}
	if dbVal == nil {
		return nil, nil
	}
	return decodeValue(dbVal)
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
	vals := make([]*statedb.VersionedValue, len(keys))
	for i, key := range keys {
		val, err := vdb.GetState(namespace, key)
		if err != nil {
			return nil, err
		}
		vals[i] = val
	}
	return vals, nil
}

// GetStateRangeScanIterator implements method in VersionedDB interface
// startKey is inclusive
// endKey is exclusive
func (vdb *versionedDB) GetStateRangeScanIterator(namespace string, startKey string, endKey string) (statedb.ResultsIterator, error) {
	// pageSize = 0 denotes unlimited page size
	return vdb.GetStateRangeScanIteratorWithPagination(namespace, startKey, endKey, 0)
}

// GetStateRangeScanIteratorWithPagination implements method in VersionedDB interface
func (vdb *versionedDB) GetStateRangeScanIteratorWithPagination(namespace string, startKey string, endKey string, pageSize int32) (statedb.QueryResultsIterator, error) {
	dataStartKey := encodeDataKey(namespace, startKey)
	dataEndKey := encodeDataKey(namespace, endKey)
	if endKey == "" {
		dataEndKey[len(dataEndKey)-1] = lastKeyIndicator
	}
	dbItr, err := vdb.db.GetIterator(dataStartKey, dataEndKey)
	if err != nil {
		return nil, err
	}
	return newKVScanner(namespace, dbItr, pageSize), nil
}

// ExecuteQuery implements method in VersionedDB interface
func (vdb *versionedDB) ExecuteQuery(namespace, query string) (statedb.ResultsIterator, error) {
	return nil, errors.New("ExecuteQuery not supported for pebbledb")
}

// ExecuteQueryWithPagination implements method in VersionedDB interface
func (vdb *versionedDB) ExecuteQueryWithPagination(namespace, query, bookmark string, pageSize int32) (statedb.QueryResultsIterator, error) {
	return nil, errors.New("ExecuteQueryWithMetadata not supported for pebbledb")
}

// ApplyUpdates implements method in VersionedDB interface
func (vdb *versionedDB) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	dbBatch := vdb.db.NewUpdateBatch()
	namespaces := batch.GetUpdatedNamespaces()
	for _, ns := range namespaces {
		updates := batch.GetUpdates(ns)
		for k, vv := range updates {
			dataKey := encodeDataKey(ns, k)
			logger.Debugf("Channel [%s]: Applying key(string)=[%s] key(bytes)=[%#v]", vdb.dbName, string(dataKey), dataKey)

			if vv.Value == nil {
				dbBatch.Delete(dataKey)
			} else {
				encodedVal, err := encodeValue(vv)
				if err != nil {
					return err
				}
				dbBatch.Put(dataKey, encodedVal)
			}
		}
	}
	if height != nil {
		dbBatch.Put(savePointKey, height.ToBytes())
	}
	return vdb.db.WriteBatch(dbBatch, true)
}

// GetLatestSavePoint implements method in VersionedDB interface
func (vdb *versionedDB) GetLatestSavePoint() (*version.Height, error) {
	versionBytes, err := vdb.db.Get(savePointKey)
	if err != nil {
		return nil, err
	}
	if versionBytes == nil {
		return nil, nil
	}
	version, _, err := version.NewHeightFromBytes(versionBytes)
	if err != nil {
		return nil, err
	}
	return version, nil
}

// GetFullScanIterator implements method in VersionedDB interface
func (vdb *versionedDB) GetFullScanIterator(skipNamespace func(string) bool) (statedb.FullScanIterator, error) {
	return newFullDBScanner(vdb.db, skipNamespace)
}

// importState implements method in VersionedDB interface
func (vdb *versionedDB) importState(itr statedb.FullScanIterator, savepoint *version.Height) error {
	if itr == nil {
		return vdb.db.Put(savePointKey, savepoint.ToBytes(), true)
	}
	dbBatch := vdb.db.NewUpdateBatch()
	batchSize := 0
	for {
		versionedKV, err := itr.Next()
		if err != nil {
			return err
		}
		if versionedKV == nil {
			break
		}
		dbKey := encodeDataKey(versionedKV.Namespace, versionedKV.Key)
		dbValue, err := encodeValue(versionedKV.VersionedValue)
		if err != nil {
			return err
		}
		batchSize += len(dbKey) + len(dbValue)
		dbBatch.Put(dbKey, dbValue)
		if batchSize >= maxDataImportBatchSize {
			if err := vdb.db.WriteBatch(dbBatch, true); err != nil {
				return err
			}
			batchSize = 0
			dbBatch.Reset()
		}
	}
	dbBatch.Put(savePointKey, savepoint.ToBytes())
	return vdb.db.WriteBatch(dbBatch, true)
}

// IsEmpty return true if the statedb does not have any content
func (vdb *versionedDB) IsEmpty() (bool, error) {
	return vdb.db.IsEmpty()
}

func encodeDataKey(ns, key string) []byte {
	k := append(dataKeyPrefix, []byte(ns)...)
	k = append(k, nsKeySep...)
	return append(k, []byte(key)...)
}

func decodeDataKey(encodedDataKey []byte) (string, string) {
	split := bytes.SplitN(encodedDataKey, nsKeySep, 2)
	return string(split[0][1:]), string(split[1])
}

func dataKeyStarterForNextNamespace(ns string) []byte {
	k := append(dataKeyPrefix, []byte(ns)...)
	return append(k, lastKeyIndicator)
}

type kvScanner struct {
	namespace            string
	dbItr                db.Iterator
	requestedLimit       int32
	totalRecordsReturned int32
}

func newKVScanner(namespace string, dbItr db.Iterator, requestedLimit int32) *kvScanner {
	return &kvScanner{namespace, dbItr, requestedLimit, 0}
}

func (scanner *kvScanner) Next() (*statedb.VersionedKV, error) {
	if scanner.requestedLimit > 0 && scanner.totalRecordsReturned >= scanner.requestedLimit {
		return nil, nil
	}
	if !scanner.dbItr.Next() {
		return nil, nil
	}

	dbKey := scanner.dbItr.Key()
	dbVal := scanner.dbItr.Value()
	dbValCopy := make([]byte, len(dbVal))
	copy(dbValCopy, dbVal)
	_, key := decodeDataKey(dbKey)
	vv, err := decodeValue(dbValCopy)
	if err != nil {
		return nil, err
	}

	scanner.totalRecordsReturned++
	return &statedb.VersionedKV{
		CompositeKey: &statedb.CompositeKey{
			Namespace: scanner.namespace,
			Key:       key,
		},
		VersionedValue: vv,
	}, nil
}

func (scanner *kvScanner) Close() {
	scanner.dbItr.Release()
}

func (scanner *kvScanner) GetBookmarkAndClose() string {
	retval := ""
	if scanner.dbItr.Next() {
		dbKey := scanner.dbItr.Key()
		_, key := decodeDataKey(dbKey)
		retval = key
	}
	scanner.Close()
	return retval
}

type fullDBScanner struct {
	db     db.DBHandle
	dbItr  db.Iterator
	toSkip func(namespace string) bool
}

func newFullDBScanner(db db.DBHandle, skipNamespace func(namespace string) bool) (*fullDBScanner, error) {
	dbItr, err := db.GetIterator(dataKeyPrefix, dataKeyStopper)
	if err != nil {
		return nil, err
	}
	return &fullDBScanner{
			db:     db,
			dbItr:  dbItr,
			toSkip: skipNamespace,
		},
		nil
}

// Next returns the key-values in the lexical order of <Namespace, key>
func (s *fullDBScanner) Next() (*statedb.VersionedKV, error) {
	if s.dbItr.Next() {
		ns, key := decodeDataKey(s.dbItr.Key())
		for s.toSkip(ns) {
			if !s.dbItr.Seek(dataKeyStarterForNextNamespace(ns)) {
				return nil, nil
			}
			ns, key = decodeDataKey(s.dbItr.Key())
		}

		versionedVal, err := decodeValue(s.dbItr.Value())
		if err != nil {
			return nil, err
		}

		return &statedb.VersionedKV{
			CompositeKey: &statedb.CompositeKey{
				Namespace: ns,
				Key:       key,
			},
			VersionedValue: versionedVal,
		}, nil
	}
	return nil, errors.Wrap(s.dbItr.Error(), "internal pebble error while retrieving data from db iterator")
}

func (s *fullDBScanner) Close() {
	if s == nil {
		return
	}
	s.dbItr.Release()
}
