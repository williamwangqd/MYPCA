// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
//
// 本文件实现内容：
// 1. 定义 Antigravity OAuth、Scope、回调端口和 API 端点等基础配置。
// 2. 将 OAuth Client ID / Client Secret 从硬编码改为环境变量读取，避免密钥进入 Git 历史。
// 3. 提供统一的凭据读取函数，供登录流程、管理 API 和运行时执行器复用。
package antigravity

import (
	"fmt"
	"os"
	"strings"
)

// OAuth client credentials and configuration.
const (
	ClientIDEnvName     = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_ID"
	ClientSecretEnvName = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_SECRET"
	CallbackPort        = 51121
)

// OAuthClientID 从环境变量读取 Antigravity OAuth Client ID。
// 这里不提供代码内默认值，避免 GitHub Push Protection 将仓库识别为包含 Google OAuth 凭据。
func OAuthClientID() string {
	return strings.TrimSpace(os.Getenv(ClientIDEnvName))
}

// OAuthClientSecret 从环境变量读取 Antigravity OAuth Client Secret。
// Client Secret 必须由部署环境显式注入，不能写入源码或提交历史。
func OAuthClientSecret() string {
	return strings.TrimSpace(os.Getenv(ClientSecretEnvName))
}

// OAuthClientCredentials 统一读取并校验 Antigravity OAuth 凭据。
// 缺少任一环境变量时返回明确错误，方便管理端和运行时快速定位配置问题。
func OAuthClientCredentials() (string, string, error) {
	clientID := OAuthClientID()
	clientSecret := OAuthClientSecret()
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("missing antigravity OAuth credentials: set %s and %s", ClientIDEnvName, ClientSecretEnvName)
	}
	return clientID, clientSecret, nil
}

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint      = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion       = "v1internal"
)
