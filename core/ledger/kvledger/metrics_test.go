/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package kvledger

import (
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-lib-go/bccsp/sw"
	"github.com/hyperledger/fabric-lib-go/common/flogging"
	floggingmock "github.com/hyperledger/fabric-lib-go/common/flogging/mock"
	"github.com/hyperledger/fabric-lib-go/common/metrics"
	"github.com/hyperledger/fabric-lib-go/common/metrics/metricsfakes"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric/common/ledger/testutil"
	lgr "github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/statedb/leveldbtrie"
	"github.com/hyperledger/fabric/core/ledger/kvledger/txmgmt/validation"
	"github.com/hyperledger/fabric/core/ledger/mock"
	"github.com/stretchr/testify/require"
)

func TestBlockRootIsLoggedInFull(t *testing.T) {
	observer := &floggingmock.Observer{}
	previous := flogging.SetObserver(observer)
	t.Cleanup(func() { flogging.SetObserver(previous) })

	testMetricProvider := testutilConstructMetricProvider()
	s := newStats(testMetricProvider.fakeProvider, false)

	root := make([]byte, 32)
	root[0] = 0xab
	root[31] = 0xcd
	s.BlockRoot("ledger1", 5, root)

	// A gauge holds a number and not a hash, so the whole of the root of a block
	// has to be readable out of the log of the peer, whatever the metrics are.
	const wantRoot = "0xab000000000000000000000000000000000000000000000000000000000000cd"
	var line string
	for i := range observer.WriteEntryCallCount() {
		entry, _ := observer.WriteEntryArgsForCall(i)
		if strings.Contains(entry.Message, wantRoot) {
			line = entry.Message
		}
	}
	require.NotEmpty(t, line, "no info line carried the whole root of the block")
	require.Contains(t, line, "ledger1")
	require.Contains(t, line, "5")
}

func TestExactTrieRootSeriesExistsOnlyWhenAskedFor(t *testing.T) {
	var gaugeNames []string
	provider := &metricsfakes.Provider{}
	provider.NewGaugeStub = func(opts metrics.GaugeOpts) metrics.Gauge {
		gaugeNames = append(gaugeNames, opts.Name)
		return testutilConstructGauge()
	}
	provider.NewHistogramStub = func(metrics.HistogramOpts) metrics.Histogram { return testutilConstructHist() }
	provider.NewCounterStub = func(metrics.CounterOpts) metrics.Counter { return testutilConstructCounter() }

	newStats(provider, false)
	// The numeric series is what a peer always has; the series whose label is the
	// whole hash must not exist at all unless the configuration asks for it.
	require.Contains(t, gaugeNames, stateTrieRootHashOpts.Name)
	require.NotContains(t, gaugeNames, stateTrieRootOpts.Name)

	gaugeNames = nil
	newStats(provider, true)
	require.Contains(t, gaugeNames, stateTrieRootOpts.Name)
}

func TestStatsStateTrieRootHashStage(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()
	s := newStats(testMetricProvider.fakeProvider, false)

	// the first eight bytes of the root are 0x0000000000000800, so the value
	// kept by the gauge is 2048 >> 11 == 1
	root := make([]byte, 32)
	root[6] = 0x08

	reporter := statedb.RootReporter(s)
	reporter.BlockRoot("ledger1", 5, root)
	reporter.TxRoot("ledger1", 5, 0, root)

	require.Equal(t, 2, testMetricProvider.fakeStateTrieRootHashGauge.SetCallCount())
	require.Equal(
		t,
		[]string{"channel", "ledger1", "stage", "block"},
		testMetricProvider.fakeStateTrieRootHashGauge.WithArgsForCall(0),
	)
	require.InDelta(t, float64(1), testMetricProvider.fakeStateTrieRootHashGauge.SetArgsForCall(0), 0)
	require.Equal(
		t,
		[]string{"channel", "ledger1", "stage", "tx"},
		testMetricProvider.fakeStateTrieRootHashGauge.WithArgsForCall(1),
	)
	require.InDelta(t, float64(1), testMetricProvider.fakeStateTrieRootHashGauge.SetArgsForCall(1), 0)
}

