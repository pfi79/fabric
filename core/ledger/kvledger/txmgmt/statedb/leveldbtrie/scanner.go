/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"github.com/ethereum/go-ethereum/trie"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/statekvdb"
	"github.com/pkg/errors"
)

// kvScanner hands out the keys of a range of one namespace in the byte order of
// the tree, which is the byte order the level db store hands them out in.
type kvScanner struct {
	namespace            string
	treeIt               *trie.Iterator
	requestedLimit       int32
	totalRecordsReturned int32
}

// newKVScanner returns a scanner over the given part of the tree. A requested
// limit of zero means no limit: every key of the range is handed out.
func newKVScanner(namespace string, treeIt *trie.Iterator, requestedLimit int32) *kvScanner {
	return &kvScanner{namespace, treeIt, requestedLimit, 0}
}

// Next implements method in ResultsIterator interface
func (scanner *kvScanner) Next() (*statedb.VersionedKV, error) {
	if scanner.requestedLimit > 0 && scanner.totalRecordsReturned >= scanner.requestedLimit {
		return nil, nil
	}
	if !scanner.treeIt.Next() {
		if scanner.treeIt.Err != nil {
			return nil, errors.Wrap(scanner.treeIt.Err, "error while walking the trie of the channel")
		}
		return nil, nil
	}

	vv, err := statekvdb.DecodeValue(scanner.treeIt.Value)
	if err != nil {
		return nil, err
	}
	scanner.totalRecordsReturned++
	return &statedb.VersionedKV{
		CompositeKey: &statedb.CompositeKey{
			Namespace: scanner.namespace,
			Key:       scanner.currentKey(),
		},
		VersionedValue: vv,
	}, nil
}

// GetBookmarkAndClose implements method in QueryResultsIterator interface. The
// bookmark is the key that comes next, and is inclusive where a start key is:
// handing it back as the start key of the next page leaves no key out and
// repeats none.
func (scanner *kvScanner) GetBookmarkAndClose() string {
	retval := ""
	if scanner.treeIt.Next() {
		retval = scanner.currentKey()
	}
	scanner.Close()
	return retval
}

// Close implements method in ResultsIterator interface
func (scanner *kvScanner) Close() {
	// the walk of the tree holds nothing that has to be given back
}

// currentKey returns the key of the namespace the tree is positioned at.
func (scanner *kvScanner) currentKey() string {
	return string(scanner.treeIt.Key[len(namespacePrefix(scanner.namespace)):])
}

// fullDBScanner hands out every key of the tree in the byte order of the tree.
// That order is the order of the pairs <namespace, key>, which is the order a
// snapshot of the state has to be written in.
type fullDBScanner struct {
	treeIt *trie.Iterator
	toSkip func(namespace string) bool
}

// newFullDBScanner returns a scanner over every key of the given tree
func newFullDBScanner(treeIt *trie.Iterator, skipNamespace func(namespace string) bool) *fullDBScanner {
	return &fullDBScanner{treeIt: treeIt, toSkip: skipNamespace}
}

// Next implements method in FullScanIterator interface. It returns the
// key-values in the lexical order of <Namespace, key>
func (s *fullDBScanner) Next() (*statedb.VersionedKV, error) {
	for s.treeIt.Next() {
		ns, key, ok := decodeTreeKey(s.treeIt.Key)
		if !ok {
			return nil, errors.Errorf("the trie of channel holds a key that names no namespace: %#v", s.treeIt.Key)
		}
		if s.toSkip(ns) {
			continue
		}

		vv, err := statekvdb.DecodeValue(s.treeIt.Value)
		if err != nil {
			return nil, err
		}
		return &statedb.VersionedKV{
			CompositeKey:   &statedb.CompositeKey{Namespace: ns, Key: key},
			VersionedValue: vv,
		}, nil
	}
	if s.treeIt.Err != nil {
		return nil, errors.Wrap(s.treeIt.Err, "error while walking the trie of the channel")
	}
	return nil, nil
}

// Close implements method in FullScanIterator interface
func (s *fullDBScanner) Close() {
	// the walk of the tree holds nothing that has to be given back
}
