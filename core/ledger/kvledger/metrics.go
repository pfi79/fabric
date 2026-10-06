/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package kvledger

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/hyperledger/fabric-lib-go/common/metrics"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/validation"
)

type stats struct {
	blockProcessingTime            metrics.Histogram
	blockAndPvtdataStoreCommitTime metrics.Histogram
	statedbCommitTime              metrics.Histogram
	transactionsCount              metrics.Counter
	stateTrieRootHash              metrics.Gauge
	stateTrieRoot                  metrics.Gauge
	stateTrieLiveNodes             metrics.Gauge
	stateTrieGCDuration            metrics.Histogram
	exactTrieMetrics               bool
}

func newStats(metricsProvider metrics.Provider, exactTrieMetrics bool) *stats {
	stats := &stats{exactTrieMetrics: exactTrieMetrics}
	stats.blockProcessingTime = metricsProvider.NewHistogram(blockProcessingTimeOpts)
	stats.blockAndPvtdataStoreCommitTime = metricsProvider.NewHistogram(blockAndPvtdataStoreCommitTimeOpts)
	stats.statedbCommitTime = metricsProvider.NewHistogram(statedbCommitTimeOpts)
	stats.transactionsCount = metricsProvider.NewCounter(transactionCountOpts)
	stats.stateTrieRootHash = metricsProvider.NewGauge(stateTrieRootHashOpts)
	if exactTrieMetrics {
		// The series whose label is the whole root hash is the one that grows
		// without bound, so it is created only when the configuration asks for it.
		stats.stateTrieRoot = metricsProvider.NewGauge(stateTrieRootOpts)
	}
	stats.stateTrieLiveNodes = metricsProvider.NewGauge(stateTrieLiveNodesOpts)
	stats.stateTrieGCDuration = metricsProvider.NewHistogram(stateTrieGCDurationOpts)
	return stats
}

// stats is the reporter of the roots of the state trie, and the ledger has no
// other one.
var _ statedb.RootReporter = (*stats)(nil)

// BlockRoot records the root of the state after the commit of a block. The
// whole of the root is written to the log as well, because the gauge keeps a
// number and not a hash: the exact series can be turned off, the log cannot, and
// the root is what an operator has to be able to write down.
func (s *stats) BlockRoot(channel string, height uint64, root []byte) {
	logger.Infof("Committed block [channel=%s, height=%d] with state trie root [0x%s]", channel, height, hex.EncodeToString(root))
	value, ok := trieRootGaugeValue(root)
	if !ok {
		return
	}
	s.stateTrieRootHash.With("channel", channel, "stage", "block").Set(value)
}

// TxRoot records the root of the state after the commit of one transaction of a
// block. When exact metrics are on, the full root hash is also recorded as its
// own series, named by the block and the transaction it belongs to.
func (s *stats) TxRoot(channel string, height uint64, txNum uint64, root []byte) {
	value, ok := trieRootGaugeValue(root)
	if !ok {
		return
	}
	s.stateTrieRootHash.With("channel", channel, "stage", "tx").Set(value)
	if !s.exactTrieMetrics {
		return
	}
	s.stateTrieRoot.With(
		"channel", channel,
		"height", strconv.FormatUint(height, 10),
		"tx_num", strconv.FormatUint(txNum, 10),
		"root_hash", "0x"+hex.EncodeToString(root),
	).Set(1)
}

// GC records what the last collection of garbage found and how long it took.
func (s *stats) GC(channel string, liveNodes int, reclaimedNodes int, durationMillis float64) {
	s.stateTrieLiveNodes.With("channel", channel).Set(float64(liveNodes))
	s.stateTrieGCDuration.With("channel", channel).Observe(durationMillis)
}

// trieRootGaugeValue reduces a root hash to the number of bits a float64 can
// hold, which is all a numeric metric can carry.
func trieRootGaugeValue(root []byte) (float64, bool) {
	if len(root) < 8 {
		return 0, false
	}
	return float64(binary.BigEndian.Uint64(root[:8]) >> 11), true
}

