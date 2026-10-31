# voidxo（维空）

技术内容的类垂直搜索引擎。**一个二进制文件**，零外部依赖：爬虫、索引、检索 API、
管理后台、前端页面全在里面。

> 部署看本文「部署」一节：源码编译 / Docker 源码构建 / Nginx 与 Apache 配置（含禁止下载数据库）、
> 端口防护、systemd 守护、在线升级。
>
> 容器部署最快一条命令：`docker compose up -d --build`（见 [`docker-compose.yml`](docker-compose.yml)）。
>
> 深入运维（备份恢复、排错、小语料行为）：[`docs/self-host.md`](docs/self-host.md)。

## 为什么是它

| 通用搜索引擎的问题 | voidxo 的做法 |
|---|---|
| 技术查询被 SEO 农场、AI 水文、转载站占满 | **白名单策展**：只索引人工审核过的站点，不做开放抓取 |
| 结果看不出为什么排在那里 | **每条结果带排序理由**（标题精确匹配 / 时效 / 繁荣度 / 同域下调…） |
| 摘要编造原文没有的结论 | **证据链**：摘要必须挂原文引用，无证据时降级为抽取式 |
| 部署要装一堆东西 | **单二进制**：下载、运行、开浏览器 |

## 快速开始

```bash
# 构建（Go 1.27+）
go build -o voidxo ./cmd/voidxo

# 运行（首次会自动建 data/ 目录）
./voidxo

# 打开 http://127.0.0.1:8080
```

空库时首页会显示引导卡片，提示去后台加站点或跑一次抓取：

```bash
./voidxo -crawl-once          # 按白名单抓一轮后退出
./voidxo -addr :8080 -admin-token 你的令牌   # 对外服务（务必设令牌）
```

发布构建（三平台交叉编译 + 版本戳 + 校验和）：

```bash
./scripts/build-release.sh                 # 版本取 VERSION，产物在 dist/
./scripts/build-release.sh --web           # 改了前端时加这个（会先重建前端）
```

## 部署

三种跑法，数据目录结构完全一致（升级时不用搬数据）。

### 1. 源码编译

**要求**：Go 1.27+（`go:embed` 与 generics）；改前端还要 Node ≥ 22.22.3（Angular CLI 22 的硬要求，
本机 managed node 常常不达标）。不需要 Redis / Postgres / 消息队列。

```bash
go build -o voidxo ./cmd/voidxo
./voidxo -addr 127.0.0.1:8080 -admin-token "$(openssl rand -hex 24)"
```

### 2. Docker（从源码构建镜像）

**要求**：

| 项 | 要求 |
|---|---|
| Docker | 20.10+ 与 Compose v2（`docker compose version`） |
| 网络 | **构建阶段要联网**：拉 npm 依赖与 Go 模块；运行时不需要 |
| 时间 | 首次构建 3–10 分钟（前端 + Go 两个编译阶段） |
| 磁盘 | 构建中间层约 2GB，最终运行镜像约 70MB |
| 内存 | 建镜像 ≥ 2GB；运行随索引规模增长，千篇级 200MB 够用 |

```bash
git clone https://github.com/zealis/voidxo.git && cd voidxo

# 令牌写进 .env（不要写在命令行里：docker inspect 能看回来）
echo "VOIDXO_ADMIN_TOKEN=$(openssl rand -hex 24)" > .env

docker compose up -d --build      # 构建并启动
docker compose logs -f            # 首次启动会打印后台令牌（若没设）
```

国内构建换源（不换也能跑，只是慢）：

```bash
docker build -t voidxo:local . \
  --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
  --build-arg GOPROXY=https://goproxy.cn,direct

docker run -d --name voidxo -p 127.0.0.1:8080:8080 \
  -e TZ=Asia/Shanghai \
  --env-file .env \
  -v voidxo-data:/data \
  voidxo:local
```

容器里要注意的几点：

- **数据全在 `/data`**（`voidxo.db`、`bleve/`、`kv/`、以及自动生成的 `web/robots.txt|security.txt|llms.txt`），
  备份就是备份这个卷。
- 镜像里是**非 root（uid 10001）**：用命名卷开箱即用，绑定挂载要先 `chown -R 10001:10001 <目录>`。
- 默认 `TZ=Asia/Shanghai`——用量统计按本地自然日切分，部署在别的时区用 `-e TZ=xxx` 覆盖。
- 容器里监听 `0.0.0.0`（镜像 `CMD` 已带 `-addr :8080`），对外一律走反向代理。

