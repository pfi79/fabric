/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package privacyenabledstate

import (
	"bytes"
	"encoding/base64"
	"maps"
	"strings"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-lib-go/common/metrics"
	"github.com/hyperledger/fabric-lib-go/healthz"
	"github.com/hyperledger/fabric/core/common/ccprovider"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/cceventmgmt"
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/bookkeeping"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/leveldbtrie"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statecouchdb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/hyperledger/fabric/core/ledger/util"
	"github.com/pkg/errors"
)

var logger = flogging.MustGetLogger("privacyenabledstate")

const (
	nsJoiner       = "$$"
	pvtDataPrefix  = "p"
	hashDataPrefix = "h"
)

// StateDBConfig encapsulates the configuration for stateDB on the ledger.
type StateDBConfig struct {
	// ledger.StateDBConfig is used to configure the stateDB for the ledger.
	*ledger.StateDBConfig
	// LevelDBPath is the filesystem path when statedb type is "goleveldb".
	// It is internally computed by the ledger component,
	// so it is not in ledger.StateDBConfig and not exposed to other components.
	LevelDBPath string
	// RootReporter receives the root of the state of every channel as the blocks
	// are committed. It is the reporter the store behind this db was given as
	// well: the roots of the transactions of a block are reported from here, and
	// the root of the block and the work of the garbage collector are reported
	// from there.
	RootReporter statedb.RootReporter
}

// DBProvider encapsulates other providers such as VersionedDBProvider and
// BookeepingProvider which are required to create DB for a channel
type DBProvider struct {
	VersionedDBProvider statedb.VersionedDBProvider
	HealthCheckRegistry ledger.HealthCheckRegistry
	// RootReporter is handed the roots of the state of the channels of this
	// provider as their blocks are committed.
	RootReporter        statedb.RootReporter
	bookkeepingProvider *bookkeeping.Provider
}

// NewDBProvider constructs an instance of DBProvider
func NewDBProvider(
	bookkeeperProvider *bookkeeping.Provider,
	metricsProvider metrics.Provider,
	healthCheckRegistry ledger.HealthCheckRegistry,
	stateDBConf *StateDBConfig,
	sysNamespaces []string,
) (*DBProvider, error) {
	var vdbProvider statedb.VersionedDBProvider
	var err error

	if stateDBConf != nil && stateDBConf.StateDatabase == ledger.CouchDB {
		if vdbProvider, err = statecouchdb.NewVersionedDBProvider(stateDBConf.CouchDB, metricsProvider, sysNamespaces); err != nil {
			return nil, err
		}
	} else if stateDBConf != nil && stateDBConf.StateDatabase == ledger.LevelDBTrie {
		// The nodes of the trie live in the LevelDB the state has always been
		// stored in, so the type of the store behind the trie is the LevelDB type.
		if vdbProvider, err = leveldbtrie.NewProvider(stateDBConf.LevelDBPath, ledger.GoLevelDB, stateDBConf.LevelDBTrie, stateDBConf.RootReporter); err != nil {
			return nil, err
		}
	} else {
		if vdbProvider, err = statekvdb.NewVersionedDBProvider(stateDBConf.LevelDBPath, stateDBConf.StateDatabase); err != nil {
			return nil, err
		}
	}

	dbProvider := &DBProvider{
		VersionedDBProvider: vdbProvider,
		HealthCheckRegistry: healthCheckRegistry,
		RootReporter:        stateDBConf.RootReporter,
		bookkeepingProvider: bookkeeperProvider,
	}

	err = dbProvider.RegisterHealthChecker()
	if err != nil {
		return nil, err
	}

	return dbProvider, nil
}

// RegisterHealthChecker registers the underlying stateDB with the healthChecker.
// For now, we register only the CouchDB as it runs as a separate process but not
// for the GoLevelDB as it is an embedded database.
func (p *DBProvider) RegisterHealthChecker() error {
	if healthChecker, ok := p.VersionedDBProvider.(healthz.HealthChecker); ok {
		return p.HealthCheckRegistry.RegisterChecker("couchdb", healthChecker)
	}
	return nil
}

