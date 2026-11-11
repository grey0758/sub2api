//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativePacketAuditOnlyCapturesScopedSyntheticRequest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	t.Setenv("OPENAI_NATIVE_PACKET_AUDIT_DIR", dir)
	marker := strings.Repeat("synthetic", 5)
	scope, _ := json.Marshal(map[string]any{"account_id": 991001, "marker": marker, "expires_at": time.Now().Add(time.Minute).Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "control.json"), scope, 0600))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(200) }))
	defer server.Close()
	request, err := http.NewRequest("POST", server.URL, strings.NewReader(`{"input":"`+marker+`"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer synthetic-never-store")
	observed, finish := prepareNativeCodexPacketAudit(request, nativeBrokerTestAccount())
	response, err := server.Client().Do(observed)
	finish(response, err)
	require.NoError(t, err)
	response.Body.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "attempt-00.wire.json"))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "synthetic-never-store")
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(raw, &metadata))
	require.Equal(t, true, metadata["wrote_request"])
	require.NotEmpty(t, metadata["headers_written"])
	for _, body := range []string{`{"input":"customer content"}`, `{"input":"` + marker + `"}`} {
		req, _ := http.NewRequest("POST", server.URL, strings.NewReader(body))
		account := nativeBrokerTestAccount()
		if strings.Contains(body, marker) {
			account.ID++
		}
		observed, finish = prepareNativeCodexPacketAudit(req, account)
		require.Same(t, req, observed)
		finish(nil, nil)
	}
	_, err = os.Stat(filepath.Join(dir, "attempt-01.intent"))
	require.True(t, os.IsNotExist(err))
}