func TestStatsStateTrieRootExactDisabled(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()
	s := newStats(testMetricProvider.fakeProvider, false)

	reporter := statedb.RootReporter(s)
	reporter.TxRoot("ledger1", 5, 2, make([]byte, 32))

	require.Equal(t, 0, testMetricProvider.fakeStateTrieRootGauge.WithCallCount())
	require.Equal(t, 0, testMetricProvider.fakeStateTrieRootGauge.SetCallCount())
}

func TestStatsStateTrieRootExactEnabled(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()
	s := newStats(testMetricProvider.fakeProvider, true)

	root := make([]byte, 32)
	root[6] = 0x08

	reporter := statedb.RootReporter(s)
	reporter.TxRoot("ledger1", 5, 2, root)

	require.Equal(
		t,
		[]string{
			"channel", "ledger1",
			"height", "5",
			"tx_num", "2",
			"root_hash", "0x0000000000000800000000000000000000000000000000000000000000000000",
		},
		testMetricProvider.fakeStateTrieRootGauge.WithArgsForCall(0),
	)
	require.InDelta(t, float64(1), testMetricProvider.fakeStateTrieRootGauge.SetArgsForCall(0), 0)
}

func TestStatsStateTrieGC(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()
	s := newStats(testMetricProvider.fakeProvider, false)

	reporter := statedb.RootReporter(s)
	reporter.GC("ledger1", 1234, 567, 42.5)

	require.Equal(
		t,
		[]string{"channel", "ledger1"},
		testMetricProvider.fakeStateTrieLiveNodesGauge.WithArgsForCall(0),
	)
	require.InDelta(t, float64(1234), testMetricProvider.fakeStateTrieLiveNodesGauge.SetArgsForCall(0), 0)
	require.Equal(
		t,
		[]string{"channel", "ledger1"},
		testMetricProvider.fakeStateTrieGCDurationHist.WithArgsForCall(0),
	)
	require.InDelta(t, float64(42.5), testMetricProvider.fakeStateTrieGCDurationHist.ObserveArgsForCall(0), 0)
}

func TestProviderStateRootReporter(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()

	cryptoProvider, err := sw.NewDefaultSecurityLevelWithKeystore(sw.NewDummyKeyStore())
	require.NoError(t, err)
	provider, err := NewProvider(
		&lgr.Initializer{
			DeployedChaincodeInfoProvider: &mock.DeployedChaincodeInfoProvider{},
			MetricsProvider:               testMetricProvider.fakeProvider,
			Config:                        testConfig(t),
			HashProvider:                  cryptoProvider,
		},
	)
	require.NoError(t, err)
	defer provider.Close()

	require.NotNil(t, provider.stats)
	require.Implements(t, (*statedb.RootReporter)(nil), provider.stats)
}

func TestExactTrieMetricsFromConfig(t *testing.T) {
	testMetricProvider := testutilConstructMetricProvider()
	conf := testConfig(t)
	conf.StateDBConfig.LevelDBTrie = &leveldbtrie.Conf{ExactMetrics: true}

	cryptoProvider, err := sw.NewDefaultSecurityLevelWithKeystore(sw.NewDummyKeyStore())
	require.NoError(t, err)
	provider, err := NewProvider(
		&lgr.Initializer{
			DeployedChaincodeInfoProvider: &mock.DeployedChaincodeInfoProvider{},
			MetricsProvider:               testMetricProvider.fakeProvider,
			Config:                        conf,
			HashProvider:                  cryptoProvider,
		},
	)
	require.NoError(t, err)
	defer provider.Close()

	require.True(t, provider.stats.exactTrieMetrics)
}

