# RecMaestro 多阶段构建镜像
# 阶段 1: 构建前端 SPA
FROM node:20-alpine AS webbuild
WORKDIR /app/web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build
# 产物输出到 /app/internal/web/dist（vite outDir 指向 ../internal/web/dist）

# 阶段 2: 编译 Go 二进制（内嵌前端产物）
FROM golang:1.26-alpine AS gobuild
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /app/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -o recmaestro ./cmd/recmaestro

# 阶段 3: 运行时（含 ffmpeg）
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=gobuild /app/recmaestro /usr/local/bin/recmaestro
COPY config/ /etc/recmaestro/config/
WORKDIR /etc/recmaestro
EXPOSE 8080
ENTRYPOINT ["recmaestro"]
