const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// ADM4 — foco após swap, indicador de rolagem e foco do modal administrativo.
// O teste usa um DOM mínimo e o arquivo real carregado pelo navegador.

function makeFocusable(tag) {
  const attrs = new Map();
  return {
    tag,
    focused: false,
    hasAttribute(name) { return attrs.has(name); },
    setAttribute(name, value) { attrs.set(name, String(value)); },
    getAttribute(name) { return attrs.has(name) ? attrs.get(name) : null; },
    focus() {
      this.focused = true;
      if (this.ownerDocument) this.ownerDocument.activeElement = this;
    },
  };
}

function makeRuntime() {
  const hintClasses = new Set();
  const hint = {
    classList: {
      toggle(name, force) {
        if (force) hintClasses.add(name);
        else hintClasses.delete(name);
      },
      contains(name) { return hintClasses.has(name); },
    },
  };
  const wrap = {
    scrollWidth: 640,
    clientWidth: 320,
    parentElement: {
      querySelector(selector) {
        return selector === '.admin-scroll-hint' ? hint : null;
      },
    },
  };
  const flash = makeFocusable('DIV');
  const heading = makeFocusable('H1');
  const trigger = makeFocusable('BUTTON');
  const cancel = makeFocusable('BUTTON');
  const modalClasses = new Set(['hidden']);
  const modal = {
    classList: {
      add(...names) { names.forEach((name) => modalClasses.add(name)); },
      remove(...names) { names.forEach((name) => modalClasses.delete(name)); },
      contains(name) { return modalClasses.has(name); },
    },
    querySelector(selector) {
      return selector === '[data-onclick="cancelGlobalConfirm"]' ? cancel : null;
    },
  };
  const payload = {
    id: 'settings-dynamic-payload',
    contains(node) { return node === this || node === trigger; },
    querySelector(selector) {
      if (selector === '[role="alert"], [role="status"]') return flash;
      if (selector === 'h1') return heading;
      return null;
    },
  };
  const tableBody = { id: 'auditoria-table-body' };
  const bodyListeners = new Map();
  const windowListeners = new Map();
  let observerCallback = null;
  const context = {
    console,
    Array,
    document: {
      readyState: 'complete',
      activeElement: null,
      body: {
        addEventListener(name, fn) { bodyListeners.set(name, fn); },
      },
      addEventListener() {},
      contains(node) {
        return node === trigger || node === cancel || node === flash || node === heading || node === payload;
      },
      querySelectorAll(selector) {
        return selector === '.admin-table-wrap' ? [wrap] : [];
      },
      getElementById(id) {
        if (id === 'settings-dynamic-payload') return payload;
        if (id === 'global-confirm-modal') return modal;
        return null;
      },
    },
    window: {
      addEventListener(name, fn) { windowListeners.set(name, fn); },
      requestAnimationFrame(fn) { fn(); },
      MutationObserver: class {
        constructor(callback) { observerCallback = callback; }
        observe() {}
        disconnect() {}
      },
    },
  };
  flash.ownerDocument = context.document;
  heading.ownerDocument = context.document;
  trigger.ownerDocument = context.document;
  cancel.ownerDocument = context.document;
  vm.createContext(context);
  const source = fs.readFileSync(require.resolve('./admin-accessibility.js'), 'utf8');
  vm.runInContext(source, context, { filename: 'admin-accessibility.js' });

  return {
    context,
    hint,
    hintClasses,
    wrap,
    flash,
    heading,
    trigger,
    cancel,
    payload,
    tableBody,
    modalClasses,
    fireAfterSwap(target) {
      bodyListeners.get('htmx:afterSwap')({ detail: { target } });
    },
    fireConfirm(elt) {
      bodyListeners.get('htmx:confirm')({ detail: { elt } });
    },
    closeConfirmExternally() {
      modalClasses.add('hidden');
      if (observerCallback) observerCallback();
    },
    fireResize() {
      windowListeners.get('resize')();
    },
  };
}

test('admin-accessibility: hint aparece apenas quando a tabela realmente transborda', () => {
  const runtime = makeRuntime();
  assert.equal(runtime.hintClasses.has('is-visible'), true, 'overflow inicial mostra o hint');

  runtime.wrap.scrollWidth = 320;
  runtime.wrap.clientWidth = 320;
  runtime.fireResize();
  assert.equal(runtime.hintClasses.has('is-visible'), false, 'sem overflow esconde o hint');

  runtime.wrap.scrollWidth = 720;
  runtime.wrap.clientWidth = 660;
  runtime.fireResize();
  assert.equal(runtime.hintClasses.has('is-visible'), true, 'overflow real reaparece');
});

test('admin-accessibility: foco após swap vai para o feedback e não para filtros internos', () => {
  const runtime = makeRuntime();
  runtime.fireAfterSwap(runtime.payload);
  assert.equal(runtime.flash.focused, true, 'feedback recebe foco');
  assert.equal(runtime.flash.getAttribute('tabindex'), '-1');
  assert.equal(runtime.context.document.activeElement, runtime.flash);

  runtime.flash.focused = false;
  runtime.context.document.activeElement = null;
  runtime.fireAfterSwap(runtime.tableBody);
  assert.equal(runtime.flash.focused, false, 'swap de filtros não rouba o foco');
  assert.equal(runtime.context.document.activeElement, null);
});

test('admin-accessibility: fechamento externo do confirm restaura o foco ao acionador', () => {
  const runtime = makeRuntime();
  runtime.modalClasses.delete('hidden');
  runtime.modalClasses.add('flex');
  runtime.context.document.activeElement = runtime.trigger;

  runtime.fireConfirm(runtime.trigger);
  assert.equal(runtime.modalClasses.has('admin-confirm-open'), true, 'modal administrativo é marcado');
  assert.equal(runtime.cancel.focused, true, 'foco inicial vai para Cancelar');

  // O modal-interceptor consome Escape antes do listener de documento. O
  // MutationObserver precisa restaurar o foco mesmo sem keydown observável.
  runtime.closeConfirmExternally();
  assert.equal(runtime.modalClasses.has('admin-confirm-open'), false);
  assert.equal(runtime.trigger.focused, true, 'foco volta ao acionador');
  assert.equal(runtime.context.document.activeElement, runtime.trigger);
});
