//go:build unit

package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeOnlyGPTSourceBlocksOtherCredentials(t *testing.T) {
	t.Setenv("OPENAI_NATIVE_ONLY_ACCOUNT_ID", "256")
	native := nativeBrokerTestAccount()
	native.ID = 256
	require.True(t, nativeCodexSourceAllowed(native, "gpt-6.1-sol"))
	other := *native
	other.ID = 257
	require.False(t, nativeCodexSourceAllowed(&other, "gpt-6.1-sol"))
	require.False(t, nativeCodexSourceAllowed(&Account{ID: 256, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "gpt-6.1-sol"))
	require.True(t, nativeCodexSourceAllowed(&other, "gemini-3.8-flash"))
	scheduler := &defaultOpenAIAccountScheduler{}
	ok, reason := scheduler.isAccountRequestCompatibleReason(context.Background(), &other, OpenAIAccountScheduleRequest{RequestedModel: "gpt-6.1-sol"})
	require.False(t, ok)
	require.Equal(t, "native_codex_source_required", reason)
}

func TestNativeEgressUsesPinnedUnixPeerWithoutProxyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "http.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Setenv("OPENAI_NATIVE_EGRESS_SOCKET", path)
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "chatgpt.com", r.Host)
		require.Equal(t, "/backend-api/codex/responses", r.URL.Path)
		require.Empty(t, r.Header.Get("Accept-Encoding"))
		data, _ := io.ReadAll(r.Body)
		require.Equal(t, `{"model":"gpt-6.1-sol"}`, string(data))
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: completed\n\n")
	})}
	go server.Serve(listener)
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), "POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6.1-sol"}`))
	require.NoError(t, err)
	response, err := doNativeCodexEgress(request)
	require.NoError(t, err)
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	require.Equal(t, "data: completed\n\n", string(raw))
	require.Equal(t, "https", request.URL.Scheme)
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()+1))
	response, err = doNativeCodexEgress(request)
	require.Error(t, err)
	require.Nil(t, response)
	t.Setenv("OPENAI_NATIVE_EGRESS_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	response, err = doNativeCodexEgress(request)
	require.Error(t, err)
	require.Nil(t, response)
}

func TestNativeCodexManifestUsesNativeTokenAndVersion(t *testing.T) {
	lease := nativeBrokerTestLease()
	nativeBrokerFixture(t, lease)
	path := filepath.Join(t.TempDir(), "manifest.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Setenv("OPENAI_NATIVE_EGRESS_SOCKET", path)
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "0.163.0", r.URL.Query().Get("client_version"))
		require.Empty(t, r.Header.Get("version"))
		require.Equal(t, lease["user_agent"], r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol"}]}`)
	})}
	go server.Serve(listener)
	defer server.Close()
	response, err := (&OpenAIGatewayService{}).FetchCodexModelsManifest(t.Context(), nativeBrokerTestAccount(), "0.999.0", "")
	require.NoError(t, err)
	require.Contains(t, string(response.Body), "gpt-6.1-sol")
	require.NotEmpty(t, response.ETag)
}

func TestNativeHTTPAcceptsActualBridgeIdentityWithoutGlobalRelabel(t *testing.T) {
	ua := "us003_outer_tool_relay/0.163.0 (Ubuntu 26.4.0; x86_64) unknown (us003_outer_tool_relay; 0.1)"
	require.True(t, validNativeCodexUserAgent(ua))
	require.False(t, validNativeCodexUserAgent("arbitrary/0.163.0"))
	ctx := context.WithValue(t.Context(), nativeCodexIdentityContextKey{}, &nativeCodexRequestIdentity{userAgent: ua})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, applyNativeCodexHTTPRequest(ctx, req, []byte(`{"model":"gpt-6.1-sol"}`)))
	require.Equal(t, ua, req.Header.Get("User-Agent"))
	require.Equal(t, "us003_outer_tool_relay", req.Header.Get("originator"))
}

func TestNativeHTTPPreservesRawInputAndOfficialTopLevelOrder(t *testing.T) {
	original := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[{"role":"user","content":"<native>"}],"store":false}`)
	final := []byte(`{"input":[{"content":"\u003cnative\u003e","role":"user"}],"model":"gpt-6.1-sol","store":false,"stream":true}`)
	body, err := marshalNativeCodexHTTPBody(original, final)
	require.NoError(t, err)
	require.Equal(t, string(original), string(body))
}

func TestNativeAccountTestRequiresNativeEgress(t *testing.T) {
	t.Setenv("OPENAI_NATIVE_EGRESS_REQUIRED", "true")
	t.Setenv("OPENAI_NATIVE_EGRESS_SOCKET", "")
	request, err := http.NewRequestWithContext(t.Context(), "POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6.1-sol","store":false}`))
	require.NoError(t, err)
	response, err := (&AccountTestService{}).doOpenAIAccountTestUpstream(request, "http://must-not-be-used.invalid", nativeBrokerTestAccount(), true)
	require.ErrorContains(t, err, "native Codex egress unavailable")
	require.Nil(t, response)
}