// GetDBHandle gets a handle to DB for a given id, i.e., a channel
func (p *DBProvider) GetDBHandle(id string, chInfoProvider channelInfoProvider) (*DB, error) {
	vdb, err := p.VersionedDBProvider.GetDBHandle(id, &namespaceProvider{chInfoProvider})
	if err != nil {
		return nil, err
	}
	bookkeeper := p.bookkeepingProvider.GetDBHandle(id, bookkeeping.MetadataPresenceIndicator)
	metadataHint, err := newMetadataHint(bookkeeper)
	if err != nil {
		return nil, err
	}
	db, err := NewDB(vdb, id, metadataHint)
	if err != nil {
		return nil, err
	}
	db.RootReporter = p.RootReporter
	return db, nil
}

// Close closes all the VersionedDB instances and releases any resources held by VersionedDBProvider
func (p *DBProvider) Close() {
	p.VersionedDBProvider.Close()
}

// Drop drops channel-specific data from the statedb
func (p *DBProvider) Drop(ledgerid string) error {
	return p.VersionedDBProvider.Drop(ledgerid)
}

// DB uses a single database to maintain both the public and private data
type DB struct {
	statedb.VersionedDB
	metadataHint *metadataHint
	// RootReporter receives the root of the state after every transaction of a
	// block, when the store behind this db is able to hand those roots out. A db
	// with no reporter is given the block as the one batch it has always been
	// given, the roots of the transactions being of no use to nobody then.
	RootReporter statedb.RootReporter
}

// NewDB wraps a VersionedDB instance. The public data is managed directly by the wrapped versionedDB.
// For managing the hashed data and private data, this implementation creates separate namespaces in the wrapped db
func NewDB(vdb statedb.VersionedDB, ledgerid string, metadataHint *metadataHint) (*DB, error) {
	return &DB{VersionedDB: vdb, metadataHint: metadataHint}, nil
}

// IsBulkOptimizable checks whether the underlying statedb implements statedb.BulkOptimizable
func (s *DB) IsBulkOptimizable() bool {
	_, ok := s.VersionedDB.(statedb.BulkOptimizable)
	return ok
}

// LoadCommittedVersionsOfPubAndHashedKeys loads committed version of given public and hashed states
func (s *DB) LoadCommittedVersionsOfPubAndHashedKeys(pubKeys []*statedb.CompositeKey,
	hashedKeys []*HashedCompositeKey,
) error {
	bulkOptimizable, ok := s.VersionedDB.(statedb.BulkOptimizable)
	if !ok {
		return nil
	}
	// Here, hashedKeys are merged into pubKeys to get a combined set of keys for combined loading
	for _, key := range hashedKeys {
		ns := deriveHashedDataNs(key.Namespace, key.CollectionName)
		// No need to check for duplicates as hashedKeys are in separate namespace
		var keyHashStr string
		if !s.BytesKeySupported() {
			keyHashStr = base64.StdEncoding.EncodeToString([]byte(key.KeyHash))
		} else {
			keyHashStr = key.KeyHash
		}
		pubKeys = append(pubKeys, &statedb.CompositeKey{
			Namespace: ns,
			Key:       keyHashStr,
		})
	}

	err := bulkOptimizable.LoadCommittedVersions(pubKeys)
	if err != nil {
		return err
	}

	return nil
}

// ClearCachedVersions clears the version cache
func (s *DB) ClearCachedVersions() {
	bulkOptimizable, ok := s.VersionedDB.(statedb.BulkOptimizable)
	if ok {
		bulkOptimizable.ClearCachedVersions()
	}
}

// GetChaincodeEventListener returns a struct that implements cceventmgmt.ChaincodeLifecycleEventListener
// if the underlying statedb implements statedb.IndexCapable.
func (s *DB) GetChaincodeEventListener() cceventmgmt.ChaincodeLifecycleEventListener {
	_, ok := s.VersionedDB.(statedb.IndexCapable)
	if ok {
		return s
	}
	return nil
}

// GetPrivateData gets the value of a private data item identified by a tuple <namespace, collection, key>
func (s *DB) GetPrivateData(namespace, collection, key string) (*statedb.VersionedValue, error) {
	return s.GetState(derivePvtDataNs(namespace, collection), key)
}

// GetPrivateDataHash gets the hash of the value of a private data item identified by a tuple <namespace, collection, key>
func (s *DB) GetPrivateDataHash(namespace, collection, key string) (*statedb.VersionedValue, error) {
	return s.GetValueHash(namespace, collection, util.ComputeStringHash(key))
}

// GetValueHash gets the value hash of a private data item identified by a tuple <namespace, collection, keyHash>
func (s *DB) GetValueHash(namespace, collection string, keyHash []byte) (*statedb.VersionedValue, error) {
	keyHashStr := string(keyHash)
	if !s.BytesKeySupported() {
		keyHashStr = base64.StdEncoding.EncodeToString(keyHash)
	}
	return s.GetState(deriveHashedDataNs(namespace, collection), keyHashStr)
}