func TestStatsBlockCommit(t *testing.T) {
	conf := testConfig(t)
	testMetricProvider := testutilConstructMetricProvider()

	cryptoProvider, err := sw.NewDefaultSecurityLevelWithKeystore(sw.NewDummyKeyStore())
	require.NoError(t, err)
	provider, err := NewProvider(
		&lgr.Initializer{
			DeployedChaincodeInfoProvider: &mock.DeployedChaincodeInfoProvider{},
			MetricsProvider:               testMetricProvider.fakeProvider,
			Config:                        conf,
			HashProvider:                  cryptoProvider,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create new Provider: %s", err)
	}
	defer provider.Close()

	// create a ledger
	ledgerid := "ledger1"
	_, gb := testutil.NewBlockGenerator(t, ledgerid, false)
	l, err := provider.CreateFromGenesisBlock(gb)
	require.NoError(t, err)
	ledger := l.(*kvLedger)
	defer ledger.Close()

	// calls during committing genesis block
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeBlockProcessingTimeHist.WithArgsForCall(0),
	)
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeBlockstorageCommitWithPvtDataTimeHist.WithArgsForCall(0),
	)
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeStatedbCommitTimeHist.WithArgsForCall(0),
	)
	require.Equal(
		t,
		[]string{
			"channel", ledgerid,
			"transaction_type", common.HeaderType_CONFIG.String(),
			"chaincode", "unknown",
			"validation_code", peer.TxValidationCode_VALID.String(),
		},
		testMetricProvider.fakeTransactionsCount.WithArgsForCall(0),
	)

	// invoke updateBlockStats api explicitly and verify the calls with fake metrics
	ledger.updateBlockStats(
		1*time.Second, 2*time.Second, 3*time.Second,
		[]*validation.TxStatInfo{
			{
				ValidationCode: peer.TxValidationCode_VALID,
				TxType:         common.HeaderType_ENDORSER_TRANSACTION,
				ChaincodeID:    &peer.ChaincodeID{Name: "mycc", Version: "1.0"},
				NumCollections: 2,
			},
			{
				ValidationCode: peer.TxValidationCode_INVALID_OTHER_REASON,
				TxType:         -1,
			},
		},
	)
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeBlockProcessingTimeHist.WithArgsForCall(1),
	)
	require.InDelta(
		t,
		float64(1),
		testMetricProvider.fakeBlockProcessingTimeHist.ObserveArgsForCall(1),
		0,
	)
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeBlockstorageCommitWithPvtDataTimeHist.WithArgsForCall(1),
	)
	require.InDelta(
		t,
		float64(2),
		testMetricProvider.fakeBlockstorageCommitWithPvtDataTimeHist.ObserveArgsForCall(1),
		0,
	)
	require.Equal(
		t,
		[]string{"channel", ledgerid},
		testMetricProvider.fakeStatedbCommitTimeHist.WithArgsForCall(1),
	)
	require.InDelta(
		t,
		float64(3),
		testMetricProvider.fakeStatedbCommitTimeHist.ObserveArgsForCall(1),
		0,
	)
	require.Equal(
		t,
		[]string{
			"channel", ledgerid,
			"transaction_type", common.HeaderType_ENDORSER_TRANSACTION.String(),
			"chaincode", "mycc:1.0",
			"validation_code", peer.TxValidationCode_VALID.String(),
		},
		testMetricProvider.fakeTransactionsCount.WithArgsForCall(1),
	)
	require.InDelta(
		t,
		float64(1),
		testMetricProvider.fakeTransactionsCount.AddArgsForCall(1),
		0,
	)

	require.Equal(
		t,
		[]string{
			"channel", ledgerid,
			"transaction_type", "unknown",
			"chaincode", "unknown",
			"validation_code", peer.TxValidationCode_INVALID_OTHER_REASON.String(),
		},
		testMetricProvider.fakeTransactionsCount.WithArgsForCall(2),
	)
	require.InDelta(
		t,
		float64(1),
		testMetricProvider.fakeTransactionsCount.AddArgsForCall(2),
		0,
	)
}

