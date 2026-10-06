/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package statekvdb

import (
	"github.com/hyperledger/fabric/core/ledger/internal/version"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"google.golang.org/protobuf/proto"
)

// EncodeValue encodes the value, version, and metadata. It is exported because
// the world state is not kept in only one shape: a store that holds the state
// somewhere other than under a LevelDB key, such as the trie of statedb
// leveldbtrie, writes the very same bytes so that the two formats stay one.
func EncodeValue(v *statedb.VersionedValue) ([]byte, error) {
	return proto.Marshal(
		&DBValue{
			Version:  v.Version.ToBytes(),
			Value:    v.Value,
			Metadata: v.Metadata,
		},
	)
}

// DecodeValue decodes the statedb value bytes
func DecodeValue(encodedValue []byte) (*statedb.VersionedValue, error) {
	dbValue := &DBValue{}
	err := proto.Unmarshal(encodedValue, dbValue)
	if err != nil {
		return nil, err
	}
	ver, _, err := version.NewHeightFromBytes(dbValue.GetVersion())
	if err != nil {
		return nil, err
	}
	val := dbValue.GetValue()
	metadata := dbValue.GetMetadata()
	// protobuf always makes an empty byte array as nil
	if val == nil {
		val = []byte{}
	}
	return &statedb.VersionedValue{Version: ver, Value: val, Metadata: metadata}, nil
}
