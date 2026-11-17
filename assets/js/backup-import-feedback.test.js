const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, 'backup-import-feedback.js'), 'utf8');

const MAX_BODY_BYTES = 50 * 1024 * 1024;
const MSG_INDETERMINADO = 'Não foi possível confirmar o resultado da importação.';

function classList(owner) {
  return {
    add(...names) {
      names.forEach((name) => owner.classes.add(name));
    },
    remove(...names) {
      names.forEach((name) => owner.classes.delete(name));
    },
    contains(name) {
      return owner.classes.has(name);
    }
  };
}

function matchesSelector(element, selector) {
  const match = String(selector || '').trim().match(/^([a-zA-Z0-9_-]+)?((?:\[[^\]]+\])*)$/);
  if (!match) return false;
  if (match[1] && element.tagName.toLowerCase() !== match[1].toLowerCase()) return false;
  const attrs = match[2].match(/\[[^\]]+\]/g) || [];
  return attrs.every((attribute) => {
    const parsed = attribute.match(/^\[([^=\]]+)(?:="([^"]*)")?\]$/);
    if (!parsed) return false;
    const actual = element.getAttribute(parsed[1]);
    if (actual === null) return false;
    return parsed[2] === undefined || actual === parsed[2];
  });
}

function findFirst(root, selector) {
  for (const child of root.children) {
    if (matchesSelector(child, selector)) return child;
    const found = findFirst(child, selector);
    if (found) return found;
  }
  return null;
}

function makeElement(tagName, attributes = {}, children = []) {
  const listeners = new Map();
  const element = {
    _listeners: listeners,
    tagName: tagName.toUpperCase(),
    children: [],
    parentElement: null,
    attributes: new Map(Object.entries(attributes).map(([key, value]) => [key, String(value)])),
    classes: new Set(),
    files: null,
    textContent: '',
    focusCount: 0,
    addEventListener(name, handler) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(handler);
    },
    append(child) {
      child.parentElement = element;
      element.children.push(child);
      return child;
    },
    getAttribute(name) {
      return element.attributes.has(name) ? element.attributes.get(name) : null;
    },
    setAttribute(name, value) {
      element.attributes.set(name, String(value));
    },
    removeAttribute(name) {
      element.attributes.delete(name);
    },
    matches(selector) {
      return matchesSelector(element, selector);
    },
    closest(selector) {
      let current = element;
      while (current) {
        if (current.matches && current.matches(selector)) return current;
        current = current.parentElement;
      }
      return null;
    },
    querySelector(selector) {
      return findFirst(element, selector);
    },
    querySelectorAll(selector) {
      const found = [];
      const visit = (node) => {
        for (const child of node.children) {
          if (matchesSelector(child, selector)) found.push(child);
          visit(child);
        }
      };
      visit(element);
      return found;
    },
    focus() {
      element.focusCount += 1;
    }
  };
  element.classList = classList(element);
  children.forEach((child) => element.append(child));
  return element;
}

function makeForm() {
  const form = makeElement('form', { 'data-backup-import-form': '' });
  const input = makeElement('input', { type: 'file', name: 'backup_file' });
  input.files = [];
  const state = makeElement('p', { 'data-backup-import-state': '', role: 'status' });
  state.classList.add('hidden');
  const status = makeElement('div', { 'data-backup-import-status': '', tabindex: '-1' });
  status.classList.add('hidden');
  const icon = makeElement('i', { 'data-backup-import-status-icon': '', 'data-lucide': 'circle-alert' });
  const text = makeElement('span', { 'data-backup-import-status-text': '' });
  const dismiss = makeElement('button', { 'data-backup-import-dismiss': '', type: 'button' });
  const submit = makeElement('button', { type: 'submit' });
  status.append(icon);
  status.append(text);
  status.append(dismiss);
  form.append(input);
  form.append(state);
  form.append(status);
  form.append(submit);
  return { form, input, state, status, icon, text, dismiss, submit };
}

function event(name, target, detail = {}, extra = {}) {
  return Object.assign({
    type: name,
    target,
    detail,
    defaultPrevented: false,
    propagationStopped: false,
    immediatePropagationStopped: false,
    preventDefault() {
      this.defaultPrevented = true;
    },
    stopPropagation() {
      this.propagationStopped = true;
    },
    stopImmediatePropagation() {
      this.immediatePropagationStopped = true;
    }
  }, extra);
}

