/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package leveldbtrie

import (
	"bytes"
)

// nsKeySep separates the namespace from the key in a key of the tree.
//
// It is the byte of the lowest value on purpose. A key of the tree is the
// namespace, this byte, and the key, so the byte order of the keys of the tree
// is the byte order of the pairs <namespace, key> that a range query and a
// full scan are expected to hand out in. A separator of any other value would
// let the key of one namespace fall between the keys of another, and the two
// stores would then disagree on the order of the very results they are meant to
// agree on.
var nsKeySep = []byte{0x00}

// encodeTreeKey returns the key of the tree that the given key of the given
// namespace is held under.
func encodeTreeKey(ns, key string) []byte {
	return append(namespacePrefix(ns), key...)
}

// namespacePrefix returns the beginning of every key of the tree that belongs
// to the given namespace.
func namespacePrefix(ns string) []byte {
	return append([]byte(ns), nsKeySep...)
}

// namespaceEnd returns the first key of the tree that does not belong to the
// given namespace. Every key of the namespace, and nothing beyond it, lies
// between the prefix of the namespace and this.
func namespaceEnd(ns string) []byte {
	return append([]byte(ns), nsKeySep[0]+1)
}

// decodeTreeKey splits a key of the tree back into the namespace and the key it
// names. A key that names no namespace is reported as such rather than as a
// namespace and a key of its own making.
func decodeTreeKey(treeKey []byte) (ns, key string, ok bool) {
	before, after, found := bytes.Cut(treeKey, nsKeySep)
	if !found {
		return "", "", false
	}
	return string(before), string(after), true
}
