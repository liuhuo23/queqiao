/* ==========================================================================
   鹊桥 管理台前端（原生 JS：无框架 / 无构建 / 无外部依赖）
   结构：
     1. 常量
     2. DOM 与格式化工具
     3. 状态与 API 层
     4. Toast 与自实现 Modal
     5. 登录、登出、会话、hash 路由与轮询
     6. 共享渲染组件
     7. 页面视图：概览 / 设备 / 映射 / 日志 / 设置
     8. 启动
   ========================================================================== */
(function () {
  'use strict';

  // ===================== 1. 常量 =====================
  const API_BASE = '/api/v1';
  const ROUTES = ['overview', 'devices', 'rules', 'logs', 'settings'];
  const DEFAULT_ROUTE = 'overview';
  const NAV_LABELS = { overview: '概览', devices: '设备', rules: '映射', logs: '日志', settings: '设置' };
  const POLL_MS = { overview: 4000, devices: 4000, rules: 4000, logs: 8000 };
  const LOG_LIMIT = 200;

  const DEVICE_STATUS = {
    pending: { label: '待审批', tone: 'warn' },
    active: { label: '已批准', tone: 'success' },
    blocked: { label: '已封禁', tone: 'danger' }
  };

  const RULE_STATES = {
    active: { label: '运行中', tone: 'success' },
    device_offline: { label: '设备离线', tone: 'offline' },
    disabled: { label: '已停用', tone: 'neutral' },
    error: { label: '异常', tone: 'danger' }
  };

  const LOG_LEVELS = {
    debug: { label: '调试', tone: 'neutral' },
    info: { label: '信息', tone: 'info' },
    warn: { label: '警告', tone: 'warn' },
    error: { label: '错误', tone: 'danger' }
  };

  const LOG_KINDS = { audit: '审计日志', agent: 'Agent 事件' };

  const DEVICE_FILTERS = [
    { value: 'all', label: '全部' },
    { value: 'pending', label: '待审批' },
    { value: 'active', label: '已批准' },
    { value: 'blocked', label: '已封禁' }
  ];

  // ===================== 2. DOM 与格式化工具 =====================
  function $(selector, root) {
    return (root || document).querySelector(selector);
  }

  function addChildren(node, children) {
    for (const child of children) {
      if (child === null || child === undefined || child === false) continue;
      if (Array.isArray(child)) {
        addChildren(node, child);
        continue;
      }
      if (child instanceof Node) node.appendChild(child);
      else node.appendChild(document.createTextNode(String(child)));
    }
  }

  /**
   * 创建元素。attrs 支持 class / text / on / dataset / value / checked / selected，
   * 其余作为属性。所有文本一律走 textContent，绝不把数据拼进 innerHTML。
   */
  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    const deferred = {};
    if (attrs) {
      for (const key of Object.keys(attrs)) {
        const value = attrs[key];
        if (value === null || value === undefined || value === false) continue;
        if (key === 'class') {
          node.className = value;
        } else if (key === 'text') {
          node.textContent = String(value);
        } else if (key === 'on') {
          for (const eventName of Object.keys(value)) node.addEventListener(eventName, value[eventName]);
        } else if (key === 'dataset') {
          for (const dataKey of Object.keys(value)) {
            if (value[dataKey] !== null && value[dataKey] !== undefined) node.dataset[dataKey] = String(value[dataKey]);
          }
        } else if (key === 'value' || key === 'checked' || key === 'selected') {
          deferred[key] = value;
        } else if (value === true) {
          node.setAttribute(key, '');
        } else {
          node.setAttribute(key, String(value));
        }
      }
    }
    addChildren(node, children);
    if ('value' in deferred) node.value = deferred.value;
    if ('checked' in deferred) node.checked = !!deferred.checked;
    if ('selected' in deferred) node.selected = !!deferred.selected;
    return node;
  }

  function monoText(value) {
    return el('span', { class: 'mono', text: value === null || value === undefined || value === '' ? '—' : String(value) });
  }

  function pad2(value) {
    return value < 10 ? '0' + value : String(value);
  }

  function fmtDateTime(ms) {
    const time = Number(ms);
    if (!Number.isFinite(time)) return '—';
    const date = new Date(time);
    if (Number.isNaN(date.getTime())) return '—';
    return date.getFullYear() + '-' + pad2(date.getMonth() + 1) + '-' + pad2(date.getDate()) +
      ' ' + pad2(date.getHours()) + ':' + pad2(date.getMinutes()) + ':' + pad2(date.getSeconds());
  }

  function parseTime(iso) {
    if (iso === null || iso === undefined || iso === '') return null;
    const ms = Date.parse(String(iso));
    return Number.isFinite(ms) ? ms : null;
  }

  function timeValue(iso) {
    const ms = parseTime(iso);
    return ms === null ? 0 : ms;
  }

  // 字节数 → B / KB / MB / GB / TB
  function fmtBytes(value) {
    let bytes = Number(value);
    if (!Number.isFinite(bytes) || bytes < 0) bytes = 0;
    if (bytes < 1024) return Math.round(bytes) + ' B';
    const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
    let size = bytes / 1024;
    let index = 0;
    while (size >= 1024 && index < units.length - 1) {
      size = size / 1024;
      index += 1;
    }
    const digits = size >= 100 ? 0 : 1;
    return size.toFixed(digits) + ' ' + units[index];
  }

  function fmtDuration(value) {
    let seconds = Math.max(0, Math.floor(Number(value) || 0));
    const days = Math.floor(seconds / 86400);
    seconds -= days * 86400;
    const hours = Math.floor(seconds / 3600);
    seconds -= hours * 3600;
    const minutes = Math.floor(seconds / 60);
    seconds -= minutes * 60;
    if (days > 0) return days + ' 天 ' + hours + ' 小时';
    if (hours > 0) return hours + ' 小时 ' + minutes + ' 分';
    if (minutes > 0) return minutes + ' 分 ' + seconds + ' 秒';
    return seconds + ' 秒';
  }

  function toNumber(value) {
    const number = Number(value);
    return Number.isFinite(number) ? number : 0;
  }

  function num(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) return '—';
    return number.toLocaleString('zh-CN');
  }

  // 相对时间：刚刚 / x 秒前 / x 分钟前 / x 小时前 / x 天前
  function relTimeFromMs(ms, nowMs) {
    const now = Number.isFinite(Number(nowMs)) ? Number(nowMs) : Date.now();
    const diff = Math.max(0, Math.floor((now - Number(ms)) / 1000));
    if (diff < 5) return '刚刚';
    if (diff < 60) return diff + ' 秒前';
    if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前';
    if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前';
    if (diff < 86400 * 30) return Math.floor(diff / 86400) + ' 天前';
    return fmtDateTime(ms).slice(0, 10);
  }

  function relTime(iso) {
    const ms = parseTime(iso);
    return ms === null ? '—' : relTimeFromMs(ms);
  }

  function fmtSecAgo(seconds) {
    const value = Math.max(0, Math.floor(Number(seconds) || 0));
    if (value < 5) return '刚刚';
    if (value < 60) return value + ' 秒前';
    return relTimeFromMs(Date.now() - value * 1000);
  }

  function displayName(device) {
    if (!device) return '未知设备';
    return device.name || device.hostname || device.key_code || device.id || '未命名设备';
  }

  function osArch(device) {
    if (!device) return '未知系统';
    const parts = [device.os, device.arch].filter(Boolean);
    return parts.length ? parts.join(' / ') : '未知系统';
  }

  function formatPortRange(range) {
    if (!Array.isArray(range) || range.length < 2) return '—';
    return range[0] + ' – ' + range[1];
  }

  // ===================== 3. 状态与 API 层 =====================
  const state = {
    me: null,               // { admin: {id, username}, csrf_token, must_change_password }
    version: '',            // 服务端版本
    mustChange: false,      // 是否仍在使用初始密码
    online: null,           // null=连接中 / true=已连接 / false=服务端不可达
    route: DEFAULT_ROUTE,
    navToken: 0,            // 防止旧页面的异步渲染覆盖新页面
    pollTimer: null,
    refreshing: false,
    loggingIn: false,
    overview: null,
    pendingDevices: [],
    devices: [],
    allDevices: [],
    deviceFilter: 'all',
    rules: [],
    ruleDeviceFilter: '',
    logs: [],
    logKind: 'audit',
    settings: null,
    refs: {},               // 当前页面需要保留的 DOM 引用（列表容器、筛选控件等）
    autoRefresh: true,      // 自动刷新开关（顶栏可切换，持久化到 localStorage）
    signatures: {},         // 各视图数据签名：内容没变时跳过整页重绘
    overviewRefs: null      // 概览页需要原地更新的节点（运行时长/流量/连接数）
  };

  const VIEWS = {};

  class ApiError extends Error {
    constructor(code, message, status) {
      super(message);
      this.name = 'ApiError';
      this.code = code;
      this.status = status || 0;
    }
  }

  function defaultHttpMessage(status) {
    if (status === 400) return '请求参数有误';
    if (status === 401) return '未登录或登录已过期';
    if (status === 403) return '没有权限执行该操作';
    if (status === 404) return '请求的资源不存在';
    if (status === 409) return '操作冲突，请刷新后重试';
    if (status === 429) return '操作过于频繁，请稍后再试';
    if (status >= 500) return '服务端内部错误';
    return '请求失败（HTTP ' + status + '）';
  }

  /**
   * 统一请求封装：
   * - credentials: same-origin（Cookie 会话）
   * - 非 GET 自动带 X-Queqiao-CSRF（来自 /me 或登录响应的 csrf_token）
   * - 非 2xx 抛出 ApiError；401 统一回到登录视图；错误 message 用 toast 呈现
   */
  async function apiRequest(path, options) {
    const opts = options || {};
    const method = (opts.method || 'GET').toUpperCase();
    const silent = !!opts.silent;
    const headers = { Accept: 'application/json' };
    let body;
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(opts.body);
    }
    if (method !== 'GET' && state.me && state.me.csrf_token) {
      headers['X-Queqiao-CSRF'] = state.me.csrf_token;
    }

    let response;
    try {
      response = await fetch(API_BASE + path, {
        method: method,
        headers: headers,
        credentials: 'same-origin',
        body: body,
        cache: 'no-store'
      });
    } catch (networkError) {
      setOnline(false);
      const error = new ApiError('network_error', '服务端不可达', 0);
      if (!silent) toast(error.message, 'error');
      throw error;
    }

    let data = null;
    if (response.status !== 204) {
      const contentType = response.headers.get('content-type') || '';
      try {
        if (contentType.indexOf('application/json') !== -1) data = await response.json();
        else {
          const text = await response.text();
          data = text ? { error: { message: text } } : null;
        }
      } catch (parseError) {
        data = null;
      }
    }

    if (response.ok) {
      setOnline(true);
      return data;
    }

    const errorBody = data && data.error ? data.error : null;
    const code = (errorBody && errorBody.code) || 'http_' + response.status;
    const message = (errorBody && errorBody.message) || defaultHttpMessage(response.status);
    if (response.status === 401) {
      onUnauthorized();
    } else if (!silent) {
      toast(message, 'error');
    }
    throw new ApiError(code, message, response.status);
  }

  function setOnline(online) {
    const next = !!online;
    if (state.online === next) return;
    state.online = next;
    renderConnBadge();
  }

  function renderConnBadge() {
    const badgeNode = $('#conn-badge');
    const textNode = $('#conn-text');
    if (!badgeNode || !textNode) return;
    const nextState = state.online === null ? 'connecting' : state.online ? 'online' : 'offline';
    badgeNode.dataset.state = nextState;
    textNode.textContent = nextState === 'online' ? '已连接' : nextState === 'offline' ? '服务端不可达' : '连接中…';
    badgeNode.title = nextState === 'offline' ? '轮询失败：服务端不可达，恢复后会自动刷新' : '与管理服务的连接状态';
  }

  // ===================== 4. Toast =====================
  function toast(message, tone) {
    const root = $('#toasts');
    if (!root) return;
    const kind = tone || 'info';
    const closeButton = el('button', { class: 'toast-close', type: 'button', 'aria-label': '关闭提示', text: '×' });
    const item = el('div', { class: 'toast toast-' + kind, role: 'status' },
      el('span', { class: 'toast-msg', text: String(message) }),
      closeButton);
    closeButton.addEventListener('click', () => dismissToast(item));
    root.appendChild(item);
    window.requestAnimationFrame(() => item.classList.add('toast-in'));
    item._dismissTimer = window.setTimeout(() => dismissToast(item), 4600);
    while (root.childElementCount > 4) {
      const first = root.firstElementChild;
      if (!first) break;
      first.remove();
    }
  }

  function dismissToast(item) {
    if (!item || !item.isConnected || item.classList.contains('toast-out')) return;
    if (item._dismissTimer) window.clearTimeout(item._dismissTimer);
    item.classList.add('toast-out');
    window.setTimeout(() => {
      if (item.isConnected) item.remove();
    }, 220);
  }

  // ===================== 5. Modal =====================
  let activeModal = null;

  // 弹窗外壳：负责遮罩、Esc 关闭、焦点循环与滚动锁定
  function createModalShell(title, bodyNode) {
    closeActiveModal();
    const titleId = 'queqiao-modal-title';
    const previousFocus = document.activeElement;
    const footer = el('div', { class: 'modal-footer' });
    const dialog = el('div', { class: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': titleId },
      el('div', { class: 'modal-head' }, el('h2', { class: 'modal-title', id: titleId, text: title })),
      el('div', { class: 'modal-body' }, bodyNode),
      footer);
    const overlay = el('div', { class: 'modal-overlay' }, dialog);

    const onKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        shell.dismiss();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusables = Array.prototype.filter.call(
        dialog.querySelectorAll('button, input, textarea, select, a[href]'),
        (node) => !node.disabled
      );
      if (!focusables.length) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    const shell = {
      overlay: overlay,
      dialog: dialog,
      footer: footer,
      dismiss: null,
      close: function () {
        overlay.remove();
        document.removeEventListener('keydown', onKeyDown, true);
        document.documentElement.classList.remove('modal-open');
        if (activeModal === shell) activeModal = null;
        if (previousFocus && previousFocus.isConnected && typeof previousFocus.focus === 'function') previousFocus.focus();
      }
    };
    shell.dismiss = shell.close;

    overlay.addEventListener('click', (event) => {
      if (event.target === overlay) shell.dismiss();
    });
    document.addEventListener('keydown', onKeyDown, true);
    document.documentElement.classList.add('modal-open');
    const root = $('#modal-root');
    if (root) root.appendChild(overlay);
    activeModal = shell;
    return shell;
  }

  function closeActiveModal() {
    if (activeModal) activeModal.dismiss();
  }

  // 二次确认；危险操作默认聚焦「取消」，避免误按回车
  function askConfirm(options) {
    return new Promise((resolve) => {
      const nodes = [el('p', { class: 'modal-text', text: options.message || '' })];
      if (Array.isArray(options.details) && options.details.length) {
        nodes.push(el('ul', { class: 'modal-list' }, options.details.map((item) => el('li', { text: item }))));
      }
      const shell = createModalShell(options.title || '确认操作', nodes);
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        shell.close();
        resolve(value);
      };
      shell.dismiss = () => finish(false);

      const cancelButton = el('button', { class: 'btn btn-ghost', type: 'button', text: options.cancelText || '取消' });
      cancelButton.addEventListener('click', () => finish(false));
      const confirmButton = el('button', {
        class: 'btn ' + (options.danger ? 'btn-danger' : 'btn-primary'),
        type: 'button',
        text: options.confirmText || '确定'
      });
      confirmButton.addEventListener('click', () => finish(true));
      shell.footer.appendChild(cancelButton);
      shell.footer.appendChild(confirmButton);
      (options.danger ? cancelButton : confirmButton).focus();
    });
  }

  // 表单弹窗；返回字段值对象，取消返回 null
  function askForm(options) {
    return new Promise((resolve) => {
      const fields = options.fields || [];
      const inputs = {};
      const form = el('form', { class: 'form', id: 'queqiao-modal-form', novalidate: true });
      const errorNode = el('p', { class: 'form-error', role: 'alert', hidden: true });
      const showError = (message) => {
        errorNode.textContent = message;
        errorNode.hidden = false;
      };

      if (options.description) {
        form.appendChild(el('p', { class: 'modal-text', text: options.description }));
      }

      for (const field of fields) {
        const inputId = 'queqiao-field-' + field.name;
        let input;
        if (field.type === 'checkbox') {
          input = el('input', { class: 'checkbox', id: inputId, type: 'checkbox' });
          input.checked = !!field.value;
          form.appendChild(el('label', { class: 'field field-inline', for: inputId },
            input,
            el('span', { class: 'field-label', text: field.label })));
        } else if (field.multiline) {
          input = el('textarea', {
            class: 'input',
            id: inputId,
            rows: field.rows || 3,
            placeholder: field.placeholder || ''
          });
          input.value = field.value === null || field.value === undefined ? '' : String(field.value);
          form.appendChild(el('label', { class: 'field', for: inputId },
            el('span', { class: 'field-label', text: field.label }),
            input,
            field.hint ? el('span', { class: 'field-hint', text: field.hint }) : null));
        } else {
          input = el('input', {
            class: 'input',
            id: inputId,
            type: field.type || 'text',
            placeholder: field.placeholder || '',
            autocomplete: 'off',
            spellcheck: 'false'
          });
          if (field.maxlength) input.maxLength = field.maxlength;
          if (field.minlength) input.minLength = field.minlength;
          input.value = field.value === null || field.value === undefined ? '' : String(field.value);
          form.appendChild(el('label', { class: 'field', for: inputId },
            el('span', { class: 'field-label', text: field.label }),
            input,
            field.hint ? el('span', { class: 'field-hint', text: field.hint }) : null));
        }
        inputs[field.name] = input;
      }

      form.appendChild(errorNode);
      const shell = createModalShell(options.title || '编辑', form);
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        shell.close();
        resolve(value);
      };
      shell.dismiss = () => finish(null);

      const cancelButton = el('button', { class: 'btn btn-ghost', type: 'button', text: '取消' });
      cancelButton.addEventListener('click', () => finish(null));
      const submitButton = el('button', {
        class: options.danger ? 'btn btn-danger' : 'btn btn-primary',
        type: 'submit',
        form: 'queqiao-modal-form',
        text: options.submitText || '保存'
      });
      shell.footer.appendChild(cancelButton);
      shell.footer.appendChild(submitButton);

      form.addEventListener('submit', (event) => {
        event.preventDefault();
        errorNode.hidden = true;
        const values = {};
        for (const field of fields) {
          const input = inputs[field.name];
          values[field.name] = field.type === 'checkbox' ? !!input.checked : input.value.trim();
        }
        for (const field of fields) {
          if (field.type === 'checkbox') continue;
          if (!field.allowEmpty && values[field.name] === '') {
            showError('请填写「' + field.label + '」');
            inputs[field.name].focus();
            return;
          }
          if (field.minLength && Array.from(values[field.name]).length < field.minLength) {
            showError('「' + field.label + '」至少需要 ' + field.minLength + ' 个字符');
            inputs[field.name].focus();
            return;
          }
        }
        if (typeof options.validate === 'function') {
          const message = options.validate(values);
          if (message) {
            showError(message);
            return;
          }
        }
        finish(values);
      });

      const firstField = fields.find((field) => field.type !== 'checkbox') || fields[0];
      const firstInput = firstField ? inputs[firstField.name] : null;
      if (firstInput) {
        firstInput.focus();
        if (typeof firstInput.select === 'function') firstInput.select();
      }
    });
  }

  // ===================== 6. 登录与会话 =====================
  function hideBoot() {
    const boot = $('#boot');
    if (boot && boot.isConnected) boot.remove();
  }

  function resetSessionState() {
    state.me = null;
    state.overview = null;
    state.pendingDevices = [];
    state.devices = [];
    state.allDevices = [];
    state.rules = [];
    state.logs = [];
    state.settings = null;
    state.refs = {};
    state.deviceFilter = 'all';
    state.ruleDeviceFilter = '';
    state.logKind = 'audit';
    state.mustChange = false;
    state.loggingIn = false;
  }

  function showLogin(message) {
    stopPolling();
    state.navToken += 1;
    closeActiveModal();
    resetSessionState();
    hideBoot();
    const loginView = $('#login-view');
    const appView = $('#app-view');
    if (appView) appView.hidden = true;
    if (loginView) loginView.hidden = false;
    const adminName = $('#admin-name');
    if (adminName) {
      adminName.textContent = '';
      adminName.removeAttribute('title');
    }
    setLoginLoading(false);
    if (message) showLoginError(message);
    else hideLoginError();
    const passwordInput = $('#login-password');
    if (passwordInput) passwordInput.value = '';
    document.title = '登录 · 鹊桥 管理台';
    renderConnBadge();
    if (location.hash) history.replaceState(null, '', location.pathname + location.search);
    window.setTimeout(() => {
      const usernameInput = $('#login-username');
      if (usernameInput && !usernameInput.disabled) usernameInput.focus();
    }, 0);
  }

  function showLoginError(message) {
    const node = $('#login-error');
    if (!node) return;
    node.textContent = message || '';
    node.hidden = !message;
  }

  function hideLoginError() {
    const node = $('#login-error');
    if (!node) return;
    node.textContent = '';
    node.hidden = true;
  }

  function setLoginLoading(loading) {
    state.loggingIn = !!loading;
    const button = $('#login-submit');
    const label = $('#login-submit-text');
    const usernameInput = $('#login-username');
    const passwordInput = $('#login-password');
    if (button) {
      button.disabled = !!loading;
      button.classList.toggle('is-loading', !!loading);
    }
    if (label) label.textContent = loading ? '登录中…' : '登录';
    if (usernameInput) usernameInput.disabled = !!loading;
    if (passwordInput) passwordInput.disabled = !!loading;
  }

  function loginErrorMessage(error) {
    if (!error) return '登录失败，请重试';
    if (error.code === 'invalid_credentials' || error.status === 401) return '用户名或密码不正确';
    if (error.code === 'too_many_attempts' || error.status === 429) return '登录尝试过于频繁，请稍后再试';
    if (error.code === 'network_error') return '无法连接服务端，请确认 鹊桥 服务正在运行';
    return error.message || '登录失败，请重试';
  }

  function onUnauthorized() {
    const loginView = $('#login-view');
    const loginVisible = loginView && !loginView.hidden;
    if (!state.me && loginVisible) return;
    const expired = !!state.me;
    showLogin(expired ? '登录已过期，请重新登录' : '');
  }

  async function handleLogin(event) {
    event.preventDefault();
    if (state.loggingIn) return;
    const usernameInput = $('#login-username');
    const passwordInput = $('#login-password');
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    if (!username) {
      showLoginError('请输入用户名');
      usernameInput.focus();
      return;
    }
    if (!password) {
      showLoginError('请输入密码');
      passwordInput.focus();
      return;
    }

    hideLoginError();
    setLoginLoading(true);
    try {
      const data = await apiRequest('/auth/login', {
        method: 'POST',
        body: { username: username, password: password },
        silent: true
      });
      state.me = {
        admin: (data && data.admin) || { username: username },
        csrf_token: (data && data.csrf_token) || '',
        must_change_password: false
      };
      // 登录响应不含 must_change_password，补取一次 /me
      try {
        const me = await apiRequest('/me', { silent: true });
        state.me = me;
        state.mustChange = !!(me && me.must_change_password);
      } catch (meError) {
        if (meError && meError.status === 401) throw meError;
      }
      setLoginLoading(false);
      await enterApp();
    } catch (error) {
      setLoginLoading(false);
      showLoginError(loginErrorMessage(error));
      const passwordInputAfter = $('#login-password');
      if (passwordInputAfter) {
        passwordInputAfter.value = '';
        passwordInputAfter.focus();
      }
    }
  }

  async function handleLogout() {
    const button = $('#logout-btn');
    if (button) button.disabled = true;
    try {
      await apiRequest('/auth/logout', { method: 'POST', silent: true });
    } catch (error) {
      // 会话可能已失效；无论结果如何都回登录页
    }
    if (button && button.isConnected) button.disabled = false;
    showLogin();
    toast('已退出登录', 'success');
  }

  async function loadHealth() {
    try {
      const health = await apiRequest('/health', { silent: true });
      if (health && health.version) {
        state.version = String(health.version);
        renderVersion();
      }
    } catch (error) {
      // 登录流程会给出更明确的提示
    }
  }

  async function checkSession() {
    try {
      const me = await apiRequest('/me', { silent: true });
      state.me = me;
      state.mustChange = !!(me && me.must_change_password);
      await enterApp();
    } catch (error) {
      if (error && error.status === 401) {
        showLogin();
        return;
      }
      showLogin('无法连接服务端：' + ((error && error.message) || '未知错误'));
    }
  }

  async function enterApp() {
    hideBoot();
    const loginView = $('#login-view');
    const appView = $('#app-view');
    if (loginView) loginView.hidden = true;
    if (appView) appView.hidden = false;
    renderHeader();
    if (state.mustChange) toast('当前仍在使用初始密码，建议尽快在「设置」中修改', 'warn');
    ensureHash();
    await navigate(currentRoute());
  }

  function renderHeader() {
    const admin = state.me && state.me.admin ? state.me.admin : null;
    const adminName = $('#admin-name');
    if (adminName) {
      adminName.textContent = admin ? admin.username || '' : '';
      adminName.title = admin ? '当前管理员：' + (admin.username || '') : '';
    }
    renderVersion();
    renderConnBadge();
    updateNavCount();
    highlightNav();
  }

  function renderVersion() {
    const pill = $('#server-version');
    if (pill) {
      if (state.version) {
        pill.textContent = 'v' + state.version;
        pill.title = '服务端版本 ' + state.version;
        pill.hidden = false;
      } else {
        pill.hidden = true;
      }
    }
    const loginVersion = $('#login-version');
    if (loginVersion) loginVersion.textContent = state.version ? '服务端版本 v' + state.version : '';
  }

  function updateNavCount() {
    const badgeNode = $('#nav-devices-count');
    if (!badgeNode) return;
    const counts = state.overview && state.overview.counts ? state.overview.counts : null;
    const pending = counts ? Number(counts.devices_pending) || 0 : 0;
    if (pending > 0) {
      badgeNode.textContent = pending > 99 ? '99+' : String(pending);
      badgeNode.title = pending + ' 台设备待审批';
      badgeNode.hidden = false;
    } else {
      badgeNode.textContent = '';
      badgeNode.removeAttribute('title');
      badgeNode.hidden = true;
    }
  }

  // ===================== 7. 路由与轮询 =====================
  function currentRoute() {
    const match = /^#\/([a-z]+)$/.exec(location.hash || '');
    if (match && ROUTES.indexOf(match[1]) !== -1) return match[1];
    return DEFAULT_ROUTE;
  }

  function ensureHash() {
    const want = '#/' + currentRoute();
    if (location.hash !== want) history.replaceState(null, '', want);
  }

  function highlightNav() {
    const links = document.querySelectorAll('.nav-link');
    Array.prototype.forEach.call(links, (link) => {
      if (link.dataset.route === state.route) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
  }

  async function navigate(route) {
    if (!state.me) return;
    const nextRoute = ROUTES.indexOf(route) === -1 ? DEFAULT_ROUTE : route;
    state.route = nextRoute;
    state.signatures = {};
    state.overviewRefs = null;
    ensureHash();
    highlightNav();
    stopPolling();
    closeActiveModal();

    const token = state.navToken + 1;
    state.navToken = token;
    const page = $('#page');
    if (!page) return;
    document.title = NAV_LABELS[nextRoute] + ' · 鹊桥 管理台';
    page.replaceChildren(pageLoading('加载中…'));

    try {
      await VIEWS[nextRoute].enter(page);
    } catch (error) {
      if (error && error.status === 401) return;
      if (token !== state.navToken) return;
      renderPageError(page, error);
      return;
    }
    if (token !== state.navToken) return;
    const interval = VIEWS[nextRoute].pollMs || 0;
    if (interval) startPolling(interval);
  }

  function startPolling(intervalMs) {
    stopPolling();
    if (!intervalMs || !state.autoRefresh) {
      updateAutoRefreshUI();
      return;
    }
    state.pollTimer = window.setInterval(() => {
      if (!state.me) return;
      if (document.visibilityState === 'hidden') return;
      // 用户正在输入、弹窗打开或选中文本时跳过本次刷新，避免打断操作
      if (isUserBusy()) return;
      refreshCurrentView();
    }, intervalMs);
    updateAutoRefreshUI();
  }

  function stopPolling() {
    if (state.pollTimer) {
      window.clearInterval(state.pollTimer);
      state.pollTimer = null;
    }
  }

  // 轮询刷新：静默请求，失败只标记「服务端不可达」，不反复弹 toast
  async function refreshCurrentView() {
    if (!state.me || state.refreshing) return;
    const view = VIEWS[state.route];
    if (!view || typeof view.refresh !== 'function') return;
    state.refreshing = true;
    try {
      await view.refresh();
    } catch (error) {
      if (!error || error.status !== 401) setOnline(false);
    } finally {
      state.refreshing = false;
    }
  }

  // ---------- 自动刷新 / 局部更新辅助 ----------
  function isUserBusy() {
    if (activeModal) return true;
    const active = document.activeElement;
    if (active) {
      const tag = active.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || active.isContentEditable) return true;
    }
    const selection = window.getSelection ? window.getSelection() : null;
    if (selection && selection.toString().length > 0) return true;
    return false;
  }

  function updateAutoRefreshUI() {
    const button = $('#auto-refresh-btn');
    if (!button) return;
    button.setAttribute('aria-pressed', String(state.autoRefresh));
    button.title = state.autoRefresh ? '点击暂停自动刷新' : '点击恢复自动刷新';
  }

  function toggleAutoRefresh() {
    state.autoRefresh = !state.autoRefresh;
    try {
      window.localStorage.setItem('queqiao.autoRefresh', state.autoRefresh ? '1' : '0');
    } catch (error) {
      // 隐私模式下 localStorage 可能不可用，忽略
    }
    const view = VIEWS[state.route];
    if (state.autoRefresh && view && view.pollMs) startPolling(view.pollMs);
    else stopPolling();
    updateAutoRefreshUI();
    toast(state.autoRefresh ? '已开启自动刷新' : '已暂停自动刷新，可点「刷新」手动更新', 'success');
  }

  function manualRefresh() {
    refreshCurrentView();
    refreshNavCount();
  }

  // 重绘后恢复滚动位置，避免自动刷新把页面滚回顶部
  function withScrollPreserved(fn) {
    const x = window.scrollX;
    const y = window.scrollY;
    fn();
    if (window.scrollX !== x || window.scrollY !== y) window.scrollTo(x, y);
  }

  function pageLoading(label) {
    return el('div', { class: 'page-loading' },
      el('span', { class: 'spinner spinner-page', 'aria-hidden': 'true' }),
      el('span', { text: label || '加载中…' }));
  }

  function renderPageError(page, error) {
    page.replaceChildren(
      pageHead(NAV_LABELS[state.route] || '页面'),
      el('section', { class: 'card' },
        el('div', { class: 'empty' },
          el('div', { class: 'empty-title', text: '加载失败' }),
          el('div', { class: 'empty-hint', text: (error && error.message) || '请稍后重试' }),
          el('button', {
            class: 'btn btn-primary',
            type: 'button',
            on: { click: () => navigate(state.route) }
          }, '重试'))));
  }

  // ===================== 8. 共享渲染组件 =====================
  function pageHead(title, description) {
    return el('div', { class: 'page-head' },
      el('div', null,
        el('h1', { class: 'page-title', text: title }),
        description ? el('p', { class: 'page-desc', text: description }) : null));
  }

  function card(title, body, options) {
    const opts = options || {};
    const hasHead = title || opts.extra;
    return el('section', { class: 'card' },
      hasHead ? el('div', { class: 'card-head' },
        el('h2', { class: 'card-title' }, title || null),
        opts.extra ? el('div', { class: 'card-head-actions' }, opts.extra) : null) : null,
      el('div', { class: 'card-body' + (opts.flush ? ' card-body-flush' : '') }, body));
  }

  function statCard(label, value, sub, tone, key) {
    return el('div', { class: 'stat', 'data-stat': key || null },
      el('div', { class: 'stat-label', text: label }),
      el('div', { class: 'stat-value' + (tone ? ' tone-' + tone : ''), text: value }),
      el('div', { class: 'stat-sub', text: sub || '' }));
  }

  function kvItem(label, value, key) {
    return el('div', { class: 'kv', 'data-kv': key || null },
      el('div', { class: 'kv-label', text: label }),
      el('div', { class: 'kv-value' }, value));
  }

  function badge(label, tone, title) {
    return el('span', { class: 'badge badge-' + (tone || 'neutral'), text: label, title: title || null });
  }

  function statusDot(online) {
    const label = online ? '在线' : '离线';
    return el('span', {
      class: 'status-dot' + (online ? ' is-online' : ''),
      title: label,
      role: 'img',
      'aria-label': label
    });
  }

  function timeCell(iso, options) {
    const opts = options || {};
    const ms = parseTime(iso);
    if (ms === null) return el('span', { class: 'muted small', text: opts.emptyText || '—' });
    return el('time', {
      class: 'small' + (opts.className ? ' ' + opts.className : ''),
      datetime: iso,
      title: fmtDateTime(ms),
      text: relTimeFromMs(ms)
    });
  }

  function emptyState(title, hint, action) {
    return el('div', { class: 'empty' },
      el('div', { class: 'empty-title', text: title }),
      hint ? el('div', { class: 'empty-hint', text: hint }) : null,
      action || null);
  }

  // 表格：列描述 { label, render(row) }，每格数据都通过 createElement/textContent 生成
  function dataTable(columns, rows, rowAttrs) {
    const headRow = el('tr', null, columns.map((column) => el('th', { text: column.label })));
    const bodyRows = rows.map((row, index) => {
      const tr = el('tr', null, columns.map((column) => {
        const cellContent = column.render(row, index);
        const cell = el('div', { class: 'cell' + (column.align === 'right' ? ' cell-right' : '') }, cellContent);
        const td = el('td', null, cell);
        if (column.label) td.dataset.label = column.label;
        return td;
      }));
      if (typeof rowAttrs === 'function') rowAttrs(tr, row, index);
      return tr;
    });
    return el('div', { class: 'table-wrap' },
      el('table', { class: 'table' },
        el('thead', null, headRow),
        el('tbody', null, bodyRows)));
  }

  function labeledField(labelText, control, hint) {
    return el('label', { class: 'field' },
      el('span', { class: 'field-label', text: labelText }),
      control,
      hint ? el('span', { class: 'field-hint', text: hint }) : null);
  }

  function actionButton(label, className, handler) {
    const button = el('button', { class: 'btn ' + className, type: 'button', text: label });
    button.addEventListener('click', () => runAction(button, handler));
    return button;
  }

  async function runAction(button, handler) {
    if (button.disabled) return;
    button.disabled = true;
    try {
      await handler();
    } catch (error) {
      // apiRequest 已经 toast；401 交给登录流程处理
    } finally {
      if (button.isConnected) button.disabled = false;
    }
  }

  // 变更后的统一刷新：当前页数据 + 顶栏待审批计数
  async function afterDataChange() {
    if (state.route === 'overview') {
      await loadOverview(true);
      return;
    }
    if (state.route === 'devices') await loadDevices(true);
    else if (state.route === 'rules') await loadRules(true);
    refreshNavCount();
  }

  function refreshNavCount() {
    apiRequest('/overview', { silent: true }).then((overview) => {
      state.overview = overview;
      updateNavCount();
    }).catch(() => {
      // 顶栏计数不是关键路径，忽略
    });
  }

  function setHint(node, text, tone) {
    if (!node) return;
    node.textContent = text;
    if (tone) node.dataset.tone = tone;
    else delete node.dataset.tone;
  }

  // 粗略校验 host:port（兼容 [::1]:8080），端口 1-65535
  function isValidLocalAddr(value) {
    if (!value) return false;
    const index = value.lastIndexOf(':');
    if (index <= 0 || index === value.length - 1) return false;
    const port = Number(value.slice(index + 1));
    return Number.isInteger(port) && port >= 1 && port <= 65535;
  }

  // ===================== 9. 概览 =====================
  VIEWS.overview = {
    pollMs: POLL_MS.overview,
    async enter() {
      await loadOverview(false);
    },
    async refresh() {
      await loadOverview(true);
    }
  };

  async function loadOverview(silent) {
    const pendingRequest = apiRequest('/devices?status=pending', { silent: silent }).catch(() => null);
    const results = await Promise.all([
      apiRequest('/overview', { silent: silent }),
      pendingRequest
    ]);
    const overview = results[0];
    const pending = results[1];
    state.overview = overview;
    if (pending && Array.isArray(pending.devices)) state.pendingDevices = pending.devices;
    if (overview && overview.server && overview.server.version) {
      const version = String(overview.server.version);
      if (version !== state.version) {
        state.version = version;
        renderVersion();
      }
    }
    updateNavCount();
    if (state.route !== 'overview') return;
    const signature = overviewSignature();
    if (state.signatures.overview !== signature) {
      state.signatures.overview = signature;
      renderOverviewPage();
    } else {
      updateOverviewLive();
    }
  }

  function renderOverviewPage() {
    const page = $('#page');
    if (!page || state.route !== 'overview' || !state.overview) return;
    const overview = state.overview;
    const server = overview.server || {};
    const counts = overview.counts || {};
    const traffic = overview.traffic || {};
    const pendingDevices = state.pendingDevices || [];
    const nodes = [pageHead('概览', '服务运行状态与设备接入概况')];

    if (state.online === false) {
      nodes.push(el('div', { class: 'alert alert-danger' },
        el('div', { class: 'alert-main' },
          el('div', { class: 'alert-title', text: '服务端不可达' }),
          el('div', { class: 'alert-body', text: '管理接口请求失败，页面数据可能不是最新的；恢复连接后会自动刷新。' }))));
    }

    if (pendingDevices.length) nodes.push(renderPendingAlert(pendingDevices));

    nodes.push(el('div', { class: 'stats' },
      statCard('设备总数', num(counts.devices_total), '在线 ' + num(counts.devices_online) + ' · 待审批 ' + num(counts.devices_pending)),
      statCard('在线设备', num(counts.devices_online), '共 ' + num(counts.devices_total) + ' 台已登记'),
      statCard('待审批', num(counts.devices_pending), pendingDevices.length ? '需要管理员处理' : '暂无待处理设备', counts.devices_pending ? 'warn' : null),
      statCard('映射规则', num(counts.rules_total), '启用 ' + num(counts.rules_active) + ' 条'),
      statCard('当前连接', num(traffic.conns_current), '累计 ' + num(traffic.conns_total) + ' 次', null, 'conns'),
      statCard('累计流量', fmtBytes(toNumber(traffic.bytes_in) + toNumber(traffic.bytes_out)),
        '入站 ' + fmtBytes(traffic.bytes_in) + ' · 出站 ' + fmtBytes(traffic.bytes_out), null, 'traffic')));

    nodes.push(card('服务信息', el('div', { class: 'kv-grid' },
      kvItem('版本', monoText(server.version ? 'v' + server.version : '—')),
      kvItem('运行时长', el('span', { text: fmtDuration(server.uptime_sec) }), 'uptime'),
      kvItem('控制地址', monoText(server.control_addr)),
      kvItem('管理地址', monoText(server.web_addr)),
      kvItem('公网主机', monoText(server.public_host)),
      kvItem('映射端口段', monoText(formatPortRange(server.port_range))),
      kvItem('HTTP 监听', monoText(server.http_addr || '未启用')))));

    withScrollPreserved(() => page.replaceChildren(...nodes));
    collectOverviewRefs(page);
  }

  // 概览的数据签名：只包含"需要整页重绘"的字段（流量/运行时长走原地更新）
  function overviewSignature() {
    const overview = state.overview || {};
    const server = overview.server || {};
    const counts = overview.counts || {};
    const pending = (state.pendingDevices || []).map((d) => [d.id, d.name, d.key_code, d.os, d.arch]);
    return JSON.stringify([server.version, server.control_addr, server.web_addr, server.public_host,
      server.port_range, server.http_addr, counts, pending]);
  }

  function collectOverviewRefs(page) {
    const refs = {};
    Array.prototype.forEach.call(page.querySelectorAll('[data-stat]'), (node) => {
      refs[node.dataset.stat] = {
        value: node.querySelector('.stat-value'),
        sub: node.querySelector('.stat-sub')
      };
    });
    refs.uptime = page.querySelector('[data-kv="uptime"] .kv-value');
    state.overviewRefs = refs;
  }

  function setTextIfChanged(node, text) {
    if (node && node.textContent !== text) node.textContent = text;
  }

  // 每轮轮询只更新数字，不重建 DOM（避免闪烁和滚动跳动）
  function updateOverviewLive() {
    const refs = state.overviewRefs;
    const overview = state.overview;
    if (!refs || !overview || state.route !== 'overview') return;
    const server = overview.server || {};
    const traffic = overview.traffic || {};
    setTextIfChanged(refs.uptime, fmtDuration(server.uptime_sec));
    if (refs.traffic) {
      setTextIfChanged(refs.traffic.value, fmtBytes(toNumber(traffic.bytes_in) + toNumber(traffic.bytes_out)));
      setTextIfChanged(refs.traffic.sub, '入站 ' + fmtBytes(traffic.bytes_in) + ' · 出站 ' + fmtBytes(traffic.bytes_out));
    }
    if (refs.conns) {
      setTextIfChanged(refs.conns.value, num(traffic.conns_current));
      setTextIfChanged(refs.conns.sub, '累计 ' + num(traffic.conns_total) + ' 次');
    }
  }

  function renderPendingAlert(devices) {
    const sample = devices.slice(0, 3).map((device) => {
      return [displayName(device), osArch(device), device.key_code || ''].filter(Boolean).join(' · ');
    });
    const more = devices.length > sample.length ? '等共 ' + devices.length + ' 台' : '';
    return el('div', { class: 'alert alert-warn' },
      el('div', { class: 'alert-main' },
        el('div', { class: 'alert-title', text: '有 ' + devices.length + ' 台设备等待审批' }),
        el('div', { class: 'alert-body', text: '新设备首次连接后会登记在这里；批准后才能建立端口映射。' }),
        el('div', { class: 'pending-list' },
          sample.map((text) => el('span', { class: 'pending-item', text: '· ' + text })),
          more ? el('span', { class: 'pending-item muted', text: '· ' + more }) : null)),
      el('div', { class: 'alert-actions' },
        el('button', {
          class: 'btn btn-primary btn-sm',
          type: 'button',
          on: {
            click: () => {
              state.deviceFilter = 'pending';
              location.hash = '#/devices';
            }
          }
        }, '去审批')));
  }

  // ===================== 10. 设备 =====================
  VIEWS.devices = {
    pollMs: POLL_MS.devices,
    async enter(page) {
      mountDevices(page);
      await loadDevices(false);
    },
    async refresh() {
      await loadDevices(true);
    }
  };

  function mountDevices(page) {
    const filters = el('div', { class: 'segmented', role: 'group', 'aria-label': '按状态筛选设备' });
    DEVICE_FILTERS.forEach((filter) => {
      const button = el('button', {
        type: 'button',
        text: filter.label,
        'aria-pressed': String(state.deviceFilter === filter.value)
      });
      button.addEventListener('click', () => {
        if (state.deviceFilter === filter.value) return;
        state.deviceFilter = filter.value;
        Array.prototype.forEach.call(filters.children, (child) => {
          child.setAttribute('aria-pressed', String(child === button));
        });
        const refs = state.refs.devices;
        if (refs && refs.host) refs.host.replaceChildren(pageLoading('加载设备…'));
        loadDevices(false).catch(() => {});
      });
      filters.appendChild(button);
    });

    const host = el('div', { class: 'table-host' }, pageLoading('加载设备…'));
    const countLabel = el('span', { class: 'small muted' });
    page.replaceChildren(
      pageHead('设备', '审批新接入设备，管理设备名称、封禁状态与机器绑定。'),
      el('div', { class: 'toolbar' }, filters, el('span', { class: 'toolbar-spacer' }), countLabel),
      el('section', { class: 'card' }, el('div', { class: 'card-body card-body-flush' }, host)));
    state.refs.devices = { filters: filters, host: host, countLabel: countLabel };
  }

  async function loadDevices(silent) {
    const query = state.deviceFilter === 'all' ? '' : '?status=' + encodeURIComponent(state.deviceFilter);
    const data = await apiRequest('/devices' + query, { silent: silent });
    state.devices = data && Array.isArray(data.devices) ? data.devices : [];
    if (state.route !== 'devices') return;
    const signature = state.deviceFilter + '|' + deviceSignature(state.devices);
    if (state.signatures.devices !== signature) {
      state.signatures.devices = signature;
      renderDevicesTable();
    } else {
      updateDeviceLiveCells();
    }
  }

  function renderDevicesTable() {
    const refs = state.refs.devices;
    if (!refs || state.route !== 'devices') return;
    refs.countLabel.textContent = state.devices.length ? '共 ' + state.devices.length + ' 台' : '';
    if (!state.devices.length) {
      refs.host.replaceChildren(devicesEmptyState());
      return;
    }
    withScrollPreserved(() => refs.host.replaceChildren(dataTable(DEVICE_COLUMNS, state.devices, (tr, device) => {
      tr.dataset.deviceId = device.id;
    })));
  }

  function devicesEmptyState() {
    if (state.deviceFilter === 'pending') {
      return emptyState('没有待审批设备', '新设备首次连接服务端后会出现在这里，等待你批准。');
    }
    if (state.deviceFilter === 'active') {
      return emptyState('还没有已批准设备', '先到「待审批」中批准设备，才能配置映射规则。');
    }
    if (state.deviceFilter === 'blocked') {
      return emptyState('没有已封禁设备', '被临时禁用的设备会显示在这里，可随时解封。');
    }
    return emptyState('还没有设备接入', '在你需要穿透的主机上运行 queqiao，它会自动向服务端登记并出现在这里。');
  }

  const DEVICE_COLUMNS = [
    {
      label: '状态',
      render: (device) => {
        const meta = DEVICE_STATUS[device.status] || { label: device.status || '未知', tone: 'neutral' };
        return el('div', { class: 'row-inline' }, statusDot(!!device.online), badge(meta.label, meta.tone));
      }
    },
    { label: '设备', render: renderDeviceNameCell },
    {
      label: '系统',
      render: (device) => el('div', { class: 'stack-tight' },
        el('span', { class: 'small', text: osArch(device) }),
        device.agent_version ? el('span', { class: 'small muted mono', text: 'Agent ' + device.agent_version }) : null)
    },
    {
      label: '密钥码',
      render: (device) => el('code', { class: 'code', text: device.key_code || '—', title: device.key_code || '密钥码' })
    },
    { label: '最后心跳', render: renderHeartbeatCell },
    {
      label: '来源 IP',
      render: (device) => el('span', { class: 'mono small', text: device.last_ip || '—', title: '最近一次连接来源 IP' })
    },
    {
      label: '规则',
      render: (device) => el('span', {
        class: 'small',
        text: String(device.rule_count === null || device.rule_count === undefined ? 0 : device.rule_count)
      })
    },
    { label: '操作', render: renderDeviceActions }
  ];

  function renderDeviceNameCell(device) {
    const name = displayName(device);
    const children = [el('div', { class: 'strong truncate', text: name, title: name })];
    if (device.hostname && device.hostname !== name) {
      children.push(el('div', { class: 'small muted truncate', text: device.hostname, title: device.hostname }));
    }
    if (device.note) {
      children.push(el('div', { class: 'small muted truncate', text: device.note, title: device.note }));
    }
    if (device.fingerprint_conflict) {
      children.push(el('div', {
        class: 'small text-danger',
        text: '⚠ 指纹冲突',
        title: '机器指纹与已绑定记录不一致，可能更换了硬件或系统；确认是本机操作后可「重绑定」。'
      }));
    }
    return el('div', { class: 'stack-tight' }, children);
  }

  function heartbeatInfo(device) {
    const online = !!device.online;
    const ms = parseTime(device.last_seen_at);
    let label;
    if (typeof device.last_seen_sec === 'number' && Number.isFinite(device.last_seen_sec)) {
      label = fmtSecAgo(device.last_seen_sec);
    } else if (ms !== null) {
      label = relTimeFromMs(ms);
    } else {
      label = '从未连接';
    }
    if (!online && ms !== null) label = '离线 · ' + label;
    return {
      text: label,
      className: 'small ' + (online ? 'text-success' : 'muted'),
      datetime: device.last_seen_at || '',
      title: ms !== null ? '最近心跳：' + fmtDateTime(ms) : '暂无心跳记录'
    };
  }

  function renderHeartbeatCell(device) {
    const info = heartbeatInfo(device);
    return el('time', {
      class: info.className,
      datetime: info.datetime,
      title: info.title,
      'data-role': 'heartbeat',
      text: info.text
    });
  }

  // 设备签名：排除 last_seen_sec 这类每轮都会变化的字段
  function deviceSignature(devices) {
    return JSON.stringify(devices.map((d) => [d.id, d.status, d.name, d.online, d.key_code, d.hostname,
      d.os, d.arch, d.agent_version, d.last_ip, d.rule_count, d.fingerprint_conflict, d.note, d.last_error, d.approved_at]));
  }

  function updateDeviceLiveCells() {
    const refs = state.refs.devices;
    if (!refs || !refs.host) return;
    const byId = new Map(state.devices.map((d) => [d.id, d]));
    Array.prototype.forEach.call(refs.host.querySelectorAll('tr[data-device-id]'), (row) => {
      const device = byId.get(row.dataset.deviceId);
      if (!device) return;
      const cell = row.querySelector('[data-role="heartbeat"]');
      if (!cell) return;
      const info = heartbeatInfo(device);
      cell.textContent = info.text;
      cell.className = info.className;
      cell.setAttribute('datetime', info.datetime);
      cell.title = info.title;
    });
  }

  function renderDeviceActions(device) {
    const actions = [];
    if (device.status === 'pending') {
      actions.push(actionButton('批准', 'btn-primary btn-sm', () => approveDevice(device)));
      actions.push(actionButton('拒绝', 'btn-ghost-danger btn-sm', () => rejectDevice(device)));
    } else {
      actions.push(actionButton('改名/备注', 'btn-ghost btn-sm', () => editDevice(device)));
      if (device.status === 'blocked') {
        actions.push(actionButton('解封', 'btn-ghost btn-sm', () => setDeviceBlocked(device, false)));
      } else {
        actions.push(actionButton('封禁', 'btn-ghost-danger btn-sm', () => setDeviceBlocked(device, true)));
      }
      actions.push(actionButton('重绑定', 'btn-ghost btn-sm', () => rebindDevice(device)));
      actions.push(actionButton('删除', 'btn-ghost-danger btn-sm', () => deleteDevice(device)));
    }
    return el('div', { class: 'actions' }, actions);
  }

  // ---- 设备操作 ----
  async function approveDevice(device) {
    const values = await askForm({
      title: '批准设备接入',
      description: (device.hostname || device.key_code || device.id) + ' 将从「待审批」变为「已批准」，可立即下发配置。',
      fields: [
        {
          name: 'name',
          label: '显示名',
          value: device.name || '',
          placeholder: device.hostname || '设备名称',
          allowEmpty: true,
          hint: '留空则使用主机名'
        }
      ],
      submitText: '批准'
    });
    if (!values) return;
    const payload = values.name ? { name: values.name } : {};
    await apiRequest('/devices/' + encodeURIComponent(device.id) + '/approve', { method: 'POST', body: payload });
    toast('已批准设备「' + displayName(device) + '」', 'success');
    await afterDataChange();
  }

  async function rejectDevice(device) {
    const confirmed = await askConfirm({
      title: '拒绝设备',
      message: '确定拒绝「' + displayName(device) + '」吗？',
      details: ['拒绝后会删除该设备的登记记录。', '设备需要重新登记，并再次等待审批。'],
      confirmText: '拒绝并删除',
      danger: true
    });
    if (!confirmed) return;
    await apiRequest('/devices/' + encodeURIComponent(device.id) + '/reject', { method: 'POST' });
    toast('已拒绝并删除设备', 'success');
    await afterDataChange();
  }

  async function editDevice(device) {
    const values = await askForm({
      title: '编辑设备「' + displayName(device) + '」',
      fields: [
        { name: 'name', label: '显示名', value: device.name || '', placeholder: device.hostname || '设备名称', allowEmpty: true },
        {
          name: 'note',
          label: '备注',
          value: device.note || '',
          multiline: true,
          rows: 3,
          placeholder: '例如：家里的 NAS、公司测试机',
          allowEmpty: true
        }
      ],
      submitText: '保存'
    });
    if (!values) return;
    await apiRequest('/devices/' + encodeURIComponent(device.id), {
      method: 'PATCH',
      body: { name: values.name, note: values.note }
    });
    toast('设备信息已保存', 'success');
    await afterDataChange();
  }

  async function setDeviceBlocked(device, blocked) {
    const confirmed = await askConfirm({
      title: blocked ? '封禁设备' : '解封设备',
      message: blocked
        ? '封禁后该设备会立即断开连接，其映射规则停止监听。'
        : '解封后该设备可以重新连接，已有映射规则会恢复。',
      details: blocked ? ['已建立的连接会被立即踢下线。'] : [],
      confirmText: blocked ? '封禁' : '解封',
      danger: blocked
    });
    if (!confirmed) return;
    await apiRequest('/devices/' + encodeURIComponent(device.id) + '/block', {
      method: 'POST',
      body: { blocked: blocked }
    });
    toast(blocked ? '已封禁设备「' + displayName(device) + '」' : '已解封设备「' + displayName(device) + '」', 'success');
    await afterDataChange();
  }

  async function rebindDevice(device) {
    const confirmed = await askConfirm({
      title: '重绑定机器',
      message: '将清空「' + displayName(device) + '」的机器指纹，并把设备置回待审批状态。',
      details: [
        '设备下次连接时需要重新登记，并由管理员再次批准。',
        '已有映射规则不会删除，但在设备重新批准前无法使用。',
        '请确认是本人在操作，避免误把正常设备踢下线。'
      ],
      confirmText: '确认重绑定',
      danger: true
    });
    if (!confirmed) return;
    await apiRequest('/devices/' + encodeURIComponent(device.id) + '/rebind', { method: 'POST' });
    toast('已重置机器绑定，等待设备重新登记', 'success');
    await afterDataChange();
  }

  async function deleteDevice(device) {
    const confirmed = await askConfirm({
      title: '删除设备',
      message: '确定删除「' + displayName(device) + '」吗？此操作不可恢复。',
      details: [
        '该设备的全部映射规则也会一并删除。',
        '设备若在线会立即断开连接，之后需重新登记并审批。'
      ],
      confirmText: '删除设备',
      danger: true
    });
    if (!confirmed) return;
    await apiRequest('/devices/' + encodeURIComponent(device.id), { method: 'DELETE' });
    toast('已删除设备', 'success');
    await afterDataChange();
  }

  // ===================== 11. 映射规则 =====================
  VIEWS.rules = {
    pollMs: POLL_MS.rules,
    async enter(page) {
      mountRules(page);
      await loadRules(false);
    },
    async refresh() {
      await loadRules(true);
    }
  };

  function mountRules(page) {
    const deviceFilter = el('select', { class: 'input', 'aria-label': '按设备筛选映射' },
      el('option', { value: '', text: '全部设备' }));
    const listHost = el('div', { class: 'table-host' }, pageLoading('加载映射…'));
    const countLabel = el('span', { class: 'small muted' });
    const formCtl = buildRuleForm();

    deviceFilter.addEventListener('change', () => {
      if (state.ruleDeviceFilter === deviceFilter.value) return;
      state.ruleDeviceFilter = deviceFilter.value;
      listHost.replaceChildren(pageLoading('加载映射…'));
      loadRules(false).catch(() => {});
    });

    page.replaceChildren(
      pageHead('映射', '为已批准设备配置 TCP 端口或 HTTP 域名映射，修改后服务端立即生效。'),
      card('新建映射', formCtl.form),
      el('div', { class: 'toolbar' },
        el('span', { class: 'toolbar-label', text: '按设备筛选' }),
        deviceFilter,
        el('span', { class: 'toolbar-spacer' }),
        countLabel),
      el('section', { class: 'card' }, el('div', { class: 'card-body card-body-flush' }, listHost)));

    state.refs.rules = {
      deviceFilter: deviceFilter,
      listHost: listHost,
      countLabel: countLabel,
      formCtl: formCtl
    };
  }

  function buildRuleForm() {
    const device = el('select', { class: 'input', id: 'rule-device', 'aria-label': '目标设备' },
      el('option', { value: '', text: '加载设备中…' }));
    const name = el('input', { class: 'input', type: 'text', id: 'rule-name', placeholder: '例如 web', autocomplete: 'off', maxlength: '64' });
    const proto = el('select', { class: 'input', id: 'rule-proto', 'aria-label': '协议' },
      el('option', { value: 'tcp', text: 'TCP（公网端口）' }),
      el('option', { value: 'http', text: 'HTTP（域名）' }));
    const localAddr = el('input', {
      class: 'input',
      type: 'text',
      id: 'rule-local',
      value: '127.0.0.1:',
      placeholder: '127.0.0.1:8080',
      autocomplete: 'off',
      spellcheck: 'false'
    });
    const port = el('input', { class: 'input', type: 'number', id: 'rule-port', min: '1', max: '65535', placeholder: '如 39001', inputmode: 'numeric' });
    const portHint = el('span', { class: 'field-hint', text: '需在设置中的端口段内，且未被占用' });
    const checkButton = el('button', { class: 'btn btn-ghost btn-sm', type: 'button', text: '检测端口' });
    checkButton.addEventListener('click', () => checkPortAvailability(port, portHint));
    const domain = el('input', {
      class: 'input',
      type: 'text',
      id: 'rule-domain',
      placeholder: 'app.example.com',
      autocomplete: 'off',
      spellcheck: 'false'
    });
    const enabled = el('input', { class: 'checkbox', type: 'checkbox', id: 'rule-enabled' });
    enabled.checked = true;

    const portField = el('div', { class: 'field' },
      el('span', { class: 'field-label', text: '公网端口' }),
      el('div', { class: 'input-row' }, port, checkButton),
      portHint);
    const domainField = el('div', { class: 'field' },
      el('span', { class: 'field-label', text: '域名' }),
      domain,
      el('span', { class: 'field-hint', text: '把域名解析到公网主机后即可通过该域名访问' }));
    domainField.hidden = true;

    const syncProto = () => {
      const isTcp = proto.value === 'tcp';
      portField.hidden = !isTcp;
      domainField.hidden = isTcp;
    };
    proto.addEventListener('change', syncProto);
    syncProto();

    const submit = el('button', { class: 'btn btn-primary', type: 'submit' }, '创建映射');
    const form = el('form', { class: 'form', id: 'rule-create-form', novalidate: true },
      el('div', { class: 'form-row' },
        el('label', { class: 'field' }, el('span', { class: 'field-label', text: '设备' }), device),
        el('label', { class: 'field' }, el('span', { class: 'field-label', text: '名称' }), name),
        el('label', { class: 'field' }, el('span', { class: 'field-label', text: '协议' }), proto)),
      el('div', { class: 'form-row' },
        el('label', { class: 'field' },
          el('span', { class: 'field-label', text: '本地地址' }),
          localAddr,
          el('span', { class: 'field-hint', text: 'Agent 所在主机上的地址，如 127.0.0.1:8080' })),
        portField,
        domainField),
      el('label', { class: 'field field-inline' },
        enabled,
        el('span', { class: 'field-label', text: '创建后立即启用' })),
      el('div', { class: 'form-actions' }, submit));

    form.addEventListener('submit', (event) => {
      event.preventDefault();
      createRule({ device, name, proto, localAddr, port, portHint, domain, enabled, submit }).catch(() => {});
    });

    return {
      form: form,
      device: device,
      name: name,
      proto: proto,
      localAddr: localAddr,
      port: port,
      portHint: portHint,
      domain: domain,
      enabled: enabled,
      submit: submit
    };
  }

  async function createRule(ctl) {
    if (ctl.submit.disabled) return;
    const deviceId = ctl.device.value;
    if (!deviceId) {
      toast('请选择目标设备', 'warn');
      ctl.device.focus();
      return;
    }
    const ruleName = ctl.name.value.trim();
    if (!ruleName) {
      toast('请填写映射名称', 'warn');
      ctl.name.focus();
      return;
    }
    const localAddr = ctl.localAddr.value.trim();
    if (!isValidLocalAddr(localAddr)) {
      toast('本地地址格式应为 host:port，例如 127.0.0.1:8080', 'error');
      ctl.localAddr.focus();
      return;
    }
    const isTcp = ctl.proto.value === 'tcp';
    const payload = {
      device_id: deviceId,
      name: ruleName,
      proto: ctl.proto.value,
      local_addr: localAddr,
      enabled: !!ctl.enabled.checked
    };
    if (isTcp) {
      const remotePort = Number(ctl.port.value);
      if (!Number.isInteger(remotePort) || remotePort < 1 || remotePort > 65535) {
        toast('请填写 1-65535 之间的公网端口', 'error');
        ctl.port.focus();
        return;
      }
      payload.remote_port = remotePort;
      payload.domain = '';
    } else {
      const domainValue = ctl.domain.value.trim();
      if (!domainValue) {
        toast('请填写 HTTP 映射域名', 'error');
        ctl.domain.focus();
        return;
      }
      payload.remote_port = 0;
      payload.domain = domainValue;
    }

    ctl.submit.disabled = true;
    try {
      await apiRequest('/rules', { method: 'POST', body: payload });
      toast('映射已创建', 'success');
      ctl.name.value = '';
      ctl.port.value = '';
      ctl.domain.value = '';
      setHint(ctl.portHint, '需在设置中的端口段内，且未被占用', null);
      await afterDataChange();
    } catch (error) {
      // apiRequest 已 toast，保留表单内容便于修正
    } finally {
      if (ctl.submit.isConnected) ctl.submit.disabled = false;
    }
  }

  async function checkPortAvailability(input, hint) {
    const port = Number(input.value);
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      setHint(hint, '请输入 1-65535 之间的端口', 'warn');
      input.focus();
      return;
    }
    setHint(hint, '检测中…', null);
    try {
      const result = await apiRequest('/rules/check-port?port=' + encodeURIComponent(port), { silent: false });
      if (result && result.available) setHint(hint, '端口 ' + port + ' 可用', 'ok');
      else setHint(hint, '端口 ' + port + ' 不可用' + (result && result.reason ? '：' + result.reason : ''), 'error');
    } catch (error) {
      setHint(hint, (error && error.message) || '检测失败', 'error');
    }
  }

  async function loadRules(silent) {
    const query = state.ruleDeviceFilter ? '?device_id=' + encodeURIComponent(state.ruleDeviceFilter) : '';
    const results = await Promise.all([
      apiRequest('/rules' + query, { silent: silent }),
      apiRequest('/devices?status=all', { silent: silent })
    ]);
    const rulesData = results[0];
    const devicesData = results[1];
    state.rules = rulesData && Array.isArray(rulesData.rules) ? rulesData.rules : [];
    state.allDevices = devicesData && Array.isArray(devicesData.devices) ? devicesData.devices : [];
    if (state.route !== 'rules') return;
    syncRuleDeviceFilter();
    syncRuleFormDevices();
    const signature = ruleSignature(state.rules);
    if (state.signatures.rules !== signature) {
      state.signatures.rules = signature;
      renderRulesTable();
    } else {
      updateRuleLiveCells();
    }
  }

  function syncRuleDeviceFilter() {
    const refs = state.refs.rules;
    if (!refs) return;
    const select = refs.deviceFilter;
    const previous = state.ruleDeviceFilter;
    const signature = state.allDevices.map((device) => {
      return device.id + ':' + displayName(device) + ':' + device.status;
    }).join('|');

    if (select.dataset.signature !== signature) {
      select.dataset.signature = signature;
      const options = [el('option', { value: '', text: '全部设备' })];
      for (const device of state.allDevices) {
        const suffix = device.status === 'active' ? '' : '（' + deviceStatusLabel(device.status) + '）';
        options.push(el('option', { value: device.id, text: displayName(device) + suffix }));
      }
      select.replaceChildren(...options);
    }

    const exists = state.allDevices.some((device) => device.id === previous);
    if (previous && !exists) {
      state.ruleDeviceFilter = '';
      select.value = '';
      loadRules(true).catch(() => {});
      return;
    }
    select.value = previous;
  }

  function syncRuleFormDevices() {
    const refs = state.refs.rules;
    if (!refs) return;
    const select = refs.formCtl.device;
    const previous = select.value;
    const available = state.allDevices.filter((device) => device.status === 'active');
    const signature = available.map((device) => device.id + ':' + displayName(device)).join('|');
    if (select.dataset.signature !== signature) {
      select.dataset.signature = signature;
      const options = [el('option', {
        value: '',
        text: available.length ? '请选择设备' : '暂无已批准设备'
      })];
      for (const device of available) {
        options.push(el('option', { value: device.id, text: displayName(device) + ' · ' + device.id }));
      }
      select.replaceChildren(...options);
    }
    const keep = available.some((device) => device.id === previous)
      ? previous
      : (available.length === 1 ? available[0].id : '');
    select.value = keep;
  }

  function deviceStatusLabel(status) {
    const meta = DEVICE_STATUS[status];
    return meta ? meta.label : (status || '未知');
  }

  function renderRulesTable() {
    const refs = state.refs.rules;
    if (!refs || state.route !== 'rules') return;
    refs.countLabel.textContent = state.rules.length ? '共 ' + state.rules.length + ' 条' : '';
    if (!state.rules.length) {
      if (!state.allDevices.length) {
        refs.listHost.replaceChildren(emptyState('还没有设备接入',
          '请先到「设备」页批准设备，然后才能创建映射。',
          el('button', {
            class: 'btn btn-primary btn-sm',
            type: 'button',
            on: { click: () => { location.hash = '#/devices'; } }
          }, '前往设备页')));
      } else if (!state.allDevices.some((device) => device.status === 'active')) {
        refs.listHost.replaceChildren(emptyState('没有已批准的设备',
          '设备批准后才能创建映射，请先到「设备」页处理待审批设备。'));
      } else if (state.ruleDeviceFilter) {
        refs.listHost.replaceChildren(emptyState('该设备还没有映射规则',
          '在上方「新建映射」中选择这台设备，创建第一条规则。'));
      } else {
        refs.listHost.replaceChildren(emptyState('还没有映射规则',
          '在上方「新建映射」表单中选择设备并填写地址，即可创建第一条规则。'));
      }
      return;
    }
    withScrollPreserved(() => refs.listHost.replaceChildren(dataTable(RULE_COLUMNS, state.rules, (tr, rule) => {
      tr.dataset.ruleId = rule.id;
    })));
  }

  const RULE_COLUMNS = [
    { label: '状态', render: renderRuleStateCell },
    { label: '名称', render: renderRuleNameCell },
    {
      label: '本地地址',
      render: (rule) => el('code', { class: 'code', text: rule.local_addr || '—', title: rule.local_addr || '' })
    },
    { label: '公网地址', render: renderRulePublicCell },
    { label: '流量', render: renderRuleTrafficCell },
    { label: '启用', render: renderRuleEnabledCell },
    { label: '操作', render: renderRuleActions }
  ];

  function renderRuleStateCell(rule) {
    const meta = RULE_STATES[rule.state] || { label: rule.state || '未知', tone: 'neutral' };
    return el('div', { class: 'stack-tight' },
      badge(meta.label, meta.tone),
      rule.state === 'error' && rule.error
        ? el('span', { class: 'small text-danger', text: rule.error, title: rule.error })
        : null);
  }

  function renderRuleNameCell(rule) {
    return el('div', { class: 'stack-tight' },
      el('div', { class: 'strong truncate', text: rule.name || '未命名', title: rule.name || '' }),
      el('div', { class: 'small muted', text: (rule.device_name || '未知设备') + ' · ' + String(rule.proto || '').toUpperCase() }));
  }

  function renderRulePublicCell(rule) {
    const mainText = rule.public_addr || rule.domain || '—';
    return el('div', { class: 'stack-tight' },
      el('span', { class: 'mono small', text: mainText, title: mainText }),
      rule.domain && rule.public_addr
        ? el('span', { class: 'small muted', text: rule.domain, title: rule.domain })
        : null);
  }

  function renderRuleTrafficCell(rule) {
    return el('div', { class: 'stack-tight' },
      el('span', { class: 'small', 'data-role': 'traffic-in', text: '入站 ' + fmtBytes(rule.bytes_in) }),
      el('span', {
        class: 'small muted',
        'data-role': 'traffic-out',
        text: '出站 ' + fmtBytes(rule.bytes_out) + ' · 连接 ' + num(rule.conns_current)
      }));
  }

  // 规则签名：排除流量/连接数，流量走单元格原地更新
  function ruleSignature(rules) {
    const devices = state.allDevices.map((d) => [d.id, d.status, d.name]);
    return JSON.stringify([state.ruleDeviceFilter, devices, rules.map((r) => [r.id, r.name, r.proto,
      r.local_addr, r.remote_port, r.domain, r.enabled, r.state, r.error, r.device_id, r.device_name, r.public_addr])]);
  }

  function updateRuleLiveCells() {
    const refs = state.refs.rules;
    if (!refs || !refs.listHost) return;
    const byId = new Map(state.rules.map((r) => [r.id, r]));
    Array.prototype.forEach.call(refs.listHost.querySelectorAll('tr[data-rule-id]'), (row) => {
      const rule = byId.get(row.dataset.ruleId);
      if (!rule) return;
      const inCell = row.querySelector('[data-role="traffic-in"]');
      const outCell = row.querySelector('[data-role="traffic-out"]');
      if (inCell) inCell.textContent = '入站 ' + fmtBytes(rule.bytes_in);
      if (outCell) outCell.textContent = '出站 ' + fmtBytes(rule.bytes_out) + ' · 连接 ' + num(rule.conns_current);
    });
  }

  function renderRuleEnabledCell(rule) {
    const input = el('input', {
      type: 'checkbox',
      checked: !!rule.enabled,
      'aria-label': (rule.enabled ? '停用' : '启用') + '映射 ' + (rule.name || '')
    });
    const wrapper = el('label', { class: 'switch', title: rule.enabled ? '点击停用' : '点击启用' },
      input,
      el('span', { class: 'switch-track' }, el('span', { class: 'switch-thumb' })));
    input.addEventListener('change', async () => {
      if (input.disabled) return;
      input.disabled = true;
      try {
        await apiRequest('/rules/' + encodeURIComponent(rule.id), {
          method: 'PATCH',
          body: { enabled: !!input.checked }
        });
        toast(input.checked ? '已启用映射' : '已停用映射', 'success');
        await afterDataChange();
      } catch (error) {
        input.checked = !input.checked;
      } finally {
        if (input.isConnected) input.disabled = false;
      }
    });
    return wrapper;
  }

  function renderRuleActions(rule) {
    return el('div', { class: 'actions' },
      actionButton('编辑', 'btn-ghost btn-sm', () => editRule(rule)),
      actionButton('删除', 'btn-ghost-danger btn-sm', () => deleteRule(rule)));
  }

  async function editRule(rule) {
    const fields = [
      { name: 'name', label: '名称', value: rule.name || '', maxlength: 64 },
      {
        name: 'local_addr',
        label: '本地地址',
        value: rule.local_addr || '',
        hint: 'host:port，例如 127.0.0.1:8080'
      }
    ];
    if (rule.proto === 'http') {
      fields.push({ name: 'domain', label: '域名', value: rule.domain || '' });
    } else {
      fields.push({
        name: 'remote_port',
        label: '公网端口（TCP）',
        type: 'number',
        value: rule.remote_port === null || rule.remote_port === undefined ? '' : String(rule.remote_port),
        hint: '1-65535，且需在端口段内'
      });
    }
    fields.push({ name: 'enabled', label: '启用', type: 'checkbox', value: !!rule.enabled });

    const values = await askForm({
      title: '编辑映射',
      description: (rule.device_name || '未知设备') + ' · ' + String(rule.proto || '').toUpperCase(),
      fields: fields,
      submitText: '保存修改',
      validate: (input) => {
        if (!isValidLocalAddr(input.local_addr)) return '本地地址格式应为 host:port，例如 127.0.0.1:8080';
        if (rule.proto === 'http') {
          if (!input.domain) return '域名不能为空';
        } else {
          const port = Number(input.remote_port);
          if (!Number.isInteger(port) || port < 1 || port > 65535) return '公网端口需为 1-65535 的整数';
        }
        return null;
      }
    });
    if (!values) return;

    const payload = {
      name: values.name,
      local_addr: values.local_addr,
      enabled: !!values.enabled
    };
    if (rule.proto === 'http') payload.domain = values.domain;
    else payload.remote_port = Number(values.remote_port);

    await apiRequest('/rules/' + encodeURIComponent(rule.id), { method: 'PATCH', body: payload });
    toast('映射已更新', 'success');
    await afterDataChange();
  }

  async function deleteRule(rule) {
    const confirmed = await askConfirm({
      title: '删除映射',
      message: '确定删除映射「' + (rule.name || '未命名') + '」吗？',
      details: ['删除后公网监听立即停止，无法恢复。'],
      confirmText: '删除',
      danger: true
    });
    if (!confirmed) return;
    await apiRequest('/rules/' + encodeURIComponent(rule.id), { method: 'DELETE' });
    toast('映射已删除', 'success');
    await afterDataChange();
  }

  // ===================== 12. 日志 =====================
  VIEWS.logs = {
    pollMs: POLL_MS.logs,
    async enter(page) {
      mountLogs(page);
      await loadLogs(false);
    },
    async refresh() {
      await loadLogs(true);
    }
  };

  function mountLogs(page) {
    const listHost = el('div', { class: 'table-host' }, pageLoading('加载日志…'));
    const countLabel = el('span', { class: 'small muted' });
    const tabs = el('div', { class: 'segmented', role: 'group', 'aria-label': '日志类型' });
    ['audit', 'agent'].forEach((kind) => {
      const button = el('button', {
        type: 'button',
        text: LOG_KINDS[kind],
        'aria-pressed': String(state.logKind === kind)
      });
      button.addEventListener('click', () => {
        if (state.logKind === kind) return;
        state.logKind = kind;
        Array.prototype.forEach.call(tabs.children, (child) => {
          child.setAttribute('aria-pressed', String(child === button));
        });
        listHost.replaceChildren(pageLoading('加载日志…'));
        loadLogs(false).catch(() => {});
      });
      tabs.appendChild(button);
    });

    page.replaceChildren(
      pageHead('日志', '审计日志记录管理员操作；Agent 事件包含客户端上送与服务端连接事件。'),
      el('div', { class: 'toolbar' }, tabs, el('span', { class: 'toolbar-spacer' }), countLabel),
      el('section', { class: 'card' }, el('div', { class: 'card-body card-body-flush' }, listHost)));
    state.refs.logs = { listHost: listHost, countLabel: countLabel };
  }

  async function loadLogs(silent) {
    const data = await apiRequest('/logs?kind=' + encodeURIComponent(state.logKind) + '&limit=' + LOG_LIMIT, { silent: silent });
    const logs = data && Array.isArray(data.logs) ? data.logs.slice() : [];
    // 契约要求倒序；服务端已是倒序时这里是无操作
    logs.sort((left, right) => timeValue(right.at) - timeValue(left.at));
    state.logs = logs.slice(0, LOG_LIMIT);
    if (state.route !== 'logs') return;
    const signature = state.logKind + '|' + JSON.stringify(state.logs);
    if (state.signatures.logs === signature) return;
    state.signatures.logs = signature;
    renderLogsTable();
  }

  function renderLogsTable() {
    const refs = state.refs.logs;
    if (!refs || state.route !== 'logs') return;
    refs.countLabel.textContent = state.logs.length ? '最近 ' + state.logs.length + ' 条（最多 ' + LOG_LIMIT + ' 条）' : '';
    if (!state.logs.length) {
      refs.listHost.replaceChildren(state.logKind === 'audit'
        ? emptyState('暂无审计日志', '管理员登录、审批设备、修改映射与设置等操作会记录在这里。')
        : emptyState('暂无 Agent 事件', 'Agent 连接、断开、心跳异常等事件会出现在这里。'));
      return;
    }
    withScrollPreserved(() => refs.listHost.replaceChildren(dataTable(LOG_COLUMNS, state.logs)));
  }

  const LOG_COLUMNS = [
    { label: '时间', render: (log) => timeCell(log.at) },
    {
      label: '级别',
      render: (log) => {
        const meta = LOG_LEVELS[log.level] || { label: log.level || '—', tone: 'neutral' };
        return badge(meta.label, meta.tone);
      }
    },
    {
      label: '动作',
      render: (log) => el('span', { class: 'mono small', text: log.action || '—', title: log.action || '' })
    },
    {
      label: '对象',
      render: (log) => el('span', { class: 'mono small', text: log.target || '—', title: log.target || '' })
    },
    {
      label: '详情',
      render: (log) => el('span', { class: 'small log-detail', text: log.detail || '—', title: log.detail || '' })
    },
    {
      label: '来源',
      render: (log) => el('div', { class: 'stack-tight' },
        el('span', { class: 'small', text: log.actor || '—' }),
        el('span', { class: 'small muted mono', text: log.ip || '—' }))
    }
  ];

  // ===================== 13. 设置 =====================
  VIEWS.settings = {
    pollMs: 0,
    async enter() {
      await loadSettings();
    }
  };

  async function loadSettings() {
    const settings = await apiRequest('/settings', { silent: false });
    state.settings = settings;
    if (state.route === 'settings') renderSettingsPage();
  }

  function renderSettingsPage() {
    const page = $('#page');
    const settings = state.settings;
    if (!page || state.route !== 'settings' || !settings) return;
    const portRange = Array.isArray(settings.port_range) ? settings.port_range : [39000, 40000];
    const refs = {};
    state.refs.settings = refs;

    refs.publicHost = el('input', {
      class: 'input',
      type: 'text',
      value: settings.public_host || '',
      placeholder: '39.107.230.175 或 queqiao.example.com',
      autocomplete: 'off',
      spellcheck: 'false'
    });
    refs.portStart = el('input', {
      class: 'input',
      type: 'number',
      min: '1024',
      max: '65535',
      value: String(portRange[0] === null || portRange[0] === undefined ? '' : portRange[0]),
      inputmode: 'numeric'
    });
    refs.portEnd = el('input', {
      class: 'input',
      type: 'number',
      min: '1024',
      max: '65535',
      value: String(portRange[1] === null || portRange[1] === undefined ? '' : portRange[1]),
      inputmode: 'numeric'
    });
    refs.httpAddr = el('input', {
      class: 'input',
      type: 'text',
      value: settings.http_addr || '',
      placeholder: '留空则不启用 HTTP 映射，如 :39080',
      autocomplete: 'off',
      spellcheck: 'false'
    });
    refs.autoApprove = el('input', { class: 'checkbox', type: 'checkbox' });
    refs.autoApprove.checked = !!settings.device_auto_approve;
    refs.maxDevices = el('input', {
      class: 'input',
      type: 'number',
      min: '1',
      value: String(settings.max_devices === null || settings.max_devices === undefined ? '' : settings.max_devices),
      inputmode: 'numeric'
    });
    refs.maxRules = el('input', {
      class: 'input',
      type: 'number',
      min: '1',
      value: String(settings.max_rules_per_device === null || settings.max_rules_per_device === undefined ? '' : settings.max_rules_per_device),
      inputmode: 'numeric'
    });
    refs.sessionTtl = el('input', {
      class: 'input',
      type: 'number',
      min: '60',
      value: String(settings.admin_session_ttl_sec === null || settings.admin_session_ttl_sec === undefined ? '' : settings.admin_session_ttl_sec),
      inputmode: 'numeric'
    });
    refs.traffic = el('input', { class: 'checkbox', type: 'checkbox' });
    refs.traffic.checked = !!settings.traffic_stats_enabled;

    const saveButton = el('button', { class: 'btn btn-primary', type: 'submit' }, '保存设置');
    const settingsForm = el('form', { class: 'form', id: 'settings-form', novalidate: true },
      el('div', { class: 'form-row' },
        labeledField('公网主机', refs.publicHost, 'Agent 连接与映射地址中使用的公网主机名或 IP'),
        labeledField('HTTP 监听地址', refs.httpAddr, '例如 :39080；留空不启用 HTTP 域名映射')),
      el('div', { class: 'form-row' },
        labeledField('端口段起始', refs.portStart, '1024-65535'),
        labeledField('端口段结束', refs.portEnd, '不小于起始端口')),
      el('div', { class: 'form-row' },
        labeledField('最大设备数', refs.maxDevices),
        labeledField('每设备最大规则数', refs.maxRules),
        labeledField('管理会话有效期（秒）', refs.sessionTtl, '默认 43200 秒（12 小时）')),
      el('label', { class: 'field field-inline' },
        refs.autoApprove,
        el('span', { class: 'field-label', text: '自动批准新设备（不推荐）' })),
      el('label', { class: 'field field-inline' },
        refs.traffic,
        el('span', { class: 'field-label', text: '启用流量统计' })),
      el('div', { class: 'form-actions' },
        saveButton,
        el('span', { class: 'field-hint', text: '端口段只影响新建映射；修改 HTTP 监听会重建服务端监听。' })));
    settingsForm.addEventListener('submit', (event) => {
      event.preventDefault();
      saveSettings(saveButton).catch(() => {});
    });

    refs.oldPassword = el('input', { class: 'input', type: 'password', autocomplete: 'current-password' });
    refs.newPassword = el('input', { class: 'input', type: 'password', autocomplete: 'new-password', minlength: '12' });
    refs.confirmPassword = el('input', { class: 'input', type: 'password', autocomplete: 'new-password', minlength: '12' });
    const passwordButton = el('button', { class: 'btn btn-primary', type: 'submit' }, '修改密码');
    const passwordForm = el('form', { class: 'form', id: 'password-form', novalidate: true },
      el('div', { class: 'form-row' },
        labeledField('当前密码', refs.oldPassword),
        labeledField('新密码', refs.newPassword, '至少 12 个字符'),
        labeledField('确认新密码', refs.confirmPassword)),
      el('div', { class: 'form-actions' },
        passwordButton,
        el('span', { class: 'field-hint', text: '修改后其它已登录会话会立即失效。' })));
    passwordForm.addEventListener('submit', (event) => {
      event.preventDefault();
      changePassword(passwordButton).catch(() => {});
    });

    page.replaceChildren(
      pageHead('设置', '服务端监听、端口段、设备与安全策略。'),
      card('服务设置', settingsForm, {
        extra: state.mustChange ? badge('正在使用初始密码', 'warn') : null
      }),
      card('修改密码', passwordForm));
  }

  async function saveSettings(button) {
    const refs = state.refs.settings;
    if (!refs) return;
    const publicHost = refs.publicHost.value.trim();
    if (!publicHost) {
      toast('请填写公网主机', 'warn');
      refs.publicHost.focus();
      return;
    }
    const start = Number(refs.portStart.value);
    const end = Number(refs.portEnd.value);
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1024 || end > 65535 || start > end) {
      toast('端口段需在 1024-65535 之间，且起始端口不能大于结束端口', 'error');
      return;
    }
    const maxDevices = Number(refs.maxDevices.value);
    if (!Number.isInteger(maxDevices) || maxDevices < 1) {
      toast('最大设备数需为正整数', 'error');
      return;
    }
    const maxRules = Number(refs.maxRules.value);
    if (!Number.isInteger(maxRules) || maxRules < 1) {
      toast('每设备最大规则数需为正整数', 'error');
      return;
    }
    const sessionTtl = Number(refs.sessionTtl.value);
    if (!Number.isInteger(sessionTtl) || sessionTtl < 60) {
      toast('管理会话有效期需为不小于 60 的整数（秒）', 'error');
      return;
    }

    const payload = {
      public_host: publicHost,
      port_range: [start, end],
      http_addr: refs.httpAddr.value.trim(),
      device_auto_approve: !!refs.autoApprove.checked,
      max_devices: maxDevices,
      max_rules_per_device: maxRules,
      admin_session_ttl_sec: sessionTtl,
      traffic_stats_enabled: !!refs.traffic.checked
    };

    button.disabled = true;
    try {
      const updated = await apiRequest('/settings', { method: 'PUT', body: payload });
      state.settings = updated || payload;
      toast('设置已保存', 'success');
    } catch (error) {
      // apiRequest 已 toast
    } finally {
      if (button.isConnected) button.disabled = false;
    }
  }

  async function changePassword(button) {
    const refs = state.refs.settings;
    if (!refs) return;
    const oldPassword = refs.oldPassword.value;
    const newPassword = refs.newPassword.value;
    const confirmPassword = refs.confirmPassword.value;
    if (!oldPassword) {
      toast('请输入当前密码', 'warn');
      refs.oldPassword.focus();
      return;
    }
    if (Array.from(newPassword).length < 12) {
      toast('新密码至少需要 12 个字符', 'error');
      refs.newPassword.focus();
      return;
    }
    if (newPassword !== confirmPassword) {
      toast('两次输入的新密码不一致', 'error');
      refs.confirmPassword.focus();
      return;
    }
    if (newPassword === oldPassword) {
      toast('新密码不能与当前密码相同', 'warn');
      return;
    }

    button.disabled = true;
    try {
      await apiRequest('/auth/password', {
        method: 'POST',
        body: { old_password: oldPassword, new_password: newPassword }
      });
      toast('密码已修改', 'success');
      state.mustChange = false;
      renderSettingsPage();
    } catch (error) {
      // apiRequest 已 toast
    } finally {
      if (button.isConnected) button.disabled = false;
    }
  }

  // ===================== 14. 启动 =====================
  function bindChrome() {
    const loginForm = $('#login-form');
    if (loginForm) loginForm.addEventListener('submit', handleLogin);
    const logoutButton = $('#logout-btn');
    if (logoutButton) logoutButton.addEventListener('click', handleLogout);
    const autoRefreshButton = $('#auto-refresh-btn');
    if (autoRefreshButton) autoRefreshButton.addEventListener('click', toggleAutoRefresh);
    const refreshNowButton = $('#refresh-now-btn');
    if (refreshNowButton) refreshNowButton.addEventListener('click', manualRefresh);
    updateAutoRefreshUI();
    window.addEventListener('hashchange', () => {
      if (state.me) navigate(currentRoute());
    });
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible' && state.me) refreshCurrentView();
    });
    const usernameInput = $('#login-username');
    const passwordInput = $('#login-password');
    if (usernameInput) usernameInput.addEventListener('input', hideLoginError);
    if (passwordInput) passwordInput.addEventListener('input', hideLoginError);
  }

  function init() {
    try {
      if (window.localStorage.getItem('queqiao.autoRefresh') === '0') state.autoRefresh = false;
    } catch (error) {
      // localStorage 不可用时保持默认开启
    }
    bindChrome();
    renderConnBadge();
    loadHealth();
    checkSession();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();

