# 更新日志（CHANGELOG）

> 版本以根目录 `VERSION` 文件为准（不带 `v` 前缀），`git tag` 与之一一对应。
> 发版走 `./scripts/release.sh`，由脚本 bump 版本号、追加本文件段落、提交并推送，
> 避免手工改三处导致版本漂移。
> 条目类型：**安全 / 修复 / 新增 / 改进 / 重构 / 移除 / 文档**。

## v0.10.0（2026-10-26）

首个带版本号与 tag 的发版。此前只有 `dist/` 里的 v0.9.0 构建产物，没有版本记录。

- **新增**：前端整体重写为 **Angular 22**（standalone 组件 + signals，zoneless，原生 `@if/@for/@switch`），四页（首页 / 结果 / 设置 / 提交）+ 后台七页（站点、审核、标签、黑名单、白名单、功能开关、提交审核）；构建产物经 `web/tools/emit-dist.mjs` 同步到 `internal/webui/dist`，继续以 `go:embed` 打进单二进制
- **新增**：语言边界硬过滤（`internal/lang`）——中文 query 只召回中文内容，中英混排才允许跨语言；不做降权保留，也不提供关闭开关
- **新增**：摘要层（`internal/summarize`）——抽取式为默认与降级路径，可接 OpenAI 兼容模型改写，每条结论必须挂原文引用
- **新增**：内容级去重（`internal/dedup`，`-dedupe`）——版本镜像 / 跨站转载 / 采集站搬运折叠成唯一「正本」
- **新增**：索引补齐（`internal/repair`，`-repair`）——reindex 中断后按 KV 快照补齐「表里有、索引里缺」的文档
- **新增**：功能开关（后台可改、即时生效、落 SQLite）与标签误判反馈接口
- **新增**：本地语义向量（`internal/embed`）——multilingual-e5-small 的纯 Go ONNX 推理与向量索引，当前只接评测链路（`cmd/eval`），未进检索主路径
- **新增**：开发工具 `cmd/bench`（索引规模压测）、`cmd/tune`（排序权重网格扫描）、`cmd/seed`（语料导入）
- **新增**：`docs/self-host.md` 自托管说明、`DESIGN.md` 设计令牌、`AGENTS.md` 协作约定
- **改进**：检索结果缓存与证据片段缓存接入热路径，长尾重查询不再重算排序与片段
- **改进**：processor 增加结构化提取（`struct.go`）与标题性能优化；索引层补词项处理（`index/terms.go`）
- **改进**：评测集扩充为中文口吻 / 术语桥 / 真实语料多套（`eval/queries-*.jsonl`）
- **修复**：描述字段在入库与读取两处封顶 160 字——部分站点把整篇正文塞进 `meta[name=description]`，结果页会被正文淹没
- **修复**：前端换 Angular 后 8080 仍吐旧页面——`go:embed` 是编译期打包，必须重新 `go build` 并停掉旧进程，只重启不重编译等于什么都没变
