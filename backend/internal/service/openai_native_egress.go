package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var nativeEgressClients sync.Map

func NativeCodexOnlyAccountID() int64 {
	value, _ := strconv.ParseInt(os.Getenv("OPENAI_NATIVE_ONLY_ACCOUNT_ID"), 10, 64)
	return value
}

func nativeCodexSourceAllowed(account *Account, model string) bool {
	id := NativeCodexOnlyAccountID()
	if id <= 0 || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-") {
		return true
	}
	return account != nil && account.ID == id && account.UsesNativeCodexTokenBroker()
}

func doNativeCodexEgress(request *http.Request) (*http.Response, error) {
	path := os.Getenv("OPENAI_NATIVE_EGRESS_SOCKET")
	uid, err := strconv.ParseUint(os.Getenv("OPENAI_NATIVE_TOKEN_BROKER_UID"), 10, 32)
	if err != nil || !strings.HasPrefix(path, "/") {
		return nil, errors.New("native Codex egress unavailable")
	}
	key := path + ":" + strconv.FormatUint(uid, 10)
	clientValue, _ := nativeEgressClients.LoadOrStore(key, &http.Client{Transport: &http.Transport{
		DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 120 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
			if err != nil {
				return nil, errors.New("native Codex egress unavailable")
			}
			raw, err := conn.(*net.UnixConn).SyscallConn()
			if err != nil {
				conn.Close()
				return nil, err
			}
			var peer *syscall.Ucred
			var peerErr error
			err = raw.Control(func(fd uintptr) {
				peer, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			})
			if err != nil || peerErr != nil || peer == nil || peer.Uid != uint32(uid) {
				conn.Close()
				return nil, errors.New("native Codex egress peer denied")
			}
			return conn, nil
		},
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	clone := request.Clone(request.Context())
	copyURL := *request.URL
	copyURL.Scheme = "http"
	clone.URL = &copyURL
	clone.Host = "chatgpt.com"
	clone.Header = request.Header.Clone()
	return clientValue.(*http.Client).Do(clone)
}

// Native model discovery must never look for a local access token or substitute
// a gateway-wide client version. Fetch through the same fixed native egress.
func (s *OpenAIGatewayService) fetchNativeCodexModels(ctx context.Context, account *Account, ifNoneMatch string) (*OpenAIModelsResponse, error) {
	if id := NativeCodexOnlyAccountID(); id > 0 && account.ID != id {
		return nil, errors.New("native Codex model source denied")
	}
	ctx = context.WithValue(ctx, nativeCodexIdentityContextKey{}, &nativeCodexRequestIdentity{})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	identity := ctx.Value(nativeCodexIdentityContextKey{}).(*nativeCodexRequestIdentity)
	version := strings.SplitN(strings.SplitN(identity.userAgent, "/", 2)[1], " ", 2)[0]
	req, err := http.NewRequestWithContext(ctx, "GET", "https://chatgpt.com/backend-api/codex/models?client_version="+version, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", account.GetChatGPTAccountID())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", identity.userAgent)
	req.Header.Set("originator", strings.SplitN(identity.userAgent, "/", 2)[0])
	response, err := doNativeCodexEgress(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || len(body) > 16*1024*1024 {
		return nil, errors.New("native Codex model catalogue unavailable")
	}
	if _, _, err = modelCatalogEntries(body, "models"); err != nil {
		return nil, err
	}
	etag := codexModelsManifestBodyETag(body)
	return &OpenAIModelsResponse{Body: body, ETag: etag, NotModified: ifNoneMatch != "" && ifNoneMatch == etag}, nil
}
