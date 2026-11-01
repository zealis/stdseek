/**
 * English. Same key set as zh-CN — missing keys fall back to the Chinese source,
 * which is intentional (a half-translated page beats a page of raw keys).
 *
 * Tone: short, verb-first, no marketing. It sits next to Chinese result titles,
 * so avoid idioms that only make sense in English.
 */
const en: Record<string, string> = {
  // ---- header ----
  'nav.search': 'Search',
  'nav.contribute': 'Submit a site',
  'nav.settings': 'Settings',
  'nav.admin': 'Admin',
  'nav.login': 'Sign in',
  'nav.logout': 'Sign out',
  'nav.lang': 'Change language',

  // ---- search box ----
  'search.placeholder': 'Search keywords',
  'search.placeholder.home': 'Privacy-first search',
  'search.button': 'Search',
  'search.suggest': 'Suggestions',

  // ---- empty index ----
  'home.empty.title': 'The index is empty',
  'home.empty.desc': 'Nothing has been crawled yet. Add a site in admin, then run a crawl.',
  'home.empty.action': 'Add sites in admin',
  'home.empty.hint': 'or start with -crawl-once',

  // ---- filters ----
  'search.all': 'All',
  'search.map': 'Map',
  'search.map.label': 'Map provider',
  'search.more': 'More',
  'search.more.label': 'All categories',
  'search.done': 'Done',
  'search.category': 'Category',
  'search.filter': 'Filters',
  'search.filter.label': 'Filter options',
  'search.exclude': 'Exclude these tags',
  'search.time': 'Time',
  'search.time.label': 'Time range',

  // ---- results ----
  'search.fresh': 'Newest first',
  'search.fresh.tip': 'The query looks time-sensitive, so results are weighted by publish date',
  'search.loading': 'Searching',
  'search.empty.title': 'No results',
  'search.empty.desc':
    'The index only covers whitelisted sites. Try removing filters or shortening the query.',
  'search.weak.more': 'More results',
  'search.weak.less': 'Show less',
  'search.discover.title': 'Deep-web finds',
  'search.discover.hint': 'Relevant but rarely cited',
  'search.wiki.source': 'Wikipedia',
  'search.wiki.all': 'Show all',
  'search.related.title': 'Learn more about',
  'search.map.open': 'Open in a new tab',
  'search.map.close': 'Close map',
  'search.map.frame': 'Search on {name}',
  'search.map.amap': 'Amap',
  'search.map.baidu': 'Baidu Maps',
  'search.map.google': 'Google Maps',

  // ---- site categories ----
  'cat.official': 'Official site',
  'cat.media': 'News',
  'cat.platform': 'Forums',
  'cat.blog': 'Blogs',
  'cat.opensource': 'Open source',
  'cat.docs': 'Documentation',
  'cat.image': 'Images',
  'cat.download': 'Downloads',
  'cat.design': 'Design',
  'cat.tool': 'Tools',
  'cat.job': 'Jobs',
  'cat.life': 'Lifestyle',
  'cat.game': 'Games & media',
  'cat.social': 'Social',
  'cat.dev': 'Dev tools',
  'cat.shopping': 'Shopping',
  'cat.other': 'Other',

  // ---- exclusion tags ----
  'filter.ai': 'AI-generated',
  'filter.ai.hint': 'Likely written by a model, with no reliable source',
  'filter.ads': 'Ad-heavy',
  'filter.ads.hint': 'Ads take up a large share of the page',
  'filter.tracker': 'Many trackers',
  'filter.tracker.hint': 'Embeds several third-party tracking scripts',
  'filter.personal': 'Personal site',
  'filter.personal.hint': 'Personal blogs and homepages',
  'filter.platform': 'UGC platform',
  'filter.platform.hint': 'User-posted content sites such as Zhihu or CSDN',

  // ---- time range ----
  'time.any': 'Any time',
  'time.1d': 'Past day',
  'time.3d': 'Past 3 days',
  'time.1w': 'Past week',
  'time.1m': 'Past month',

  // ---- sign in / sign up / reset ----
  'auth.login.title': 'Sign in',
  'auth.login.sub': 'Sign in with your registered email. Admins land in the admin panel.',
  'auth.login.closed': 'Public sign-in is disabled here; only admin accounts can sign in.',
  'auth.email': 'Email',
  'auth.password': 'Password',
  'auth.totp': 'Two-factor code',
  'auth.login.submit': 'Sign in',
  'auth.passkey.login': 'Sign in with a passkey',
  'auth.passkey.needEmail': 'Enter your email first, then use a passkey',
  'auth.passkey.noResponse': 'No passkey response received',
  'auth.verify.ok': 'Email verified',
  'auth.verify.failed': 'This verification link is invalid or has expired',
  'auth.forgot': 'Forgot password',
  'auth.register': 'Create account',
  'auth.register.title': 'Sign up',
  'auth.register.sub': 'All you need is an email and a password.',
  'auth.register.closed': 'Registration is disabled on this site.',
  'auth.gotoLogin': 'Go to sign in',
  'auth.register.sent': 'We sent a verification link to {email}. Open it to finish signing up.',
  'auth.confirm': 'Confirm password',
  'auth.password.hint': 'At least 8 characters — length beats complexity',
  'auth.register.submit': 'Sign up',
  'auth.haveAccount': 'Already have an account? Sign in',
  'auth.forgot.title': 'Reset password',
  'auth.forgot.sub': "Enter your email and we'll send a reset link.",
  'auth.forgot.sentHint':
    'Nothing yet? Check spam, or make sure SMTP is configured on the server (without it the mail only goes to the log).',
  'auth.forgot.submit': 'Send reset link',
  'auth.backToLogin': 'Back to sign in',
  'auth.reset.title': 'Set a new password',
  'auth.reset.noToken': 'This link is missing its token — open it again from the email.',
  'auth.reset.resend': 'Send a new link',
  'auth.reset.sub': 'At least 8 characters. Every device will be signed out.',
  'auth.newPassword': 'New password',
  'auth.confirmNew': 'Confirm new password',
  'auth.reset.submit': 'Save new password',

  // ---- submit a site ----
  'contribute.title': 'Submit a site',
  'contribute.desc':
    'Submissions go into the review queue and become searchable once approved. Duplicate domains are merged.',
  'contribute.queued':
    'Added to the review queue. Save the reference code below to check progress later.',
  'contribute.ticket': 'Reference code',
  'contribute.copied': 'Copied',
  'contribute.copy': 'Copy',
  'contribute.ticketHint':
    'This code belongs to this submission only. It shows the review status; no account needed.',
  'contribute.gate.desc': 'Please read the submission guidelines first.',
  'contribute.gate.open': 'Read the guidelines',
  'contribute.url': 'URL',
  'contribute.url.hint':
    "The site's home page is enough; the crawl entry is configured by an admin.",
  'contribute.category.ai':
    'Set automatically by AI — nothing to pick. Sites are categorised after approval.',
  'contribute.reason': 'Reason (optional)',
  'contribute.reason.placeholder':
    'Why is this site worth indexing? e.g. “the official Redis command reference”',
  'contribute.submitting': 'Submitting',
  'contribute.submit': 'Submit',
  'contribute.query.title': 'Check an existing submission',
  'contribute.query.hint': "Without the reference code it can't be recovered.",
  'contribute.querying': 'Checking',
  'contribute.query': 'Check',
  'contribute.refresh': 'Refresh',
  'contribute.noProgress': 'No progress yet',
  'contribute.again': 'Submit another',
  'contribute.submittedAt': 'Submitted {time}',
  'contribute.approvedSuffix': ' · whitelisted, searchable after the next crawl',
  'contribute.step.submitted': 'Submitted',
  'contribute.step.review': 'In review',
  'contribute.step.indexed': 'Indexed',
  'contribute.step.rejected': 'Rejected',
  'contribute.hello': 'Hello 👋',
  'contribute.guide.lead': 'Thanks for contributing to stdseek!',
  'contribute.guide.sub':
    'Three things to check before submitting — it saves you time and makes approval far more likely:',
  'contribute.guide.legal.t': 'Legal and safe.',
  'contribute.guide.legal.d': 'Nothing illegal, no malware, phishing or aggressive tracking.',
  'contribute.guide.notfarm.t': 'Not a scraper or an AI content farm.',
  'contribute.guide.notfarm.d':
    'Reposted, rewritten or bulk-generated content is rejected outright.',
  'contribute.guide.public.t': 'Publicly accessible.',
  'contribute.guide.public.d': 'Login walls and ad-stuffed pages are rarely worth indexing.',
  'contribute.guide.note':
    'Everything is reviewed by hand; nothing shows up in search before approval. Duplicate domains merge automatically, so no need to retry.',
  'contribute.guide.agree': "I've read and agree to the guidelines",
  'contribute.guide.start': 'Start',

  // ---- settings ----
  'settings.title': 'Settings',
  'settings.desc':
    'Search preferences live on this device and are applied by default on the results page.',
  'settings.account': 'Account',
  'settings.loading': 'Loading your account…',
  'settings.loggedOut':
    'Not signed in. Searching needs no account — sign in to manage account and security settings.',
  'settings.login': 'Sign in',
  'settings.register': 'Sign up',
  'settings.role.admin': 'Admin',
  'settings.role.user': 'Member',
  'settings.email.verified': 'verified',
  'settings.email.unverified': 'unverified',
  'settings.totp.on': '2FA on',
  'settings.logout': 'Sign out',
  'settings.changePassword': 'Change password',
  'settings.changeEmail': 'Change email',
  'settings.totp.enable': 'Enable 2FA',
  'settings.totp.disable': 'Disable 2FA',
  'settings.newEmail': 'New email',
  'settings.currentPassword': 'Current password',
  'settings.emailHint': 'Changing the email signs you out and resets the verification status.',
  'settings.saveEmail': 'Save email',
  'settings.savePassword': 'Save password',
  'settings.passkey': 'Passkeys',
  'settings.passkey.hint':
    'Sign in with fingerprint, face or a security key — harder to phish than a password',
  'settings.passkey.waiting': 'Waiting for device…',
  'settings.passkey.add': 'Add',
  'settings.passkey.remove': 'Remove',
  'settings.passkey.ipBlocked':
    'You are on a bare IP, but passkeys need a domain. Open this page via http://localhost:{port}',
  'settings.passkey.unsupported': "This browser doesn't support passkeys",
  'settings.passkey.none': 'None registered',
  'settings.passkey.addedAt': 'Added {time}',
  'settings.passkey.lastUsed': ' · last used {time}',
  'settings.totp.scan':
    'Scan the QR code with an authenticator app (1Password, Google Authenticator…) and enter the 6-digit code it shows.',
  'settings.totp.qrAlt': 'Two-factor QR code',
  'settings.totp.manual': "Can't scan? Enter the secret manually:",
  'settings.totp.code': '6-digit code',
  'settings.totp.confirm': 'Turn on',
  'settings.totp.offHint': 'Enter your password to turn 2FA off.',
  'settings.totp.off': 'Turn off',
  'settings.passkey.removeTitle': 'Remove passkey',
  'settings.passkey.removeDesc':
    "That device won't be able to sign in with it any more — register a new one or use your password.",
  'settings.sessions': 'Recent devices',
  'settings.session.current': 'Active now',
  'settings.session.detail': 'Details for this sign-in',
  'settings.session.none': 'No sign-ins yet',
  'settings.session.local': 'This device',
  'settings.device.title': 'Is this you?',
  'settings.device.prefix': "If this wasn't you, ",
  'settings.device.link': 'change your password',
  'settings.device.suffix': ' right away.',
  'settings.device.details': 'Details',
  'settings.device.reputation': 'Reputation {score} ({status})',
  'settings.device.browserOn': '{browser} on {os}',
  'settings.device.ip': 'IP: {ip}',
  'settings.logoutTitle': 'Sign out',
  'settings.logoutDesc': "You'll need your email and password again.",
  'settings.logoutConfirm': 'Sign out',
  'settings.prefs': 'Search preferences',
  'settings.prefs.related': 'Related searches',
  'settings.prefs.overview': 'AI overview',
  'settings.prefs.time': 'Default time range',
  'settings.prefs.theme': 'Appearance',
  'settings.theme.system': 'System',
  'settings.theme.light': 'Light',
  'settings.theme.dark': 'Dark',
  'settings.getRequest': 'GET requests',
  'settings.getRequest.hint':
    'Keeps the query in the URL (turn off to search over POST — a refresh then loses the query)',
  'settings.save': 'Save preferences',
  'settings.saved': 'Saved on this device',

  // ---- help ----
  'help.title': 'Help',
  'help.desc': 'Search syntax and what gets indexed.',
  'help.tips': 'Search syntax',
  'help.combine': 'They combine:',
  'help.example':
    '+beijing -university\nsite:php.net "getting started"\n+redis -jedis site:redis.io',
  'help.syntaxHint':
    'Recognised syntax shows up in the filter row of the results page, so you can see it took effect.',
  'help.scope': "What's indexed",
  'help.more': 'More',
  'help.api': 'API docs',
  'help.usage': 'Daily usage',
  'help.syntax.plus.code': '+word',
  'help.syntax.plus.desc':
    'Results must contain this word. +beijing -university means “has beijing, without university”.',
  'help.syntax.minus.code': '-word',
  'help.syntax.minus.desc':
    'Results must not contain this word. Exclusions alone work too, but return a lot of noise.',
  'help.syntax.phrase.code': '"phrase"',
  'help.syntax.phrase.desc':
    'Matched as a whole instead of split into words. Use quotes for long queries or proper nouns.',
  'help.syntax.site.code': 'site:domain',
  'help.syntax.site.desc':
    'Searches only that site, subdomains included. Pasting a full URL works as well.',
  'help.scope.1':
    'Only hand-reviewed, whitelisted sites are indexed — no whole-web crawling, so SEO farms and AI filler never get in.',
  'help.scope.2':
    'A Chinese query recalls Chinese content only; mixed-language queries may cross languages through a term dictionary (命名空间 → namespaces).',
  'help.scope.3':
    'Filters and time range are hard conditions, not soft ranking: excluded tags never leak in with a lower score.',
  'help.scope.4':
    'Every result points back to the source: ranking reasons expand, and each summary claim carries a citation number.',

  // ---- device labels (core/ua.ts returns keys only) ----
  'ua.device.unknown': 'Unknown device',
  'ua.device.ipad': 'iPad',
  'ua.device.androidTablet': 'Android tablet',
  'ua.device.iphone': 'iPhone',
  'ua.device.android': 'Android phone',
  'ua.device.windows': 'Windows PC',
  'ua.device.mac': 'Mac',
  'ua.device.linux': 'Linux PC',
  'ua.device.chromeos': 'ChromeOS',
  'ua.browser.other': 'Other browser',
  'ua.browser.edge': 'Microsoft Edge',
  'ua.browser.opera': 'Opera',
  'ua.browser.firefox': 'Firefox',
  'ua.browser.chrome': 'Google Chrome',
  'ua.browser.safari': 'Safari',

  // ---- privacy ----
  'privacy.title': 'Privacy',
  'privacy.effective': 'Effective {created} · last updated {updated}',
  'privacy.s1': '1. What we collect',
  'privacy.s1.1':
    'Account: email and a password hash — never the plaintext password. Passkeys store the public key only.',
  'privacy.s1.2':
    'Sign-in devices: each sign-in records IP, browser and OS, and time, so you can see who is using your account. The settings page lists them; admins see a fuller picture in the admin panel.',
  'privacy.s1.3':
    'What you submit: site submissions and feedback (page URL, your verdict, the category you think is right, and an optional note), plus the category the site had at that moment. Feedback is recorded under an anonymous id, not tied to your account.',
  'privacy.s1.4':
    'Security records: whether a password appears in a known breach (hash prefix only), and IP ban rules.',
  'privacy.s2': '2. How we use it',
  'privacy.s2.1':
    'We use this information only to serve you content and, for security, to make sure you are not a malicious bot.',
  'privacy.s2.2':
    'Sign-in records exist so you can audit account activity. They are never used for ads or profiling.',
  'privacy.s3': "3. What we don't do",
  'privacy.s3.1':
    "We don't log search keywords: queries are never written to the database or linked to your account.",
  'privacy.s3.2':
    'This site runs no ads and no third-party analytics, and we never sell or share data. If an admin adds their own analytics, see the last item in section 4.',
  'privacy.s4': '4. Third-party services',
  'privacy.s4.lead':
    'Each of these is switched on or off by the site admin. When off, none of your data is sent anywhere. Two exceptions: the captcha always applies when submitting a site (with no third-party configured the site uses a local arithmetic question and sends nothing), and the site-icon proxy on the results page is always on (see below).',
  'privacy.s4.1':
    'Site icons (Google favicon service, always on): the icons at the bottom of the results page are fetched by our server from Google (google.com/s2/favicons). Only the domain of an indexed site is sent — never your IP, query or history. Your browser talks to this site only. Icons are cached in memory for a few days and never written to disk.',
  'privacy.s4.2':
    'Domain WHOIS (xxapi.cn by default, admin-configurable, always on): to give ranking a sense of domain age, our server asks a WHOIS endpoint when a site was registered. Only the domain itself is sent. Results live in our database and are re-checked every 90 days; your query and IP play no part.',
  'privacy.s4.3':
    'Captcha (e.g. Cloudflare Turnstile): used for sign-up, sign-in and site submissions. With a third party configured, your response and IP are submitted to that service.',
  'privacy.s4.4':
    'IP geolocation and reputation (ip.huzhihui.com): sign-in IPs are sent to it and come back with region, ISP, ASN, a reputation score and risk flags (data centre, proxy, Tor…), used to judge whether an IP looks suspicious.',
  'privacy.s4.5':
    'Wikipedia panel (zh.wikipedia.org, off by default): when enabled, a single-word query is looked up on Wikipedia and the summary appears in the right-hand column.',
  'privacy.s4.6':
    'Maps (Google Maps / Bing Maps / Apple Maps): no map is loaded by default. If you pick a provider on the results page, that provider’s embed loads directly in your browser — it can see your IP and the current query, and may set its own cookies.',
  'privacy.s4.7':
    'Site categorisation (admin-configured model endpoint, off by default): when an admin runs AI categorisation on an indexed site, the domain and a few article titles are sent to the model. Your searches are never included.',
  'privacy.s4.8':
    'Page summaries (a summariser configured by the operator, off by default): when you explicitly ask for a summary, the page text excerpt and your query are sent to the model endpoint. Without one configured, an extractive summary is used and nothing is sent. The rest of the site and other results are never sent.',
  'privacy.s4.9':
    'AI overview (admin-configured model endpoint, off by default): for question-style queries, the query and the titles/excerpts of the top results are sent to the model to build the overview at the top. Plain keywords never trigger it, and you can turn it off in settings.',
  'privacy.s4.10':
    'Cloud backup (admin-configured object storage such as Backblaze B2 or Cloudflare R2, off by default): crawled page text and document metadata (URL, title, summary, labels, word count, crawl time) are uploaded in packs. A full-database backup also uploads a snapshot containing account emails, password hashes (irreversible), the site list and site settings — but never sign-in sessions or captcha tokens. Search history is never written to the database.',
  'privacy.s4.11':
    "Code added by the site itself (entered by an admin under Site info → head and footer code): that code is maintained by the admin and may include analytics or support widgets. What it can see is up to that script and is not part of this site's built-in behaviour, so it is not covered here. Admin pages and the sign-in page never load it.",
  'privacy.s5': '5. Cookies and local storage',
  'privacy.s5.1':
    'One session cookie (HttpOnly, SameSite=Lax, 30 days) keeps you signed in. Signing out clears it.',
  'privacy.s5.2': 'No analytics, advertising or cross-site tracking cookies.',
  'privacy.s5.3':
    "Theme and display preferences live in the browser's localStorage and are never uploaded.",
  'privacy.s5.4':
    "Where the browser supports it, this site's own static files (scripts, styles, icons, the offline page) are cached locally so pages still open when you are offline. That cache holds no queries, results or page snapshots and uploads nothing; clearing this site's data in the browser removes it.",
  'privacy.s6': '6. Retention and deletion',
  'privacy.s6.1': 'Sign-in records are kept long-term, but the list only shows the last month.',
  'privacy.s6.2':
    'Sessions expire and are cleaned up automatically; IP bans lapse on the schedule an admin sets.',
  'privacy.s6.3':
    'Signing out clears the session on this device. There is no self-service account deletion yet — contact us to have account data removed.',
  'privacy.s7': '7. Children',
  'privacy.s7.1': 'This is a web tool, not a service aimed at children under 13.',
  'privacy.s7.2': "We don't knowingly collect personal information from children.",
  'privacy.s8': '8. Your choices',
  'privacy.s8.1': 'Signing out clears the session on this device.',
  'privacy.s8.2':
    'The settings page lists recent devices; if something looks wrong, change your password immediately.',
  'privacy.s8.3':
    'Third-party features (Wikipedia panel, AI overview, page summaries) are off by default and switched on by the site admin.',
  'privacy.s9': '9. Changes',
  'privacy.s9.1':
    'We may update this policy by publishing a revised version on this page with a new date. Revisions apply from publication.',
  'privacy.s10': '10. Contact',
  'privacy.s10.email': 'Email: ',
  'privacy.s10.missing': 'The admin has not set a contact email yet.',
  'privacy.note':
    'The Chinese text of this page is the original; translations are provided for convenience and the Chinese version prevails if they differ.',

  // ---- daily usage ----
  'usage.title': 'Daily usage',
  'usage.desc': 'Search requests per day. Refreshed every 30 seconds.',
  'usage.trend': 'Search requests',
  'usage.days': '{n}d',
  'usage.today': 'Today',
  'usage.th.date': 'Date',
  'usage.th.searches': 'Requests',
  'usage.th.succeeded': 'Successful',
  'usage.th.api': 'API calls',
  'usage.th.hits': 'Results',
  'usage.empty': 'No usage yet',
  'usage.stat.searches': 'Requests today',
  'usage.stat.succeeded': 'Successful today',
  'usage.stat.hits': 'Results today',
  'usage.stat.api': 'API calls today',
  'usage.unit.times': 'requests',
  'usage.unit.hits': 'results',
  'usage.rate': '{rate}% hit rate',
  'usage.fromApi': 'from external calls',
  'usage.tip': '{day}: {searches} requests · {succeeded} ok · {hits} results · {api} API',

  // ---- API docs ----
  'api.title': 'API',
  'api.desc': 'Results are available over plain HTTP, with fields trimmed on request.',
  'api.search.desc':
    'Returns results plus the deep-web section. Rate limit {rate} requests/min/IP; beyond that you get 429.',
  'api.search.syntax': 'Query syntax (+include / -exclude / "phrase" / site:domain):',
  'api.search.cors':
    'Cross-origin browser calls need the server started with -cors; same-origin only by default.',
  'api.suggest.desc':
    'Query completion: q is the prefix (≤64 chars) and the response is a suggestions array of at most 8 items. Data comes from Bing, cached server-side for 10 minutes; on failure you get an empty array, not an error.',
  'api.params': 'Parameters',
  'api.fields': 'Optional fields',
  'api.fields.desc': 'Omit it to get everything; list fields to get only those.',
  'api.example': 'Examples',
  'api.try': 'Try it',
  'api.try.placeholder': 'Query, e.g. kubernetes',
  'api.try.run': 'Run',
  'api.param.q':
    'Query string, required, max 512 chars; supports +include / -exclude / "phrase" / site:domain',
  'api.param.size': 'Results per page, default 10, max 50',
  'api.param.page': 'Page number, starting at 1',
  'api.param.exclude':
    'Exclude tags: ai / ads / personal / platform, comma separated (the old name no still works)',
  'api.param.since': 'Time window: 7d / 12h / 4w / 1y, or seconds; omit for all time',
  'api.param.fields': 'Return only these fields, comma separated — see the list below',
  'api.param.pretty': 'Any non-zero value pretty-prints the JSON for curl',

  // ---- footer ----
  'footer.tagline': 'Type less, find the source',
  'nav.help': 'Help',
  'nav.usage': 'Usage',
  'nav.privacy': 'Privacy',

  // ---- relative time on result cards ----
  'time.justNow': 'just now',
  'time.minutesAgo': '{n} min ago',
  'time.hoursAgo': '{n} hr ago',
  'time.daysAgo': '{n} days ago',
  'time.indexed': ' · indexed',

  // ---- page titles ----
  'title.search': 'Search',
  'title.contribute': 'Submit a site',
  'title.settings': 'Settings',
  'title.api': 'API',
  'title.usage': 'Daily usage',
  'title.help': 'Help',
  'title.privacy': 'Privacy',
  'title.login': 'Sign in',
  'title.register': 'Sign up',
  'title.forgot': 'Reset password',
  'title.reset': 'Set a new password',
};

export default en;
