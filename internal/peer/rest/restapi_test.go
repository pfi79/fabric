/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rest_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"runtime"
	"testing"

	common "github.com/hyperledger/fabric/common/metadata"
	"github.com/hyperledger/fabric/internal/peer/rest"
	"github.com/hyperledger/fabric/internal/peer/rest/pbrest"
	"github.com/hyperledger/fabric/internal/peer/version"
	"github.com/stretchr/testify/require"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestNewHTTPHandler(t *testing.T) {
	h := rest.NewRestAPIHandler()
	require.NotNilf(t, h, "cannot create handler")
}

func TestHTTPHandler_ServeHTTP_InvalidMethods(t *testing.T) {
	h := rest.NewRestAPIHandler()
	require.NotNilf(t, h, "cannot create handler")

	invalidMethods := []string{http.MethodConnect, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPut, http.MethodTrace}

	t.Run("on /version", func(t *testing.T) {
		invalidMethodsExt := append(invalidMethods, http.MethodDelete, http.MethodPost)
		for _, method := range invalidMethodsExt {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(method, path.Join(rest.URLBaseV1, "version"), nil)
			h.ServeHTTP(resp, req)
			checkErrorResponse(t, http.StatusNotImplemented, "Method Not Allowed", resp)
		}
	})
}

func TestHTTPHandler_ServeHTTP_Errors(t *testing.T) {
	h := rest.NewRestAPIHandler()
	require.NotNilf(t, h, "cannot create handler")

	t.Run("bad base", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/oops", nil)
		h.ServeHTTP(resp, req)
		require.Equal(t, http.StatusNotFound, resp.Result().StatusCode)
	})

	t.Run("bad resource", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, rest.URLBaseV1+"oops", nil)
		h.ServeHTTP(resp, req)
		require.Equal(t, http.StatusNotFound, resp.Result().StatusCode)
	})
}

func TestHTTPHandler_ServeHTTP_Version(t *testing.T) {
	h := rest.NewRestAPIHandler()
	require.NotNilf(t, h, "cannot create handler")

	t.Run("version - ok", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path.Join(rest.URLBaseV1, "version"), nil)
		h.ServeHTTP(resp, req)
		require.Equal(t, http.StatusOK, resp.Result().StatusCode)

		headerArray, headerOK := resp.Result().Header["Content-Type"]
		require.True(t, headerOK)
		require.Len(t, headerArray, 1)
		require.Equal(t, "application/json", headerArray[0])

		respBody, err := io.ReadAll(resp.Result().Body)
		require.NoError(t, err)
		respVersion := &pbrest.VersionResponse{}
		err = protojson.Unmarshal(respBody, respVersion)
		require.NoError(t, err)

		ccinfo := fmt.Sprintf("  Base Docker Label: %s\n"+
			"  Docker Namespace: %s\n",
			common.BaseDockerLabel,
			common.DockerNamespace)
		expected := fmt.Sprintf(
			"%s:\n Version: %s\n Commit SHA: %s\n Go version: %s\n OS/Arch: %s\n Chaincode:\n%s\n",
			version.ProgramName, common.Version,
			common.CommitSHA,
			runtime.Version(),
			fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			ccinfo,
		)

		require.Equal(t, expected, respVersion.GetInfo())
	})
}

func checkErrorResponse(t *testing.T, expectedCode int, expectedErrMsg string, resp *httptest.ResponseRecorder) {
	require.Equal(t, expectedCode, resp.Result().StatusCode)

	headerArray, headerOK := resp.Result().Header["Content-Type"]
	require.True(t, headerOK)
	require.Len(t, headerArray, 1)
	require.Equal(t, "application/json", headerArray[0])

	respErr := &spb.Status{}
	err := protojson.Unmarshal(resp.Body.Bytes(), respErr)
	require.NoError(t, err, "body: %s", resp.Body.String())
	require.Contains(t, respErr.GetMessage(), expectedErrMsg)
}
