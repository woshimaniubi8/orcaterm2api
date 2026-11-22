/* ============================================================
   OrcaTerm2API 管理面板前端
   纯原生 JS，无外部依赖；所有渲染内容均经过 HTML 转义。
   ============================================================ */
(function () {
  'use strict';

  /* ---------------- 常量 ---------------- */
  var VIEW_META = {
    overview: { title: '概览', sub: '服务运行状态、账号健康度与最近请求。' },
    accounts: { title: '账号', sub: '账号池状态、令牌有效期与调度健康度。' },
    models: { title: '模型', sub: '可用模型目录、别名映射与默认路由。' },
    logs: { title: '日志', sub: '检索网关请求记录，点击行查看请求详情。' },
    settings: { title: '设置', sub: '网关、账号池、会话与日志配置。' }
  };

  var HEALTH_LABEL = {
    healthy: '健康',
    cooling: '冷却中',
    breaker: '熔断',
    expired: '已过期',
    disabled: '已禁用'
  };

  var MODE_LABEL = { 0: 'RE_ACT', 1: 'PLAN_ACT', 2: 'AUTO_ACT' };
  var ESC_MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

  var state = {
    session: null,
    view: 'overview',
    accounts: [],
    models: null,
    cfg: null,
    logs: [],
    logsSeq: 0,
    logsQuery: { q: '', limit: 100 },
    countdownTimer: null,
    addTab: 'token'
  };

  var modalStack = [];

  /* ---------------- 基础工具 ---------------- */
  function $(id) { return document.getElementById(id); }

  function esc(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, function (ch) {
      return ESC_MAP[ch];
    });
  }

  function attr(value) { return esc(value == null ? '' : value); }

  function pad2(n) { return n < 10 ? '0' + n : String(n); }

  function fmtAbs(ms) {
    var n = Number(ms);
    if (!n || n <= 0) return '—';
    var d = new Date(n);
    if (isNaN(d.getTime())) return '—';
    return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) +
      ' ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }

  function fmtRel(ms) {
    var n = Number(ms);
    if (!n || n <= 0) return '—';
    var diff = Date.now() - n;
    if (diff < 0) return '刚刚';
    if (diff < 10000) return '刚刚';
    if (diff < 60000) return Math.floor(diff / 1000) + ' 秒前';
    if (diff < 3600000) return Math.floor(diff / 60000) + ' 分钟前';
    if (diff < 86400000) return Math.floor(diff / 3600000) + ' 小时前';
    if (diff < 604800000) return Math.floor(diff / 86400000) + ' 天前';
    return fmtAbs(n).slice(0, 10);
  }

  function timeSpan(ms) {
    return '<span class="mono" title="' + attr(fmtAbs(ms)) + '">' + esc(fmtRel(ms)) + '</span>';
  }

  function fmtDuration(ms) {
    var n = Number(ms) || 0;
    if (n < 1000) return n + ' ms';
    if (n < 60000) return (n / 1000).toFixed(n < 10000 ? 2 : 1) + ' s';
    var m = Math.floor(n / 60000);
    var s = Math.round((n % 60000) / 1000);
    return m + 'm ' + s + 's';
  }

  function fmtUptime(sec) {
    var n = Number(sec) || 0;
    if (n < 60) return n + ' 秒';
    var d = Math.floor(n / 86400);
    var h = Math.floor((n % 86400) / 3600);
    var m = Math.floor((n % 3600) / 60);
    if (d > 0) return d + '天' + h + '小时';
    if (h > 0) return h + '小时' + m + '分';
    return m + '分钟';
  }

  function fmtInt(n) {
    var v = Number(n);
    return Number.isFinite(v) ? v.toLocaleString('zh-CN') : '0';
  }

  function fmtPct(part, total) {
    var t = Number(total) || 0;
    if (!t) return '—';
    return ((Number(part) || 0) / t * 100).toFixed(1) + '%';
  }

  function fmtContext(n) {
    var v = Number(n) || 0;
    if (!v) return '—';
    if (v >= 1000000) return (v % 1000000 === 0 ? String(v / 1000000) : (v / 1000000).toFixed(1)) + 'M';
    if (v >= 1000) return Math.round(v / 1000) + 'K';
    return String(v);
  }

  function fmtExpiry(target) {
    var t = Number(target) || 0;
    if (!t) return '未知';
    var diff = t - Date.now();
    if (diff <= 0) return '已过期';
    var d = Math.floor(diff / 86400000);
    var h = Math.floor((diff % 86400000) / 3600000);
    var m = Math.floor((diff % 3600000) / 60000);
    var s = Math.floor((diff % 60000) / 1000);
    if (d > 0) return d + '天' + h + '小时';
    if (h > 0) return h + '小时' + m + '分';
    return m + '分' + pad2(s) + '秒';
  }

  function fmtCooldown(target) {
    var t = Number(target) || 0;
    if (!t || t <= Date.now()) return '—';
    return fmtExpiry(t);
  }

  function debounce(fn, wait) {
    var timer = null;
    return function () {
      var args = arguments;
      var self = this;
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { fn.apply(self, args); }, wait);
    };
  }

  function toast(message, type, duration) {
    var root = $('toast-root');
    if (!root) return;
    type = type || 'info';
    while (root.children.length >= 4) root.removeChild(root.firstElementChild);
    var node = document.createElement('div');
    node.className = 'toast toast-' + type;
    node.innerHTML = '<span class="toast-mark"></span><div class="toast-msg">' + esc(message || '') +
      '</div><button class="toast-close" type="button" aria-label="关闭">×</button>';
    function remove() {
      node.classList.remove('show');
      setTimeout(function () { if (node.parentNode) node.parentNode.removeChild(node); }, 220);
    }
    node.querySelector('.toast-close').addEventListener('click', remove);
    root.appendChild(node);
    requestAnimationFrame(function () { requestAnimationFrame(function () { node.classList.add('show'); }); });
    setTimeout(remove, duration || 3600);
  }

  function setBtnLoading(btn, loading) {
    if (!btn) return;
    if (loading) {
      if (btn.dataset.origHtml === undefined) btn.dataset.origHtml = btn.innerHTML;
      btn.dataset.wasDisabled = btn.disabled ? '1' : '0';
      btn.classList.add('is-loading');
      btn.disabled = true;
    } else {
      btn.classList.remove('is-loading');
      btn.disabled = btn.dataset.wasDisabled === '1';
      if (btn.dataset.origHtml !== undefined) {
        btn.innerHTML = btn.dataset.origHtml;
        delete btn.dataset.origHtml;
      }
    }
  }

  async function withBtn(btn, task) {
    setBtnLoading(btn, true);
    try {
      await task();
    } catch (err) {
      toast(err.message || '操作失败', 'error');
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  async function copyText(text) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      toast('已复制到剪贴板', 'success');
    } catch (err) {
      toast('复制失败，请手动选择文本复制', 'error');
    }
  }

  function loadingHtml(label) {
    return '<div class="loading-block"><span class="spinner"></span><span>' + esc(label || '加载中…') + '</span></div>';
  }

  function emptyState(icon, title, text, actionHtml) {
    return '<div class="empty-state"><div class="empty-icon"><svg class="icon"><use href="#i-' + esc(icon || 'inbox') +
      '"></use></svg></div><strong>' + esc(title) + '</strong><p>' + esc(text || '') + '</p>' + (actionHtml || '') + '</div>';
  }

  function errorPanel(err) {
    return '<div class="empty-state error-state"><div class="empty-icon"><svg class="icon"><use href="#i-alert"></use></svg></div>' +
      '<strong>加载失败</strong><p>' + esc(err && err.message ? err.message : '未知错误') + '</p>' +
      '<button class="btn btn-sm" type="button" data-action="retry">重试</button></div>';
  }

  function statusPill(status) {
    var n = Number(status) || 0;
    var cls = 'status-none';
    if (n >= 500) cls = 'status-5xx';
    else if (n >= 400) cls = 'status-4xx';
    else if (n >= 300) cls = 'status-3xx';
    else if (n >= 200) cls = 'status-2xx';
    return '<span class="status-pill ' + cls + '">' + (n || '—') + '</span>';
  }

  /* ---------------- API 封装 ---------------- */
  class ApiError extends Error {
    constructor(message, status) {
      super(message);
      this.name = 'ApiError';
      this.status = status || 0;
    }
  }

  async function api(path, options) {
    var opts = options || {};
    var init = {
      method: opts.method || 'GET',
      credentials: 'same-origin',
      headers: { 'Accept': 'application/json' }
    };
    if (opts.body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(opts.body);
    }
    var res;
    try {
      res = await fetch('api/' + path, init);
    } catch (err) {
      throw new ApiError('网络请求失败，请检查服务是否正在运行', 0);
    }
    if (res.status === 401) {
      if (!opts.allow401) handleUnauthorized();
      throw new ApiError('未登录或会话已过期', 401);
    }
    var text = '';
    try { text = await res.text(); } catch (err) { /* 忽略读取失败 */ }
    var data = null;
    if (text) {
      try { data = JSON.parse(text); } catch (err) { data = { error: text }; }
    }
    if (!res.ok) {
      var msg = data && data.error ? data.error : ('请求失败（HTTP ' + res.status + '）');
      throw new ApiError(msg, res.status);
    }
    return data || {};
  }

  /* ---------------- 弹窗 ---------------- */
  function openModal(options) {
    var opts = options || {};
    var root = $('modal-root');
    var backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML =
      '<div class="modal ' + (opts.wide ? 'modal-wide' : '') + '" role="dialog" aria-modal="true" aria-label="' + attr(opts.title || '对话框') + '">' +
        '<div class="modal-head"><h3 class="modal-title">' + esc(opts.title || '') + '</h3>' +
        '<button class="icon-btn" type="button" data-close aria-label="关闭"><svg class="icon"><use href="#i-x"></use></svg></button></div>' +
        '<div class="modal-body">' + (opts.body || '') + '</div>' +
        (opts.footer ? '<div class="modal-foot">' + opts.footer + '</div>' : '') +
      '</div>';
    root.appendChild(backdrop);

    var closing = false;
    var entry = { close: null };

    function onKey(e) {
      if (e.key === 'Escape' && modalStack[modalStack.length - 1] === entry) close();
    }

    function close() {
      if (closing) return;
      closing = true;
      backdrop.classList.remove('open');
      var idx = modalStack.indexOf(entry);
      if (idx >= 0) modalStack.splice(idx, 1);
      if (!modalStack.length) document.body.classList.remove('modal-open');
      document.removeEventListener('keydown', onKey);
      setTimeout(function () { if (backdrop.parentNode) backdrop.parentNode.removeChild(backdrop); }, 180);
      if (typeof opts.onClose === 'function') opts.onClose();
    }

    entry.close = close;
    modalStack.push(entry);
    document.body.classList.add('modal-open');
    document.addEventListener('keydown', onKey);
    backdrop.addEventListener('mousedown', function (e) { if (e.target === backdrop) close(); });
    backdrop.querySelector('[data-close]').addEventListener('click', close);

    if (typeof opts.onMount === 'function') opts.onMount(backdrop, close);
    requestAnimationFrame(function () { backdrop.classList.add('open'); });
    var focusEl = backdrop.querySelector('.modal-body input:not([type="hidden"]), .modal-body textarea, .modal-body select, .modal-foot .btn-primary, .modal-body button');
    if (focusEl) {
      setTimeout(function () { try { focusEl.focus(); } catch (err) { /* 忽略 */ } }, 60);
    }
    return close;
  }

  function closeAllModals() {
    while (modalStack.length) modalStack[modalStack.length - 1].close();
  }

  function confirmDialog(options) {
    var opts = options || {};
    return new Promise(function (resolve) {
      var settled = false;
      function finish(value) {
        if (settled) return;
        settled = true;
        resolve(value);
      }
      openModal({
        title: opts.title || '确认操作',
        body: '<p class="confirm-text">' + esc(opts.message || '') + '</p>',
        footer: '<button class="btn" type="button" data-cancel>取消</button>' +
          '<button class="btn ' + (opts.danger ? 'btn-danger' : 'btn-primary') + '" type="button" data-ok>' +
          esc(opts.confirmText || '确认') + '</button>',
        onMount: function (wrap, close) {
          wrap.querySelector('[data-cancel]').addEventListener('click', function () { finish(false); close(); });
          wrap.querySelector('[data-ok]').addEventListener('click', function () { finish(true); close(); });
        },
        onClose: function () { finish(false); }
      });
    });
  }

  /* ---------------- 登录 / 会话 ---------------- */
  function showLogin(message) {
    closeAllModals();
    stopCountdown();
    state.session = null;
    state.view = 'overview';
    var boot = $('boot-view');
    if (boot) boot.hidden = true;
    $('main-view').hidden = true;
    $('login-view').hidden = false;
    var errBox = $('login-error');
    errBox.textContent = '';
    errBox.hidden = true;
    var pwd = $('login-password');
    pwd.value = '';
    pwd.type = 'password';
    var eye = $('password-eye');
    eye.innerHTML = '<svg class="icon"><use href="#i-eye"></use></svg>';
    eye.setAttribute('aria-label', '显示密码');
    if (message) {
      errBox.textContent = message;
      errBox.hidden = false;
    }
    document.title = '登录 · OrcaTerm2API';
    setTimeout(function () { try { pwd.focus(); } catch (err) { /* 忽略 */ } }, 80);
  }

  function handleUnauthorized() {
    var main = $('main-view');
    if (main && !main.hidden) toast('登录已过期，请重新登录', 'error');
    showLogin();
  }

  function enterApp() {
    var boot = $('boot-view');
    if (boot) boot.hidden = true;
    $('login-view').hidden = true;
    $('main-view').hidden = false;
    var version = state.session && state.session.version ? state.session.version : '—';
    $('side-version').textContent = version;
    renderView(currentView());

    var params = new URLSearchParams(location.search);
    var oauth = params.get('oauth');
    if (oauth) {
      var messages = {
        ok: '微信账号已添加',
        exchange_failed: '扫码登录失败',
        session_expired: '扫码会话已过期',
        missing_session: '扫码会话缺失',
        bad_token: '令牌解析失败',
        save_failed: '账号保存失败'
      };
      var msg = messages[oauth] || ('扫码结果：' + oauth);
      if (params.get('msg')) msg += '：' + params.get('msg');
      toast(msg, oauth === 'ok' ? 'success' : 'error', 5000);
      history.replaceState(null, '', location.pathname + location.hash);
      if (oauth === 'ok') go('accounts');
    }
  }

  async function bootstrap() {
    try {
      var session = await api('session', { allow401: true });
      state.session = session;
      enterApp();
    } catch (err) {
      showLogin(err.status && err.status !== 401 ? err.message : '');
    }
  }

  async function onLogin(e) {
    e.preventDefault();
    var input = $('login-password');
    var errBox = $('login-error');
    var btn = $('login-submit');
    var password = input.value;
    errBox.hidden = true;
    if (!password) {
      errBox.textContent = '请输入面板密码';
      errBox.hidden = false;
      input.focus();
      return;
    }
    setBtnLoading(btn, true);
    try {
      await api('login', { method: 'POST', body: { password: password }, allow401: true });
      var session = await api('session');
      state.session = session;
      enterApp();
    } catch (err) {
      errBox.textContent = err.status === 401 ? '密码错误，请重试' : (err.message || '登录失败');
      errBox.hidden = false;
      input.select();
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  async function onLogout() {
    var btn = $('logout-btn');
    setBtnLoading(btn, true);
    try { await api('logout', { method: 'POST', allow401: true }); } catch (err) { /* 忽略 */ }
    if (btn && btn.isConnected) setBtnLoading(btn, false);
    showLogin();
    toast('已退出登录', 'info');
  }

  function togglePassword() {
    var input = $('login-password');
    var btn = $('password-eye');
    var show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    btn.innerHTML = '<svg class="icon"><use href="#i-' + (show ? 'eye-off' : 'eye') + '"></use></svg>';
    btn.setAttribute('aria-label', show ? '隐藏密码' : '显示密码');
  }

  /* ---------------- 主题 / 路由 ---------------- */
  function applyTheme(theme) {
    theme = theme === 'light' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem('orca-theme', theme); } catch (err) { /* 忽略 */ }
    var btn = $('theme-toggle');
    if (btn) {
      btn.innerHTML = '<svg class="icon"><use href="#i-' + (theme === 'dark' ? 'sun' : 'moon') + '"></use></svg>' +
        '<span>' + (theme === 'dark' ? '浅色模式' : '深色模式') + '</span>';
      btn.setAttribute('aria-label', theme === 'dark' ? '切换到浅色模式' : '切换到深色模式');
    }
  }

  function toggleTheme() {
    var next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
    applyTheme(next);
  }

  function currentView() {
    var match = location.hash.match(/^#\/?([a-z]+)/);
    return match && VIEW_META[match[1]] ? match[1] : 'overview';
  }

  function go(view) {
    if (!VIEW_META[view]) view = 'overview';
    if (currentView() === view) renderView(view);
    else location.hash = '#/' + view;
  }

  function renderView(view) {
    if (!VIEW_META[view]) view = 'overview';
    state.view = view;
    if (view !== 'accounts') stopCountdown();
    document.querySelectorAll('.nav-item').forEach(function (el) {
      var active = el.dataset.view === view;
      el.classList.toggle('active', active);
      if (active) el.setAttribute('aria-current', 'page');
      else el.removeAttribute('aria-current');
    });
    document.title = VIEW_META[view].title + ' · OrcaTerm2API';
    try { window.scrollTo(0, 0); } catch (err) { /* 忽略 */ }
    if (view === 'overview') renderOverview();
    else if (view === 'accounts') renderAccounts();
    else if (view === 'models') renderModels();
    else if (view === 'logs') renderLogs();
    else if (view === 'settings') renderSettings();
  }

  function pageHead(eyebrow, title, sub, actions) {
    return '<header class="page-head"><div><div class="eyebrow">' + esc(eyebrow) + '</div>' +
      '<h1 class="page-title">' + esc(title) + '</h1>' +
      '<p class="page-sub">' + esc(sub) + '</p></div>' +
      '<div class="page-actions">' + (actions || '') + '</div></header>';
  }

  /* ---------------- 概览 ---------------- */
  function renderOverview() {
    var content = $('content');
    content.innerHTML = pageHead('OVERVIEW', '概览', VIEW_META.overview.sub,
      '<button class="btn" type="button" data-action="refresh-overview"><svg class="icon"><use href="#i-refresh"></use></svg>刷新</button>') +
      '<div id="overview-body">' + loadingHtml('加载运行数据…') + '</div>';
    loadOverview();
  }

  async function loadOverview() {
    var body = $('overview-body');
    if (!body) return;
    body.innerHTML = loadingHtml('加载运行数据…');
    try {
      var results = await Promise.all([api('overview'), api('logs?limit=20')]);
      if (state.view !== 'overview') return;
      body.innerHTML = overviewHtml(results[0], (results[1] && results[1].data) || []);
    } catch (err) {
      if (state.view !== 'overview') return;
      body.innerHTML = errorPanel(err);
    }
  }

  function overviewHtml(ov, logs) {
    var req = ov.requests || {};
    var acct = ov.accounts || {};
    var total = Number(req.total) || 0;
    var success = Number(req.success) || 0;
    var errors = Number(req.errors) || 0;
    var cards =
      '<article class="stat-card"><div class="stat-label">服务版本</div>' +
        '<div class="stat-value">' + esc(ov.version || '—') + '</div>' +
        '<div class="stat-meta">监听 ' + esc(ov.listen || '—') + '</div></article>' +
      '<article class="stat-card"><div class="stat-label">运行时长</div>' +
        '<div class="stat-value">' + esc(fmtUptime(ov.uptime_seconds)) + '</div>' +
        '<div class="stat-meta">会话模式 ' + esc(ov.mode || '—') + '</div></article>' +
      '<article class="stat-card"><div class="stat-label">账号池</div>' +
        '<div class="stat-value">' + fmtInt(acct.total) + '<small>个</small></div>' +
        '<div class="stat-meta">健康 ' + fmtInt(acct.healthy) + ' · 冷却 ' + fmtInt(acct.cooling) + '</div></article>' +
      '<article class="stat-card"><div class="stat-label">请求总数 · 成功率</div>' +
        '<div class="stat-value">' + fmtInt(total) + '<small>次</small></div>' +
        '<div class="stat-meta">成功率 ' + esc(fmtPct(success, total)) + ' · 错误 ' + fmtInt(errors) + '</div></article>' +
      '<article class="stat-card"><div class="stat-label">平均耗时</div>' +
        '<div class="stat-value">' + esc(total ? fmtDuration(req.avg_duration_ms) : '—') + '</div>' +
        '<div class="stat-meta">平均 TTFB ' + esc(req.avg_ttfb_ms ? fmtDuration(req.avg_ttfb_ms) : '—') + '</div></article>' +
      '<article class="stat-card"><div class="stat-label">会话数</div>' +
        '<div class="stat-value">' + fmtInt(ov.conversations) + '<small>个</small></div>' +
        '<div class="stat-meta">默认模型 ' + esc(ov.default_model || '—') + '</div></article>';

    var facts =
      '<div class="fact-strip">' +
        '<div class="fact"><span>24h 请求</span><strong>' + fmtInt(req.last_24h) + '</strong></div>' +
        '<div class="fact"><span>流式请求</span><strong>' + fmtInt(req.streaming) + '</strong></div>' +
        '<div class="fact"><span>池请求</span><strong>' + fmtInt(ov.pool_requests) + '</strong></div>' +
        '<div class="fact"><span>池错误</span><strong>' + fmtInt(ov.pool_errors) + '</strong></div>' +
      '</div>';

    return '<div class="stat-grid">' + cards + '</div>' + facts +
      '<section class="card table-card"><div class="panel-head">' +
        '<h2 class="panel-title">最近请求 <span class="tag">24h</span></h2>' +
        '<span class="panel-sub">最近 20 条 · 24h 共 ' + fmtInt(req.last_24h) + ' 次</span></div>' +
        overviewLogsHtml(logs) + '</section>';
  }

  function overviewLogsHtml(logs) {
    if (!logs.length) {
      return emptyState('inbox', '暂无请求记录', '网关收到请求后，最近记录会显示在这里。');
    }
    var rows = logs.map(function (log) {
      return '<tr>' +
        '<td class="nowrap">' + timeSpan(log.time) + '</td>' +
        '<td class="mono truncate" title="' + attr(log.endpoint) + '">' + esc(log.endpoint || '—') + '</td>' +
        '<td class="mono">' + esc(log.model || '—') + '</td>' +
        '<td class="mono">' + esc(log.account || '—') + '</td>' +
        '<td>' + statusPill(log.status) + '</td>' +
        '<td class="mono">' + esc(fmtDuration(log.duration_ms)) + '</td>' +
        '<td class="mono">' + esc(log.ttfb_ms ? fmtDuration(log.ttfb_ms) : '—') + '</td>' +
        '<td>' + (log.stream ? '<span class="tag tag-blue">流式</span>' : '<span class="tag">普通</span>') + '</td>' +
      '</tr>';
    }).join('');
    return '<div class="table-wrap"><table class="data"><thead><tr>' +
      '<th>时间</th><th>端点</th><th>模型</th><th>账号</th><th>状态</th><th>耗时</th><th>TTFB</th><th>流式</th>' +
      '</tr></thead><tbody>' + rows + '</tbody></table></div>';
  }

  /* ---------------- 账号 ---------------- */
  function renderAccounts() {
    $('content').innerHTML = pageHead('ACCOUNTS', '账号', VIEW_META.accounts.sub,
      '<button class="btn" type="button" data-action="refresh-accounts"><svg class="icon"><use href="#i-refresh"></use></svg>刷新</button>' +
      '<button class="btn btn-primary" type="button" data-action="open-add-account"><svg class="icon"><use href="#i-plus"></use></svg>添加账号</button>') +
      '<div id="accounts-body">' + loadingHtml('加载账号列表…') + '</div>';
    loadAccounts();
  }

  async function loadAccounts() {
    var body = $('accounts-body');
    if (!body) return;
    body.innerHTML = loadingHtml('加载账号列表…');
    try {
      var res = await api('accounts');
      if (state.view !== 'accounts') return;
      state.accounts = res.data || [];
      body.innerHTML = accountsHtml();
      startCountdown();
    } catch (err) {
      if (state.view !== 'accounts') return;
      body.innerHTML = errorPanel(err);
    }
  }

  function accountsHtml() {
    var list = state.accounts || [];
    if (!list.length) {
      return emptyState('users', '还没有账号', '添加 OrcaTerm 账号后，网关才能向上游发起请求。',
        '<button class="btn btn-primary btn-sm" type="button" data-action="open-add-account">' +
        '<svg class="icon"><use href="#i-plus"></use></svg>添加账号</button>');
    }
    var counts = { total: list.length, healthy: 0, cooling: 0, breaker: 0, expired: 0, disabled: 0 };
    list.forEach(function (a) {
      if (counts[a.health] !== undefined) counts[a.health] += 1;
    });
    var chips =
      '<span class="chip"><span class="dot h-healthy"></span>健康 <b>' + counts.healthy + '</b></span>' +
      '<span class="chip"><span class="dot h-cooling"></span>冷却 <b>' + counts.cooling + '</b></span>' +
      '<span class="chip"><span class="dot h-breaker"></span>熔断 <b>' + counts.breaker + '</b></span>' +
      '<span class="chip"><span class="dot h-expired"></span>过期 <b>' + counts.expired + '</b></span>' +
      '<span class="chip"><span class="dot h-disabled"></span>禁用 <b>' + counts.disabled + '</b></span>' +
      '<span class="chip">总计 <b>' + counts.total + '</b></span>';
    return '<div class="summary-chips">' + chips + '</div><div class="acct-grid">' +
      list.map(accountCard).join('') + '</div>';
  }

  function accountCard(a) {
    var health = HEALTH_LABEL[a.health] ? a.health : 'disabled';
    var healthLabel = HEALTH_LABEL[health];
    var blocked = Array.isArray(a.blocked_models) && a.blocked_models.length ? a.blocked_models.join(', ') : '无';
    var expiryText = a.expires_at ? fmtExpiry(a.expires_at) : '未知';
    var cooldownText = a.cooldown_until ? fmtCooldown(a.cooldown_until) : '—';
    var refreshText = a.has_refresh ? (a.masked_refresh || '已配置') : '无';
    var refreshDisabled = a.refreshable ? '' : ' disabled title="该账号没有可用的 refresh_token"';
    return '<article class="acct-card">' +
      '<div class="acct-head"><span class="dot h-' + health + '"></span>' +
        '<div class="acct-title"><span class="acct-name">' + esc(a.name || '未命名账号') + '</span>' +
        '<span class="acct-id">' + esc(a.id || '—') + '</span></div>' +
        '<div class="acct-badges"><span class="badge h-' + health + '">' + esc(healthLabel) + '</span>' +
        '<span class="badge ' + (a.enabled ? 'badge-on' : 'badge-off') + '">' + (a.enabled ? '已启用' : '已禁用') + '</span></div>' +
      '</div>' +
      '<div class="acct-kv">' +
        '<div class="kv"><span class="kv-label">Access Token</span><span class="kv-value mono" title="' + attr(a.masked_token || '') + '">' + esc(a.masked_token || '—') + '</span></div>' +
        '<div class="kv"><span class="kv-label">Refresh Token</span><span class="kv-value mono" title="' + attr(a.masked_refresh || '') + '">' + esc(refreshText) + '</span></div>' +
        '<div class="kv"><span class="kv-label">令牌到期</span><span class="kv-value mono' + (a.expires_at && a.expires_at <= Date.now() ? ' is-expired' : '') + '" data-countdown="expires" data-target="' + attr(a.expires_at || 0) + '" title="' + attr(fmtAbs(a.expires_at)) + '">' + esc(expiryText) + '</span></div>' +
        '<div class="kv"><span class="kv-label">并发占用</span><span class="kv-value mono">' + fmtInt(a.in_flight) + ' / ' + fmtInt(a.max_in_flight) + '</span></div>' +
        '<div class="kv"><span class="kv-label">请求 / 错误</span><span class="kv-value mono">' + fmtInt(a.requests) + ' / ' + fmtInt(a.errors) + '</span></div>' +
        '<div class="kv"><span class="kv-label">失败次数</span><span class="kv-value mono">' + fmtInt(a.fails) + '</span></div>' +
        '<div class="kv"><span class="kv-label">最近使用</span><span class="kv-value">' + timeSpan(a.last_used) + '</span></div>' +
        '<div class="kv"><span class="kv-label">冷却剩余</span><span class="kv-value mono" data-countdown="cooldown" data-target="' + attr(a.cooldown_until || 0) + '" title="' + attr(fmtAbs(a.cooldown_until)) + '">' + esc(cooldownText) + '</span></div>' +
        '<div class="kv"><span class="kv-label">可刷新</span><span class="kv-value">' + (a.refreshable ? '是' : '否') + '</span></div>' +
        '<div class="kv"><span class="kv-label">阻断模型</span><span class="kv-value" title="' + attr(blocked) + '">' + esc(blocked) + '</span></div>' +
      '</div>' +
      '<div class="acct-error' + (a.last_error ? '' : ' empty') + '">' + (a.last_error ? esc(a.last_error) : '无最近错误') + '</div>' +
      '<div class="acct-actions">' +
        '<button class="btn btn-sm" type="button" data-action="account-test" data-id="' + attr(a.id) + '">测试</button>' +
        '<button class="btn btn-sm" type="button" data-action="account-quota" data-id="' + attr(a.id) + '">额度</button>' +
        '<button class="btn btn-sm" type="button" data-action="account-refresh" data-id="' + attr(a.id) + '"' + refreshDisabled + '>刷新令牌</button>' +
        '<button class="btn btn-sm" type="button" data-action="account-toggle" data-id="' + attr(a.id) + '">' + (a.enabled ? '禁用' : '启用') + '</button>' +
        '<button class="btn btn-sm btn-danger" type="button" data-action="account-delete" data-id="' + attr(a.id) + '">删除</button>' +
      '</div></article>';
  }

  function startCountdown() {
    stopCountdown();
    updateCountdowns();
    state.countdownTimer = setInterval(updateCountdowns, 1000);
  }

  function stopCountdown() {
    if (state.countdownTimer) {
      clearInterval(state.countdownTimer);
      state.countdownTimer = null;
    }
  }

  function updateCountdowns() {
    document.querySelectorAll('[data-countdown]').forEach(function (el) {
      var target = Number(el.dataset.target) || 0;
      if (el.dataset.countdown === 'expires') {
        el.textContent = target ? fmtExpiry(target) : '未知';
        el.classList.toggle('is-expired', !!target && target <= Date.now());
      } else {
        el.textContent = target ? fmtCooldown(target) : '—';
      }
    });
  }

  function accountById(id) {
    var found = null;
    state.accounts.forEach(function (a) { if (a.id === id) found = a; });
    return found;
  }

  async function accountAction(btn, task, successMessage) {
    setBtnLoading(btn, true);
    try {
      await task();
      if (successMessage) toast(successMessage, 'success');
      await loadAccounts();
    } catch (err) {
      toast(err.message || '操作失败', 'error', 5000);
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  /* ---------------- 模型 ---------------- */
  function renderModels() {
    $('content').innerHTML = pageHead('MODELS', '模型', VIEW_META.models.sub,
      '<button class="btn" type="button" data-action="refresh-models"><svg class="icon"><use href="#i-refresh"></use></svg>刷新</button>') +
      '<div id="models-body">' + loadingHtml('加载模型目录…') + '</div>';
    loadModels();
  }

  async function loadModels() {
    var body = $('models-body');
    if (!body) return;
    body.innerHTML = loadingHtml('加载模型目录…');
    try {
      var data = await api('models');
      if (state.view !== 'models') return;
      state.models = data;
      body.innerHTML = modelsHtml(data);
    } catch (err) {
      if (state.view !== 'models') return;
      body.innerHTML = errorPanel(err);
    }
  }

  function modelsHtml(data) {
    var list = data.data || [];
    var hidden = data.hidden || [];
    var aliases = data.aliases || {};
    var mode = MODE_LABEL[Number(data.mode)];
    var cat = data.catalog || {};
    var catInfo = cat.count
      ? (cat.count + ' 个 · ' + (cat.source || '内置') + (cat.fetched_at ? ' · ' + new Date(cat.fetched_at).toLocaleTimeString() : ''))
      : '内置目录';
    var info =
      '<div class="model-info-grid">' +
        '<div class="model-info"><span>默认模型</span><strong title="' + attr(data.default) + '">' + esc(data.default || '—') + '</strong></div>' +
        '<div class="model-info"><span>模型目录</span><strong title="' + attr(cat.error || '') + '">' + esc(catInfo) + '</strong></div>' +
        '<div class="model-info"><span>Assistant</span><strong>' + esc(data.assistant || '—') + '</strong></div>' +
        '<div class="model-info"><span>运行模式</span><strong>' + esc(mode || ('mode ' + (data.mode == null ? '—' : data.mode))) + '</strong></div>' +
        '<div class="model-info"><span>上游根地址</span><strong title="' + attr(data.base) + '">' + esc(data.base || '—') + '</strong></div>' +
      '</div>';
    var cards = list.map(function (m) {
      var badges = [];
      (m.tags || []).forEach(function (t) {
        var cls = t.type === 'free' ? 'tag-green' : t.type === 'new' ? 'tag-amber' : '';
        badges.push('<span class="tag ' + cls + '">' + esc(t.text) + '</span>');
      });
      if (m.context_length) badges.push('<span class="tag">' + esc(fmtContext(m.context_length)) + ' ctx</span>');
      if (m.support_img) badges.push('<span class="tag">图片识别</span>');
      if (hidden.indexOf(m.id) >= 0) badges.push('<span class="tag badge-hidden">hidden</span>');
      var caps = (m.capabilities || []).map(function (c) {
        return '<span class="cap">' + esc(c) + '</span>';
      }).join('');
      var levels = [];
      if (m.show_thinking && (m.thinking_levels || []).length) {
        (m.thinking_levels || []).forEach(function (lv) {
          levels.push('<span class="lv' + (lv.default ? ' is-default' : '') + '">' + esc(lv.label || lv.value) + '</span>');
        });
      }
      var thinking = levels.length
        ? '<div class="model-thinking"><span class="model-thinking-label">思考强度</span>' + levels.join('') + '</div>'
        : '';
      var price = m.show_price_rate ? (Number(m.price_rate || 0).toFixed(2) + 'x') : (m.free ? '限时免费' : '—');
      return '<article class="model-card">' +
        '<div class="model-top"><span class="model-id mono">' + esc(m.id) + '</span><span class="model-price">' + esc(price) + '</span></div>' +
        '<div class="model-label">' + esc(m.display_name || m.label || m.id) + '</div>' +
        (m.description ? '<div class="model-desc">' + esc(m.description) + '</div>' : '') +
        '<div class="model-meta">' + badges.join('') + '</div>' +
        (caps ? '<div class="model-caps">' + caps + '</div>' : '') +
        thinking +
        '<div class="model-upstream mono">upstream: ' + esc(m.upstream || '—') + '</div></article>';
    }).join('');
    var aliasKeys = Object.keys(aliases);
    var aliasRows = aliasKeys.length ? aliasKeys.map(function (key) {
      return '<tr><td>' + esc(key) + '</td><td class="mono">' + esc(aliases[key]) + '</td></tr>';
    }).join('') : '<tr><td colspan="2">' + emptyState('inbox', '暂无别名', '未配置模型别名映射。') + '</td></tr>';

    return '<div class="model-block">' + info + '</div>' +
      '<section class="card table-card model-block"><div class="panel-head"><h2 class="panel-title">可用模型</h2>' +
        '<span class="panel-sub">共 ' + list.length + ' 个</span></div>' +
        (list.length ? '<div class="model-grid" style="padding:16px">' + cards + '</div>'
          : emptyState('cpu', '暂无模型', '模型目录为空，请检查服务端配置。')) + '</section>' +
      '<section class="card table-card"><div class="panel-head"><h2 class="panel-title">别名映射</h2>' +
        '<span class="panel-sub">' + aliasKeys.length + ' 条</span></div>' +
        '<div class="table-wrap"><table class="data alias-table"><thead><tr><th>别名</th><th>指向模型</th></tr></thead><tbody>' +
        aliasRows + '</tbody></table></div></section>';
  }

  /* ---------------- 日志 ---------------- */
  function renderLogs() {
    var q = state.logsQuery;
    var limitOptions = [50, 100, 200, 500].map(function (n) {
      return '<option value="' + n + '"' + (n === q.limit ? ' selected' : '') + '>' + n + ' 条</option>';
    }).join('');
    $('content').innerHTML = pageHead('LOGS', '日志', VIEW_META.logs.sub, '') +
      '<div class="toolbar">' +
        '<div class="search-box"><svg class="icon"><use href="#i-search"></use></svg>' +
        '<input id="log-search" type="text" placeholder="搜索端点、模型、账号、IP、错误…" value="' + attr(q.q) + '"></div>' +
        '<select id="log-limit" aria-label="日志条数">' + limitOptions + '</select>' +
        '<button class="btn" type="button" data-action="refresh-logs"><svg class="icon"><use href="#i-refresh"></use></svg>刷新</button>' +
      '</div>' +
      '<div id="log-summary" class="log-summary"></div>' +
      '<div id="logs-table">' + loadingHtml('加载日志…') + '</div>';

    $('log-search').addEventListener('input', debounce(function () {
      state.logsQuery.q = $('log-search').value.trim();
      loadLogs();
    }, 300));
    $('log-limit').addEventListener('change', function () {
      state.logsQuery.limit = Number($('log-limit').value) || 100;
      loadLogs();
    });
    $('logs-table').addEventListener('click', onLogTableClick);
    loadLogs();
  }

  async function loadLogs() {
    var box = $('logs-table');
    if (!box) return;
    var seq = ++state.logsSeq;
    box.innerHTML = loadingHtml('加载日志…');
    try {
      var q = state.logsQuery;
      var path = 'logs?limit=' + encodeURIComponent(q.limit) + (q.q ? '&q=' + encodeURIComponent(q.q) : '');
      var res = await api(path);
      if (seq !== state.logsSeq || state.view !== 'logs') return;
      state.logs = res.data || [];
      var stats = res.stats || {};
      var summary = $('log-summary');
      if (summary) {
        summary.innerHTML = '<span>总计 <b>' + fmtInt(stats.total) + '</b></span>' +
          '<span>成功 <b>' + fmtInt(stats.success) + '</b></span>' +
          '<span>错误 <b>' + fmtInt(stats.errors) + '</b></span>' +
          '<span>流式 <b>' + fmtInt(stats.streaming) + '</b></span>' +
          '<span>平均耗时 <b>' + esc(fmtDuration(stats.avg_duration_ms)) + '</b></span>';
      }
      box.innerHTML = '<section class="card table-card"><div class="table-wrap"><table class="data">' +
        '<thead><tr><th>时间</th><th>端点</th><th>模型</th><th>账号</th><th>状态</th><th>耗时</th><th>TTFB</th><th>流式</th><th>IP</th><th>错误</th></tr></thead>' +
        '<tbody>' + logRowsHtml(state.logs) + '</tbody></table></div></section>';
    } catch (err) {
      if (seq !== state.logsSeq || state.view !== 'logs') return;
      box.innerHTML = errorPanel(err);
    }
  }

  function logRowsHtml(logs) {
    if (!logs.length) {
      return '<tr><td colspan="10">' + emptyState('inbox', '没有匹配的日志', '尝试更换搜索关键词或增大条数。') + '</td></tr>';
    }
    return logs.map(function (log, index) {
      return '<tr class="log-row" data-log-index="' + index + '">' +
        '<td class="nowrap">' + timeSpan(log.time) + '</td>' +
        '<td class="mono truncate" title="' + attr(log.endpoint) + '">' + esc(log.endpoint || '—') + '</td>' +
        '<td class="mono">' + esc(log.model || '—') + '</td>' +
        '<td class="mono">' + esc(log.account || '—') + '</td>' +
        '<td>' + statusPill(log.status) + '</td>' +
        '<td class="mono">' + esc(fmtDuration(log.duration_ms)) + '</td>' +
        '<td class="mono">' + esc(log.ttfb_ms ? fmtDuration(log.ttfb_ms) : '—') + '</td>' +
        '<td>' + (log.stream ? '<span class="tag tag-blue">流式</span>' : '<span class="tag">普通</span>') + '</td>' +
        '<td class="mono">' + esc(log.client_ip || '—') + '</td>' +
        '<td class="truncate" title="' + attr(log.error || '') + '">' + (log.error ? esc(log.error) : '—') + '</td>' +
      '</tr>' +
      '<tr class="log-detail" hidden><td colspan="10">' + logDetailHtml(log) + '</td></tr>';
    }).join('');
  }

  function logDetailHtml(log) {
    var blocks = '';
    if (log.prompt) blocks += '<div class="detail-block"><div class="detail-label">Prompt</div><pre class="detail-pre">' + esc(log.prompt) + '</pre></div>';
    if (log.completion) blocks += '<div class="detail-block"><div class="detail-label">Completion</div><pre class="detail-pre">' + esc(log.completion) + '</pre></div>';
    if (!log.prompt && !log.completion) {
      blocks = '<p class="detail-empty">未记录请求内容（需在设置 → 日志中开启 include_prompts）。</p>';
    }
    return '<div class="log-detail-inner">' + blocks +
      '<div class="detail-inline">' +
        '<span><b>Conversation</b><code class="mono">' + esc(log.conversation || '—') + '</code></span>' +
        '<span><b>UA</b>' + esc(log.user_agent || '—') + '</span>' +
        '<span><b>API Key</b><code class="mono">' + esc(log.api_key || '—') + '</code></span>' +
      '</div></div>';
  }

  function onLogTableClick(e) {
    var row = e.target.closest('.log-row');
    if (!row) return;
    var detail = row.nextElementSibling;
    if (!detail || !detail.classList.contains('log-detail')) return;
    var open = detail.hidden;
    detail.hidden = !open;
    row.classList.toggle('expanded', open);
  }

  /* ---------------- 设置 ---------------- */
  function renderSettings() {
    $('content').innerHTML = pageHead('SETTINGS', '设置', VIEW_META.settings.sub, '') +
      '<div id="settings-body">' + loadingHtml('加载配置…') + '</div>';
    loadSettings();
  }

  async function loadSettings() {
    var body = $('settings-body');
    if (!body) return;
    body.innerHTML = loadingHtml('加载配置…');
    try {
      var cfg = await api('config');
      if (state.view !== 'settings') return;
      state.cfg = cfg;
      body.innerHTML = settingsHtml(cfg);
      bindSettings();
    } catch (err) {
      if (state.view !== 'settings') return;
      body.innerHTML = errorPanel(err);
    }
  }

  function selectOptions(pairs, selected) {
    return pairs.map(function (pair) {
      return '<option value="' + attr(pair[0]) + '"' + (String(pair[0]) === String(selected) ? ' selected' : '') + '>' +
        esc(pair[1]) + '</option>';
    }).join('');
  }

  function settingsHtml(cfg) {
    var up = cfg.upstream || {};
    var pool = cfg.pool || {};
    var conv = cfg.conversation || {};
    var log = cfg.logging || {};
    var apiKeys = (cfg.api_keys_mask || []).join('\n');

    return '' +
      '<section class="card settings-section">' +
        '<div class="section-head"><div class="section-icon"><svg class="icon"><use href="#i-key"></use></svg></div>' +
          '<div><h2 class="section-title">访问凭据</h2><p class="section-desc">网关 API Key 与面板登录密码，API Key 默认脱敏展示。</p></div></div>' +
        '<div class="form-grid">' +
          '<div class="form-field full"><div class="section-row"><label class="field-label" for="set-api-keys">API Keys（每行一个）</label>' +
            '<label class="switch"><input type="checkbox" id="key-edit"><span class="switch-track"></span><span>修改 API Key</span></label></div>' +
            '<textarea id="set-api-keys" class="mono is-disabled" rows="4" readonly spellcheck="false">' + esc(apiKeys) + '</textarea>' +
            '<span class="field-hint">关闭开关时不提交 api_keys，现有 Key 保持不变；开启后请填写完整 Key，脱敏值不可保存。</span></div>' +
          '<div class="form-field"><label class="field-label" for="set-panel-password">面板密码</label>' +
            '<input type="password" id="set-panel-password" placeholder="留空则不修改" autocomplete="new-password">' +
            '<span class="field-hint">修改后当前会话仍然有效，下次登录使用新密码。</span></div>' +
        '</div>' +
      '</section>' +

      '<section class="card settings-section">' +
        '<div class="section-head"><div class="section-icon"><svg class="icon"><use href="#i-server"></use></svg></div>' +
          '<div><h2 class="section-title">上游服务</h2><p class="section-desc">OrcaTerm / LightAI 接入地址与请求参数。</p></div></div>' +
        '<div class="form-grid">' +
          '<div class="form-field full"><label class="field-label" for="up-assistant-base">assistant_base</label>' +
            '<input type="text" id="up-assistant-base" value="' + attr(up.assistant_base) + '" placeholder="https://lightai.cloud.tencent.com/assistant"></div>' +
          '<div class="form-field full"><label class="field-label" for="up-cgi-base">cgi_base</label>' +
            '<input type="text" id="up-cgi-base" value="' + attr(up.cgi_base) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="up-assistant-name">assistant_name</label>' +
            '<input type="text" id="up-assistant-name" value="' + attr(up.assistant_name) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="up-mode">mode</label>' +
            '<select id="up-mode">' + selectOptions([['0', '0 · RE_ACT'], ['1', '1 · PLAN_ACT'], ['2', '2 · AUTO_ACT']], up.mode) + '</select></div>' +
          '<div class="form-field"><label class="field-label" for="up-default-model">default_model</label>' +
            '<input type="text" id="up-default-model" value="' + attr(up.default_model) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="up-product">product</label>' +
            '<input type="text" id="up-product" value="' + attr(up.product) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="up-timeout">timeout_seconds</label>' +
            '<input type="number" id="up-timeout" min="1" value="' + attr(up.timeout_seconds) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="up-header-timeout">header_timeout_seconds</label>' +
            '<input type="number" id="up-header-timeout" min="1" value="' + attr(up.header_timeout_seconds) + '"></div>' +
        '</div>' +
      '</section>' +

      '<section class="card settings-section">' +
        '<div class="section-head"><div class="section-icon"><svg class="icon"><use href="#i-users"></use></svg></div>' +
          '<div><h2 class="section-title">账号池调度</h2><p class="section-desc">并发、熔断、冷却与限速参数，保存后热生效。</p></div></div>' +
        '<div class="form-grid">' +
          '<div class="form-field"><label class="field-label" for="pool-max-in-flight">max_in_flight</label>' +
            '<input type="number" id="pool-max-in-flight" min="1" value="' + attr(pool.max_in_flight) + '"><span class="field-hint">单账号最大并发</span></div>' +
          '<div class="form-field"><label class="field-label" for="pool-max-in-flight-total">max_in_flight_total</label>' +
            '<input type="number" id="pool-max-in-flight-total" min="0" value="' + attr(pool.max_in_flight_total) + '"><span class="field-hint">0 表示不限制总并发</span></div>' +
          '<div class="form-field"><label class="field-label" for="pool-breaker-threshold">breaker_threshold</label>' +
            '<input type="number" id="pool-breaker-threshold" min="1" value="' + attr(pool.breaker_threshold) + '"><span class="field-hint">连续失败多少次触发熔断</span></div>' +
          '<div class="form-field"><label class="field-label" for="pool-max-per-minute">max_per_minute</label>' +
            '<input type="number" id="pool-max-per-minute" min="0" value="' + attr(pool.max_per_minute) + '"><span class="field-hint">单账号每分钟请求上限</span></div>' +
          '<div class="form-field"><label class="field-label" for="pool-breaker-cooldown">breaker_cooldown</label>' +
            '<input type="text" id="pool-breaker-cooldown" class="mono" value="' + attr(pool.breaker_cooldown) + '" placeholder="3m0s"></div>' +
          '<div class="form-field"><label class="field-label" for="pool-soft-cooldown">soft_cooldown</label>' +
            '<input type="text" id="pool-soft-cooldown" class="mono" value="' + attr(pool.soft_cooldown) + '" placeholder="1m0s"></div>' +
          '<div class="form-field"><label class="field-label" for="pool-max-cooldown">max_cooldown</label>' +
            '<input type="text" id="pool-max-cooldown" class="mono" value="' + attr(pool.max_cooldown) + '" placeholder="15m0s"></div>' +
          '<div class="form-field"><label class="field-label" for="pool-min-interval">min_interval</label>' +
            '<input type="text" id="pool-min-interval" class="mono" value="' + attr(pool.min_interval) + '" placeholder="2s"></div>' +
          '<div class="form-field full"><label class="switch"><input type="checkbox" id="pool-prefer-fresh"' + (pool.prefer_fresh_token ? ' checked' : '') + '>' +
            '<span class="switch-track"></span><span>优先使用剩余有效期长的令牌（prefer_fresh_token）</span></label></div>' +
        '</div>' +
      '</section>' +

      '<section class="card settings-section">' +
        '<div class="section-head"><div class="section-icon"><svg class="icon"><use href="#i-message"></use></svg></div>' +
          '<div><h2 class="section-title">会话与历史</h2><p class="section-desc">会话粘性、历史回填与 system prompt 处理方式。</p></div></div>' +
        '<div class="form-grid">' +
          '<div class="form-field"><label class="field-label" for="conv-mode">mode</label>' +
            '<select id="conv-mode">' + selectOptions([['stateless', 'stateless · 每次携带全量历史'], ['sticky', 'sticky · 按会话复用 cid']], conv.mode) + '</select></div>' +
          '<div class="form-field"><label class="field-label" for="conv-sticky-ttl">sticky_ttl</label>' +
            '<input type="text" id="conv-sticky-ttl" class="mono" value="' + attr(conv.sticky_ttl) + '" placeholder="30m0s"></div>' +
          '<div class="form-field"><label class="field-label" for="conv-max-messages">max_history_messages</label>' +
            '<input type="number" id="conv-max-messages" min="0" value="' + attr(conv.max_history_messages) + '"></div>' +
          '<div class="form-field"><label class="field-label" for="conv-max-chars">max_history_chars</label>' +
            '<input type="number" id="conv-max-chars" min="0" value="' + attr(conv.max_history_chars) + '"></div>' +
          '<div class="form-field full"><label class="field-label" for="conv-system-mode">system_prompt_mode</label>' +
            '<select id="conv-system-mode">' + selectOptions([['prefix', 'prefix · 拼进首条用户消息'], ['drop', 'drop · 丢弃 system']], conv.system_prompt_mode) + '</select></div>' +
        '</div>' +
      '</section>' +

      '<section class="card settings-section">' +
        '<div class="section-head"><div class="section-icon"><svg class="icon"><use href="#i-list"></use></svg></div>' +
          '<div><h2 class="section-title">日志与归档</h2><p class="section-desc">内存环形日志、归档开关与请求内容记录。</p></div></div>' +
        '<div class="form-grid">' +
          '<div class="form-field"><label class="field-label" for="log-ring-size">ring_size</label>' +
            '<input type="number" id="log-ring-size" min="1" value="' + attr(log.ring_size) + '"><span class="field-hint">内存中保留的日志条数</span></div>' +
          '<div class="form-field"><label class="field-label">归档文件</label>' +
            '<div class="kv-value mono" style="padding-top:8px">' + esc(log.archive_file || '—') + '</div><span class="field-hint">由服务端保留，此处只读</span></div>' +
          '<div class="form-field"><label class="switch"><input type="checkbox" id="log-archive-enabled"' + (log.archive_enabled ? ' checked' : '') + '>' +
            '<span class="switch-track"></span><span>启用 JSONL 归档</span></label></div>' +
          '<div class="form-field"><label class="switch"><input type="checkbox" id="log-include-prompts"' + (log.include_prompts ? ' checked' : '') + '>' +
            '<span class="switch-track"></span><span>记录 prompt / completion 内容</span></label></div>' +
        '</div>' +
      '</section>' +

      '<div class="settings-actions"><span class="settings-hint">保存后账号池参数立即热生效，其余配置按服务端策略生效。</span>' +
        '<div class="page-actions"><button class="btn" type="button" data-action="reload-config">重新载入</button>' +
        '<button class="btn btn-primary" type="button" data-action="save-config"><svg class="icon"><use href="#i-check"></use></svg>保存配置</button></div></div>';
  }

  function bindSettings() {
    var keyToggle = $('key-edit');
    var keyArea = $('set-api-keys');
    if (!keyToggle || !keyArea) return;
    function sync() {
      keyArea.readOnly = !keyToggle.checked;
      keyArea.classList.toggle('is-disabled', !keyToggle.checked);
    }
    keyToggle.addEventListener('change', function () {
      sync();
      if (keyToggle.checked) keyArea.focus();
    });
    sync();
  }

  function fieldValue(id) {
    var el = $(id);
    return el ? String(el.value).trim() : '';
  }

  function intField(id, label, min) {
    var raw = fieldValue(id);
    if (raw === '') throw new Error(label + ' 不能为空');
    var n = Number(raw);
    if (!Number.isInteger(n) || n < min) throw new Error(label + ' 必须是 ≥ ' + min + ' 的整数');
    return n;
  }

  function durationField(id, label) {
    var value = fieldValue(id);
    if (!value) throw new Error(label + ' 不能为空');
    if (!/^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))*$/.test(value)) {
      throw new Error(label + ' 格式无效，例如 3m0s');
    }
    return value;
  }

  async function saveConfig(btn) {
    setBtnLoading(btn, true);
    try {
      var cfg = state.cfg || {};
      var payload = {};

      if ($('key-edit').checked) {
        var keys = fieldValue('set-api-keys').split('\n').map(function (k) { return k.trim(); }).filter(Boolean);
        if (!keys.length) throw new Error('API Keys 不能为空');
        if (keys.some(function (k) { return k.indexOf('...') >= 0; })) {
          throw new Error('检测到脱敏值，请填写完整 API Key');
        }
        payload.api_keys = keys;
      }
      var password = fieldValue('set-panel-password');
      if (password) payload.panel_password = password;

      payload.upstream = Object.assign({}, cfg.upstream || {}, {
        assistant_base: fieldValue('up-assistant-base'),
        cgi_base: fieldValue('up-cgi-base'),
        assistant_name: fieldValue('up-assistant-name'),
        mode: intField('up-mode', 'mode', 0),
        default_model: fieldValue('up-default-model'),
        product: fieldValue('up-product'),
        timeout_seconds: intField('up-timeout', 'timeout_seconds', 1),
        header_timeout_seconds: intField('up-header-timeout', 'header_timeout_seconds', 1)
      });
      payload.pool = Object.assign({}, cfg.pool || {}, {
        max_in_flight: intField('pool-max-in-flight', 'max_in_flight', 1),
        max_in_flight_total: intField('pool-max-in-flight-total', 'max_in_flight_total', 0),
        breaker_threshold: intField('pool-breaker-threshold', 'breaker_threshold', 1),
        max_per_minute: intField('pool-max-per-minute', 'max_per_minute', 0),
        breaker_cooldown: durationField('pool-breaker-cooldown', 'breaker_cooldown'),
        soft_cooldown: durationField('pool-soft-cooldown', 'soft_cooldown'),
        max_cooldown: durationField('pool-max-cooldown', 'max_cooldown'),
        min_interval: durationField('pool-min-interval', 'min_interval'),
        prefer_fresh_token: $('pool-prefer-fresh').checked
      });
      payload.conversation = Object.assign({}, cfg.conversation || {}, {
        mode: fieldValue('conv-mode'),
        sticky_ttl: durationField('conv-sticky-ttl', 'sticky_ttl'),
        max_history_messages: intField('conv-max-messages', 'max_history_messages', 0),
        max_history_chars: intField('conv-max-chars', 'max_history_chars', 0),
        system_prompt_mode: fieldValue('conv-system-mode')
      });
      payload.logging = Object.assign({}, cfg.logging || {}, {
        ring_size: intField('log-ring-size', 'ring_size', 1),
        archive_enabled: $('log-archive-enabled').checked,
        include_prompts: $('log-include-prompts').checked
      });

      await api('config', { method: 'PUT', body: payload });
      toast('配置已保存', 'success');
      state.cfg = await api('config');
      if (state.view === 'settings') {
        var body = $('settings-body');
        if (body) {
          body.innerHTML = settingsHtml(state.cfg);
          bindSettings();
        }
      }
    } catch (err) {
      toast(err.message || '保存失败', 'error', 5000);
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  /* ---------------- 添加账号弹窗 ---------------- */
  function extractTokens(input) {
    var result = { access: '', refresh: '' };
    var text = String(input || '').trim();
    if (!text) return result;

    function walk(value, depth) {
      if (!value || depth > 4) return;
      if (Array.isArray(value)) {
        value.forEach(function (item) { walk(item, depth + 1); });
        return;
      }
      if (typeof value !== 'object') return;
      Object.keys(value).forEach(function (key) {
        var lower = key.toLowerCase();
        var item = value[key];
        if (typeof item === 'string') {
          if (!result.access && (lower === 'access_token' || lower === 'accesstoken' || lower === 'oauth_access_token')) {
            result.access = item.trim();
          }
          if (!result.refresh && (lower === 'refresh_token' || lower === 'refreshtoken' || lower === 'oauth_refresh_token_v2')) {
            result.refresh = item.trim();
          }
        } else {
          walk(item, depth + 1);
        }
      });
    }

    try { walk(JSON.parse(text), 0); } catch (err) { /* 非 JSON，继续正则提取 */ }
    if (!result.access) {
      var ma = text.match(/"access_token"\s*:\s*"([^"]+)"/i);
      if (ma) result.access = ma[1];
    }
    if (!result.refresh) {
      var mr = text.match(/"refresh_token"\s*:\s*"([^"]+)"/i);
      if (mr) result.refresh = mr[1];
    }
    if (!result.access && /^[A-Za-z0-9._\-]{20,}$/.test(text)) result.access = text;
    return result;
  }

  function renderOAuthResult(wrap, res) {
    var box = wrap.querySelector('#oauth-result');
    if (!box) return;
    box.innerHTML = '<div class="oauth-result">' +
      '<div class="field-label">扫码登录链接</div>' +
      '<div class="copy-row"><input id="oauth-url" class="mono" readonly value="' + attr(res.url) + '">' +
      '<button class="btn btn-sm" type="button" data-copy-url><svg class="icon"><use href="#i-copy"></use></svg>复制</button></div>' +
      '<div><a class="btn btn-primary" href="' + attr(res.url) + '" target="_blank" rel="noopener noreferrer">' +
      '<svg class="icon"><use href="#i-external"></use></svg>打开浏览器</a></div>' +
      '<div class="field-hint">会话 ID：<code class="mono">' + esc(res.session_id || '—') + '</code> · 链接 15 分钟内有效。扫码完成后账号会自动写入。</div>' +
      '<div class="oauth-status" id="oauth-link-status">等待扫码…（浏览器提示打开 OrcaTerm 客户端可以忽略）</div>' +
      '</div>';
    box.querySelector('[data-copy-url]').addEventListener('click', function () {
      copyText(box.querySelector('#oauth-url').value);
    });
    pollOAuth(res.session_id, box.querySelector('#oauth-link-status'));
  }

  // pollOAuth 轮询旧「浏览器授权链接」流程的扫码结果：只要用户完成扫码，
  // 服务端会话就可用，不依赖 return_url / 深层链接。
  async function pollOAuth(sessionId, statusEl) {
    if (!sessionId || !statusEl) return;
    for (var i = 0; i < 100; i++) {
      await new Promise(function (r) { setTimeout(r, 3000); });
      if (!statusEl.isConnected) return;
      try {
        var res = await api('oauth/poll', { method: 'POST', body: { session_id: sessionId } });
        if (res.status === 'ok') {
          statusEl.textContent = '登录成功，账号已添加';
          statusEl.className = 'oauth-status ok';
          toast('微信账号已添加', 'success');
          go('accounts');
          return;
        }
        if (res.status === 'expired') {
          statusEl.textContent = '扫码会话已过期，请重新生成';
          statusEl.className = 'oauth-status error';
          return;
        }
      } catch (err) {
        statusEl.textContent = err.message || '轮询失败';
        statusEl.className = 'oauth-status error';
        return;
      }
    }
    statusEl.textContent = '等待超时，请重新生成链接';
    statusEl.className = 'oauth-status warn';
  }

  // loadWechatQR 在面板内直接渲染微信二维码并轮询扫码结果。
  // 二维码来自 /oauth/authorize 的 302（open.weixin.qq.com/qrconnect），
  // 扫码后服务端会话即被授权，这里用 /api/oauth/poll 直接兑换令牌，
  // 所以浏览器提示「打开 OrcaTerm 客户端」与面板无关，可以忽略。
  function loadWechatQR(wrap, close) {
    var box = wrap.querySelector('#orca-qr');
    var statusEl = wrap.querySelector('#orca-qr-status');
    var refreshBtn = wrap.querySelector('#orca-qr-refresh');
    if (!box || !statusEl) return;
    var st = { sessionId: null, done: false, timer: null };

    function setStatus(text, kind) {
      statusEl.textContent = text;
      statusEl.className = 'oauth-status' + (kind ? ' ' + kind : '');
    }
    function cleanup() {
      st.done = true;
      if (st.timer) clearTimeout(st.timer);
      st.timer = null;
    }
    async function poll(sessionId) {
      var failures = 0;
      for (var i = 0; i < 150; i++) {
        await new Promise(function (r) { setTimeout(r, 2000); });
        if (st.done || !statusEl.isConnected) return;
        try {
          var res = await api('oauth/poll', { method: 'POST', body: { session_id: sessionId } });
          failures = 0;
          if (res.status === 'ok') {
            st.done = true;
            setStatus('登录成功，账号已添加', 'ok');
            toast('微信账号已添加' + (res.account && res.account.name ? '：' + res.account.name : ''), 'success');
            setTimeout(function () { close(); go('accounts'); }, 700);
            return;
          }
          if (res.status === 'expired') {
            setStatus('二维码已过期，请点「刷新二维码」', 'warn');
            return;
          }
        } catch (err) {
          failures++;
          if (failures >= 5) {
            setStatus(err.message || '轮询扫码结果失败', 'error');
            return;
          }
        }
      }
      setStatus('等待超时，请刷新二维码', 'warn');
    }
    async function render() {
      cleanup();
      st.done = false;
      box.innerHTML = '<span class="spinner"></span>';
      setStatus('正在获取微信二维码…');
      try {
        var res = await api('oauth/start', { method: 'POST', body: {} });
        if (st.done || !box.isConnected) return;
        if (!res.qr_url) {
          setStatus('未能解析微信二维码，请使用下方「备用：浏览器授权链接」', 'warn');
          return;
        }
        st.sessionId = res.session_id;
        box.innerHTML = '<iframe class="wx-qr-frame" width="300" height="400" frameborder="0" scrolling="no" title="微信扫码登录" src="' + attr(res.qr_url) + '"></iframe>';
        setStatus('请用微信扫一扫');
        poll(res.session_id);
      } catch (err) {
        setStatus(err.message || '二维码加载失败，请重试', 'error');
      }
    }
    if (refreshBtn) refreshBtn.addEventListener('click', render);
    render();
    return { cleanup: cleanup, render: render };
  }

  function openAddAccount() {
    state.addTab = 'token';
    var closeModal = openModal({
      title: '添加账号',
      wide: true,
      body:
        '<div class="tabs" role="tablist">' +
          '<button class="tab active" type="button" data-tab="token" role="tab">粘贴令牌</button>' +
          '<button class="tab" type="button" data-tab="import" role="tab">本机导入</button>' +
          '<button class="tab" type="button" data-tab="oauth" role="tab">微信扫码</button>' +
        '</div>' +
        '<div class="tab-panel" data-panel="token">' +
          '<div class="form-field"><label class="field-label" for="add-access-token">access_token <span class="req">*</span></label>' +
            '<textarea id="add-access-token" class="mono" rows="5" spellcheck="false" placeholder="粘贴 access_token，或直接粘贴整段包含 access_token / refresh_token 的 JSON"></textarea>' +
            '<span class="field-hint" id="token-hint">支持整段 JSON 自动提取令牌，提交时会自动解析。</span></div>' +
          '<div class="form-grid compact">' +
            '<div class="form-field"><label class="field-label" for="add-refresh-token">refresh_token</label>' +
              '<input type="text" id="add-refresh-token" class="mono" placeholder="选填" autocomplete="off"></div>' +
            '<div class="form-field"><label class="field-label" for="add-name">名称</label>' +
              '<input type="text" id="add-name" placeholder="选填，默认使用上游昵称" autocomplete="off"></div>' +
          '</div>' +
        '</div>' +
        '<div class="tab-panel" data-panel="import" hidden>' +
          '<div class="form-field"><label class="field-label" for="import-path">data.bin 路径</label>' +
            '<input type="text" id="import-path" class="mono" placeholder="留空自动探测，例如 %APPDATA%\\OrcaTerm\\data.bin">' +
            '<span class="field-hint">从本机 OrcaTerm 客户端数据导入账号，需面板与客户端在同一台机器。</span></div>' +
          '<div class="form-field"><label class="field-label" for="import-refresh">refresh_token</label>' +
            '<input type="text" id="import-refresh" class="mono" placeholder="选填，覆盖本地文件中的刷新令牌" autocomplete="off"></div>' +
        '</div>' +
        '<div class="tab-panel" data-panel="oauth" hidden>' +
          '<div class="oauth-box">' +
            '<div id="orca-qr" class="orca-qr"><span class="spinner"></span></div>' +
            '<p id="orca-qr-status" class="oauth-status">正在获取微信二维码…</p>' +
            '<p class="field-hint">用微信「扫一扫」扫描上方二维码。扫码成功后账号会自动写入；浏览器若提示打开 OrcaTerm 客户端，忽略即可。</p>' +
            '<button class="btn btn-sm" type="button" id="orca-qr-refresh">刷新二维码</button>' +
          '</div>' +
          '<details class="oauth-fallback"><summary>备用：浏览器授权链接</summary>' +
            '<p class="field-hint">如果面板内的二维码无法显示，可生成浏览器链接；扫码完成后本页会自动轮询结果。</p>' +
            '<button class="btn btn-sm" type="button" id="oauth-link-btn">生成链接</button>' +
            '<div id="oauth-result"></div>' +
          '</details>' +
        '</div>',
      footer: '<button class="btn" type="button" data-cancel>取消</button>' +
        '<button class="btn btn-primary" type="button" data-submit>添加账号</button>',
      onMount: function (wrap, close) {
        var submit = wrap.querySelector('[data-submit]');
        var accessArea = wrap.querySelector('#add-access-token');
        var refreshInput = wrap.querySelector('#add-refresh-token');
        var hint = wrap.querySelector('#token-hint');

        function setTab(tab) {
          state.addTab = tab;
          wrap.querySelectorAll('.tab').forEach(function (b) {
            b.classList.toggle('active', b.dataset.tab === tab);
          });
          wrap.querySelectorAll('.tab-panel').forEach(function (p) {
            p.hidden = p.dataset.panel !== tab;
          });
          submit.textContent = tab === 'token' ? '添加账号' : tab === 'import' ? '导入账号' : '刷新二维码';
          if (tab === 'oauth') {
            if (wrap._wx) wrap._wx.cleanup();
            wrap._wx = loadWechatQR(wrap, close);
          }
        }

        wrap.querySelectorAll('.tab').forEach(function (b) {
          b.addEventListener('click', function () { setTab(b.dataset.tab); });
        });

        var linkBtn = wrap.querySelector('#oauth-link-btn');
        if (linkBtn) {
          linkBtn.addEventListener('click', async function () {
            linkBtn.disabled = true;
            try {
              var res = await api('oauth/start', { method: 'POST', body: {} });
              renderOAuthResult(wrap, res);
              toast('链接已生成，扫码后本页会自动完成', 'success');
            } catch (err) {
              toast(err.message || '生成链接失败', 'error');
            } finally {
              linkBtn.disabled = false;
            }
          });
        }

        function applyExtract() {
          var found = extractTokens(accessArea.value);
          if (found.access) {
            if (found.refresh && !refreshInput.value.trim()) refreshInput.value = found.refresh;
            hint.textContent = '已识别 access_token' + (found.refresh ? ' 与 refresh_token' : '') + '，提交时自动使用。';
            hint.classList.add('ok');
          } else {
            hint.textContent = '支持整段 JSON 自动提取令牌，提交时会自动解析。';
            hint.classList.remove('ok');
          }
        }
        accessArea.addEventListener('input', applyExtract);
        accessArea.addEventListener('paste', function () { setTimeout(applyExtract, 0); });

        wrap.querySelector('[data-cancel]').addEventListener('click', close);

        submit.addEventListener('click', async function () {
          if (state.addTab === 'oauth') {
            if (wrap._wx) wrap._wx.cleanup();
            wrap._wx = loadWechatQR(wrap, close);
            return;
          }

          setBtnLoading(submit, true);
          try {
            if (state.addTab === 'token') {
              var raw = accessArea.value.trim();
              var found = extractTokens(raw);
              var access = found.access;
              if (!access && raw && raw.charAt(0) !== '{') access = raw;
              var refresh = refreshInput.value.trim() || found.refresh;
              if (!access) throw new Error('请填写 access_token');
              await api('accounts', {
                method: 'POST',
                body: { access_token: access, refresh_token: refresh, name: wrap.querySelector('#add-name').value.trim() }
              });
              close();
              toast('账号已添加', 'success');
              go('accounts');
            } else {
              var res = await api('accounts/import-local', {
                method: 'POST',
                body: {
                  path: wrap.querySelector('#import-path').value.trim(),
                  refresh_token: wrap.querySelector('#import-refresh').value.trim()
                }
              });
              close();
              toast(res.path ? '账号已导入：' + res.path : '账号已导入', 'success', 5000);
              go('accounts');
            }
          } catch (err) {
            toast(err.message || '操作失败', 'error', 5000);
          } finally {
            if (submit.isConnected) setBtnLoading(submit, false);
          }
        });
      }
    });
    return closeModal;
  }

  /* ---------------- 账号测试弹窗 ---------------- */
  async function openTestModal(id, btn) {
    var acct = accountById(id);
    if (!acct) return;
    setBtnLoading(btn, true);
    try {
      if (!state.models) state.models = await api('models');
      var models = (state.models && state.models.data) || [];
      var def = (state.models && state.models.default) || '';
      var options = models.map(function (m) {
        return '<option value="' + attr(m.id) + '"' + (m.id === def ? ' selected' : '') + '>' +
          esc(m.id) + (m.label ? ' · ' + esc(m.label) : '') + '</option>';
      }).join('');

      openModal({
        title: '测试账号 · ' + (acct.name || acct.id),
        body:
          '<div class="form-field" style="margin-bottom:14px"><label class="field-label" for="test-model">模型</label>' +
            '<select id="test-model">' + (options || '<option value="">使用默认模型</option>') + '</select></div>' +
          '<div class="form-field"><label class="field-label" for="test-prompt">Prompt</label>' +
            '<textarea id="test-prompt" rows="3">你好，请用一句话介绍你自己。</textarea>' +
            '<span class="field-hint">测试会真实请求上游，并消耗一次账号额度。</span></div>' +
          '<div id="test-result" class="test-result" hidden></div>',
        footer: '<button class="btn" type="button" data-cancel>关闭</button>' +
          '<button class="btn btn-primary" type="button" data-run-test>开始测试</button>',
        onMount: function (wrap, close) {
          wrap.querySelector('[data-cancel]').addEventListener('click', close);
          var run = wrap.querySelector('[data-run-test]');
          run.addEventListener('click', async function () {
            setBtnLoading(run, true);
            var result = wrap.querySelector('#test-result');
            result.hidden = false;
            result.innerHTML = loadingHtml('等待上游回复…');
            try {
              var res = await api('accounts/' + encodeURIComponent(id) + '/test', {
                method: 'POST',
                body: {
                  model: wrap.querySelector('#test-model').value,
                  prompt: wrap.querySelector('#test-prompt').value
                }
              });
              result.innerHTML = '<div class="detail-label">回复</div><pre class="detail-pre">' + esc(res.reply || '（空回复）') + '</pre>';
              toast('测试成功', 'success');
            } catch (err) {
              result.innerHTML = '<div class="detail-label">错误</div><div class="acct-error">' + esc(err.message || '测试失败') + '</div>';
              toast(err.message || '测试失败', 'error', 5000);
            } finally {
              if (run.isConnected) setBtnLoading(run, false);
            }
          });
        }
      });
    } catch (err) {
      toast(err.message || '加载模型列表失败', 'error');
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  /* ---------------- 账号额度弹窗 ---------------- */
  function quotaHtml(quota) {
    var q = quota || {};
    var limit = Number(q.quota_limit) || 0;
    var used = Number(q.quota_used) || 0;
    var remaining = q.remaining == null ? Math.max(limit - used, 0) : Number(q.remaining);
    var percent = Number(q.percent_used);
    if (!isFinite(percent)) percent = limit > 0 ? used / limit * 100 : 0;
    percent = Math.max(0, Math.min(100, percent));
    var low = limit > 0 && remaining / limit <= 0.1;
    var period = (q.quota_start_date || '—') + ' ~ ' + (q.quota_end_date || '—');
    return '<div class="quota-hero"><div><span class="quota-label">已用 / 总量</span>' +
      '<strong class="quota-number">' + fmtInt(used) + '<small> / ' + fmtInt(limit) + '</small></strong></div>' +
      '<span class="badge ' + (low ? 'h-breaker' : 'h-healthy') + '">' + percent.toFixed(1) + '% 已用</span></div>' +
      '<div class="quota-bar"><div class="quota-fill' + (low ? ' is-low' : '') + '" style="width:' + percent.toFixed(2) + '%"></div></div>' +
      '<div class="quota-stats">' +
        '<div class="quota-stat"><span>剩余额度</span><strong>' + fmtInt(remaining) + '</strong></div>' +
        '<div class="quota-stat"><span>统计周期</span><strong class="quota-period">' + esc(period) + '</strong></div>' +
        '<div class="quota-stat"><span>配额类型</span><strong>' + esc(q.feature || 'hunyuan_ai') + '</strong></div>' +
      '</div>' +
      '<p class="quota-foot">额度由上游账号实时查询，可能因缓存存在短暂延迟。</p>';
  }

  async function openQuotaModal(id, btn) {
    var acct = accountById(id);
    if (!acct) return;
    setBtnLoading(btn, true);
    try {
      var res = await api('accounts/' + encodeURIComponent(id) + '/quota');
      openModal({
        title: 'AI 用量额度 · ' + (acct.name || acct.id),
        body: quotaHtml(res.quota || {}),
        footer: '<button class="btn" type="button" data-close-quota>关闭</button>',
        onMount: function (wrap, close) {
          wrap.querySelector('[data-close-quota]').addEventListener('click', close);
        }
      });
    } catch (err) {
      toast(err.message || '查询额度失败', 'error', 5000);
    } finally {
      if (btn && btn.isConnected) setBtnLoading(btn, false);
    }
  }

  /* ---------------- 统一事件处理 ---------------- */
  async function onContentClick(e) {
    var target = e.target.closest('[data-action]');
    if (!target || target.disabled) return;
    var action = target.dataset.action;
    var id = target.dataset.id;

    switch (action) {
      case 'retry':
        renderView(state.view);
        break;
      case 'refresh-overview':
        await withBtn(target, loadOverview);
        break;
      case 'refresh-accounts':
        await withBtn(target, loadAccounts);
        break;
      case 'open-add-account':
        openAddAccount();
        break;
      case 'account-test':
        await openTestModal(id, target);
        break;
      case 'account-quota':
        await openQuotaModal(id, target);
        break;
      case 'account-refresh':
        await accountAction(target, function () {
          return api('accounts/' + encodeURIComponent(id) + '/refresh', { method: 'POST' });
        }, '令牌已刷新');
        break;
      case 'account-toggle': {
        var acct = accountById(id);
        if (!acct) break;
        var next = !acct.enabled;
        await accountAction(target, function () {
          return api('accounts/' + encodeURIComponent(id) + '/toggle', { method: 'POST', body: { enabled: next } });
        }, next ? '账号已启用' : '账号已禁用');
        break;
      }
      case 'account-delete': {
        var targetAcct = accountById(id);
        if (!targetAcct) break;
        var ok = await confirmDialog({
          title: '删除账号',
          message: '确定删除账号「' + (targetAcct.name || targetAcct.id) + '」吗？该操作不可撤销。',
          confirmText: '删除',
          danger: true
        });
        if (!ok) break;
        await accountAction(target, function () {
          return api('accounts/' + encodeURIComponent(id), { method: 'DELETE' });
        }, '账号已删除');
        break;
      }
      case 'refresh-models':
        await withBtn(target, loadModels);
        break;
      case 'refresh-logs':
        await withBtn(target, loadLogs);
        break;
      case 'reload-config':
        await withBtn(target, loadSettings);
        toast('已重新载入配置', 'info');
        break;
      case 'save-config':
        await saveConfig(target);
        break;
      default:
        break;
    }
  }

  /* ---------------- 初始化 ---------------- */
  function init() {
    applyTheme(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark');
    document.querySelectorAll('.nav-item').forEach(function (btn) {
      btn.addEventListener('click', function () { go(btn.dataset.view); });
    });
    $('theme-toggle').addEventListener('click', toggleTheme);
    $('logout-btn').addEventListener('click', onLogout);
    $('login-form').addEventListener('submit', onLogin);
    $('password-eye').addEventListener('click', togglePassword);
    $('content').addEventListener('click', onContentClick);
    window.addEventListener('hashchange', function () {
      if (state.session) renderView(currentView());
    });
    bootstrap();
  }

  document.addEventListener('DOMContentLoaded', init);
})();
