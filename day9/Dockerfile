# 选择go镜像并命名阶段
FROM golang:1.26 AS builder
# 设置工作目录(这个目录是在镜像内部) 
# mkdir -p /cloudtask/day9
# cd /cloudtask/day9
# WORKDIR /cloudtask/day9
WORKDIR /cloudtask
#复制Module文件
COPY go.mod go.sum ./
# 下载依赖
RUN GOPROXY=https://goproxy.cn,direct go mod download
#复制整个day9中源码
COPY day9 /cloudtask/day9/
#编译Linux程序并明确输出位置
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cloudtask-api ./day9/cmd/server




# 运行阶段 
# alpine是一个独立的基础镜像
# 提供基本的Linux环境，不包含Go编译器
FROM alpine:3.23 AS runtime

# 如需验证HTTPS或数据库TLS证书，安装CA证书和url，安装软件需要root权限
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates curl


# 创建 app 用户组和用户
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app

LABEL org.opencontainers.image.source="https://github.com/Im-Sage/cloudtask-go"

# 设置运行阶段的自己的工作目录
WORKDIR /app

# 复制编译阶段的builder的可执行二进制文件
# COPY --from 的源路径从指定阶段的文件系统根目录开始解析
# /out/cloudtask-api是构建阶段的输出文件
# /app/cloudtask-api属于新的 Runtime 阶段。
COPY --from=builder --chown=0:0 --chmod=0555 /out/cloudtask-api /app/cloudtask-api

# 后续指令及容器默认进程使用此用户组中的用户，在此之前都是root
USER 10001:10001


# 增加HEALTHCHECK
# --start-period=2s为启动宽限期，--retries=3连续失败3次，标记为unhealthy
HEALTHCHECK --interval=30s --timeout=30s --start-period=5s --retries=3 CMD curl -fsS --max-time 2 http://127.0.0.1:8080/readyz || exit 1

# 配置API的启动命令
CMD [ "/app/cloudtask-api" ]