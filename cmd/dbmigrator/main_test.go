/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bytes"
	"os/exec"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

// TestHelpNamesBothDirectionsAndEveryFlag is what an operator reads before a
// migration: the two formats the command writes, and the flags that bound the
// memory, turn the check off, and name a channel.
func TestHelpNamesBothDirectionsAndEveryFlag(t *testing.T) {
	gt := gomega.NewWithT(t)
	dbmigrator, err := gexec.Build("github.com/hyperledger/fabric/cmd/dbmigrator")
	gt.Expect(err).NotTo(gomega.HaveOccurred())
	defer gexec.CleanupBuildArtifacts()

	var stderr bytes.Buffer
	session, err := gexec.Start(exec.Command(dbmigrator, "migrate", "--help"), nil, &stderr)
	gt.Expect(err).NotTo(gomega.HaveOccurred())
	gt.Eventually(session, 5*time.Second).Should(gexec.Exit(0))

	help := stderr.String()
	for _, expected := range []string{"leveldbtrie", "goleveldb", "--batch-size", "--verify", "--channel", "--source", "--target"} {
		gt.Expect(help).To(gomega.ContainSubstring(expected))
	}
}

// TestVersionIsPrinted checks that the command answers to --version like the
// commands beside it, rather than failing on an unknown flag.
func TestVersionIsPrinted(t *testing.T) {
	gt := gomega.NewWithT(t)
	dbmigrator, err := gexec.Build("github.com/hyperledger/fabric/cmd/dbmigrator")
	gt.Expect(err).NotTo(gomega.HaveOccurred())
	defer gexec.CleanupBuildArtifacts()

	session, err := gexec.Start(exec.Command(dbmigrator, "--version"), nil, nil)
	gt.Expect(err).NotTo(gomega.HaveOccurred())
	gt.Eventually(session, 5*time.Second).Should(gexec.Exit(0))
}
