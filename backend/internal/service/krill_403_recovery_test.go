//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func krillTestAccount() *Account {
	return &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{8}, Credentials: map[string]any{Krill403RecoveryEnabled: true, "api_key": "adapter-test", "base_url": "https://sub2api.9gpt.net/_krill_codex_egress"}}
}

func krillTestRate(repo AccountRepository) *RateLimitService {
	cfg := &config.Config{}
	cfg.Gateway.KrillStrictGroupIsolation = true
	cfg.Gateway.KrillCompatibilityGroupID = 8
	cfg.Gateway.KrillCompatibilityGroupIDs = []int64{9}
	return NewRateLimitService(repo, nil, cfg, nil, nil)
}

func TestKrill403NeverPermanentlyDisablesOptedInAccount(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"insufficient balance, please redeem a plan code or recharge credits"}}`, `{"error":{"message":"forbidden"}}`, `<html>forbidden</html>`, "", `broken`} {
		t.Run(body, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := krillTestRate(repo)
			counter := &openAI403CounterCacheStub{counts: []int64{99}}
			svc.SetOpenAI403CounterCache(counter)
			for i := 0; i < 4; i++ {
				require.True(t, svc.HandleUpstreamError(context.Background(), krillTestAccount(), 403, http.Header{}, []byte(body), "gpt-6.1-sol"))
			}
			require.Zero(t, repo.setErrorCalls)
			require.Equal(t, 4, repo.tempCalls)
			h, ok := parseKrill403Hold(repo.lastTempReason)
			require.True(t, ok)
			require.WithinDuration(t, time.Now().Add(5*time.Minute), time.Unix(h.NextProbeAt, 0), 3*time.Second)
		})
	}
}

func TestKrill403OptInAnd401Isolation(t *testing.T) {
	for _, change := range []string{"disabled", "other_group", "oauth", "other_platform", "proxy", "flag_off", "401"} {
		t.Run(change, func(t *testing.T) {
			a := krillTestAccount()
			repo := &rateLimitAccountRepoStub{}
			svc := krillTestRate(repo)
			status := 403
			switch change {
			case "disabled":
				svc.cfg.Gateway.KrillStrictGroupIsolation = false
			case "other_group":
				a.GroupIDs = []int64{7}
			case "oauth":
				a.Type = AccountTypeOAuth
			case "other_platform":
				a.Platform = PlatformAnthropic
			case "proxy":
				id := int64(2)
				a.ProxyID = &id
			case "flag_off":
				a.Credentials[Krill403RecoveryEnabled] = false
			case "401":
				status = 401
			}
			svc.HandleUpstreamError(context.Background(), a, status, http.Header{}, []byte(`{"error":{"message":"forbidden"}}`))
			require.Equal(t, 1, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

type krillRecoveryRepo struct {
	mockAccountRepoForGemini
	account   *Account
	writes    int
	casReject bool
}

func (r *krillRecoveryRepo) CompareAndSwapKrill403Hold(_ context.Context, a *Account, reason string, until *time.Time) (bool, error) {
	if r.casReject || r.account.Status != StatusActive || !r.account.Schedulable || !reflect.DeepEqual(a.Credentials, r.account.Credentials) || a.TempUnschedulableReason != r.account.TempUnschedulableReason || !reflect.DeepEqual(a.TempUnschedulableUntil, r.account.TempUnschedulableUntil) {
		return false, nil
	}
	r.account.TempUnschedulableReason = reason
	r.account.TempUnschedulableUntil = until
	r.writes++
	return true, nil
}

type krillFixtureHTTP struct {
	client    *http.Client
	serverURL string
	calls     int
	before    func()
	mode      string
	t         *testing.T
}

func (h *krillFixtureHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	h.calls++
	require.True(h.t, HTTPUpstreamRedirectsDisabled(req.Context()))
	require.Equal(h.t, "Bearer adapter-test", req.Header.Get("Authorization"))
	require.Equal(h.t, "https://sub2api.9gpt.net/_krill_codex_egress/v1/responses", req.URL.String())
	if h.before != nil {
		h.before()
	}
	if h.mode == "transport" {
		return nil, errors.New("transport")
	}
	raw, _ := io.ReadAll(req.Body)
	clone, _ := http.NewRequestWithContext(req.Context(), "POST", h.serverURL, strings.NewReader(string(raw)))
	clone.Header = req.Header.Clone()
	return h.client.Do(clone)
}
func (h *krillFixtureHTTP) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return h.Do(req, p, id, n)
}

func TestKrill403ProbeRestoresOnlyExactCompletedResult(t *testing.T) {
	for _, mode := range []string{"success", "balance403", "429", "503", "redirect", "transport", "incomplete", "wrongmarker", "failed", "race_credentials", "race_manual_pause", "race_new_hold", "claim_conflict", "not_due", "healthy"} {
		t.Run(mode, func(t *testing.T) {
			a := krillTestAccount()
			now := time.Now()
			h := krill403Hold{Kind: krill403RecoveryKind, StatusCode: 403, TriggeredAt: now.Add(-10 * time.Minute).Unix(), NextProbeAt: now.Add(-time.Second).Unix()}
			raw, _ := json.Marshal(h)
			until := now.Add(time.Hour)
			a.TempUnschedulableReason = string(raw)
			a.TempUnschedulableUntil = &until
			repo := &krillRecoveryRepo{account: a}
			svc := krillTestRate(repo)
			counter := &openAI403CounterCacheStub{}
			svc.SetOpenAI403CounterCache(counter)
			blocker := &runtimeBlockRecorder{}
			svc.SetAccountRuntimeBlocker(blocker)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				input := body["input"].([]any)[0].(map[string]any)["content"].(string)
				marker := strings.TrimPrefix(input, "Return exactly ")
				switch mode {
				case "balance403":
					w.WriteHeader(403)
					return
				case "429":
					w.WriteHeader(429)
					return
				case "503":
					w.WriteHeader(503)
					return
				case "redirect":
					w.WriteHeader(302)
					return
				case "incomplete":
					io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
					return
				case "wrongmarker":
					marker = "wrong"
				case "failed":
					io.WriteString(w, "data: {\"type\":\"response.failed\"}\n\n")
					return
				}
				e := map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": marker}}}}}}
				b, _ := json.Marshal(e)
				io.WriteString(w, "data: "+string(b)+"\n\n")
			}))
			defer server.Close()
			up := &krillFixtureHTTP{t: t, serverURL: server.URL, client: server.Client(), mode: mode}
			up.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
			switch mode {
			case "race_credentials":
				up.before = func() { a.Credentials = map[string]any{"api_key": "changed"} }
			case "race_manual_pause":
				up.before = func() { a.Schedulable = false }
			case "race_new_hold":
				up.before = func() { a.TempUnschedulableReason = "another_failure" }
			case "claim_conflict":
				repo.casReject = true
			case "not_due":
				h.NextProbeAt = now.Add(time.Hour).Unix()
				b, _ := json.Marshal(h)
				a.TempUnschedulableReason = string(b)
			case "healthy":
				a.TempUnschedulableReason = ""
				a.TempUnschedulableUntil = nil
			}
			runner := newKrill403Recovery(svc, up)
			defer runner.Stop()
			observed := *a
			runner.probeIfDue(context.Background(), repo, &observed, now)
			if mode == "success" {
				require.Empty(t, a.TempUnschedulableReason)
				require.Nil(t, a.TempUnschedulableUntil)
				require.Equal(t, []int64{11}, counter.resetCalls)
				require.Equal(t, []int64{11}, blocker.clearedIDs)
			} else {
				require.Empty(t, counter.resetCalls)
				require.Empty(t, blocker.clearedIDs)
				if mode != "healthy" {
					require.NotEmpty(t, a.TempUnschedulableReason)
				}
			}
			if mode == "claim_conflict" || mode == "not_due" || mode == "healthy" {
				require.Zero(t, up.calls)
			} else {
				require.Equal(t, 1, up.calls)
			}
			// A restart or next scan cannot replay the claimed probe before its durable due time.
			latest := *a
			runner.probeIfDue(context.Background(), repo, &latest, now)
			if mode != "healthy" {
				require.LessOrEqual(t, up.calls, 1)
			}
		})
	}
}

func TestKrill403ProbeRejectsUntrustedEndpoint(t *testing.T) {
	for _, u := range []string{"http://sub2api.9gpt.net/_krill_codex_egress", "https://evil.example/_krill_codex_egress", "https://user@sub2api.9gpt.net/_krill_codex_egress", "https://sub2api.9gpt.net/_krill_codex_egress?q=x", "https://sub2api.9gpt.net:443/_krill_codex_egress", "https://sub2api.9gpt.net/_krill_codex_egress/other"} {
		a := krillTestAccount()
		a.Credentials["base_url"] = u
		_, ok := krill403ProbeURL(a)
		require.False(t, ok)
	}
}

func TestKrill403TerminalValidation(t *testing.T) {
	for _, raw := range []string{"", `data: {"type":"response.completed","response":{"status":"incomplete"}}`, "data: broken\n", `data: {"type":"response.completed","response":{"status":"completed","error":{"code":"x"}}}`} {
		require.False(t, krill403Complete([]byte(raw), "marker"))
	}
}
