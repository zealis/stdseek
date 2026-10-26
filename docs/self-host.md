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

```nginx
server {
    listen 443 ssl http2;
    server_name search.example.com;

    ssl_certificate     /etc/ssl/certs/search.crt;
    ssl_certificate_key /etc/ssl/private/search.key;

    # 检索响应是 JSON，压缩收益明显（结果页含摘要片段）
    gzip on;
    gzip_types application/json text/css application/javascript;

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

## 首次配置

1. **加白名单**：访问 `/manage/whitelist`（需在页面里填入管理令牌），按「域名 + 分类
   （official / opensource / blog / platform / media + 抓取周期）」录入。
   分类会影响标签判定（`blog` 会打 `personal` 标）与排序质量分。
2. **跑首轮抓取**：`./voidxo -data /var/lib/voidxo -crawl-once`（停服执行），
   或等定时任务（默认 `-crawl-every 1h`，只抓到期站点）。
3. **私有语料**：外部站点抓不到的内网文档，用 `cmd/seed` 导入（JSONL，一行一篇）：

   ```json
   {"url":"https://wiki.internal/onboarding","domain":"wiki.internal","category":"official",
    "title":"新人入职手册","description":"环境搭建与权限申请","body":"正文……",
    "labels":[],"published_days_ago":30}
   ```

   只有 `url` 和 `body` 是必需的；`domain` 缺省从 URL 推，`category` 影响质量分与
   `no 个人` / `no 平台` 标签。导入后不需要 `-reindex`（seed 会直接写索引与快照）。
4. **验证**：`curl http://127.0.0.1:8080/api/status` 看文档数；
   `/healthz` 只回答「进程活着」，`/api/status` 才回答「有没有数据可用」。

## 数据与备份

```
<data>/
  voidxo.db      SQLite（文档元数据 / 白名单 / 出链 / 繁荣度 / 提交队列）
  bleve/         全文索引（可从快照重建）
  kv/            原文快照（证据链地基，**不可重建**）
```

**备份**：停服后整目录拷贝即可（SQLite 用 WAL，运行中拷贝可能不一致）：

```bash
systemctl stop voidxo
tar czf voidxo-backup-$(date +%F).tar.gz -C /var/lib voidxo
systemctl start voidxo
```

**恢复索引**：`bleve/` 丢了不用慌，从快照重建即可（`-reindex`，1137 篇约 2 分钟）。
**恢复元数据**：`voidxo.db` 丢了就只能重抓——它是唯一记录「哪些文档该存在」的地方
（索引重建会跳过表里没有的孤儿快照，防止已下架文档复活）。

## 升级

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
| 搜索结果比预期少 | 检查结果页的 `no ai` / `no 广告` 等标签开关，以及时间筛选 |
| 后台 401 | 请求头缺 `X-Admin-Token`，或令牌与服务启动时的不一致 |
| 重建后文档少了 | `-reindex` 只重建「表里有记录」的文档；先确认 `voidxo.db` 完整，必要时用 `-repair` 补 |
| 抓取很慢 | 单站串行 + 站点配额，属设计行为；看 `-log-level debug` 的 `crawl` 日志确认为何被限速 |
| 内存涨得快 | 索引常驻内存，按语料量估算；确认没有同时跑 `-reindex`（重建期 24 worker 会到 GB 级） |