### 3. 反向代理：Nginx / Apache

**不需要伪静态（Rewrite / `.htaccess` 规则）。** 前端虽然是 SPA，但路由兜底在 Go 里：
除 `/api/*` 与 `/admin/*` 之外的所有路径都返回 `index.html`，页面资源是 `go:embed`
编译进二进制的，磁盘上**没有任何需要代理直接提供的静态目录**。所以把全部路径转发给 Go 即可；
配 `root` + `try_files` 反而会 404（或者把目录列出来）。

**Nginx**（≥ 1.18）：

```nginx
upstream voidxo {
    server 127.0.0.1:8080;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name search.example.com;

    ssl_certificate     /etc/ssl/certs/search.crt;
    ssl_certificate_key /etc/ssl/private/search.key;

    # 不设 root：磁盘上没有该被直接下载的东西
    client_max_body_size 8m;     # 后台上传站点图标（≤512KB）与 JSONL 导入
    proxy_read_timeout 120s;     # 导出/冷备这类接口慢，别被掐断
    gzip on;
    gzip_types application/json text/css application/javascript image/svg+xml;

    # ---- 防止访问与下载数据库、配置、运行缓存 ----
    # 这几条是兜底：即使以后有人加了 root，也拿不到这些东西。
    location ^~ /data/ { deny all; }        # SQLite、Bleve 索引、原文快照、冷备临时包、web/ 三个文件
    location ~* \.(db|db-wal|db-shm|sqlite3?|env|ini|toml|ya?ml)$ { deny all; }
    location ~ /\.(git|svn|ht) { deny all; }   # 点目录；/.well-known/security.txt 不受影响

    # 后台只给内网（可选，按需取舍）
    location ^~ /manage/ {
        allow 10.0.0.0/8;
        deny  all;
        proxy_pass http://voidxo;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location / {
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_pass http://voidxo;
        # 这三个头决定分享卡片 og:url / security.txt / llms.txt 里的域名，别漏
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host  $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    }
}
```

**Apache**（2.4，需要模块：`a2enmod proxy proxy_http headers ssl`，**不需要 rewrite**）：

```apache
<VirtualHost *:443>
    ServerName search.example.com
    SSLEngine on
    SSLCertificateFile    /etc/ssl/certs/search.crt
    SSLCertificateKeyFile /etc/ssl/private/search.key

    ProxyRequests Off                 # 别让自己变成开放代理
    ProxyPreserveHost On
    ProxyPass        / http://127.0.0.1:8080/
    ProxyPassReverse / http://127.0.0.1:8080/
    RequestHeader set X-Forwarded-Proto "https"
    LimitRequestBody 8388608

    # ---- 防止访问与下载数据库、配置、运行缓存 ----
    <Directory "/var/lib/voidxo/data">
        Require all denied
    </Directory>
    <FilesMatch "\.(db|db-wal|db-shm|sqlite3?|env|ini|toml|ya?ml)$">
        Require all denied
    </FilesMatch>
    <DirectoryMatch "\.git">
        Require all denied
    </DirectoryMatch>

    # 后台只给内网（可选；加了之后外网管理员就进不去了，按需取舍）
    <LocationMatch "^/(manage|admin)/">
        Require ip 10.0.0.0/8 192.168.0.0/16
    </LocationMatch>
</VirtualHost>
```

> `/manage/` 是后台页面，`/admin/*` 是它调用的接口，二者要一起放行或一起限制；
> 只封 `/admin/` 会让后台页面登录成功却什么都读不到。

### 静态资源：不要挂公共 CDN

前端的所有 JS/CSS 都是 `npm run build` 的产物，**编译进二进制**（`go:embed`），
文件名带内容哈希，由本服务自己发。所以：

- 公共静态库（BootCDN / 360 / jsDelivr / unpkg 等）**没有这些文件**——它们托管的是
  知名开源库，不是你的构建产物。真要放上去只能自己上传，那已经是自建 CDN 了。
- **不要为了「快」去引第三方 CDN**：JS 一旦被中间人改一行就是全站接管（登录框、
  后台令牌都在这套 JS 里）；访客 IP 也会暴露给第三方，而隐私政策写的是「无第三方脚本」。
  跨域加载还多一次 DNS + TLS 握手，国内环境下常常比同源更慢。
- 页面**不请求任何第三方资源**（字体用系统字体、图标自己发），这条是有意的。

真正有效的加速手段，按性价比排序：

