/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/hyperledger/fabric/integration/nwo"
	"github.com/hyperledger/fabric/integration/nwo/commands"
	dcli "github.com/moby/moby/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/gmeasure"
	"github.com/tedsuo/ifrit"
	ginkgomon "github.com/tedsuo/ifrit/ginkgomon_v2"
)

var _ = Describe("PebbleDB State Database", func() {
	var (
		tempDir        string
		network        *nwo.Network
		chaincode      nwo.Chaincode
		orderer        *nwo.Orderer
		ordererRunner  *ginkgomon.Runner
		ordererProcess ifrit.Process
		peerProcess    ifrit.Process
		peer           *nwo.Peer
		client         dcli.APIClient
	)

	BeforeEach(func() {
		var err error
		tempDir = GinkgoT().TempDir()

		client, err = dcli.New(dcli.FromEnv)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("checking how to work with pebbledb", func() {
		BeforeEach(func() {
			network = nwo.New(nwo.BasicEtcdRaft(), tempDir, client, StartPort(), components)
			network.StateDatabase = nwo.PebbleDB
			network.StateStateDatabase = nwo.PebbleDB

			// Generate config and bootstrap the network
			network.GenerateConfigTree()
			network.Bootstrap()

			// Start all the fabric processes
			ordererRunner, ordererProcess, peerProcess = network.StartSingleOrdererNetwork("orderer")

			orderer = network.Orderer("orderer")
			nwo.JoinOrdererJoinPeersAppChannel(network, "testchannel", orderer, ordererRunner)

			chaincode = nwo.Chaincode{
				Name:            "simplecc",
				Version:         "0.0",
				Path:            "github.com/hyperledger/fabric/integration/chaincode/simple/cmd",
				Lang:            "golang",
				PackageFile:     filepath.Join(tempDir, "simplecc.tar.gz"),
				Label:           "simplecc",
				SignaturePolicy: `OR ('Org1MSP.member','Org2MSP.member')`,
				Sequence:        "1",
				InitRequired:    true,
				Ctor:            `{"Args":["init","a","100","b","200"]}`,
			}

			By("verifying membership")
			network.VerifyMembership(network.PeersWithChannel("testchannel"), "testchannel")

			By("enabling V2_0 capabilities")
			nwo.EnableCapabilities(network, "testchannel", "Application", "V2_0", orderer, network.PeersWithChannel("testchannel")...)

			By("deploying chaincode")
			nwo.DeployChaincode(network, "testchannel", orderer, chaincode)
		})

		AfterEach(func() {
			if peerProcess != nil {
				peerProcess.Signal(syscall.SIGTERM)
				Eventually(peerProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}

			if ordererProcess != nil {
				ordererProcess.Signal(syscall.SIGTERM)
				Eventually(ordererProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}

			network.Cleanup()
		})

		It("executes chaincode invoke and query with pebbledb", func() {
			By("invoking chaincode")
			peer = network.Peer("Org1", "peer0")
			invokeArgs, err := json.Marshal(map[string][]string{
				"Args": {"invoke", "a", "b", "10"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
				ChannelID: "testchannel",
				Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
				Name:      chaincode.Name,
				Ctor:      string(invokeArgs),
				PeerAddresses: []string{
					network.PeerAddress(peer, nwo.ListenPort),
				},
				WaitForEvent: true,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Err).To(gbytes.Say("Chaincode invoke successful."))

			By("querying chaincode")
			queryArgs, err := json.Marshal(map[string][]string{
				"Args": {"query", "a"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err = network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
				ChannelID: "testchannel",
				Name:      chaincode.Name,
				Ctor:      string(queryArgs),
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("90"))
		})
	})

	Describe("pebbleDB migration from goLevelDB", func() {
		BeforeEach(func() {
			network = nwo.New(nwo.BasicEtcdRaft(), tempDir, client, StartPort(), components)
			network.StateDatabase = nwo.GoLevelDB
			network.StateStateDatabase = nwo.GoLevelDB

			network.GenerateConfigTree()
			network.Bootstrap()

			ordererRunner, ordererProcess, peerProcess = network.StartSingleOrdererNetwork("orderer")
			orderer = network.Orderer("orderer")
			nwo.JoinOrdererJoinPeersAppChannel(network, "testchannel", orderer, ordererRunner)

			chaincode = nwo.Chaincode{
				Name:            "simplecc",
				Version:         "0.0",
				Path:            "github.com/hyperledger/fabric/integration/chaincode/simple/cmd",
				Lang:            "golang",
				PackageFile:     filepath.Join(tempDir, "simplecc.tar.gz"),
				Label:           "simplecc",
				SignaturePolicy: `OR ('Org1MSP.member','Org2MSP.member')`,
				Sequence:        "1",
				InitRequired:    true,
				Ctor:            `{"Args":["init","a","100","b","200"]}`,
			}

			By("verifying membership")
			network.VerifyMembership(network.PeersWithChannel("testchannel"), "testchannel")

			By("enabling V2_0 capabilities")
			nwo.EnableCapabilities(network, "testchannel", "Application", "V2_0", orderer, network.PeersWithChannel("testchannel")...)

			By("deploying chaincode")
			nwo.DeployChaincode(network, "testchannel", orderer, chaincode)
		})

		AfterEach(func() {
			if peerProcess != nil {
				peerProcess.Signal(syscall.SIGTERM)
				Eventually(peerProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}
			if ordererProcess != nil {
				ordererProcess.Signal(syscall.SIGTERM)
				Eventually(ordererProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}
			network.Cleanup()
		})

		It("migrates peer and orderer databases from goleveldb to pebbledb", func() {
			peer = network.Peer("Org1", "peer0")

			By("invoking chaincode to create state with goleveldb")
			invokeArgs, err := json.Marshal(map[string][]string{
				"Args": {"invoke", "a", "b", "10"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
				ChannelID: "testchannel",
				Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
				Name:      chaincode.Name,
				Ctor:      string(invokeArgs),
				PeerAddresses: []string{
					network.PeerAddress(peer, nwo.ListenPort),
				},
				WaitForEvent: true,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Err).To(gbytes.Say("Chaincode invoke successful."))

			By("verifying state with goleveldb")
			queryArgs, err := json.Marshal(map[string][]string{
				"Args": {"query", "a"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err = network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
				ChannelID: "testchannel",
				Name:      chaincode.Name,
				Ctor:      string(queryArgs),
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("90"))

			By("stopping the network")
			peerProcess.Signal(syscall.SIGTERM)
			Eventually(peerProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			ordererProcess.Signal(syscall.SIGTERM)
			Eventually(ordererProcess.Wait(), network.EventuallyTimeout).Should(Receive())

			By("running dbmigrator on peer LevelDB directories")
			dbDirs := []string{}
			for _, tmpPeer := range network.Peers {
				ledgersDataDir := network.PeerLedgerDir(tmpPeer)
				dbDirs = append(
					dbDirs,
					filepath.Join(ledgersDataDir, "stateLeveldb"),
					filepath.Join(ledgersDataDir, "historyLeveldb"),
					filepath.Join(ledgersDataDir, "bookkeeper"),
					filepath.Join(ledgersDataDir, "pvtdataStore"),
					filepath.Join(ledgersDataDir, "configHistory"),
					filepath.Join(ledgersDataDir, "chains", "index"),
					filepath.Join(ledgersDataDir, "ledgerProvider"),
					filepath.Join(network.PeerDir(tmpPeer), "filesystem", "transientstore"),
				)
			}

			By("running dbmigrator on orderer index directory")
			ordererIndexDir := filepath.Join(network.OrdererDir(orderer), "system", "index")
			Expect(ordererIndexDir).To(BeADirectory())
			dbDirs = append(dbDirs, ordererIndexDir)

			dbmigratorPath := components.Build("github.com/hyperledger/fabric/cmd/dbmigrator")
			for _, dbDir := range dbDirs {
				Expect(dbDir).To(BeADirectory())
				cmd := exec.Command(dbmigratorPath, "--db-path", dbDir)
				sess, err = gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			}

			By("removing old LevelDB file lock directories")
			for _, tmpPeer := range network.Peers {
				ledgersDataDir := network.PeerLedgerDir(tmpPeer)
				Expect(os.RemoveAll(filepath.Join(ledgersDataDir, "fileLock"))).NotTo(HaveOccurred())
				Expect(os.RemoveAll(filepath.Join(network.PeerDir(tmpPeer), "filesystem", "transientStoreFileLock"))).NotTo(HaveOccurred())
			}

			By("updating peer config to use pebbledb")
			for _, tmpPeer := range network.Peers {
				core := network.ReadPeerConfig(tmpPeer)
				core.Ledger.StateDatabase = nwo.PebbleDB
				core.Ledger.State.StateDatabase = nwo.PebbleDB
				network.WritePeerConfig(tmpPeer, core)
			}

			By("updating orderer config to use pebbledb")
			ordererConfig := network.ReadOrdererConfig(orderer)
			ordererConfig.FileLedger.StateDatabase = nwo.PebbleDB
			network.WriteOrdererConfig(orderer, ordererConfig)

			By("restarting the network")
			ordererRunner = network.OrdererRunner(network.Orderer("orderer"))
			ordererProcess = ifrit.Invoke(ordererRunner)
			Eventually(ordererProcess.Ready(), network.EventuallyTimeout).Should(BeClosed())
			Eventually(ordererRunner.Err(), network.EventuallyTimeout, time.Second).Should(gbytes.Say("Raft leader changed: 0 -> 1 channel=testchannel node=1"))

			peerGroupRunner := network.PeerGroupRunner()
			peerProcess = ifrit.Invoke(peerGroupRunner)
			Eventually(peerProcess.Ready(), network.EventuallyTimeout).Should(BeClosed())

			By("verifying old data is accessible after migration")
			queryArgs, err = json.Marshal(map[string][]string{
				"Args": {"query", "a"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err = network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
				ChannelID: "testchannel",
				Name:      chaincode.Name,
				Ctor:      string(queryArgs),
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("90"))

			By("executing new transactions after migration")
			invokeArgs, err = json.Marshal(map[string][]string{
				"Args": {"invoke", "a", "b", "20"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err = network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
				ChannelID: "testchannel",
				Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
				Name:      chaincode.Name,
				Ctor:      string(invokeArgs),
				PeerAddresses: []string{
					network.PeerAddress(peer, nwo.ListenPort),
				},
				WaitForEvent: true,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Err).To(gbytes.Say("Chaincode invoke successful."))

			By("verifying new state after migration")
			queryArgs2, err := json.Marshal(map[string][]string{
				"Args": {"query", "a"},
			})
			Expect(err).NotTo(HaveOccurred())
			sess, err = network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
				ChannelID: "testchannel",
				Name:      chaincode.Name,
				Ctor:      string(queryArgs2),
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("70"))
		})
	})

	DescribeTableSubtree(
		"benchmark storage performance",
		func(desc string, dbType string) {
			BeforeEach(func() {
				network = nwo.New(nwo.BasicEtcdRaft(), tempDir, client, StartPort(), components)
				network.StateDatabase = dbType
				network.StateStateDatabase = dbType

				network.GenerateConfigTree()
				network.Bootstrap()

				ordererRunner, ordererProcess, peerProcess = network.StartSingleOrdererNetwork("orderer")
				orderer = network.Orderer("orderer")
				nwo.JoinOrdererJoinPeersAppChannel(network, "testchannel", orderer, ordererRunner)

				peer = network.Peer("Org1", "peer0")

				channelCC := nwo.Chaincode{
					Name:            "benchcc",
					Version:         "0.0",
					Path:            "github.com/hyperledger/fabric/integration/chaincode/multi/cmd",
					Lang:            "golang",
					PackageFile:     filepath.Join(tempDir, "benchcc.tar.gz"),
					Label:           "benchcc",
					SignaturePolicy: `OR ('Org1MSP.member','Org2MSP.member')`,
					Sequence:        "1",
					InitRequired:    true,
					Ctor:            `{"Args":["init"]}`,
				}

				network.VerifyMembership(network.PeersWithChannel("testchannel"), "testchannel")
				nwo.EnableCapabilities(network, "testchannel", "Application", "V2_0", orderer, network.PeersWithChannel("testchannel")...)
				nwo.DeployChaincode(network, "testchannel", orderer, channelCC)
			})

			AfterEach(func() {
				if peerProcess != nil {
					peerProcess.Signal(syscall.SIGTERM)
					Eventually(peerProcess.Wait(), network.EventuallyTimeout).Should(Receive())
				}
				if ordererProcess != nil {
					ordererProcess.Signal(syscall.SIGTERM)
					Eventually(ordererProcess.Wait(), network.EventuallyTimeout).Should(Receive())
				}
				network.Cleanup()
			})

			It("measures sequential write, random read, range scan, batch write", func() {
				experiment := gmeasure.NewExperiment("Storage Benchmark — " + desc)
				AddReportEntry(experiment.Name, experiment)

				numKeys := 1000000 // 1000

				By("sequential write (put " + strconv.Itoa(numKeys) + " keys)")
				experiment.SampleDuration("sequential-write", func(idx int) {
					sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeInvoke{
						ChannelID: "testchannel",
						Orderer:   network.OrdererAddress(orderer, nwo.ListenPort),
						Name:      "benchcc",
						Ctor:      `{"Args":["invoke","true","` + fmt.Sprint(numKeys) + `"]}`,
						PeerAddresses: []string{
							network.PeerAddress(network.Peer("Org1", "peer0"), nwo.ListenPort),
							network.PeerAddress(network.Peer("Org2", "peer0"), nwo.ListenPort),
						},
						WaitForEvent: true,
					})
					Expect(err).NotTo(HaveOccurred())
					Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
				}, gmeasure.SamplingConfig{N: 10}) // 5

				By("random read (get 100 keys)")
				experiment.SampleDuration("random-read", func(idx int) {
					keyNum := (idx * 37) % numKeys
					sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
						ChannelID: "testchannel",
						Name:      "benchcc",
						Ctor:      `{"Args":["get-key","` + fmt.Sprint(keyNum) + `"]}`,
					})
					Expect(err).NotTo(HaveOccurred())
					Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
				}, gmeasure.SamplingConfig{N: 100})

				By("range scan (get-multiple-keys 100 keys)")
				experiment.SampleDuration("range-scan", func(idx int) {
					sess, err := network.PeerUserSession(peer, "User1", commands.ChaincodeQuery{
						ChannelID: "testchannel",
						Name:      "benchcc",
						Ctor:      `{"Args":["get-multiple-keys","100"]}`,
					})
					Expect(err).NotTo(HaveOccurred())
					Eventually(sess, network.EventuallyTimeout).Should(gexec.Exit(0))
				}, gmeasure.SamplingConfig{N: 10})
			})
		},
		Entry("with GoLevelDB", "GoLevelDB", "goleveldb"),
		Entry("with PebbleDB", "PebbleDB", "pebbledb"),
	)
})