// GetKeyHashVersion gets the version of a private data item identified by a tuple <namespace, collection, keyHash>
func (s *DB) GetKeyHashVersion(namespace, collection string, keyHash []byte) (*version.Height, error) {
	keyHashStr := string(keyHash)
	if !s.BytesKeySupported() {
		keyHashStr = base64.StdEncoding.EncodeToString(keyHash)
	}
	return s.GetVersion(deriveHashedDataNs(namespace, collection), keyHashStr)
}

// GetCachedKeyHashVersion retrieves the keyhash version from cache
func (s *DB) GetCachedKeyHashVersion(namespace, collection string, keyHash []byte) (*version.Height, bool) {
	bulkOptimizable, ok := s.VersionedDB.(statedb.BulkOptimizable)
	if !ok {
		return nil, false
	}

	keyHashStr := string(keyHash)
	if !s.BytesKeySupported() {
		keyHashStr = base64.StdEncoding.EncodeToString(keyHash)
	}
	return bulkOptimizable.GetCachedVersion(deriveHashedDataNs(namespace, collection), keyHashStr)
}

// GetPrivateDataMultipleKeys gets the values for the multiple private data items in a single call
func (s *DB) GetPrivateDataMultipleKeys(namespace, collection string, keys []string) ([]*statedb.VersionedValue, error) {
	return s.GetStateMultipleKeys(derivePvtDataNs(namespace, collection), keys)
}

// GetPrivateDataRangeScanIterator returns an iterator that contains all the key-values between given key ranges.
// startKey is included in the results and endKey is excluded.
func (s *DB) GetPrivateDataRangeScanIterator(namespace, collection, startKey, endKey string) (statedb.ResultsIterator, error) {
	return s.GetStateRangeScanIterator(derivePvtDataNs(namespace, collection), startKey, endKey)
}

// ExecuteQueryOnPrivateData executes the given query and returns an iterator that contains results of type specific to the underlying data store.
func (s DB) ExecuteQueryOnPrivateData(namespace, collection, query string) (statedb.ResultsIterator, error) {
	return s.ExecuteQuery(derivePvtDataNs(namespace, collection), query)
}

// ApplyUpdates overrides the function in statedb.VersionedDB and throws appropriate error message
// Otherwise, somewhere in the code, usage of this function could lead to updating only public data.
func (s *DB) ApplyUpdates(batch *statedb.UpdateBatch, height *version.Height) error {
	return errors.New("this function should not be invoked on this type. Please invoke function ApplyPrivacyAwareUpdates")
}

// ApplyPrivacyAwareUpdates applies the batch to the underlying db
func (s *DB) ApplyPrivacyAwareUpdates(updates *UpdateBatch, height *version.Height) error {
	// combinedUpdates includes both updates to public db and private db, which are partitioned by a separate namespace
	combinedUpdates := updates.PubUpdates
	addPvtUpdates(combinedUpdates, updates.PvtUpdates)
	addHashedUpdates(combinedUpdates, updates.HashUpdates, !s.BytesKeySupported())
	if err := s.metadataHint.setMetadataUsedFlag(updates); err != nil {
		return err
	}
	// A db that can hand out the root of the state after every transaction of the
	// block is committed transaction by transaction, and the boundaries of the
	// block are what it needs to do so. A db that cannot is committed the one
	// batch it has always been given, and so is a peer with no reporter, which
	// has nowhere to put the roots of the transactions: a root that is not
	// reported is not a reason to leave the block half committed, nor to keep
	// the boundaries of its transactions to no end.
	store, ok := s.VersionedDB.(statedb.IntermediateRoots)
	if !ok || s.RootReporter == nil {
		return s.VersionedDB.ApplyUpdates(combinedUpdates.UpdateBatch, height)
	}
	return store.ApplyUpdatesWithRoots(
		combinedUpdates.UpdateBatch,
		height,
		s.mergedPerTx(updates, combinedUpdates.UpdateBatch),
		s.RootReporter,
	)
}

