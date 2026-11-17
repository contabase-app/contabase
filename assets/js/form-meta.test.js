const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Contratos comportamentais do formulário de Limite:
// - validação client-side de categoria e valor > 0, com foco e aria-invalid;
// - erro do servidor recebe foco uma única vez;
// - selecionar categoria limpa o erro e atualiza nome/ícone;
// - picker devolve foco ao gatilho e recebe foco na busca após swap;
// - o formulário é inicializado no afterSwap do bottom sheet.
const source = fs.readFileSync(path.join(__dirname, 'form-meta.js'), 'utf8');

function classList(initial = []) {
  const values = new Set(initial);
  return {
    add: (...names) => names.forEach((name) => values.add(name)),
    remove: (...names) => names.forEach((name) => values.delete(name)),
    contains: (name) => values.has(name),
    toggle: (name, force) => {
      const enabled = force === undefined ? !values.has(name) : !!force;
      if (enabled) values.add(name);
      else values.delete(name);
      return enabled;
    },
  };
}

function attrs(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getAttribute: (name) => (values.has(name) ? values.get(name) : null),
    setAttribute: (name, value) => values.set(name, String(value)),
    removeAttribute: (name) => values.delete(name),
    hasAttribute: (name) => values.has(name),
  };
}

function control(initial = {}) {
  const api = attrs(initial.attrs);
  let focusCalls = 0;
  return {
    ...api,
    id: initial.id || '',
    value: initial.value || '',
    textContent: initial.textContent || '',
    innerText: initial.innerText || '',
    innerHTML: initial.innerHTML || '',
    hidden: false,
    dataset: initial.dataset || {},
    classList: classList(initial.classes || []),
    listeners: {},
    addEventListener(name, fn) {
      (this.listeners[name] = this.listeners[name] || []).push(fn);
    },
    focusCalls: () => focusCalls,
    focus() {
      focusCalls += 1;
      if (currentDocument) currentDocument.activeElement = this;
    },
  };
}

let currentDocument;

function runtime(options = {}) {
  const categoryId = control({ id: 'categoriaId_limite', value: options.categoryId || '' });
  const amount = control({ id: 'limiteMaxAmount', value: options.amount || '' });
  const categoryName = control({ id: 'categoriaNome_limite', innerText: 'Buscar categoria...' });
  const categoryIcon = control({ id: 'categoriaIcon_limite' });
  const categoryWrap = control({ id: 'categoriaIconWrap_limite' });
  const categoryError = control({ id: 'categoria-error-limite', classes: ['hidden'] });
  const amountError = control({ id: 'limite-amount-error', classes: ['hidden'] });
  const formError = control({ id: 'limite-form-error', classes: ['hidden'] });
  const bottomSheet = control({ id: 'bottom-sheet-container', innerHTML: options.sheetPresent === false ? '' : '<div></div>' });
  const picker = control({ id: 'picker-limite', attrs: { 'aria-invalid': 'false' } });
  picker.closest = (selector) => (selector.indexOf('[data-meta-category-picker]') >= 0 ? picker : null);
  const form = control({ id: 'meta-limite-form' });
  const root = control({ id: 'meta-form-root' });
  const search = control({ id: 'picker-search' });
  const modal = control({ id: 'modalPickerBackdrop' });
  modal.querySelector = (selector) => (selector === '[data-picker-search]' ? search : null);

  const ids = {
    categoriaId_limite: categoryId,
    limiteMaxAmount: amount,
    categoriaNome_limite: categoryName,
    categoriaIcon_limite: categoryIcon,
    categoriaIconWrap_limite: categoryWrap,
    'categoria-error-limite': categoryError,
    'limite-amount-error': amountError,
    'limite-form-error': formError,
    'bottom-sheet-container': bottomSheet,
    'meta-limite-form': form,
  };
  const documentListeners = {};
  const bodyListeners = {};
  const document = {
    readyState: 'complete',
    activeElement: null,
    body: {
      addEventListener(name, fn) {
        (bodyListeners[name] = bodyListeners[name] || []).push(fn);
      },
    },
    addEventListener(name, fn) {
      (documentListeners[name] = documentListeners[name] || []).push(fn);
    },
    getElementById(id) {
      return ids[id] || null;
    },
    querySelector(selector) {
      if (selector === '[data-meta-form-root]') return options.formPresent === false ? null : root;
      if (selector === '#categoriaPicker_limite [data-meta-category-picker]') return picker;
      if (selector === '#limite-form-error:not(.hidden)') return formError.classList.contains('hidden') ? null : formError;
      return null;
    },
  };
  currentDocument = document;

  let pickerCloseCalls = 0;
  const window = {
    applyRuntimeColor() {},
    refreshIcons() {},
    closePickerModal() {
      pickerCloseCalls += 1;
    },
  };
  const context = vm.createContext({ document, window, console });
  vm.runInNewContext(source, context, { filename: 'form-meta.js' });

  return {
    document,
    window,
    ids,
    form,
    picker,
    categoryId,
    amount,
    categoryName,
    categoryIcon,
    categoryWrap,
    categoryError,
    amountError,
    formError,
    bottomSheet,
    search,
    modal,
    documentListeners,
    bodyListeners,
    pickerCloseCalls: () => pickerCloseCalls,
    submitEvent(target = form) {
      let prevented = false;
      let stopped = false;
      const event = {
        target,
        preventDefault() { prevented = true; },
        stopImmediatePropagation() { stopped = true; },
      };
      (documentListeners.submit || []).forEach((fn) => fn(event));
      return { prevented, stopped };
    },
    inputEvent(target) {
      (documentListeners.input || []).forEach((fn) => fn({ target }));
    },
    settle() {
      (bodyListeners['htmx:afterSettle'] || []).forEach((fn) => fn({}));
    },
    afterSwap(target) {
      (bodyListeners['htmx:afterSwap'] || []).forEach((fn) => fn({ detail: { target } }));
    },
  };
}

