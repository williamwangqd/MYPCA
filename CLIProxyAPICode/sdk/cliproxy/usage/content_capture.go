// 本文件实现客户端请求与响应正文的跨层捕获容器。
// 具体内容：
// 1. 保存进入代理前的原始请求正文，供用户使用明细持久化提示词和请求参数。
// 2. 在 HTTP 请求完成后保存最终写给客户端的响应正文，包括普通 JSON 与流式 SSE 数据。
// 3. 通过 context 在 Gin handler、executor usage reporter 与 usage 插件之间传递同一个捕获对象。
// 4. 使用完成信号保证 usage 插件等待响应写入结束后再读取正文，避免落盘时只有半段流式内容。
package usage

import (
	"context"
	"sync"
)

type contentCaptureContextKey struct{}

// ContentCapture 保存一次客户端请求对应的原始请求正文和最终响应正文。
// 字节切片在写入和读取时都会复制，避免调用方后续修改底层数组造成数据竞争。
type ContentCapture struct {
	mu           sync.RWMutex
	requestBody  []byte
	responseBody []byte
	done         chan struct{}
	completeOnce sync.Once
}

// NewContentCapture 创建正文捕获对象，并立即保存原始请求正文。
func NewContentCapture(requestBody []byte) *ContentCapture {
	return &ContentCapture{
		requestBody: append([]byte(nil), requestBody...),
		done:        make(chan struct{}),
	}
}

// Complete 保存最终响应正文并关闭完成信号。
// 多次调用时仅第一次生效，防止异常恢复路径重复关闭 channel。
func (c *ContentCapture) Complete(responseBody []byte) {
	if c == nil {
		return
	}
	c.completeOnce.Do(func() {
		c.mu.Lock()
		c.responseBody = append([]byte(nil), responseBody...)
		c.mu.Unlock()
		close(c.done)
	})
}

// Wait 等待请求处理结束，然后返回请求与响应正文副本。
func (c *ContentCapture) Wait() ([]byte, []byte) {
	if c == nil {
		return nil, nil
	}
	<-c.done
	return c.Snapshot()
}

// Snapshot 返回当前已捕获内容的副本，不等待请求完成。
func (c *ContentCapture) Snapshot() ([]byte, []byte) {
	if c == nil {
		return nil, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]byte(nil), c.requestBody...), append([]byte(nil), c.responseBody...)
}

// WithContentCapture 将正文捕获对象写入 context。
func WithContentCapture(ctx context.Context, capture *ContentCapture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if capture == nil {
		return ctx
	}
	return context.WithValue(ctx, contentCaptureContextKey{}, capture)
}

// ContentCaptureFromContext 读取当前请求对应的正文捕获对象。
func ContentCaptureFromContext(ctx context.Context) *ContentCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(contentCaptureContextKey{}).(*ContentCapture)
	return capture
}
