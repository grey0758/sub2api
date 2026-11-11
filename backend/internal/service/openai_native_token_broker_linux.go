package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Opt-in native Codex account; no access/refresh tokens are stored in its credentials.
// The socket and its owner are process configuration, never tenant-controlled.
func (a *Account) UsesNativeCodexTokenBroker() bool {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth {
		return false
	}
	enabled, _ := a.Extra["openai_native_token_broker"].(bool)
	return enabled
}

type nativeCodexTokenLease struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
	UserID      string `json:"user_id"`
	ExpiresAt   int64  `json:"expires_at"`
	Source      string `json:"source"`
	UserAgent   string `json:"user_agent,omitempty"`
}

func readNativeCodexTokenLease(ctx context.Context, expectedAccountID string) (*nativeCodexTokenLease, error) {
	const failure = "native Codex token broker unavailable or lease invalid"
	path := os.Getenv("OPENAI_NATIVE_TOKEN_BROKER_SOCKET")
	uid, err := strconv.ParseUint(os.Getenv("OPENAI_NATIVE_TOKEN_BROKER_UID"), 10, 32)
	if err != nil || !strings.HasPrefix(path, "/") || expectedAccountID == "" {
		return nil, errors.New(failure)
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New(failure)
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return nil, errors.New(failure)
	}
	var peer *syscall.Ucred
	var peerErr error
	if err = raw.Control(func(fd uintptr) {
		peer, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || peerErr != nil || peer == nil || peer.Uid != uint32(uid) {
		return nil, errors.New(failure)
	}
	request := map[string]any{"account_id": expectedAccountID}
	if ctx.Value(nativeCodexIdentityContextKey{}) != nil {
		request["include_client_identity"] = true
	}
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return nil, errors.New(failure)
	}
	var lease nativeCodexTokenLease
	decoder := json.NewDecoder(io.LimitReader(conn, 32768))
	decoder.DisallowUnknownFields() // In particular, never accept a refresh_token.
	if err = decoder.Decode(&lease); err != nil || lease.Source != "codex-app-server" || lease.AccountID != expectedAccountID || lease.ExpiresAt <= time.Now().Add(3*time.Minute).Unix() {
		return nil, errors.New(failure)
	}
	parts := strings.Split(lease.AccessToken, ".")
	if len(parts) != 3 {
		return nil, errors.New(failure)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New(failure)
	}
	var claims struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
			UserID    string `json:"chatgpt_user_id"`
		} `json:"https://api.openai.com/auth"`
	}
	// Transport is authenticated using SO_PEERCRED. Claims are consistency checks,
	// not a substitute for signature validation by the upstream OAuth resource.
	if json.Unmarshal(claimsJSON, &claims) != nil || claims.Exp != lease.ExpiresAt || claims.Auth.AccountID != lease.AccountID || claims.Auth.UserID != lease.UserID {
		return nil, errors.New(failure)
	}
	return &lease, nil
}

func getNativeCodexAccessToken(ctx context.Context, account *Account) (string, error) {
	if account.GetCredential("refresh_token") != "" || account.GetCredential("access_token") != "" || account.GetCredential("id_token") != "" || account.IsShadow() {
		return "", errors.New("native Codex broker account must not contain local tokens or shadow credentials")
	}
	lease, err := readNativeCodexTokenLease(ctx, account.GetChatGPTAccountID())
	if err != nil {
		return "", err
	}
	if expectedUser := account.GetCredential("chatgpt_user_id"); expectedUser != "" && expectedUser != lease.UserID {
		return "", errors.New("native Codex token broker identity mismatch")
	}
	if capture, ok := ctx.Value(nativeCodexIdentityContextKey{}).(*nativeCodexRequestIdentity); ok {
		if !validNativeCodexUserAgent(lease.UserAgent) {
			return "", errors.New("native Codex client identity unavailable")
		}
		capture.userAgent = lease.UserAgent
	}
	return lease.AccessToken, nil
}
