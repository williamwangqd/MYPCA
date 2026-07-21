// 本测试文件验证用户使用明细正文捕获中间件。
// 覆盖内容：
// 1. 中间件读取请求正文后会完整恢复 Request.Body，不影响业务 handler 再次读取。
// 2. Write 与 WriteString 写出的响应会按客户端实际接收顺序合并保存。
// 3. 管理接口不会创建正文捕获对象，避免记录管理操作中的敏感信息。
package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestClientUsageContentCaptureMiddlewareCapturesRequestAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ClientUsageContentCaptureMiddleware())

	var captured *coreusage.ContentCapture
	var handlerRequestBody string
	engine.POST("/v1/responses", func(c *gin.Context) {
		captured = coreusage.ContentCaptureFromContext(c.Request.Context())
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			t.Fatalf("read restored request body: %v", errRead)
		}
		handlerRequestBody = string(body)
		_, _ = c.Writer.Write([]byte(`{"type":"response.output_text.delta","delta":"你好"}`))
		_, _ = c.Writer.WriteString("\n完成")
	})

	requestBody := `{"model":"gpt-test","input":"请解释今天的统计"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if handlerRequestBody != requestBody {
		t.Fatalf("handler request body = %q, want %q", handlerRequestBody, requestBody)
	}
	if captured == nil {
		t.Fatal("content capture is nil")
	}
	storedRequest, storedResponse := captured.Wait()
	if string(storedRequest) != requestBody {
		t.Fatalf("captured request = %q, want %q", storedRequest, requestBody)
	}
	if string(storedResponse) != recorder.Body.String() {
		t.Fatalf("captured response = %q, want %q", storedResponse, recorder.Body.String())
	}
}

func TestClientUsageContentCaptureMiddlewareSkipsManagementRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ClientUsageContentCaptureMiddleware())
	engine.POST("/v0/management/config", func(c *gin.Context) {
		if capture := coreusage.ContentCaptureFromContext(c.Request.Context()); capture != nil {
			t.Fatal("management request must not create content capture")
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v0/management/config", strings.NewReader(`{"secret-key":"secret"}`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestDecodeClientUsageRequestBodySupportsZstd(t *testing.T) {
	writer, errWriter := zstd.NewWriter(nil)
	if errWriter != nil {
		t.Fatalf("create zstd writer: %v", errWriter)
	}
	plain := []byte(`{"input":"压缩后的提示词"}`)
	compressed := writer.EncodeAll(plain, nil)
	writer.Close()
	decoded := decodeClientUsageRequestBody(compressed, "zstd")
	if string(decoded) != string(plain) {
		t.Fatalf("decoded body = %q, want %q", decoded, plain)
	}
}
