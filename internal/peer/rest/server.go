/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rest

import (
	"context"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/hyperledger/fabric/internal/peer/rest/pbrest"
)

const URLBaseV1 = "/peer/v1/"

func NewRestAPIHandler() *runtime.ServeMux {
	ctx := context.Background()

	server := NewRestAPIServer()
	mux := runtime.NewServeMux()
	_ = pbrest.RegisterAPIHandlerServer(ctx, mux, server)

	return mux
}
