package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Im-Sage/cloudtask-go/internal/config"
	"github.com/gin-gonic/gin"
)

func runServer(logger *slog.Logger, cfg config.Config, router *gin.Engine) error {
	// 创建http.Server
	server := &http.Server{
		Addr: cfg.HTTP.Addr,
		// 处理请求的路由
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		// 	保持连接时，等待下一次请求的最长空闲时间
		IdleTimeout: cfg.HTTP.IdleTimeout,
	}

	signalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,    // Ctrl + C
		syscall.SIGTERM, // docker stop 或者K8s进行Pod滚动更新等操作时
	)
	defer stop()

	serverErr := make(chan error, 1)

	go func() {
		// serverErr的通道缓冲只有1，ListenAndServe只要服务还在正常运行没有出错，就会一直在内部循环中监听端口，接收请求，不会返回
		serverErr <- server.ListenAndServe()
	}()

	select {
	// 从错误通道里取错误
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 发生意外崩溃或底层网络错误而退出
			return fmt.Errorf("HTTP 服务失败: %w", err)
		}
		return nil

	case <-signalCtx.Done():
		// HTTP 服务器是通过显式调用 Server.Shutdown()
		// 或 Server.Close() 被正常关闭的，
		// 而不是因为发生意外崩溃或底层网络错误而退出的。
		logger.Info("收到退出信号，开始关闭服务")
		stop()

		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("优雅关闭失败，强制关闭连接", "error", err)

			if closeErr := server.Close(); closeErr != nil {
				logger.Error("强制关闭失败", "error", closeErr)
			}
			return fmt.Errorf("HTTP 服务优雅关闭失败: %w", err)
		}
		logger.Info("HTTP 服务已关闭")
		return nil
	}
}
