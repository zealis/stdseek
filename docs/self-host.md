# 自托管部署

面向企业内网：把 voidxo 装在一台机器上，给内部技术文档/知识库提供搜索。
不需要 Node、不需要数据库服务、不需要模型文件——只有一个二进制。

## 系统要求

| 项 | 要求 |
|---|---|
| 平台 | linux/amd64、linux/arm64、windows/amd64（发布脚本交叉编译这三目标） |
| 运行时依赖 | 无（纯 Go，CGO 关闭，不依赖 libc 版本） |
| 内存 | **实测 1137 篇文档 = 383MB 常驻**（预热后）。索引常驻内存，按语料线性估算：1 万篇预留 1.5~2GB，生产环境给 2 倍余量 |
| 语料规模 | 从 2 篇到 20 万篇都可用。小语料（几十~几百篇）无需特殊配置——排序层的相关性闸门会按文档数自动缩放（见下） |
| 磁盘 | 实测约 **113MB / 1000 篇**（Bleve 34M + 原文快照 44M + SQLite 50M），另需留出重建时的临时空间 |
| CPU | 抓取与重建受益于多核（`-reindex-workers` 默认取核数）；在线检索单核即可 |

## 三步部署

```bash
# 1. 解压/拷贝二进制
chmod +x voidxo-v0.9.0-linux-amd64

# 2. 建数据目录（也可以让它自己建）
mkdir -p /var/lib/voidxo

# 3. 启动（对外必须显式设令牌）
./voidxo-v0.9.0-linux-amd64 \
  -data /var/lib/voidxo \
  -addr 0.0.0.0:8080 \
  -admin-token "$(openssl rand -hex 16)"
```

浏览器打开 `http://<host>:8080`。首次是空库，首页会显示引导卡片。

**令牌务必记下来**——后台所有接口都要 `X-Admin-Token` 头，留空时每次启动随机生成并打印到日志，
重启即失效。

## Docker 部署

仓库根目录带 `Dockerfile` 与 `docker-compose.yml`。镜像里只有一个静态二进制（前端已经被
`go:embed` 打进去），没有 Node、没有 Go 工具链、不依赖 libc。

```bash
# 一键起：构建 + 启动，数据落在 voidxo-data 卷
VOIDXO_ADMIN_TOKEN=$(openssl rand -hex 24) docker compose up -d --build

# 看日志（没设令牌时，启动日志里会打印一个随机令牌）
docker compose logs -f
```

不想要 compose 也行：

```bash
docker build -t voidxo .
docker run -d --name voidxo \
  -p 127.0.0.1:8080:8080 \
  -v voidxo-data:/data \
  -e TZ=Asia/Shanghai \
  -e VOIDXO_ADMIN_TOKEN=$(openssl rand -hex 24) \
  voidxo
```

与裸机部署的差别都集中在这几条：

| 点 | 说明 |
|---|---|
| 监听地址 | 容器里必须是 `-addr :8080`（镜像的 CMD 已带）。默认 `127.0.0.1` 只有容器自己连得上 |
| 后台令牌 | 用 `-e VOIDXO_ADMIN_TOKEN`，**别写进 CMD**——`docker inspect` / `ps` 都能看到明文。命令行参数仍优先，本地调试更方便 |
| 数据目录 | `/data` 是唯一写入点（SQLite + Bleve + 原文快照都在里面）。命名卷开箱即用；绑定挂载要 `chown 10001:10001`，镜像以非 root 的 uid 10001 运行 |
| 时区 | **必须设 `TZ`**：用量统计按本地自然日切分，容器默认 UTC 会让「今日」差 8 小时（镜像已默认 `Asia/Shanghai`，部署在别处就覆盖） |
| 健康检查 | 镜像自带 `HEALTHCHECK` 打 `/healthz`；`docker ps` 显示 `(healthy)` 即进程与 HTTP 都正常 |
| 备份 | `docker compose stop` 后打包卷：`docker run --rm -v voidxo-data:/data -v "$PWD:/backup" alpine tar czf /backup/voidxo-$(date +%F).tgz -C /data .` |
| 升级 | `docker compose build --pull && docker compose up -d`，卷不动。需要重建索引时：`docker compose run --rm voidxo -data /data -reindex` |