| 做法 | 效果 |
|---|---|
| **HTTPS + HTTP/2**（上面的 Nginx 配置已带 `http2`） | 去掉浏览器「每域 6 条连接」的限制，多 chunk 并行。**这是最值得做的一条** |
| 首屏就这些字节：`main` 37KB + 共享 chunk 56KB + HTML 3.5KB（均为 gzip 后） | 已经压到框架本身的下限 |
| 自己的域名挂国内 CDN（需 ICP 备案）回源到本服务 | 静态文件全量缓存，跨省延迟下降；没备案就别折腾 |
| 图标换成 128×128 的 PNG | 图标会进首屏；几百 KB 的 SVG 比整个首页的 JS 还大 |

> 验证缓存是否生效：`curl -I https://你的域名/main-xxxx.js` 应看到
> `cache-control: public, max-age=31536000, immutable`；`index.html` 则是 `no-cache`。

### 安装到桌面（PWA）

桌面版 Chrome / Edge 地址栏右侧、手机浏览器菜单里的「安装应用 / 添加到主屏幕」都能装，
不需要应用商店。站点这边只需要三样东西，都已经内置：

- **`/manifest.webmanifest`**：名称、图标、主题色由服务端**动态生成**，读的是后台
  「站点信息」里的站名与「站点标识」里上传的图标。所以改完站名、换了图标，
  已安装的客户端下次打开就是新的——不要把它改成静态文件。
- **`/sw.js`**：service worker。缓存站点自身的静态文件（入口 JS/CSS、图标、离线页），
  **不缓存页面 HTML，也不缓存搜索结果**。预缓存清单由服务端在响应时注入（资源名带内容
  哈希），发版后版本号随之变化，客户端会在页脚提示「有新版本 · 刷新」。
- **`/offline.html`**：断网时打开的兜底页，写明搜索需要联网。

两个前提：

1. **必须 HTTPS**（`http://localhost` 例外）。Service Worker 只在安全上下文注册；
   纯 HTTP 部署时页面照常能用，只是没有离线能力，控制台也不会报错。
2. **反向代理别缓存 `/sw.js`**。服务端已给它 `Cache-Control: no-cache`，
   如果 Nginx 又加 `expires 30d`，客户端就永远收不到新版本。

### 4. 在线升级

**Docker**：

```bash
cd voidxo && git pull
docker compose up -d --build          # 重建镜像并替换容器，/data 卷原样保留
docker compose exec voidxo /app/voidxo -version
docker compose logs -f --tail=50
```

**源码 / systemd**：

```bash
systemctl stop voidxo
cp /opt/voidxo/voidxo /opt/voidxo/voidxo.bak    # 留退路

cd /src/voidxo && git pull
(cd web && npm run build)                       # 只有改了前端才需要（会把产物同步进 internal/webui/dist）
go build -o /opt/voidxo/voidxo ./cmd/voidxo
systemctl start voidxo
```

- **必须先停服再编译**：① 前端是编译期 embed 进二进制的，只重启不重编译＝旧页面；
  ② `go build` 覆盖正在运行的 exe 会留下 `voidxo~` 备份文件。
- **需要重建索引时**（发布说明里会写明）：停服后 `./voidxo -data /var/lib/voidxo -reindex`，再启动。
- 升级不动数据目录，但升级前照例备一份：
  `tar czf voidxo-data-$(date +%F).tar.gz -C /var/lib voidxo`
- 回滚：换回 `voidxo.bak` + 升级前那份 `data` 备份。

### 5. 端口防护与进程守护

**端口**：只把 8080 暴露给本机，对外一律走反代。这台机器上唯一该对外的是 443（和 80 跳转）。

```bash
# 容器（compose 里已经是这个写法）——注意是 127.0.0.1:8080:8080，不是 8080:8080
ports:
  - "127.0.0.1:8080:8080"
```

```bash
# 源码部署：默认就是只绑本机，别改成 :8080
./voidxo -addr 127.0.0.1:8080
ufw allow 443/tcp        # 只开反代的端口
```

> **Docker 会绕过 UFW**：Docker 自己往 iptables 里写规则，`ufw deny 8080` 拦不住
> `-p 8080:8080` 发布的端口。所以唯一的可靠做法就是**把发布地址写死成 `127.0.0.1:8080:8080`**，
> 而不是靠防火墙。云服务器还要确认安全组没放行 8080。

**进程守护**：

