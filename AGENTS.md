# voidxo 工作约定（AGENTS.md）

## 设计铁律（用户明确要求，改 UI 前必读）

1. **不要在界面添加冗余的解释性描述文本。** 界面文字只保留功能必需（操作项、状态、
   空态引导）；规则说明、流程解释这类内容放文档（DESIGN.md / README），不进 UI——
   它们会弄脏界面、浪费用户注意力。新增页面/组件时先问：这段字删掉用户会不会迷失？
   不会就删。（2026-10-26 用户提出，永久生效）
2. 图标按钮不放文字（如后台「菜单」钮是纯 ☰ 图标，语义靠 aria-label）。
3. 视觉与令牌的唯一事实来源是 `/DESIGN.md`，改样式先改文档再改 `web/src/styles.css`。

## 技术约定

- 前端：Angular 22（web/），zoneless + signals-first，standalone 不显式声明 OnPush，
  原生控制流 `@if/@for/@switch`，禁止 ngClass/ngStyle/CommonModule。
- 路由 `withComponentInputBinding` 会把缺省 query param 绑成 undefined、有值时绑成
  字符串——组件里必须归一化（见 `pages/search.ts` 的 `pageIndex`）。
- 构建：`cd web && npm run build` 会自动同步产物到 `internal/webui/dist`（go:embed）。
  必须用 Node ≥ 22.22.3 构建 Angular 22（本机用 `D:\sdk\Nodejs`，managed 的 22.22.2 不达标）。
- 开发预览：`cd web && npm start`（:4200，代理 /api /admin 到 127.0.0.1:8080）。

## 发版约定（2026-10-26 用户提出，永久生效）

- **版本号只写在根目录 `VERSION`（不带 `v`），不要在代码或 README 里另起一份。**
- 每次更新必须同时做三件事：bump `VERSION`、往 `CHANGELOG.md` 加一段、打 `vX.Y.Z` tag。
  三处由 `./scripts/release.sh` 一次完成，禁止手工分开改（必然漂）。
- 提交推送到 GitHub（`git@github.com:zealis/voidxo.git`）也由该脚本负责。
- `CHANGELOG.md` 段落格式：`## vX.Y.Z（YYYY-MM-DD）`，条目写
  `- **类型**：内容`，类型限定为 安全 / 修复 / 新增 / 改进 / 重构 / 移除 / 文档。
- 条目写「用户能感知的变化」，不写「改了哪个文件」；一次发版里的同类改动合并成一条。