镜像大概 70MB：alpine + CA 证书 + tzdata + `-s -w` 的静态二进制。构建分三段
（Node 编前端 → Go 编译 → 运行时），改 Go 代码不会重跑 `npm ci`；`--build-arg VERSION=`
可以带版本号进二进制（`voidxo -version` 能看到）。

构建需要联网拉 npm 依赖与 Go 模块，国内可以换源：

```bash
docker build -t voidxo \
  --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
  --build-arg GOPROXY=https://goproxy.cn,direct .
```

内存与磁盘量级和裸机一致，照上面「系统要求」那张表估：语料按线性，1 万篇预留 1.5~2GB。

## 作为系统服务

### Linux（systemd）

```ini
# /etc/systemd/system/voidxo.service
[Unit]
Description=voidxo search
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=voidxo
Group=voidxo
ExecStart=/opt/voidxo/voidxo -data /var/lib/voidxo -addr 127.0.0.1:8080
Environment=VOIDXO_LLM_KEY=            # 用改写式摘要时填，留空则抽取式
Restart=on-failure
RestartSec=5

# 数据目录加固：只有服务自己能写
ReadWritePaths=/var/lib/voidxo
ProtectSystem=strict
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

> `-addr` 绑 `127.0.0.1`，由反向代理对外——这样令牌不会明文暴露在公网，
> 也避免把内部管理面直接暴露出去。

### Windows

```powershell
# 用 sc 注册（需管理员）
sc.exe create voidxo binPath= "C:\voidxo\voidxo.exe -data C:\voidxo\data -addr 127.0.0.1:8080" start= auto
sc.exe start voidxo
```

## 反向代理与 TLS

> 完整可复制的配置（含 Apache、禁止下载数据库/配置/缓存、端口防护、systemd 守护）
> 见 README「部署」一节，这里留最小可用版本。

```nginx
server {
    listen 443 ssl http2;
    server_name search.example.com;

    ssl_certificate     /etc/ssl/certs/search.crt;
    ssl_certificate_key /etc/ssl/private/search.key;

    # 检索响应是 JSON，压缩收益明显（结果页含摘要片段）
    # text/javascript 是 esbuild 产物实际用的类型，漏了它 JS 不会被压
    gzip on;
    gzip_types application/json application/manifest+json text/css text/javascript application/javascript;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 60s;   # 摘要模型较慢时留足时间
    }

    # 后台只对内网开放
    location /manage/ {
        allow 10.0.0.0/8;
        deny all;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

> **PWA（安装到桌面 / 离线页）需要 HTTPS**：Service Worker 只在安全上下文注册，
> `http://localhost` 例外。局域网 IP 走 HTTP 时页面功能不受影响，只是没有离线能力。
> 另外别给 `/sw.js` 加 `expires`——它必须是 `no-cache`，否则客户端收不到新版本。

## 首次配置

1. **加白名单**：访问 `/manage/whitelist`（需在页面里填入管理令牌），按「域名 + 分类
   （official / opensource / blog / platform / media + 抓取周期）」录入。
   分类会影响标签判定（`blog` 会打 `personal` 标）与排序质量分。
2. **跑首轮抓取**：`./voidxo -data /var/lib/voidxo -crawl-once`（停服执行），
   或等定时任务（默认 `-crawl-every 15m`，只抓到期站点；这个周期不能大于最短分类间隔 30 分钟，
   否则「新闻阅读 30 分钟」的站点会退化成一小时一次）。
3. **私有语料**：外部站点抓不到的内网文档，用 `cmd/seed` 导入（JSONL，一行一篇）：

   ```json
   {"url":"https://wiki.internal/onboarding","domain":"wiki.internal","category":"official",
    "title":"新人入职手册","description":"环境搭建与权限申请","body":"正文……",
    "labels":[],"published_days_ago":30}
   ```

   只有 `url` 和 `body` 是必需的；`domain` 缺省从 URL 推，`category` 影响质量分与
   「独立网站」/「UGC 平台」标签。导入后不需要 `-reindex`（seed 会直接写索引与快照）。
4. **验证**：`curl http://127.0.0.1:8080/api/status` 看文档数；
   `/healthz` 只回答「进程活着」，`/api/status` 才回答「有没有数据可用」。

### 站点门面文件（随安装自动生成）

首次启动会在数据目录下写三个文件，**用不着手工创建**：

```
<data>/web/robots.txt       爬虫规则（默认不开放 /api、/admin、/manage 与 ?q= 结果页）
<data>/web/security.txt     漏洞联系方式（/.well-known/security.txt 读的就是它）
<data>/web/llms.txt         给大模型看的站点说明书
```

另有 `/sitemap.xml`（**不落盘**，按请求现算）：只列本站的首页、帮助、API 文档、用量、
提交与隐私页。robots.txt 里那行 `Sitemap:` 由服务端自动补上（文件里自己写了就不动）。

- 内容是按当前站名、收录规模、管理员邮箱、站点地址生成的，**可以直接改**。
  服务优先读磁盘上的文件，改完不用重启（缓存 1 小时）。
- **已经存在的不会被覆盖**，所以你的修改在升级后仍在。
- 配好「站点信息 → 站点地址」后跑一次，让文件里的链接换成正式域名：

  ```bash
  ./voidxo -data /var/lib/voidxo -emit-web-files
  ```

Docker 不用额外操作：文件写在 `/data/web`（卷里），改完重启或等缓存过期即可。

## 数据与备份

```
<data>/
  voidxo.db      SQLite（文档元数据 / 白名单 / 账号 / 反馈 / 设置）
  bleve/         全文索引（可从快照重建）
  kv/            原文快照（证据链地基）
```

### 方式一：手动整目录（最简单）

停服后整目录拷贝即可（SQLite 用 WAL，运行中拷贝可能不一致）：

```bash
systemctl stop voidxo
tar czf voidxo-backup-$(date +%F).tar.gz -C /var/lib voidxo
systemctl start voidxo
```

### 方式二：内置云备份（推荐，可异地）

后台「功能开关 → 原文快照 → 管理」配好 S3/B2/R2 桶后，会备份三类东西：

| 内容 | 位置 | 频率 |
|---|---|---|
| 页面原文（按包，逐条 gzip） | `parts/*.pack` | 每小时增量（内容变了才重传） |
| 文档元数据（JSONL.gz） | `exports/` | 每天 |
| **整库快照**（白名单/账号/反馈/设置） | `db/voidxo-*.db.gz` | 可在「整库备份」里选每天 / 每 2 天 / 每 3 天 / 每周 / 关闭，保留最近 7 份 |

四类数据共用**一个私有桶**（前缀 `voidxo/` 下分 `parts/` `idx/` `exports/` `db/`）。
**桶必须设为私有（Private）**：整库快照里有账号邮箱与密码哈希，公开桶等于把它们挂到公网。
客户端只用签名请求，不依赖桶的公开读权限。

整库快照用 `VACUUM INTO` 导出一致性副本，并**清掉登录会话与验证码**——
桶里不会有能直接登录的令牌，但仍有账号邮箱与密码哈希，桶权限要按机密数据对待。

### 灾难恢复（三步，顺序不能换）

```bash
# 0. 停服，把备份下载到本地（用 rclone / aws-cli 均可）
systemctl stop voidxo

# 1. 还原数据库（.db 或 .db.gz；现有库会自动另存为 voidxo.db.before-restore-*）
./voidxo -data /var/lib/voidxo -restore-db ./voidxo-20261029T130145Z-1he8.db.gz

# 2. 启动服务，在后台「原文快照 → 管理 → 取回全部快照」把页面拉回本地
#    （已经在本地的不覆盖；这一步只在快照曾被清空 / 换机器时需要）

# 3. 停服，重建索引（索引不进对象存储，它由「数据库 + 快照」重建出来）
systemctl stop voidxo
./voidxo -data /var/lib/voidxo -reindex
systemctl start voidxo
```

实测（1145 页）：还原数据库约 1 秒，取回全部快照约 45 秒（每页一次 Range GET），
重建索引约 6 分钟（6 并发）。恢复后搜索结果数与备份时一致。

> 只想恢复到「能搜、能看原文」而不重建索引也可以：第 2 步之后直接启动，
> 搜索结果是空的（索引库没了），但管理后台与原文快照都正常。

## 升级

> Docker 升级与「先停服再编译」的坑见 README「部署 → 在线升级」。

```bash
systemctl stop voidxo
cp /opt/voidxo/voidxo /opt/voidxo/voidxo.bak      # 留退路
cp voidxo-v1.0.0-linux-amd64 /opt/voidxo/voidxo
systemctl start voidxo
```

- 只改代码、没改索引结构：换二进制即可。
- 改了索引字段（看发布说明）：停服后跑一次 `-reindex`。
- 升级前用 `-version` 记下旧版本，出问题好回滚。

## 安全清单

- [ ] `-addr` 绑 `127.0.0.1`，由反向代理对外
- [ ] `-admin-token` 用 32 位随机值，不写在命令行历史里（可用 systemd `Environment=`）
- [ ] `/manage/` 限内网来源 IP
- [ ] 确认 `-submit` 符合预期（默认开：用户可提交站点，进审核队列 + 每分钟 120 次限流）
- [ ] 数据目录权限只给服务账号（`chown -R voidxo:voidxo /var/lib/voidxo`）
- [ ] 备份落在另一台机器上

## 小语料的行为（几十~几百篇）

内网导入常见规模是几十到几百篇，这个区间有一个必须知道的机制：

排序层有「相关性闸门」——整批候选的最高 BM25 低于阈值时判定为噪音、返回空。
BM25 的分值由 IDF 主导，而 IDF 随文档数下降，所以**阈值必须跟着语料规模缩放**。
这一版已按 `log(文档数)/log(1000)` 缩放（下限 0.2 倍）：千篇及以上与原行为完全一致，
2 篇时阈值降到 0.02，避免「回滚」这类真实命中被 0.1 的固定阈值全部挡掉
（这正是修复前小语料"什么都搜不到"的原因）。

如果你从很小规模开始爬、后来语料涨到几千篇，不需要做任何配置——阈值随索引文档数自动变化。

## 排错

| 症状 | 查什么 |
|---|---|
| 首页显示"索引还是空的" | `GET /api/status` 看 `documents`；为 0 说明没抓过或白名单为空 |
| 搜不到预期文档 | `-log-level debug` 看逐页过滤原因（thin 薄页 / listpage 列表页 / lang 语言 / boilerplate 站务页） |
| 搜索结果比预期少 | 检查结果页「过滤」里勾选的站点分类与排除标签，以及时间筛选 |
| 后台 401 | 请求头缺 `X-Admin-Token`，或令牌与服务启动时的不一致 |
| 重建后文档少了 | `-reindex` 只重建「表里有记录」的文档；先确认 `voidxo.db` 完整，必要时用 `-repair` 补 |
| 抓取很慢 | 单站串行 + 站点配额，属设计行为；看 `-log-level debug` 的 `crawl` 日志确认为何被限速 |
| 内存涨得快 | 索引常驻内存，按语料量估算；确认没有同时跑 `-reindex`（重建期 24 worker 会到 GB 级） |
