/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package restpeer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hyperledger/fabric/integration/nwo"
	"github.com/hyperledger/fabric/internal/peer/rest/pbrest"
	. "github.com/onsi/gomega"
)

func Version(n *nwo.Network, p *nwo.Peer, expectedResponse string) {
	protocol := "http"
	if n.TLSEnabled {
		protocol = "https"
	}
	url := fmt.Sprintf("%s://127.0.0.1:%d/peer/v1/version", protocol, n.PeerPort(p, nwo.RestAPI))
	authClient, unauthClient := nwo.PeerOperationalClients(n, p)

	client := unauthClient
	if n.TLSEnabled {
		client = authClient
	}

	body := getBody(client, url)()
	verResp := &pbrest.VersionResponse{}
	err := json.Unmarshal([]byte(body), verResp)
	Expect(err).NotTo(HaveOccurred())
	Expect(verResp.GetInfo()).To(Equal(expectedResponse))
}

func getBody(client *http.Client, url string) func() string {
	return func() string {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		bodyBytes, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		err = resp.Body.Close()
		Expect(err).NotTo(HaveOccurred())
		return string(bodyBytes)
	}
}
