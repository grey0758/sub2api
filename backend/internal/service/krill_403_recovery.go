package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	Krill403RecoveryEnabled = "krill_403_auto_recovery"
	krill403RecoveryKind    = "krill_403_recovery_v1"
	krill403ProbeInterval   = 5 * time.Minute
	krill403HoldDuration    = 24 * time.Hour
)

// The reason is both the durable probe schedule and the CAS generation. A claim
// is persisted before a request, so a crash never immediately repeats that POST.
type krill403Hold struct {
	Kind        string `json:"kind"`
	StatusCode  int    `json:"status_code"`
	TriggeredAt int64  `json:"triggered_at"`
	NextProbeAt int64  `json:"next_probe_at"`
	Attempts    int    `json:"attempts"`
	Claim       string `json:"claim,omitempty"`
}

type krill403HoldWriter interface {
	CompareAndSwapKrill403Hold(context.Context, *Account, string, *time.Time) (bool, error)
}

func (s *RateLimitService) krill403Enabled(a *Account) bool {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.KrillStrictGroupIsolation || a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeAPIKey || a.ProxyID != nil {
		return false
	}
	if enabled, _ := a.Credentials[Krill403RecoveryEnabled].(bool); !enabled {
		return false
	}
	g := &OpenAIGatewayService{cfg: s.cfg}
	for _, id := range a.GroupIDs {
		if g.krillStrictGroupIsolation(&id) {
			return true
		}
	}
	for _, binding := range a.AccountGroups {
		id := binding.GroupID
		if g.krillStrictGroupIsolation(&id) {
			return true
		}
	}
	return false
}

func parseKrill403Hold(reason string) (krill403Hold, bool) {
	var h krill403Hold
	err := json.Unmarshal([]byte(reason), &h)
	return h, err == nil && h.Kind == krill403RecoveryKind && h.StatusCode == 403 && h.TriggeredAt > 0 && h.NextProbeAt > 0
}

func (s *RateLimitService) holdKrill403(ctx context.Context, a *Account) bool {
	if !s.krill403Enabled(a) {
		return false
	}
	now := time.Now()
	h := krill403Hold{Kind: krill403RecoveryKind, StatusCode: 403, TriggeredAt: now.Unix(), NextProbeAt: now.Add(krill403ProbeInterval).Unix()}
	raw, _ := json.Marshal(h)
	until := now.Add(krill403HoldDuration)
	s.notifyAccountSchedulingBlocked(a, until, krill403RecoveryKind)
	// Failure must not fall through to the generic permanent-disable path.
	if err := s.accountRepo.SetTempUnschedulable(ctx, a.ID, until, string(raw)); err != nil {
		slog.Error("krill403_hold_write_failed", "account_id", a.ID)
	}
	slog.Warn("krill403_recovery_pending", "account_id", a.ID, "next_probe_at", h.NextProbeAt)
	return true
}

type Krill403Recovery struct {
	rate   *RateLimitService
	http   HTTPUpstream
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newKrill403Recovery(rate *RateLimitService, upstream HTTPUpstream) *Krill403Recovery {
	ctx, cancel := context.WithCancel(context.Background())
	return &Krill403Recovery{rate: rate, http: upstream, ctx: ctx, cancel: cancel}
}

func (r *Krill403Recovery) Start() {
	if r.rate == nil || r.rate.cfg == nil || !r.rate.cfg.Gateway.KrillStrictGroupIsolation {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				r.runDue(r.ctx, time.Now())
			}
		}
	}()
}

func (r *Krill403Recovery) Stop() { r.cancel(); r.wg.Wait() }

func (r *Krill403Recovery) runDue(ctx context.Context, now time.Time) {
	writer, ok := r.rate.accountRepo.(krill403HoldWriter)
	if !ok || r.http == nil {
		return
	}
	groups := append([]int64{r.rate.cfg.Gateway.KrillCompatibilityGroupID}, r.rate.cfg.Gateway.KrillCompatibilityGroupIDs...)
	seen := map[int64]bool{}
	for _, group := range groups {
		if group <= 0 {
			continue
		}
		accounts, err := r.rate.accountRepo.ListByGroup(ctx, group)
		if err != nil {
			continue
		}
		for i := range accounts {
			a := &accounts[i]
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			r.probeIfDue(ctx, writer, a, now)
		}
	}
}