type testMetricProvider struct {
	fakeProvider                              *metricsfakes.Provider
	fakeBlockProcessingTimeHist               *metricsfakes.Histogram
	fakeBlockstorageCommitWithPvtDataTimeHist *metricsfakes.Histogram
	fakeStatedbCommitTimeHist                 *metricsfakes.Histogram
	fakeTransactionsCount                     *metricsfakes.Counter
	fakeStateTrieRootHashGauge                *metricsfakes.Gauge
	fakeStateTrieRootGauge                    *metricsfakes.Gauge
	fakeStateTrieLiveNodesGauge               *metricsfakes.Gauge
	fakeStateTrieGCDurationHist               *metricsfakes.Histogram
}

func testutilConstructMetricProvider() *testMetricProvider {
	fakeProvider := &metricsfakes.Provider{}
	fakeBlockProcessingTimeHist := testutilConstructHist()
	fakeBlockstorageCommitWithPvtDataTimeHist := testutilConstructHist()
	fakeStatedbCommitTimeHist := testutilConstructHist()
	fakeTransactionsCount := testutilConstructCounter()
	fakeStateTrieRootHashGauge := testutilConstructGauge()
	fakeStateTrieRootGauge := testutilConstructGauge()
	fakeStateTrieLiveNodesGauge := testutilConstructGauge()
	fakeStateTrieGCDurationHist := testutilConstructHist()
	fakeProvider.NewGaugeStub = func(opts metrics.GaugeOpts) metrics.Gauge {
		switch opts.Name {
		case stateTrieRootHashOpts.Name:
			return fakeStateTrieRootHashGauge
		case stateTrieRootOpts.Name:
			return fakeStateTrieRootGauge
		case stateTrieLiveNodesOpts.Name:
			return fakeStateTrieLiveNodesGauge
		default:
			// return a gauge for metrics in common/ledger
			return testutilConstructGauge()
		}
	}
	fakeProvider.NewHistogramStub = func(opts metrics.HistogramOpts) metrics.Histogram {
		switch opts.Name {
		case blockProcessingTimeOpts.Name:
			return fakeBlockProcessingTimeHist
		case blockAndPvtdataStoreCommitTimeOpts.Name:
			return fakeBlockstorageCommitWithPvtDataTimeHist
		case statedbCommitTimeOpts.Name:
			return fakeStatedbCommitTimeHist
		case stateTrieGCDurationOpts.Name:
			return fakeStateTrieGCDurationHist
		default:
			// return a histogram for metrics in common/ledger
			return testutilConstructHist()
		}
	}

	fakeProvider.NewCounterStub = func(opts metrics.CounterOpts) metrics.Counter {
		switch opts.Name {
		case transactionCountOpts.Name:
			return fakeTransactionsCount
		}
		return nil
	}
	return &testMetricProvider{
		fakeProvider,
		fakeBlockProcessingTimeHist,
		fakeBlockstorageCommitWithPvtDataTimeHist,
		fakeStatedbCommitTimeHist,
		fakeTransactionsCount,
		fakeStateTrieRootHashGauge,
		fakeStateTrieRootGauge,
		fakeStateTrieLiveNodesGauge,
		fakeStateTrieGCDurationHist,
	}
}

func testutilConstructGauge() *metricsfakes.Gauge {
	fakeGauge := &metricsfakes.Gauge{}
	fakeGauge.WithStub = func(lableValues ...string) metrics.Gauge {
		return fakeGauge
	}
	return fakeGauge
}

func testutilConstructHist() *metricsfakes.Histogram {
	fakeHist := &metricsfakes.Histogram{}
	fakeHist.WithStub = func(lableValues ...string) metrics.Histogram {
		return fakeHist
	}
	return fakeHist
}

func testutilConstructCounter() *metricsfakes.Counter {
	fakeCounter := &metricsfakes.Counter{}
	fakeCounter.WithStub = func(lableValues ...string) metrics.Counter {
		return fakeCounter
	}
	return fakeCounter
}