// mergedPerTx merges the public, the private and the hashed updates of every
// transaction of the block into one batch per transaction, in the order of the
// transactions of the block, and returns those batches: the boundaries of the
// block are what a store behind a tree needs to hand out the root of the state
// after each of them.
//
// Every transaction of the list is given a batch, an empty one for a transaction
// that writes nothing, so that a store can walk the block transaction by
// transaction without having to make anything of an entry that is not there.
//
// A write of the block that no transaction of the block accounts for - an expiry
// marker of the purge manager, the delete of an application initiated purge, or
// the version a private value has to be re-stamped with because a metadata only
// transaction moved the version of its hash - belongs to no transaction. It is
// put into the batch of the last transaction that writes something, which is
// also the transaction the root of the block as a whole is the root of. A block
// none of whose transactions writes anything, of which only the bookkeeping
// above is left, credits them to its last transaction.
func (s *DB) mergedPerTx(updates *UpdateBatch, combined *statedb.UpdateBatch) []*statedb.UpdateBatch {
	if len(updates.PerTx) == 0 {
		return nil
	}
	base64Key := !s.BytesKeySupported()
	perTx := make([]*statedb.UpdateBatch, len(updates.PerTx))
	lastWriting := -1
	for i, txUpdates := range updates.PerTx {
		mergedTx := statedb.NewUpdateBatch()
		if txUpdates != nil {
			pubUpdates := txUpdates.PubUpdates
			addPvtUpdates(pubUpdates, txUpdates.PvtUpdates)
			addHashedUpdates(pubUpdates, txUpdates.HashUpdates, base64Key)
			mergedTx = pubUpdates.UpdateBatch
		}
		perTx[i] = mergedTx
		if batchHasWrites(mergedTx) {
			lastWriting = i
		}
	}
	if unclaimed := unclaimedUpdates(combined, perTx); batchHasWrites(unclaimed) {
		if lastWriting < 0 {
			lastWriting = len(perTx) - 1
		}
		perTx[lastWriting].Merge(unclaimed)
	}
	return perTx
}

// unclaimedUpdates returns the writes of the block that the batches of the
// transactions do not already carry as they are.
func unclaimedUpdates(combined *statedb.UpdateBatch, perTx []*statedb.UpdateBatch) *statedb.UpdateBatch {
	claimed := map[string]map[string]*statedb.VersionedValue{}
	for _, txBatch := range perTx {
		if txBatch == nil {
			continue
		}
		for _, ns := range txBatch.GetUpdatedNamespaces() {
			keys, ok := claimed[ns]
			if !ok {
				keys = map[string]*statedb.VersionedValue{}
				claimed[ns] = keys
			}
			maps.Copy(keys, txBatch.GetUpdates(ns))
		}
	}

	unclaimed := statedb.NewUpdateBatch()
	for _, ns := range combined.GetUpdatedNamespaces() {
		for key, vv := range combined.GetUpdates(ns) {
			if isCarriedAsIs(claimed[ns], key, vv) {
				continue
			}
			unclaimed.Update(ns, key, vv)
		}
	}
	return unclaimed
}

// isCarriedAsIs tells whether the last of the transactions of the block that
// writes this key writes it exactly as the block does. The version alone is not
// enough to tell: a bookkeeping write of the block is allowed to re-stamp the
// version of a value, and a write whose value or metadata differs at the same
// version would be dropped were only the version compared. A write the block
// carries as-is is one whose version, value and metadata are all the same.
func isCarriedAsIs(keys map[string]*statedb.VersionedValue, key string, vv *statedb.VersionedValue) bool {
	carried, ok := keys[key]
	if !ok {
		return false
	}
	return version.AreSame(carried.Version, vv.Version) &&
		bytes.Equal(carried.Value, vv.Value) &&
		bytes.Equal(carried.Metadata, vv.Metadata)
}

func batchHasWrites(batch *statedb.UpdateBatch) bool {
	if batch == nil {
		return false
	}
	for _, ns := range batch.GetUpdatedNamespaces() {
		if len(batch.GetUpdates(ns)) > 0 {
			return true
		}
	}
	return false
}

// GetStateMetadata implements corresponding function in interface DB. This implementation provides
// an optimization such that it keeps track if a namespaces has never stored metadata for any of
// its items, the value 'nil' is returned without going to the db. This is intended to be invoked
// in the validation and commit path. This saves the chaincodes from paying unnecessary performance
// penalty if they do not use features that leverage metadata (such as key-level endorsement),
func (s *DB) GetStateMetadata(namespace, key string) ([]byte, error) {
	if !s.metadataHint.metadataEverUsedFor(namespace) {
		return nil, nil
	}
	vv, err := s.GetState(namespace, key)
	if err != nil || vv == nil {
		return nil, err
	}
	return vv.Metadata, nil
}

