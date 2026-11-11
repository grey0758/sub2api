package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	request, finishAudit := prepareNativeCodexPacketAudit(request, account)
	if account.UsesNativeCodexTokenBroker() && os.Getenv("OPENAI_NATIVE_EGRESS_SOCKET") != "" {
		response, err := doNativeCodexEgress(request)
		finishAudit(response, err)
		return response, err
	}
	if account.UsesNativeCodexTokenBroker() && os.Getenv("OPENAI_NATIVE_EGRESS_REQUIRED") == "true" {
		err := errors.New("native Codex egress unavailable")
		finishAudit(nil, err)
		return nil, err
	}

	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			finishAudit(response, err)
			return response, err
		}
	}
	response, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	finishAudit(response, err)
	return response, err
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if account.UsesNativeCodexTokenBroker() && (os.Getenv("OPENAI_NATIVE_EGRESS_SOCKET") != "" || os.Getenv("OPENAI_NATIVE_EGRESS_REQUIRED") == "true") {
		if os.Getenv("OPENAI_NATIVE_EGRESS_SOCKET") == "" {
			return nil, errors.New("native Codex egress unavailable")
		}
		ctx := context.WithValue(request.Context(), nativeCodexIdentityContextKey{}, &nativeCodexRequestIdentity{})
		token, err := getNativeCodexAccessToken(ctx, account)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, 16*1024*1024+1))
		if err != nil || len(body) > 16*1024*1024 {
			return nil, errors.New("native Codex test body unavailable")
		}
		request.Body.Close()
		request = request.Clone(ctx)
		request.Header.Set("Authorization", "Bearer "+token)
		if err := applyNativeCodexHTTPRequest(ctx, request, body); err != nil {
			return nil, err
		}
		return doNativeCodexEgress(request)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