- **Docker 不用另配**：镜像自带 `HEALTHCHECK`，compose 里有 `restart: unless-stopped`
  和 20s 的 `stop_grace_period`（启动要开 Bleve + Pebble，别被秒杀）。
  不要往容器里再塞 systemd / supervisord——一个容器一个进程。
- **源码部署用 systemd**（`Restart=always` 覆盖崩溃与 OOM；不需要 supervisor）：

```ini
# /etc/systemd/system/voidxo.service
[Unit]
Description=voidxo search engine
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=voidxo
Group=voidxo
WorkingDirectory=/var/lib/voidxo
# 令牌写这里（VOIDXO_ADMIN_TOKEN=...），不写进 ExecStart——ps 能看到命令行
EnvironmentFile=/etc/voidxo/voidxo.env
ExecStart=/opt/voidxo/voidxo -addr 127.0.0.1:8080 -data /var/lib/voidxo
Restart=always
RestartSec=5
# 冷备 / 重建索引是分钟级操作，别在收尾时被 SIGKILL
TimeoutStopSec=30

# 加固：只给数据目录写权限，其余只读
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=/var/lib/voidxo
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
chmod 600 /etc/voidxo/voidxo.env      # 令牌文件只给 root 和服务账号
systemctl daemon-reload && systemctl enable --now voidxo
systemctl status voidxo               # 看 Active / 最近日志
journalctl -u voidxo -f               # 跟日志
```

- 服务**不需要 root**，也不要用 root 跑（数据目录 `chown -R voidxo:voidxo /var/lib/voidxo`）。
- 备份与 `-reindex` 这类任务挂 systemd timer / cron：它们要先停服，
  别和常驻服务抢同一个数据目录（SQLite + Pebble 都不接受两个进程同时写）。

## 版本与发版

版本号的唯一来源是根目录 [`VERSION`](VERSION)（不带 `v`），[`CHANGELOG.md`](CHANGELOG.md)
记录每个版本改了什么，`git tag` 与之一一对应。每次更新走一条命令：

```bash
./scripts/release.sh patch -m "修复：xxx" -m "改进：yyy"   # 0.10.0 → 0.10.1
./scripts/release.sh minor -m "新增：zzz"                  # 0.10.1 → 0.11.0
```

脚本会 bump 版本号、把条目写进 CHANGELOG、提交、打 `vX.Y.Z` tag 并推送到 GitHub。
`-m` 里的类型前缀（安全 / 修复 / 新增 / 改进 / 重构 / 移除 / 文档）会转成粗体，
不给 `-m` 就从上次 tag 以来的提交自动聚合。**不要手工分别改 VERSION / CHANGELOG / tag**
——三处必然漂。

## 命令行

| 模式 | 用途 |
|---|---|
| `./voidxo` | 启动服务（HTTP + 定时抓取 + 定时繁荣度统计） |
| `-crawl-once` | 按白名单抓一轮后退出（`-crawl-only 域名` 只抓单站，`-crawl-pages N` 控制单站上限） |
| `-reindex` | 从 KV 快照重建索引（改索引结构后必做，需停服） |
| `-repair` | 补齐「表里有、索引里缺」的文档（reindex 中断后的恢复手段，秒级） |
| `-dedupe` | 折叠近重复内容（同站版本镜像 + 跨站转载） |
| `-prosperity-once` | 重算站点繁荣度（入链统计）后退出 |
| `-version` | 打印版本 / 提交 / 构建时间（报障时先给这个） |

开发与评测工具在 `cmd/` 下：`bench`（索引与查询压测）、`eval`（检索指标）、
`seed`（导入语料）、`tune`（排序权重网格搜索）。

## 账号

站内第一个注册的账号自动成为管理员，之后注册的都是普通用户。顶栏不再常驻「管理后台」，
管理员登录后才出现；直接访问 `/manage` 会被送到登录页并在登录后跳回原地址。

```bash
./voidxo -addr :8080 -admin-token devtoken
```

- 密码用 bcrypt（cost 12）存储，邮箱统一小写归一。
- 登录失败按「账号 + IP」计数，10 次锁 15 分钟；成功一次清空计数。
- 会话在库里只存 token 的 sha256，浏览器侧是 HttpOnly + SameSite=Lax 的 cookie。
- 改密码会清空该账号的全部会话。

**邮件**：邮箱验证与找回密码需要 SMTP。在**后台「功能开关」页的「邮件（SMTP）」卡片**里填
服务器、端口、用户名、密码、发件地址，保存即生效，旁边有「发送测试邮件」可以当场验证。