// GetPrivateDataMetadataByHash implements corresponding function in interface DB. For additional details, see
// description of the similar function 'GetStateMetadata'
func (s *DB) GetPrivateDataMetadataByHash(namespace, collection string, keyHash []byte) ([]byte, error) {
	if !s.metadataHint.metadataEverUsedFor(namespace) {
		return nil, nil
	}
	vv, err := s.GetValueHash(namespace, collection, keyHash)
	if err != nil || vv == nil {
		return nil, err
	}
	return vv.Metadata, nil
}

// HandleChaincodeDeploy initializes database artifacts for the database associated with the namespace
// This function deliberately suppresses the errors that occur during the creation of the indexes on couchdb.
// This is because, in the present code, we do not differentiate between the errors because of couchdb interaction
// and the errors because of bad index files - the later being unfixable by the admin. Note that the error suppression
// is acceptable since peer can continue in the committing role without the indexes. However, executing chaincode queries
// may be affected, until a new chaincode with fixed indexes is installed and instantiated
func (s *DB) HandleChaincodeDeploy(chaincodeDefinition *cceventmgmt.ChaincodeDefinition, dbArtifactsTar []byte) error {
	// Check to see if the interface for IndexCapable is implemented
	indexCapable, ok := s.VersionedDB.(statedb.IndexCapable)
	if !ok {
		return nil
	}
	if chaincodeDefinition == nil {
		return errors.New("chaincode definition not found while creating couchdb index")
	}
	dbArtifacts, err := ccprovider.ExtractFileEntries(dbArtifactsTar, indexCapable.GetDBType())
	if err != nil {
		logger.Errorf("Index creation: error extracting db artifacts from tar for chaincode [%s]: %s", chaincodeDefinition.Name, err)
		return nil
	}

	collectionConfigMap := extractCollectionNames(chaincodeDefinition)
	for directoryPath, indexFiles := range dbArtifacts {
		indexFilesData := make(map[string][]byte)
		for _, f := range indexFiles {
			indexFilesData[f.FileHeader.Name] = f.FileContent
		}

		indexInfo := getIndexInfo(directoryPath)
		switch {
		case indexInfo.hasIndexForChaincode:
			err := indexCapable.ProcessIndexesForChaincodeDeploy(chaincodeDefinition.Name, indexFilesData)
			if err != nil {
				logger.Errorf("Error processing index for chaincode [%s]: %s", chaincodeDefinition.Name, err)
			}
		case indexInfo.hasIndexForCollection:
			_, ok := collectionConfigMap[indexInfo.collectionName]
			if !ok {
				logger.Errorf("Error processing index for chaincode [%s]: cannot create an index for an undefined collection=[%s]",
					chaincodeDefinition.Name, indexInfo.collectionName)
				continue
			}
			err := indexCapable.ProcessIndexesForChaincodeDeploy(derivePvtDataNs(chaincodeDefinition.Name, indexInfo.collectionName), indexFilesData)
			if err != nil {
				logger.Errorf("Error processing collection index for chaincode [%s]: %s", chaincodeDefinition.Name, err)
			}
		}
	}
	return nil
}

// ChaincodeDeployDone is a noop for couchdb state impl
func (s *DB) ChaincodeDeployDone(succeeded bool) {
	// NOOP
}

func derivePvtDataNs(namespace, collection string) string {
	return namespace + nsJoiner + pvtDataPrefix + collection
}

func deriveHashedDataNs(namespace, collection string) string {
	return namespace + nsJoiner + hashDataPrefix + collection
}

func decodeHashedDataNsColl(hashedDataNs string) (string, string, error) {
	strs := strings.Split(hashedDataNs, nsJoiner+hashDataPrefix)
	if len(strs) != 2 {
		return "", "", errors.Errorf("not a valid hashedDataNs [%s]", hashedDataNs)
	}
	return strs[0], strs[1], nil
}

func isPvtdataNs(namespace string) bool {
	return strings.Contains(namespace, nsJoiner+pvtDataPrefix)
}

func isHashedDataNs(namespace string) bool {
	return strings.Contains(namespace, nsJoiner+hashDataPrefix)
}

func addPvtUpdates(pubUpdateBatch *PubUpdateBatch, pvtUpdateBatch *PvtUpdateBatch) {
	for ns, nsBatch := range pvtUpdateBatch.UpdateMap {
		for _, coll := range nsBatch.GetCollectionNames() {
			for key, vv := range nsBatch.GetUpdates(coll) {
				pubUpdateBatch.Update(derivePvtDataNs(ns, coll), key, vv)
			}
		}
	}
}

