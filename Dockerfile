# 坤面板 KunPanel - 单阶段源码构建镜像
# 构建: docker build -t kunpanel:latest .
FROM golang:1.23-alpine AS builder

ARG VERSION=v1.0.0
WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/kunpanel .

# ---------------- 运行镜像 ----------------
FROM alpine:3.20

# docker-cli: 让面板能够管理宿主机 Docker(通过挂载 sock)
RUN apk add --no-cache ca-certificates tzdata docker-cli curl \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

COPY --from=builder /out/kunpanel /opt/kunpanel/bin/kunpanel
RUN chmod +x /opt/kunpanel/bin/kunpanel \
    && mkdir -p /opt/kunpanel/data

ENV KP_DATA_DIR=/opt/kunpanel/data
ENV GIN_MODE=release
WORKDIR /opt/kunpanel

ENV KP_PORT=20000
ENV GIN_MODE=release
EXPOSE 20000
VOLUME ["/opt/kunpanel/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD curl -sf http://127.0.0.1:${KP_PORT:-20000}/api/v1/auth/captcha > /dev/null || exit 1

ENTRYPOINT ["/opt/kunpanel/bin/kunpanel"]