func (r *Krill403Recovery) probeIfDue(ctx context.Context, writer krill403HoldWriter, a *Account, now time.Time) {
	if !r.rate.krill403Enabled(a) || a.Status != StatusActive || !a.Schedulable || (a.ExpiresAt != nil && !a.ExpiresAt.After(now)) {
		return
	}
	h, ok := parseKrill403Hold(a.TempUnschedulableReason)
	if !ok || h.NextProbeAt > now.Unix() {
		return
	}
	if _, ok := krill403ProbeURL(a); !ok {
		return
	}
	claimBytes := make([]byte, 16)
	if _, err := rand.Read(claimBytes); err != nil {
		return
	}
	h.Claim = hex.EncodeToString(claimBytes)
	h.Attempts++
	h.NextProbeAt = now.Add(krill403ProbeInterval).Unix()
	raw, _ := json.Marshal(h)
	until := now.Add(krill403HoldDuration)
	applied, err := writer.CompareAndSwapKrill403Hold(ctx, a, string(raw), &until)
	if err != nil || !applied {
		return
	}
	claimed := *a
	claimed.TempUnschedulableReason = string(raw)
	claimed.TempUnschedulableUntil = &until
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if !r.probe(probeCtx, &claimed) {
		slog.Info("krill403_probe_pending", "account_id", a.ID, "attempt", h.Attempts, "next_probe_at", h.NextProbeAt)
		return
	}
	applied, err = writer.CompareAndSwapKrill403Hold(ctx, &claimed, "", nil)
	if err != nil || !applied {
		return
	}
	r.rate.ResetOpenAI403Counter(ctx, a.ID)
	if r.rate.tempUnschedCache != nil {
		_ = r.rate.tempUnschedCache.DeleteTempUnsched(ctx, a.ID)
	}
	r.rate.notifyAccountSchedulingBlockCleared(a.ID)
	slog.Info("krill403_recovered", "account_id", a.ID, "attempt", h.Attempts)
}

func krill403ProbeURL(a *Account) (string, bool) {
	u, err := url.Parse(a.GetCredential("base_url"))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || u.Path != "/_krill_codex_egress" {
		return "", false
	}
	if u.Host != "sub2api.9gpt.net" && u.Host != "ovh003.ip.xiannai.me" {
		return "", false
	}
	return u.String() + "/v1/responses", true
}

func (r *Krill403Recovery) probe(ctx context.Context, a *Account) bool {
	endpoint, ok := krill403ProbeURL(a)
	if !ok {
		return false
	}
	markerBytes := make([]byte, 24)
	if _, err := rand.Read(markerBytes); err != nil {
		return false
	}
	marker := hex.EncodeToString(markerBytes)
	body, _ := json.Marshal(map[string]any{"model": "gpt-6.1-sol", "stream": true, "store": false, "instructions": "Return only the requested marker.", "input": []map[string]string{{"role": "user", "content": "Return exactly " + marker}}, "reasoning": map[string]string{"effort": "low"}})
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+a.GetCredential("api_key"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "Krill403Recovery/1.0")
	resp, err := r.http.Do(req, "", a.ID, 1)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp == nil || resp.StatusCode != 200 {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return false
	}
	return krill403Complete(raw, marker)
}

func krill403Complete(raw []byte, marker string) bool {
	completed := 0
	text := ""
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		var e struct {
			Type     string `json:"type"`
			Response struct {
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
				Output []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(line[5:]), &e); err != nil {
			return false
		}
		switch e.Type {
		case "error", "response.failed", "response.incomplete":
			return false
		case "response.completed":
			if e.Response.Status != "completed" || (len(e.Response.Error) > 0 && string(e.Response.Error) != "null") {
				return false
			}
			completed++
			for _, o := range e.Response.Output {
				for _, c := range o.Content {
					if c.Type == "output_text" {
						text += c.Text
					}
				}
			}
		}
	}
	return completed == 1 && strings.TrimSpace(text) == marker
}
