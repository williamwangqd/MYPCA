// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// 本文件实现用户使用明细所需的请求与响应正文捕获中间件。
// 具体内容：
// 1. 对非管理接口的 POST、PUT、PATCH 请求读取原始正文，并恢复 Request.Body 供后续 handler 正常解析。
// 2. 包装 Gin ResponseWriter，捕获普通 JSON、SSE 流式输出以及 WriteString 写出的最终客户端响应。
// 3. 将捕获对象写入 request context，供 usage 统计链路在请求结束后安全读取。
// 4. 不采集管理接口和静态页面，避免把管理密钥操作混入用户模型调用明细。
package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// ClientUsageContentCaptureMiddleware 为模型请求建立正文捕获对象。
func ClientUsageContentCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !shouldCaptureClientUsageContent(c) {
			c.Next()
			return
		}

		requestBody, errRead := readAndRestoreClientUsageRequestBody(c.Request)
		if errRead != nil {
			c.Next()
			return
		}

		capture := coreusage.NewContentCapture(requestBody)
		c.Request = c.Request.WithContext(coreusage.WithContentCapture(c.Request.Context(), capture))
		writer := &clientUsageCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = writer

		defer func() {
			capture.Complete(writer.Bytes())
		}()
		c.Next()
	}
}

func shouldCaptureClientUsageContent(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v0/management") ||
		strings.HasPrefix(path, "/management") ||
		strings.HasPrefix(path, "/static") {
		return false
	}
	switch c.Request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func readAndRestoreClientUsageRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, nil
	}
	body, errRead := io.ReadAll(request.Body)
	if errRead != nil {
		return nil, errRead
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return decodeClientUsageRequestBody(body, request.Header.Get("Content-Encoding")), nil
}

// decodeClientUsageRequestBody 只改变用于落盘的副本，真实 Request.Body 始终保留客户端原始字节。
// Codex 客户端可能使用 zstd 压缩 JSON；解压失败时保留原文，不能因为统计功能阻断正常请求。
func decodeClientUsageRequestBody(raw []byte, encoding string) []byte {
	if len(raw) == 0 {
		return raw
	}
	parts := strings.Split(encoding, ",")
	body := append([]byte(nil), raw...)
	for index := len(parts) - 1; index >= 0; index-- {
		switch strings.ToLower(strings.TrimSpace(parts[index])) {
		case "", "identity":
			continue
		case "zstd":
			decoder, errDecoder := zstd.NewReader(bytes.NewReader(body))
			if errDecoder != nil {
				return raw
			}
			decoded, errRead := io.ReadAll(decoder)
			decoder.Close()
			if errRead != nil {
				return raw
			}
			body = decoded
		default:
			return raw
		}
	}
	return body
}

// clientUsageCaptureWriter 在不改变客户端响应行为的前提下复制最终响应字节。
type clientUsageCaptureWriter struct {
	gin.ResponseWriter
	mu   sync.Mutex
	body bytes.Buffer
}

func (w *clientUsageCaptureWriter) Write(data []byte) (int, error) {
	n, errWrite := w.ResponseWriter.Write(data)
	if n > 0 {
		w.mu.Lock()
		_, _ = w.body.Write(data[:n])
		w.mu.Unlock()
	}
	return n, errWrite
}

func (w *clientUsageCaptureWriter) WriteString(data string) (int, error) {
	n, errWrite := w.ResponseWriter.WriteString(data)
	if n > 0 {
		w.mu.Lock()
		_, _ = w.body.WriteString(data[:n])
		w.mu.Unlock()
	}
	return n, errWrite
}

func (w *clientUsageCaptureWriter) Bytes() []byte {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...)
}
