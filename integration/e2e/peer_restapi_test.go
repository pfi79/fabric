/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"github.com/hyperledger/fabric/integration/restpeer"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/hyperledger/fabric/integration/nwo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("PeerRestAPI", func() {
	var (
		client  *docker.Client
		tempDir string
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "nwo")
		Expect(err).NotTo(HaveOccurred())

		client, err = docker.NewClientFromEnv()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err := os.RemoveAll(tempDir)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Rest tests", func() {
		var network *nwo.Network
		// var ordererRunner *ginkgomon.Runner
		var ordererProcess, peerProcess ifrit.Process

		BeforeEach(func() {
			network = nwo.New(nwo.BasicEtcdRaft(), tempDir, client, StartPort(), components)

			// Generate config and bootstrap the network
			network.GenerateConfigTree()
			network.Bootstrap()

			// Start all the fabric processes
			_, ordererProcess, peerProcess = network.StartSingleOrdererNetwork("orderer")
		})

		AfterEach(func() {
			if ordererProcess != nil {
				ordererProcess.Signal(syscall.SIGTERM)
				Eventually(ordererProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}

			if peerProcess != nil {
				peerProcess.Signal(syscall.SIGTERM)
				Eventually(peerProcess.Wait(), network.EventuallyTimeout).Should(Receive())
			}

			network.Cleanup()
		})

		It("GET version", func() {
			peer := network.Peer("Org1", "peer0")
			infoStr := fmt.Sprintf(
				"peer:\n Version: latest\n Commit SHA: development build\n Go version: %s\n OS/Arch: %s\n Chaincode:\n  Base Docker Label: org.hyperledger.fabric\n  Docker Namespace: hyperledger\n\n",
				runtime.Version(),
				fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			)
			restpeer.Version(network, peer, infoStr)
		})
	})
})
