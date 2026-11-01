/**
 * 中文（源语言）。这份字典同时是**缺译时的回退表**：任何 key 在别的语言里没有译文，
 * 页面显示这里的原文，而不是一个 `nav.search` 这样的裸 key。
 *
 * 命名按页面分层：nav / home / search / cat / filter / time / title。
 * 加 key 的规矩：中英两份必须同时加，缺一边由 `web/tools/check-i18n.mjs` 在构建前拦住。
 */
const zhCN: Record<string, string> = {
  // ---- 顶栏 ----
  'nav.search': '搜索',
  'nav.contribute': '提交站点',
  'nav.settings': '设置',
  'nav.admin': '管理后台',
  'nav.login': '登录',
  'nav.logout': '退出',
  'nav.lang': '切换语言',

  // ---- 搜索框（首页与顶栏共用）----
  'search.placeholder': '搜索关键词',
  'search.placeholder.home': '隐私持续保护中',
  'search.button': '搜索',
  'search.suggest': '搜索建议',

  // ---- 首页空索引 ----
  'home.empty.title': '索引还是空的',
  'home.empty.desc': '还没有抓取任何内容，先录入站点再执行一次抓取。',
  'home.empty.action': '去后台加站点',
  'home.empty.hint': '或启动时加 -crawl-once',

  // ---- 结果页：过滤栏 ----
  'search.all': '全部',
  'search.map': '地图',
  'search.map.label': '地图服务',
  'search.more': '更多',
  'search.more.label': '全部分类',
  'search.done': '完成',
  'search.category': '分类',
  'search.filter': '过滤',
  'search.filter.label': '过滤选项',
  'search.exclude': '排除这些标签',
  'search.time': '时间',
  'search.time.label': '时间范围',

  // ---- 结果页：结果区 ----
  'search.fresh': '按发布时间优先',
  'search.fresh.tip': '查询含时效意图，结果已按发布时间加权',
  'search.loading': '正在检索',
  'search.empty.title': '没有找到结果',
  'search.empty.desc': '索引只覆盖白名单站点，试着去掉过滤或缩短关键词',
  'search.weak.more': '更多结果',
  'search.weak.less': '收起',
  'search.discover.title': '深网发现',
  'search.discover.hint': '相关但少被引用的内容',
  'search.wiki.source': '维基百科',
  'search.wiki.all': '全部显示',
  'search.related.title': '深入了解',
  'search.map.open': '在新窗口打开',
  'search.map.close': '关闭地图',
  'search.map.frame': '{name} 搜索',
  'search.map.amap': '高德地图',
  'search.map.baidu': '百度地图',
  'search.map.google': '谷歌地图',

  // ---- 站点分类（后台分类下拉仍用 api.ts 里的中文 label，这里供前台）----
  'cat.official': '官方网站',
  'cat.media': '新闻阅读',
  'cat.platform': '信息论坛',
  'cat.blog': '技术博客',
  'cat.opensource': '开源项目',
  'cat.docs': '知识文档',
  'cat.image': '图片素材',
  'cat.download': '软件下载',
  'cat.design': '设计创意',
  'cat.tool': '效率工具',
  'cat.job': '求职招聘',
  'cat.life': '生活服务',
  'cat.game': '游戏影音',
  'cat.social': '社交平台',
  'cat.dev': '开发工具',
  'cat.shopping': '购物平台',
  'cat.other': '其他',

  // ---- 排除标签 ----
  'filter.ai': 'AI 生成',
  'filter.ai.hint': '疑似由模型生成，查不到可靠出处',
  'filter.ads': '广告较多',
  'filter.ads.hint': '正文里广告位占比过高',
  'filter.tracker': '追踪器较多',
  'filter.tracker.hint': '嵌入多类第三方追踪/统计脚本',
  'filter.personal': '独立网站',
  'filter.personal.hint': '个人博客、个人主页这类',
  'filter.platform': 'UGC 平台',
  'filter.platform.hint': '知乎 / CSDN 等用户发帖平台',

  // ---- 时间范围 ----
  'time.any': '全部',
  'time.1d': '1 天',
  'time.3d': '3 天',
  'time.1w': '1 周',
  'time.1m': '1 月',

  // ---- 页脚 ----
  'footer.tagline': '轻松输入，直连标准',
  'nav.help': '帮助',
  'nav.usage': '用量',
  'nav.privacy': '隐私',

  // ---- 结果卡上的相对时间 ----
  'time.justNow': '刚刚',
  'time.minutesAgo': '{n} 分钟前',
  'time.hoursAgo': '{n} 小时前',
  'time.daysAgo': '{n} 天前',
  'time.indexed': ' 收录',

  // ---- 页面标题（路由表里存 key，由 AppTitleStrategy 翻）----
  'title.search': '搜索',
  'title.contribute': '提交站点',
  'title.settings': '设置',
  'title.api': 'API',
  'title.usage': '每日用量',
  'title.help': '帮助',
  'title.privacy': '隐私',
  'title.login': '登录',
  'title.register': '注册',
  'title.forgot': '找回密码',
  'title.reset': '设置新密码',
};

export default zhCN;
