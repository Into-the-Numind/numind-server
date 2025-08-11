package middleware

import (
	"compress/gzip"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GzipCompression 中间件，自动压缩响应以减少带宽使用
func GzipCompression() gin.HandlerFunc {
	return gin.HandlerFunc(func(c *gin.Context) {
		// 检查客户端是否支持gzip压缩
		acceptEncoding := c.GetHeader("Accept-Encoding")
		if !strings.Contains(acceptEncoding, "gzip") {
			c.Next()
			return
		}

		// 设置响应头
		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")

		// 创建gzip writer
		gw := gzip.NewWriter(c.Writer)
		defer gw.Close()

		// 包装response writer
		c.Writer = &gzipWriter{
			ResponseWriter: c.Writer,
			gzipWriter:     gw,
		}

		c.Next()
	})
}

// gzipWriter 包装gin的ResponseWriter以支持gzip压缩
type gzipWriter struct {
	gin.ResponseWriter
	gzipWriter *gzip.Writer
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	return g.gzipWriter.Write(data)
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.gzipWriter.Write([]byte(s))
}

func (g *gzipWriter) WriteHeader(code int) {
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Flush() {
	g.gzipWriter.Flush()
	g.ResponseWriter.Flush()
}

func (g *gzipWriter) CloseNotify() <-chan bool {
	return g.ResponseWriter.CloseNotify()
}

func (g *gzipWriter) Status() int {
	return g.ResponseWriter.Status()
}

func (g *gzipWriter) Size() int {
	return g.ResponseWriter.Size()
}

func (g *gzipWriter) Written() bool {
	return g.ResponseWriter.Written()
}

func (g *gzipWriter) Pusher() http.Pusher {
	return g.ResponseWriter.Pusher()
}
