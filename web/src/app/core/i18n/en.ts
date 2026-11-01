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