func addHashedUpdates(pubUpdateBatch *PubUpdateBatch, hashedUpdateBatch *HashedUpdateBatch, base64Key bool) {
	for ns, nsBatch := range hashedUpdateBatch.UpdateMap {
		for _, coll := range nsBatch.GetCollectionNames() {
			for key, vv := range nsBatch.GetUpdates(coll) {
				if base64Key {
					key = base64.StdEncoding.EncodeToString([]byte(key))
				}
				pubUpdateBatch.Update(deriveHashedDataNs(ns, coll), key, vv)
			}
		}
	}
}

func extractCollectionNames(chaincodeDefinition *cceventmgmt.ChaincodeDefinition) map[string]bool {
	collectionConfigs := chaincodeDefinition.CollectionConfigs
	collectionConfigsMap := make(map[string]bool)
	if collectionConfigs != nil {
		for _, config := range collectionConfigs.GetConfig() {
			sConfig := config.GetStaticCollectionConfig()
			if sConfig == nil {
				continue
			}
			collectionConfigsMap[sConfig.GetName()] = true
		}
	}
	return collectionConfigsMap
}

type indexInfo struct {
	hasIndexForChaincode  bool
	hasIndexForCollection bool
	collectionName        string
}

const (
	// Example for chaincode indexes:
	// "META-INF/statedb/couchdb/indexes"
	chaincodeIndexDirDepth = 3

	// Example for collection scoped indexes:
	// "META-INF/statedb/couchdb/collections/collectionMarbles/indexes"
	collectionDirDepth      = 3
	collectionNameDepth     = 4
	collectionIndexDirDepth = 5
)

// Note previous functions will have ensured that the path starts
// with 'META-INF/statedb' and does not have leading or trailing
// path deliminators.
func getIndexInfo(indexPath string) *indexInfo {
	indexInfo := &indexInfo{}
	pathParts := strings.Split(indexPath, "/")
	pathDepth := len(pathParts)

	switch {
	case pathDepth > chaincodeIndexDirDepth && pathParts[chaincodeIndexDirDepth] == "indexes":
		indexInfo.hasIndexForChaincode = true
	case pathDepth > collectionIndexDirDepth && pathParts[collectionDirDepth] == "collections" && pathParts[collectionIndexDirDepth] == "indexes":
		indexInfo.hasIndexForCollection = true
		indexInfo.collectionName = pathParts[collectionNameDepth]
	}
	return indexInfo
}

// channelInfoProvider interface enables the privateenabledstate package to retrieve all the config blocks
// and  namespaces and collections.
type channelInfoProvider interface {
	// NamespacesAndCollections returns namespaces and collections for the channel.
	NamespacesAndCollections(vdb statedb.VersionedDB) (map[string][]string, error)
}

// namespaceProvider implements statedb.NamespaceProvider interface
type namespaceProvider struct {
	channelInfoProvider
}

// PossibleNamespaces returns all possible namespaces for a channel. In ledger, a private data namespace is
// created only if the peer is a member of the collection or owns the implicit collection. However, this function
// adopts a simple implementation that always adds private data namespace for a collection without checking
// peer membership/ownership. As a result, it returns a superset of namespaces that may be created.
// However, it will not cause any inconsistent issue because the caller in statecouchdb will check if any
// existing database matches the namespace and filter out all extra namespaces if no databases match them.
// Checking peer membership is complicated because it requires retrieving all the collection configs from
// the collection config store. Because this is a temporary function needed to retroactively build namespaces
// when upgrading v2.0/2.1 peers to a newer v2.x version and because returning extra private data namespaces
// does not cause inconsistence, it makes sense to use the simple implementation.
func (p *namespaceProvider) PossibleNamespaces(vdb statedb.VersionedDB) ([]string, error) {
	retNamespaces := []string{}
	nsCollMap, err := p.NamespacesAndCollections(vdb)
	if err != nil {
		return nil, err
	}
	for ns, collections := range nsCollMap {
		retNamespaces = append(retNamespaces, ns)
		for _, collection := range collections {
			retNamespaces = append(retNamespaces, deriveHashedDataNs(ns, collection))
			retNamespaces = append(retNamespaces, derivePvtDataNs(ns, collection))
		}
	}
	return retNamespaces, nil
}
