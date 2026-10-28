# syntax=docker/dockerfile:1
#
# voidxo 维空 —— 单镜像部署。
#
# 为什么是三个阶段：前端（Angular）编译产物要用 go:embed 打进二进制，
# 所以镜像里必须有一次 npm 构建；但运行时不需要 Node，也不需要 Go 工具链。
# 最终镜像只留一个静态二进制 + CA 证书 + 时区库（约 70MB）。
#
# 构建：
#   docker build -t voidxo .
#   docker build -t voidxo --build-arg VERSION=$(cat VERSION) .
# 运行：见 docker-compose.yml 或 docs/self-host.md

# ---- 阶段 1：前端 ----
# Angular 22 要求 Node ≥ 22.22.3，用 24（LTS）。alpine 版镜像小，构建不需要 glibc。
FROM node:24-alpine AS web
WORKDIR /src
# 国内构建可以换源（默认走官方源）：
#   docker build --build-arg NPM_REGISTRY=https://registry.npmmirror.com ... 
ARG NPM_REGISTRY=https://registry.npmjs.org
# 先只拷依赖清单：改源码不会让 npm ci 的层缓存失效。
# --dangerously-allow-all-scripts：npm 11 起默认不跑依赖的 install/postinstall 脚本
# （供应链加固），而 Angular 构建依赖 esbuild 的 postinstall 把平台二进制就位，
# 不放行的话 ng build 会直接失败。版本已由 package-lock 钉死（含 integrity），
# 且 node_modules 只留在构建阶段、不进运行时镜像，所以这个风险是可控的。
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci --registry="${NPM_REGISTRY}" --dangerously-allow-all-scripts
COPY web ./web
# npm run build 末尾会调 tools/emit-dist.mjs，把产物同步到 ../internal/webui/dist
RUN cd web && npm run build

# ---- 阶段 2：后端 ----
FROM golang:1.27-alpine AS build
WORKDIR /src
# 同上，国内可以 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用前端阶段的产物覆盖仓库里那份：镜像不该依赖「本机是否跑过 npm run build」，
# 否则在干净克隆上构建会打进去一个旧前端（页面看着没变就是这么来的）。
COPY --from=web /src/internal/webui/dist ./internal/webui/dist

ARG VERSION=dev
ARG COMMIT=
ARG BUILD_TIME=
# CGO_ENABLED=0：SQLite 用纯 Go 驱动（modernc），产物不依赖 libc，alpine/scratch 都能跑。
# -trimpath 去掉本机路径，-s -w 去符号表（二进制约小 30%，不影响排查 panic 栈行号）。
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/voidxo ./cmd/voidxo

# ---- 阶段 3：运行时 ----
FROM alpine:3
# ca-certificates：爬虫要能校验目标站点的 HTTPS 证书，缺了会一律 TLS 失败。
# tzdata：用量统计按「本地自然日」切分，容器默认 UTC 会让「今日」错 8 小时。
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 voidxo \
 && adduser -S -u 10001 -G voidxo voidxo \
 && mkdir -p /data \
 && chown voidxo:voidxo /data

WORKDIR /app
COPY --from=build /out/voidxo /app/voidxo

# 默认时区跟着产品走：用量统计按本地自然日切分，容器默认 UTC 会让「今日」差 8 小时。
# 部署在别的时区就 -e TZ=xxx 覆盖（镜像里已装 tzdata）。
ENV TZ=Asia/Shanghai

# 以非 root 运行：数据目录是唯一需要写权限的地方。
# 用命名卷时 Docker 会继承 /data 的属主，开箱即用；用绑定挂载要自己 chown 10001。
USER voidxo
VOLUME ["/data"]
EXPOSE 8080

# 探针打 /healthz（不查索引，只回进程活着）。
# start-period 给足 20s：启动要打开 Bleve + Pebble，语料大时更慢。
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/voidxo"]
# 容器里必须监听 0.0.0.0（默认 127.0.0.1 只有容器自己连得上）；
# 后台令牌不要写在这里——用 -e VOIDXO_ADMIN_TOKEN，否则 docker inspect 就能看到明文。
CMD ["-addr", ":8080", "-data", "/data"]
