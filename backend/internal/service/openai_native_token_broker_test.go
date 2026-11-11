//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func nativeBrokerTestAccount() *Account {
	return &Account{ID: 991001, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "account-synthetic", "chatgpt_user_id": "user-synthetic"},
		Extra:       map[string]any{"openai_native_token_broker": true}}
}

func nativeBrokerTestLease() map[string]any {
	exp := time.Now().Add(time.Hour).Unix()
	claims, _ := json.Marshal(map[string]any{"exp": exp, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-synthetic", "chatgpt_user_id": "user-synthetic"}})
	return map[string]any{"source": "codex-app-server", "account_id": "account-synthetic", "user_id": "user-synthetic", "expires_at": exp,
		"access_token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"}
}

func nativeBrokerFixture(t *testing.T, lease map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_SOCKET", path)
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()))
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]string
		if json.NewDecoder(conn).Decode(&request) == nil {
			_ = json.NewEncoder(conn).Encode(lease)
		}
	}()
}

func TestNativeCodexBrokerBypassesCacheAndAllRefreshPaths(t *testing.T) {
	lease := nativeBrokerTestLease()
	nativeBrokerFixture(t, lease)
	cache := newOpenAITokenCacheStub()
	provider := NewOpenAITokenProvider(nil, cache, nil)
	account := nativeBrokerTestAccount()
	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.True(t, token == lease["access_token"])
	require.Zero(t, atomic.LoadInt32(&cache.getCalled))
	require.Zero(t, atomic.LoadInt32(&cache.setCalled))
	require.Zero(t, atomic.LoadInt32(&cache.lockCalled))
	require.Zero(t, provider.SnapshotRuntimeMetrics().RefreshRequests)
	require.Len(t, account.Credentials, 2)
	refresher := NewOpenAITokenRefresher(nil, nil)
	require.False(t, refresher.CanRefresh(account))
	require.False(t, refresher.NeedsRefresh(account, time.Hour))
	_, err = (&OpenAIOAuthService{}).RefreshAccountToken(context.Background(), account)
	require.Error(t, err)
	_, err = refresher.Refresh(context.Background(), account)
	require.Error(t, err)
}

func TestNativeCodexBrokerRejectsInvalidLease(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wrong_account":           func(v map[string]any) { v["account_id"] = "other" },
		"wrong_user":              func(v map[string]any) { v["user_id"] = "other" },
		"near_expiry":             func(v map[string]any) { v["expires_at"] = time.Now().Add(time.Minute).Unix() },
		"claim_expiry_mismatch":   func(v map[string]any) { v["expires_at"] = time.Now().Add(2 * time.Hour).Unix() },
		"refresh_token_disclosed": func(v map[string]any) { v["refresh_token"] = "synthetic-secret" },
		"wrong_source":            func(v map[string]any) { v["source"] = "other" },
		"bad_token":               func(v map[string]any) { v["access_token"] = "invalid-synthetic" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			lease := nativeBrokerTestLease()
			mutate(lease)
			nativeBrokerFixture(t, lease)
			token, err := NewOpenAITokenProvider(nil, nil, nil).GetAccessToken(context.Background(), nativeBrokerTestAccount())
			require.Error(t, err)
			require.Empty(t, token)
			require.NotContains(t, err.Error(), "synthetic-secret")
		})
	}
}

func TestNativeCodexBrokerRejectsWrongSocketOwner(t *testing.T) {
	nativeBrokerFixture(t, nativeBrokerTestLease())
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()+1))
	token, err := getNativeCodexAccessToken(context.Background(), nativeBrokerTestAccount())
	require.Error(t, err)
	require.Empty(t, token)
}

func TestNativeCodexBrokerNoGatewayFallbackOrLocalTokens(t *testing.T) {
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("OPENAI_NATIVE_TOKEN_BROKER_UID", strconv.Itoa(os.Geteuid()))
	account := nativeBrokerTestAccount()
	_, _, err := (&OpenAIGatewayService{}).GetAccessToken(context.Background(), account)
	require.Error(t, err)
	account.Credentials["access_token"] = "synthetic-local-access"
	account.Credentials["refresh_token"] = "synthetic-local-refresh"
	token, err := getNativeCodexAccessToken(context.Background(), account)
	require.Error(t, err)
	require.Empty(t, token)
	require.NotContains(t, err.Error(), "synthetic-local")
}

type nativeBrokerShadowRepo struct {
	AccountRepository
	parent *Account
}

func (r *nativeBrokerShadowRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.parent, nil
}

func TestNativeCodexBrokerNoShadowFallbackWithoutProvider(t *testing.T) {
	parent := nativeBrokerTestAccount()
	parent.Credentials["access_token"] = "synthetic-stale-access"
	child := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID}
	svc := &OpenAIGatewayService{accountRepo: &nativeBrokerShadowRepo{parent: parent}}
	token, _, err := svc.GetAccessToken(context.Background(), child)
	require.Error(t, err)
	require.Empty(t, token)
}