function dispatch(listeners, name, value) {
  for (const handler of listeners.get(name) || []) handler(value);
}

function makeRuntime() {
  const elements = makeForm();
  const settingsPayload = makeElement('div', { id: 'settings-dynamic-payload' });
  const body = makeElement('body');
  const documentListeners = new Map();
  const timers = new Map();
  let nextTimer = 1;
  let refreshIcons = 0;

  const document = {
    body,
    addEventListener(name, handler) {
      if (!documentListeners.has(name)) documentListeners.set(name, []);
      documentListeners.get(name).push(handler);
    },
    querySelector(selector) {
      return selector === 'form[data-backup-import-form]' ? elements.form : null;
    },
    querySelectorAll() {
      return [];
    }
  };
  const windowListeners = new Map();
  const window = {
    addEventListener(name, handler) {
      if (!windowListeners.has(name)) windowListeners.set(name, []);
      windowListeners.get(name).push(handler);
    },
    refreshIcons() {
      refreshIcons += 1;
    }
  };

  vm.runInNewContext(script, {
    window,
    document,
    console,
    TextDecoder,
    Uint8Array,
    setTimeout(callback, delay) {
      const id = nextTimer;
      nextTimer += 1;
      timers.set(id, { callback, delay });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    }
  });

  function htmx(name, options = {}) {
    const detail = Object.assign({}, options.detail || {});
    const target = options.target || detail.elt || elements.form;
    if (!Object.prototype.hasOwnProperty.call(detail, 'elt')) detail.elt = target;
    const value = event(name, target, detail);
    dispatch(body._listeners, name, value);
    return value;
  }

  function change(files) {
    elements.input.files = files;
    const value = event('change', elements.input, {});
    dispatch(body._listeners, 'change', value);
    return value;
  }

  function submit() {
    const value = event('submit', elements.form, {});
    dispatch(body._listeners, 'submit', value);
    return value;
  }

  function click(target) {
    const value = event('click', target, {});
    dispatch(body._listeners, 'click', value);
    return value;
  }

  function mostrarAlerta(target, detail) {
    const value = event('mostrarAlerta', target, detail);
    dispatch(documentListeners, 'mostrarAlerta', value);
    if (!value.propagationStopped) dispatch(body._listeners, 'mostrarAlerta', value);
    return value;
  }

  function pageshow(persisted) {
    const value = event('pageshow', window, {}, { persisted });
    dispatch(windowListeners, 'pageshow', value);
    return value;
  }

  function xhr(status, headers = {}) {
    const normalized = new Map(
      Object.entries(headers).map(([name, value]) => [name.toLowerCase(), String(value)])
    );
    const fake = {
      status,
      aborted: 0,
      getResponseHeader(name) {
        return normalized.has(name.toLowerCase()) ? normalized.get(name.toLowerCase()) : null;
      },
      abort() {
        fake.aborted += 1;
        htmx('htmx:sendAbort', { target: elements.form, detail: { elt: elements.form, xhr: fake } });
      }
    };
    return fake;
  }

  function runTimeout() {
    const pending = [...timers.values()];
    timers.clear();
    pending.forEach((timer) => timer.callback());
  }

  function selectValidFile() {
    change([{ name: 'backup.db', size: 1024 }]);
    const config = htmx('htmx:configRequest', {
      target: elements.form,
      detail: { elt: elements.form, xhr: xhr(0) }
    });
    assert.equal(config.defaultPrevented, false);
  }

  function beforeRequest() {
    const fake = xhr(0);
    const value = htmx('htmx:beforeRequest', {
      target: elements.form,
      detail: { elt: elements.form, xhr: fake }
    });
    return { event: value, xhr: fake };
  }

  return {
    body,
    document,
    elements,
    settingsPayload,
    window,
    change,
    submit,
    click,
    htmx,
    mostrarAlerta,
    pageshow,
    xhr,
    runTimeout,
    selectValidFile,
    beforeRequest,
    get refreshIcons() {
      return refreshIcons;
    },
    get pendingTimers() {
      return timers.size;
    }
  };
}

function latin1Header(value) {
  return Array.from(Buffer.from(value, 'utf8'))
    .map((byte) => String.fromCharCode(byte))
    .join('');
}