type ledgerStats struct {
	stats    *stats
	ledgerid string
}

func (s *stats) ledgerStats(ledgerid string) *ledgerStats {
	return &ledgerStats{
		s, ledgerid,
	}
}

func (s *ledgerStats) updateBlockProcessingTime(timeTaken time.Duration) {
	s.stats.blockProcessingTime.With("channel", s.ledgerid).Observe(timeTaken.Seconds())
}

func (s *ledgerStats) updateBlockstorageAndPvtdataCommitTime(timeTaken time.Duration) {
	s.stats.blockAndPvtdataStoreCommitTime.With("channel", s.ledgerid).Observe(timeTaken.Seconds())
}

func (s *ledgerStats) updateStatedbCommitTime(timeTaken time.Duration) {
	s.stats.statedbCommitTime.With("channel", s.ledgerid).Observe(timeTaken.Seconds())
}

func (s *ledgerStats) updateTransactionsStats(
	txstatsInfo []*validation.TxStatInfo,
) {
	for _, txstat := range txstatsInfo {
		transactionTypeStr := "unknown"
		if txstat.TxType != -1 {
			transactionTypeStr = txstat.TxType.String()
		}

		chaincodeName := "unknown"
		if txstat.ChaincodeID != nil {
			chaincodeName = txstat.ChaincodeID.GetName() + ":" + txstat.ChaincodeID.GetVersion()
		}

		s.stats.transactionsCount.With(
			"channel", s.ledgerid,
			"transaction_type", transactionTypeStr,
			"chaincode", chaincodeName,
			"validation_code", txstat.ValidationCode.String(),
		).Add(1)
	}
}

var (
	blockProcessingTimeOpts = metrics.HistogramOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "block_processing_time",
		Help:         "Time taken in seconds for ledger block processing.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
		Buckets:      []float64{0.005, 0.01, 0.015, 0.05, 0.1, 1, 10},
	}

	blockAndPvtdataStoreCommitTimeOpts = metrics.HistogramOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "blockstorage_and_pvtdata_commit_time",
		Help:         "Time taken in seconds for committing the block and private data to storage.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
		Buckets:      []float64{0.005, 0.01, 0.015, 0.05, 0.1, 1, 10},
	}

	statedbCommitTimeOpts = metrics.HistogramOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "statedb_commit_time",
		Help:         "Time taken in seconds for committing block changes to state db.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
		Buckets:      []float64{0.005, 0.01, 0.015, 0.05, 0.1, 1, 10},
	}

	transactionCountOpts = metrics.CounterOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "transaction_count",
		Help:         "Number of transactions processed.",
		LabelNames:   []string{"channel", "transaction_type", "chaincode", "validation_code"},
		StatsdFormat: "%{#fqname}.%{channel}.%{transaction_type}.%{chaincode}.%{validation_code}",
	}

	stateTrieRootHashOpts = metrics.GaugeOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "state_trie_root_hash",
		Help:         "Root hash of the state trie after a block and after each transaction.",
		LabelNames:   []string{"channel", "stage"},
		StatsdFormat: "%{#fqname}.%{channel}.%{stage}",
	}

	stateTrieRootOpts = metrics.GaugeOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "state_trie_root",
		Help:         "Exact root of the state trie, one series per commit.",
		LabelNames:   []string{"channel", "height", "tx_num", "root_hash"},
		StatsdFormat: "%{#fqname}.%{channel}.%{height}.%{tx_num}.%{root_hash}",
	}

	stateTrieLiveNodesOpts = metrics.GaugeOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "state_trie_live_nodes",
		Help:         "Number of nodes live in the state trie of the channel.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}

	stateTrieGCDurationOpts = metrics.HistogramOpts{
		Namespace:    "ledger",
		Subsystem:    "",
		Name:         "state_trie_gc_duration",
		Help:         "Time taken in milliseconds to collect garbage from the state trie.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
		Buckets:      []float64{0.5, 1, 5, 10, 50, 100, 500, 1000, 5000},
	}
)