未配置时邮件正文只打印到服务端日志（本地开发能从日志里拿到验证 / 重置链接）。

首次部署也可以用启动参数给一套初始值，之后一律以后台为准：

```bash
./voidxo -smtp-host smtp.qq.com -smtp-user you@qq.com -smtp-from you@qq.com
export VOIDXO_SMTP_PASS=...   # 密码走环境变量，避免出现在进程列表里
```

`-public-url` 决定邮件里链接的域名；留空则从请求推断（反代场景看 `X-Forwarded-Proto`）。

**通行密钥**：设置页可以登记指纹 / 面容 / 安全密钥，登录页有「用通行密钥登录」。
WebAuthn 的 RPID 由请求来源自动推断（localhost、局域网 IP、正式域名各自成一套），
所以同一份二进制换个地址跑也能用。登记需要 HTTPS 或 localhost。

**人机验证**：后台「功能开关」页可以选验证方式并填各自的 key（改完即时生效，不用重启）：

| 方式 | 需要的配置 |
|---|---|
| 内置算术题 | 无（零依赖降级方案） |
| Cloudflare Turnstile | Site Key + Secret Key |
| 极验 Geetest v4 | Captcha ID + Captcha Key |
| VAPTCHA | VID + Secret Key |
| Cap（自托管） | 实例地址 + Site Key + Secret |

开关是同一个 `require_captcha`：关掉就不验证，打开就按选定方式验证。
**配了方式但 key 不全时按未启用处理**——宁可漏过机器人，也不能把所有人挡在门外。

注意：这些第三方组件会把用户 IP 暴露给对应厂商，与页脚的「不追踪」有出入，介意就选内置算术题。

`-admin-token` 仍然有效，是「账号密码都进不去」时的应急入口。

## API

检索结果可以直接用 HTTP 调用，返回字段按需裁剪（不传 `fields` 则返回全部）：

```bash
curl "http://127.0.0.1:8080/api/search?q=kubernetes&size=5&fields=url,title,score&pretty=1"
```

| 参数 | 说明 |
|---|---|
| `q` | 查询词，必填，最长 512 字符，支持下方查询语法 |
| `size` / `page` | 每页条数（默认 10，上限 50）/ 页码 |
| `exclude` | 排除标签：`ai` / `ads` / `personal` / `platform`，逗号分隔 |
| `since` | 时间窗：`7d` / `12h` / `4w` / `1y`，或直接给秒数 |
| `fields` | 只返回点名的字段，清单见 `/api-docs` |
| `pretty` | 非 0 时缩进输出，方便 curl 直接看 |

```js
const r = await fetch('/api/search?q=kubernetes&fields=url,title,score');
const { hits, total } = await r.json();
```

### 查询语法

`q` 里可以直接用下面几种写法（网页搜索框与 API 完全一致）：

| 写法 | 含义 |
|---|---|
| `+词` | 结果必须包含这个词 |
| `-词` | 结果不能包含这个词 |
| `"短语"` | 作为一个整体匹配，不拆词；中英文引号都认 |
| `site:域名` | 只在这个站点内搜索，自动包含子域 |

```bash
curl "http://127.0.0.1:8080/api/search?q=+redis+-jedis+site:redis.io&fields=url,title"
```

用了语法时响应会多一个 `syntax` 字段回显解析结果（`required` / `excluded` /
`phrases` / `site`），便于确认写法真的生效而不是被当成普通词。

同一个检索也支持 POST（参数放 JSON 体，语义完全一致），给不想让搜索词出现在
访问日志 / Referer / 地址栏的场景用：

```bash
curl -X POST http://127.0.0.1:8080/api/search \
  -H "Content-Type: application/json" \
  -d '{"q":"+redis -jedis site:redis.io","size":5,"fields":"url,title"}'
```

网页端在 **设置 → GET 请求** 里可以切到 POST：地址栏不再出现搜索词，代价是刷新
页面会丢失当前查询（查询只活在内存里）。

搜索词补全 `GET /api/suggest?q=前缀` 由服务端代理 Bing 的 qsonhs 接口（它只提供
JSONP + http，浏览器直连会被当混合内容拦掉），结果缓存 10 分钟，取不到时返回空数组。

浏览器跨域调用需要服务端用 `-cors` 放开来源（默认只允许同源）。启动后访问
`/api-docs` 有完整参数表、字段清单和在线试用。

