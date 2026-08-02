# ============================
# Stage 1: Build
# ============================
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# 先复制依赖文件，利用缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译 Linux 静态二进制
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/server ./cmd/server

# ============================
# Stage 2: Runtime
# ============================
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 从 builder 复制二进制
COPY --from=builder /build/server /app/server

# 复制前端静态文件
COPY --from=builder /build/web /app/web

# 创建数据目录
RUN mkdir -p /app/data

EXPOSE 8080

# 使用非 root 用户运行（安全检查兼容）
RUN adduser -D -h /app filesync && chown -R filesync:filesync /app
USER filesync

# 默认使用 SQLite 存储（免 MySQL）
# 可通过环境变量 MYSQL_DSN 启用 MySQL
CMD ["/app/server"]