test('seleção de arquivo válido e inválido mantém estado e alerta coerentes', () => {
  const runtime = makeRuntime();

  runtime.change([{ name: 'backup.db', size: 1024 }]);
  assert.equal(runtime.elements.state.textContent, 'Arquivo selecionado: backup.db (0.0 MiB)');
  assert.equal(runtime.elements.state.classList.contains('hidden'), false);
  assert.equal(runtime.elements.status.classList.contains('hidden'), true);

  runtime.change([{ name: 'backup.zip', size: 1024 }]);
  assert.match(runtime.elements.text.textContent, /extensão \.db/);
  assert.equal(runtime.elements.status.getAttribute('role'), 'alert');
  assert.equal(runtime.elements.status.classList.contains('hidden'), false);
  assert.equal(runtime.elements.state.textContent, '');
  assert.equal(runtime.elements.state.classList.contains('hidden'), true);

  runtime.change([]);
  assert.match(runtime.elements.text.textContent, /Selecione um arquivo de backup/);
});

test('limite de 50 MiB é aplicado na seleção e no envio', () => {
  const runtime = makeRuntime();

  runtime.change([{ name: 'grande.db', size: MAX_BODY_BYTES }]);
  assert.match(runtime.elements.text.textContent, /excede o limite de 50 MiB/);
  const blocked = runtime.htmx('htmx:configRequest', {
    target: runtime.elements.form,
    detail: { elt: runtime.elements.form }
  });
  assert.equal(blocked.defaultPrevented, true);

  runtime.change([{ name: 'limite.db', size: MAX_BODY_BYTES - 1024 }]);
  assert.equal(runtime.elements.status.classList.contains('hidden'), true);
  const allowed = runtime.htmx('htmx:configRequest', {
    target: runtime.elements.form,
    detail: { elt: runtime.elements.form }
  });
  assert.equal(allowed.defaultPrevented, false);
});

test('bloqueia envio duplicado no submit e no configRequest', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();
  assert.match(runtime.elements.state.textContent, /Enviando backup e aguardando o servidor/);

  const duplicateSubmit = runtime.submit();
  assert.equal(duplicateSubmit.defaultPrevented, true);
  assert.equal(duplicateSubmit.immediatePropagationStopped, true);
  assert.match(runtime.elements.text.textContent, /já está em andamento/);

  const duplicateRequest = runtime.htmx('htmx:configRequest', {
    target: runtime.elements.form,
    detail: { elt: runtime.elements.form }
  });
  assert.equal(duplicateRequest.defaultPrevented, true);
  assert.match(runtime.elements.text.textContent, /já está em andamento/);
});

test('afterSettle de outra navegação não libera importação em andamento', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();

  runtime.htmx('htmx:afterSettle', {
    target: runtime.settingsPayload,
    detail: {
      elt: runtime.settingsPayload,
      pathInfo: { requestPath: '/configuracoes/backups' }
    }
  });
  assert.equal(runtime.submit().defaultPrevented, true);

  runtime.htmx('htmx:afterSettle', {
    target: runtime.settingsPayload,
    detail: {
      elt: runtime.settingsPayload,
      pathInfo: { requestPath: '/admin/backups/importar' }
    }
  });
  const next = runtime.htmx('htmx:configRequest', {
    target: runtime.elements.form,
    detail: { elt: runtime.elements.form }
  });
  assert.equal(next.defaultPrevented, false);
});

