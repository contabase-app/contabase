const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const script = fs.readFileSync(path.join(__dirname, 'admin-updates.js'), 'utf8');

function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}

function setup(options = {}) {
  const listeners = new Map();
  const state = {
    clipboard: [],
    appended: 0,
    removed: 0,
    execCalls: 0,
    execCommand: '',
    listenerAdds: 0,
    lastArea: null,
  };
  const status = { textContent: '' };
  const command = { textContent: options.command || 'sudo contabase-update' };
  const scope = {
    querySelector(selector) {
      return selector === '[data-copy-status]' ? status : null;
    },
  };
  const button = {
    attrs: { 'data-copy-target': 'adminUpdatesPrimaryCommand' },
    parentElement: scope,
    getAttribute(name) {
      return this.attrs[name] || null;
    },
    closest(selector) {
      if (selector === '[data-copy-target]') return this;
      if (selector === '[data-copy-scope]') return scope;
      return null;
    },
  };
  const document = {
    addEventListener(name, fn) {
      state.listenerAdds += 1;
      listeners.set(name, fn);
    },
    getElementById(id) {
      return id === 'adminUpdatesPrimaryCommand' && !options.missingTarget ? command : null;
    },
    createElement(tag) {
      return {
        tagName: tag,
        value: '',
        style: {},
        setAttribute() {},
        focus() {},
        select() {},
      };
    },
    body: {
      appendChild(element) {
        state.appended += 1;
        state.lastArea = element;
      },
      removeChild() {
        state.removed += 1;
      },
    },
    execCommand(commandName) {
      state.execCalls += 1;
      state.execCommand = commandName;
      return options.execCommand !== false;
    },
  };
  const window = { __contabaseAdminUpdates: false };
  const navigator = {};
  if (options.clipboard !== false) {
    navigator.clipboard = {
      writeText(text) {
        state.clipboard.push(text);
        if (options.clipboardReject) return Promise.reject(new Error('denied'));
        return Promise.resolve();
      },
    };
  }
  const context = { window, document, navigator, Promise, Error, console };
  vm.runInNewContext(script, context);
  return { context, listeners, state, status, button };
}

function click(runtime) {
  runtime.listeners.get('click')({
    target: runtime.button,
    preventDefault() {},
  });
}

test('copies the static command with navigator.clipboard', async () => {
  const runtime = setup({ command: 'sudo\n  contabase-update' });
  click(runtime);
  await flush();
  assert.deepEqual(runtime.state.clipboard, ['sudo contabase-update']);
  assert.equal(runtime.status.textContent, 'Comando copiado.');
  assert.equal(runtime.state.appended, 0);
});

test('falls back to execCommand on HTTP/LAN without clipboard', async () => {
  const runtime = setup({ clipboard: false, command: 'contabase-update' });
  click(runtime);
  await flush();
  assert.equal(runtime.state.execCalls, 1);
  assert.equal(runtime.state.execCommand, 'copy');
  assert.equal(runtime.state.lastArea.value, 'contabase-update');
  assert.equal(runtime.state.appended, 1);
  assert.equal(runtime.state.removed, 1);
  assert.equal(runtime.status.textContent, 'Comando copiado.');
});

test('preserves the quoted healthcheck argument through both copy paths', async () => {
  for (const command of [
    "curl 'http://127.0.0.1:8080/health'",
    "curl 'http://contabase.lan:8080/health'",
    "curl 'https://financeiro.example.com/health'",
    "curl 'http://[fd00::50]:8080/health'",
  ]) {
    for (const clipboard of [true, false]) {
      const runtime = setup({ command, clipboard });
      click(runtime);
      await flush();
      const copied = clipboard ? runtime.state.clipboard[0] : runtime.state.lastArea.value;
      assert.equal(copied, command);
      assert.equal(runtime.status.textContent, 'Comando copiado.');
    }
  }
});

test('falls back when clipboard permission is denied', async () => {
  const runtime = setup({ clipboardReject: true, command: 'sudo contabase-update' });
  click(runtime);
  await flush();
  assert.equal(runtime.state.execCalls, 1);
  assert.equal(runtime.status.textContent, 'Comando copiado.');
});

test('reports a safe failure without throwing', async () => {
  const runtime = setup({ clipboard: false, execCommand: false });
  click(runtime);
  await flush();
  assert.equal(runtime.status.textContent, 'Não foi possível copiar. Selecione o comando manualmente.');
});

test('ignores clicks without a valid command target', async () => {
  const runtime = setup({ missingTarget: true });
  click(runtime);
  await flush();
  assert.equal(runtime.state.clipboard.length, 0);
  assert.equal(runtime.status.textContent, '');
});

test('installs one delegated listener and survives HTMX swaps', async () => {
  const runtime = setup({ command: 'contabase-update' });
  vm.runInNewContext(script, runtime.context);
  assert.equal(runtime.state.listenerAdds, 1);

  click(runtime);
  await flush();
  assert.deepEqual(runtime.state.clipboard, ['contabase-update']);

  click(runtime);
  await flush();
  assert.deepEqual(runtime.state.clipboard, ['contabase-update', 'contabase-update']);
});