每日检索用量在 `/usage` 页（接口 `/api/usage?days=30`），只统计请求数、成功数、
返回结果与 API 调用；网页端的请求会带 `X-Voidxo-Client: web` 以便和 API 调用区分。

## 配置

全部通过命令行参数，无配置文件（`./voidxo -h` 有完整说明）。常用：

| 参数 | 说明 |
|---|---|
| `-addr` | 监听地址，默认 `127.0.0.1:8080`（默认只绑本机） |
| `-data` | 数据目录，默认 `./data` |
| `-admin-token` | 后台令牌；留空则每次启动随机生成并打印 |
| `-submit` | 是否开放用户提交站点（默认开，进审核队列 + 限流） |
| `-crawl-every` | 定时抓取的扫描周期，默认 15m（只抓到期站点）。必须 ≤ 最短分类间隔（30m），否则新闻类站点会被扫描周期卡住 |
| `-log-level` | `info` / `debug`（debug 会打印逐页过滤原因与检索分段耗时） |
| `-llm-base-url` `-llm-model` | 配了才启用改写式摘要；否则用抽取式 |
| `-reindex-workers` | 重建时的 processor 并行度，默认 CPU 核数 |
| `-outbound-proxy` | 抓取与预检的出网代理，如 `http://127.0.0.1:7890`；**留空直连**，不读 `HTTP_PROXY`/`HTTPS_PROXY`（内网与回环地址始终直连） |

摘要模型的密钥优先读环境变量 `VOIDXO_LLM_KEY`，避免出现在进程列表里。

## 数据布局

```
data/
  voidxo.db      SQLite：文档元数据、白名单、出链、繁荣度、提交队列
  bleve/         Bleve 全文索引（标题/摘要/正文/标签/时间）
  kv/            Pebble：原文快照（证据链与原文回溯的地基）
```

备份只需停服后整目录拷贝；`data/` 已在 `.gitignore` 里。

## 架构

```
 白名单爬虫（按站并发、域内串行、动态配额）
        │
        ├─▶ processor：正文提取 / 标签判定 / simhash + shingle 指纹 / 语言判定
        │
        ├─▶ Bleve 索引  ◀── 检索（BM25 + 精确匹配 + 时效 + 繁荣度 + 同域多样性）
        ├─▶ SQLite 元数据
        └─▶ Pebble 原文快照 ──▶ 证据片段 ──▶ 原文摘要（抽取式 / 可选 LLM 改写）
```

关键约束：不手写倒排索引、不提前上集群、语义相似度不压过精确匹配、
语言边界是硬过滤（中文查询只召回中文内容）。

## 质量

检索指标（`go run ./cmd/eval -data ./data -queries eval/<集合>.jsonl`，135 条查询）：

| 集合 | Recall@20 | NDCG@10 | MRR@10 |
|---|---|---|---|
| 纯中文 61 条（真实口吻） | 0.877 | 0.788 | 0.776 |
| 术语桥 39 条（中文查询 → 英文官方文档） | 0.885 | 0.748 | 0.733 |
| 术语桥 v2 20 条（Redis/Node/Vue/Python/nginx） | 0.975 | 0.732 | 0.723 |
| 中文口吻 v2 15 条（Go 原理 / 美团 / .NET 实践） | 1.000 | 0.917 | 0.889 |

性能（1137 篇真实语料）：检索本身约 0.2ms，含摘要片段与排序信号的热路径约 8ms/次。
索引写入 1154 篇/秒（批量）；全量重建 1137 篇约 2 分钟（受机器负载影响明显）。

## 已知边界

- **跨语言「同义改写」不支持**：词典能覆盖「中文词 → 常见英文术语」（响应式→reactivity），
  但换了说法的查询（如「怎么让页面不卡」→ 性能优化）需要向量检索，已实验否决
  （收益不稳定、延迟 37 倍、118MB 常驻）。
- **单机上限**：查询延迟由命中集规模决定。命中集过万或总量破 50 万时考虑迁 OpenSearch，
  实测 20 万篇合成语料 P99 149ms（悲观上界）。
- **SPA 站点**：爬虫不执行 JS。SSR 首屏没有标题的页面只能用域名兜底（当前 13 篇，
  全是真实标题就是「Training」「Glossary」这类导航页）。
- **不做的**：翻译查询、双语索引、跨语言向量召回（产品红线）。

小语料（几十~几百篇文档）可直接用：相关性闸门按语料规模缩放，不需要调参。

## 联系方式

如有问题或版权相关沟通，请通过 [Issue](https://github.com/zealis/voidxo/issues) 联系。