test('initializes an existing sheet and installs the picker handler', () => {
  const rt = runtime();
  assert.equal(typeof rt.window.selectPickerItem, 'function');
});

test('validation blocks empty category and zero amount with focus and aria-invalid', () => {
  const rt = runtime();

  let result = rt.submitEvent();
  assert.equal(result.prevented, true);
  assert.equal(result.stopped, true);
  assert.equal(rt.categoryError.classList.contains('hidden'), false);
  assert.equal(rt.categoryError.textContent, 'Selecione uma categoria.');
  assert.equal(rt.picker.getAttribute('aria-invalid'), 'true');
  assert.equal(rt.picker.focusCalls(), 1);
  assert.equal(rt.document.activeElement, rt.picker);

  rt.categoryId.value = 'cat-1';
  rt.amount.value = 'R$ 0,00';
  result = rt.submitEvent();
  assert.equal(result.prevented, true);
  assert.equal(rt.amountError.classList.contains('hidden'), false);
  assert.equal(rt.amountError.textContent, 'Informe um valor maior que zero.');
  assert.equal(rt.amount.getAttribute('aria-invalid'), 'true');
  assert.equal(rt.amount.focusCalls(), 1);
  assert.equal(rt.document.activeElement, rt.amount);
});

test('valid category and positive amount allow submit', () => {
  const rt = runtime({ categoryId: 'cat-1', amount: 'R$ 0,01' });
  const result = rt.submitEvent();
  assert.equal(result.prevented, false);
  assert.equal(result.stopped, false);
  assert.equal(rt.categoryError.classList.contains('hidden'), true);
  assert.equal(rt.amountError.classList.contains('hidden'), true);
  assert.equal(rt.picker.getAttribute('aria-invalid'), 'false');
  assert.equal(rt.amount.getAttribute('aria-invalid'), 'false');
});

test('input clears the amount error', () => {
  const rt = runtime({ categoryId: 'cat-1' });
  rt.amount.value = '0';
  rt.submitEvent();
  assert.equal(rt.amountError.classList.contains('hidden'), false);

  rt.amount.value = 'R$ 10,00';
  rt.inputEvent(rt.amount);
  assert.equal(rt.amountError.classList.contains('hidden'), true);
  assert.equal(rt.amount.getAttribute('aria-invalid'), 'false');
});

test('server error receives focus exactly once after htmx settle', () => {
  const rt = runtime();
  rt.formError.classList.remove('hidden');
  rt.formError.textContent = 'erro do servidor';
  rt.settle();
  assert.equal(rt.formError.focusCalls(), 1);
  assert.equal(rt.formError.getAttribute('tabindex'), '-1');
  rt.settle();
  assert.equal(rt.formError.focusCalls(), 1);
});

test('selecting a category clears the error and updates name, icon and focus', () => {
  const rt = runtime();
  rt.submitEvent();
  assert.equal(rt.categoryError.classList.contains('hidden'), false);

  const focusBefore = rt.picker.focusCalls();
  rt.window.selectPickerItem('cat-1', 'Alimentação', 'utensils', '#8b5cf6');
  assert.equal(rt.categoryId.value, 'cat-1');
  assert.equal(rt.categoryError.classList.contains('hidden'), true);
  assert.equal(rt.picker.getAttribute('aria-invalid'), 'false');
  assert.equal(rt.categoryName.innerText, 'Alimentação');
  assert.equal(rt.categoryIcon.getAttribute('data-lucide'), 'utensils');
  assert.equal(rt.picker.focusCalls(), focusBefore + 1);
});

test('picker closes and returns focus to the trigger once', () => {
  const rt = runtime();
  rt.document.emit = undefined;
  const click = (rt.documentListeners.click || [])[0];
  assert.equal(typeof click, 'function');
  click({ target: { closest: () => rt.picker } });

  rt.window.closePickerModal();
  assert.equal(rt.picker.focusCalls(), 1);
  rt.window.closePickerModal();
  assert.equal(rt.picker.focusCalls(), 1);
});

test('picker search receives focus after its HTMX swap', () => {
  const rt = runtime();
  rt.afterSwap(rt.modal);
  assert.equal(rt.search.focusCalls(), 1);
  assert.equal(rt.document.activeElement, rt.search);
});

test('afterSwap initializes the form from the bottom sheet', () => {
  const rt = runtime({ sheetPresent: false, formPresent: false });
  assert.equal(typeof rt.window.selectPickerItem, 'undefined');

  const sheetTarget = { id: 'bottom-sheet-container', querySelector: () => null };
  rt.document.querySelector = (selector) => {
    if (selector === '[data-meta-form-root]') return {};
    if (selector === '#categoriaPicker_limite [data-meta-category-picker]') return rt.picker;
    if (selector === '#limite-form-error:not(.hidden)') return null;
    return null;
  };
  rt.afterSwap(sheetTarget);
  assert.equal(typeof rt.window.selectPickerItem, 'function');
});
