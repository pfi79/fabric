/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rest

import (
	"context"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric/internal/peer/rest/pbrest"
	"github.com/hyperledger/fabric/internal/peer/version"
)

type APIServer struct {
	pbrest.UnimplementedAPIServer

	logger *flogging.FabricLogger
}

func NewRestAPIServer() *APIServer {
	return &APIServer{
		logger: flogging.MustGetLogger("peer.rest.server"),
	}
}

func (s *APIServer) Version(context.Context, *pbrest.VersionRequest) (*pbrest.VersionResponse, error) {
	return &pbrest.VersionResponse{Info: version.GetInfo()}, nil
}

func (s *APIServer) Invoke(ctx context.Context, req *pbrest.InvokeRequest) (*pbrest.InvokeResponse, error) {
	return &pbrest.InvokeResponse{
		Status:  0,
		Message: "",
		Payload: nil,
	}, nil
}

func (s *APIServer) Query(ctx context.Context, req *pbrest.QueryRequest) (*pbrest.QueryResponse, error) {
	return &pbrest.QueryResponse{
		Status:  0,
		Message: "",
		Payload: nil,
	}, nil
}