test('estado de envio, sucesso, erro e cancelamento', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();
  assert.match(runtime.elements.state.textContent, /Enviando backup/);
  assert.equal(runtime.elements.status.classList.contains('hidden'), true);

  // htmx 2.0.4 trata HX-Redirect antes do beforeSwap: o término chega só pelo afterRequest.
  runtime.beforeRequest();
  assert.equal(runtime.pendingTimers, 1);
  runtime.htmx('htmx:afterRequest', {
    target: runtime.elements.form,
    detail: {
      elt: runtime.elements.form,
      xhr: runtime.xhr(200, { 'HX-Redirect': '/login' })
    }
  });
  assert.equal(runtime.pendingTimers, 0);
  assert.equal(runtime.elements.status.classList.contains('hidden'), true);
  assert.doesNotMatch(runtime.elements.state.textContent, /conclu|sucesso/i);
  assert.equal(
    runtime.htmx('htmx:configRequest', {
      target: runtime.elements.form,
      detail: { elt: runtime.elements.form }
    }).defaultPrevented,
    false
  );

  const errorRuntime = makeRuntime();
  errorRuntime.selectValidFile();
  errorRuntime.htmx('htmx:responseError', {
    target: errorRuntime.elements.form,
    detail: { elt: errorRuntime.elements.form, xhr: errorRuntime.xhr(400) }
  });
  assert.equal(errorRuntime.elements.state.textContent, 'Importação não concluída.');
  assert.match(errorRuntime.elements.text.textContent, /validação/);

  const cancelRuntime = makeRuntime();
  cancelRuntime.selectValidFile();
  cancelRuntime.beforeRequest();
  cancelRuntime.htmx('htmx:sendAbort', {
    target: cancelRuntime.elements.form,
    detail: { elt: cancelRuntime.elements.form }
  });
  assert.match(cancelRuntime.elements.text.textContent, /cancelado antes de completar/);
  assert.match(cancelRuntime.elements.text.textContent, /Não foi possível confirmar o resultado/);
  assert.equal(cancelRuntime.elements.state.textContent, 'Envio cancelado.');
});

test('respostas HTTP usam fallback seguro e tom de alerta', () => {
  const cases = [
    { status: 400, pattern: /validação/ },
    { status: 403, pattern: /permissão/ },
    { status: 409, pattern: /outra operação/ },
    { status: 413, pattern: /50 MiB/ },
    { status: 429, pattern: /Muitas tentativas/ },
    { status: 429, headers: { 'Retry-After': '120' }, pattern: /Aguarde 2 minutos/ },
    { status: 500, pattern: /Erro interno/ },
    { status: 503, pattern: /Erro interno/ }
  ];

  for (const item of cases) {
    const runtime = makeRuntime();
    runtime.selectValidFile();
    runtime.htmx('htmx:responseError', {
      target: runtime.elements.form,
      detail: {
        elt: runtime.elements.form,
        xhr: runtime.xhr(item.status, item.headers || {})
      }
    });
    assert.match(runtime.elements.text.textContent, item.pattern, `HTTP ${item.status}`);
    assert.equal(runtime.elements.status.getAttribute('role'), 'alert');
    assert.equal(runtime.elements.status.classList.contains('cb-flash-danger'), true);
    assert.equal(runtime.elements.state.textContent, 'Importação não concluída.');
  }
});

test('HX-Trigger usa mensagem do servidor e decodifica UTF-8', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();
  const message = 'Sessão expirada; confirme novamente.';
  runtime.htmx('htmx:responseError', {
    target: runtime.elements.form,
    detail: {
      elt: runtime.elements.form,
      xhr: runtime.xhr(400, {
        'HX-Trigger': latin1Header(JSON.stringify({ mostrarAlerta: message }))
      })
    }
  });
  assert.equal(runtime.elements.text.textContent, message);
  assert.equal(runtime.elements.status.getAttribute('role'), 'alert');

  const objectRuntime = makeRuntime();
  objectRuntime.selectValidFile();
  objectRuntime.htmx('htmx:responseError', {
    target: objectRuntime.elements.form,
    detail: {
      elt: objectRuntime.elements.form,
      xhr: objectRuntime.xhr(409, {
        'HX-Trigger': latin1Header(JSON.stringify({ mostrarAlerta: { value: 'Mensagem em objeto.' } }))
      })
    }
  });
  assert.equal(objectRuntime.elements.text.textContent, 'Mensagem em objeto.');

  const fallbackRuntime = makeRuntime();
  fallbackRuntime.selectValidFile();
  fallbackRuntime.htmx('htmx:responseError', {
    target: fallbackRuntime.elements.form,
    detail: {
      elt: fallbackRuntime.elements.form,
      xhr: fallbackRuntime.xhr(400, { 'HX-Trigger': 'não-json' })
    }
  });
  assert.match(fallbackRuntime.elements.text.textContent, /validação/);
});

test('mostrarAlerta do formulário não vaza para o feedback global', () => {
  const runtime = makeRuntime();
  let globalAlerts = 0;
  runtime.body.addEventListener('mostrarAlerta', () => {
    globalAlerts += 1;
  });

  const local = runtime.mostrarAlerta(runtime.elements.text, 'Mensagem local');
  assert.equal(local.propagationStopped, true);
  assert.equal(globalAlerts, 0);

  const outside = makeElement('div');
  const global = runtime.mostrarAlerta(outside, 'Mensagem global');
  assert.equal(global.propagationStopped, false);
  assert.equal(globalAlerts, 1);
});

test('falha de rede e timeout htmx permanecem indeterminados', () => {
  for (const name of ['htmx:sendError', 'htmx:timeout']) {
    const runtime = makeRuntime();
    runtime.selectValidFile();
    runtime.htmx(name, {
      target: runtime.elements.form,
      detail: { elt: runtime.elements.form, xhr: runtime.xhr(0) }
    });
    assert.match(runtime.elements.text.textContent, /Não foi possível confirmar o resultado/);
    assert.doesNotMatch(runtime.elements.text.textContent, /importação cancelada|revertida/i);
    assert.equal(runtime.elements.state.textContent, 'Resultado não confirmado.');
    assert.equal(runtime.elements.status.getAttribute('role'), 'alert');

    const next = runtime.htmx('htmx:configRequest', {
      target: runtime.elements.form,
      detail: { elt: runtime.elements.form }
    });
    assert.equal(next.defaultPrevented, false);
  }
});

test('watchdog próprio aborta e registra timeout como indeterminado', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();
  const request = runtime.beforeRequest();
  assert.equal(runtime.pendingTimers, 1);

  runtime.runTimeout();
  assert.equal(request.xhr.aborted, 1);
  assert.equal(runtime.pendingTimers, 0);
  assert.match(runtime.elements.text.textContent, /Não foi possível confirmar o resultado/);
  assert.match(runtime.elements.text.textContent, /demorou demais/);
  assert.doesNotMatch(runtime.elements.text.textContent, /cancelad|revertid/i);
  assert.equal(runtime.elements.state.textContent, 'Resultado não confirmado.');
});

test('bfcache e histórico do htmx liberam envio sem afirmar cancelamento', () => {
  const bfcache = makeRuntime();
  bfcache.selectValidFile();
  bfcache.beforeRequest();
  bfcache.pageshow(true);
  assert.equal(bfcache.pendingTimers, 0);
  assert.match(bfcache.elements.text.textContent, /página foi restaurada/);
  assert.match(bfcache.elements.text.textContent, /Não foi possível confirmar o resultado/);
  assert.doesNotMatch(bfcache.elements.text.textContent, /cancelad|revertid/i);
  assert.equal(bfcache.elements.state.textContent, 'Resultado não confirmado.');
  assert.equal(
    bfcache.htmx('htmx:configRequest', {
      target: bfcache.elements.form,
      detail: { elt: bfcache.elements.form }
    }).defaultPrevented,
    false
  );

  const history = makeRuntime();
  history.selectValidFile();
  history.beforeRequest();
  history.htmx('htmx:historyRestore', {
    target: history.body,
    detail: { elt: history.body }
  });
  assert.equal(history.pendingTimers, 0);
  assert.match(history.elements.text.textContent, /página foi restaurada/);
  assert.equal(history.elements.state.textContent, 'Resultado não confirmado.');
});

test('pageshow sem persistência não libera envio em andamento', () => {
  const runtime = makeRuntime();
  runtime.selectValidFile();
  runtime.pageshow(false);
  assert.equal(runtime.submit().defaultPrevented, true);
});

test('role é aplicado antes da mensagem e Dispensar oculta o painel', () => {
  const runtime = makeRuntime();
  let roleAtMessage = null;
  let message = '';
  Object.defineProperty(runtime.elements.text, 'textContent', {
    configurable: true,
    get() {
      return message;
    },
    set(value) {
      roleAtMessage = runtime.elements.status.getAttribute('role');
      message = String(value);
    }
  });

  runtime.selectValidFile();
  runtime.htmx('htmx:responseError', {
    target: runtime.elements.form,
    detail: { elt: runtime.elements.form, xhr: runtime.xhr(500) }
  });
  assert.equal(roleAtMessage, 'alert');
  assert.equal(runtime.elements.status.getAttribute('tabindex'), '-1');
  assert.equal(runtime.elements.status.classList.contains('hidden'), false);
  assert.equal(runtime.elements.status.focusCount, 1);
  assert.ok(runtime.refreshIcons >= 1);

  runtime.click(runtime.elements.dismiss);
  assert.equal(runtime.elements.status.classList.contains('hidden'), true);
});
