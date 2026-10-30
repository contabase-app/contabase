const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const templatePath = path.join(__dirname, '..', '..', 'templates', 'components', 'form_lancamento.html');
const template = fs.readFileSync(templatePath, 'utf8');

// --- Template contract ----------------------------------------------------
// The financial form is presentation-sensitive but contract-stable: ids,
// names, hx-* attributes and hidden fields must survive every visual change.

test('sheet exposes a named dialog with a close button that cannot submit', () => {
  assert.match(template, /role="dialog"/);
  assert.match(template, /aria-modal="true"/);
  assert.match(template, /aria-labelledby="lancamento-sheet-title"/);
  assert.match(template, /aria-describedby="lancamento-sheet-context"/);
  assert.match(template, /id="lancamento-sheet-title"/);
  assert.match(template, /id="lancamento-sheet-context"/);
  assert.match(
    template,
    /<button type="button"[^>]*data-onclick="closeBottomSheet"[^>]*aria-label="Fechar formulário"/
  );
});

test('every button declares an explicit type', () => {
  const buttons = template.match(/<button\b[^>]*>/g) || [];
  assert.ok(buttons.length > 10);
  buttons.forEach((button) => {
    assert.match(
      button,
      /type="(button|submit)"/,
      `button without explicit type: ${button.slice(0, 160)}`
    );
  });
});

test('creation and edit endpoints keep their original htmx contracts', () => {
  assert.match(template, /hx-post="\/transacoes"/);
  assert.match(template, /hx-put="\/transacoes\/{{\.EditID}}"/);
  assert.match(template, /hx-post="\/transacoes\/{{\.EditID}}\/salvar"/);
  assert.match(template, /hx-target="#tx-{{\.EditID}}"/);
  assert.match(template, /hx-swap="outerHTML"/);
  assert.match(template, /hx-indicator="#htmx-indicator"/);
  assert.match(template, /hx-sync="this:drop"/);
  assert.match(template, /enctype="multipart\/form-data"/);
});

test('hidden fields keep their names and ids', () => {
  const hidden = {
    tipoLancamento: 'tipo',
    origemContaId: 'origem_conta_id',
    origemTipo: 'origem_tipo',
    destinoContaId: 'destino_conta_id',
    escopoEdicao: 'escopo_edicao',
    invoiceAssignmentMode: 'invoice_assignment_mode',
    permitirExcedenteCaixinha: 'permitir_excedente_caixinha',
    csrfToken: 'csrf_token',
    statusPagamentoHidden: 'status_pagamento',
    categoriaId: 'categoria_id',
    categoriaType: 'categoria_type',
    faturaOffset: 'fatura_offset'
  };
  for (const [id, name] of Object.entries(hidden)) {
    const tag = (template.match(new RegExp(`<input[^>]*id="${id}"[^>]*>`)) || [])[0] || '';
    assert.ok(tag, `missing hidden input #${id}`);
    assert.match(tag, new RegExp(`name="${name}"`), `missing name ${name} on #${id}`);
    assert.match(tag, /type="hidden"/, `#${id} must stay hidden`);
  }
  assert.match(template, /name="contact_id"/);
  assert.match(template, /name="return_invoice_id"/);
});

test('visible fields keep their names, ids and money mask', () => {
  assert.match(template, /id="valor"[^>]*name="valor"[^>]*data-money-mask/);
  assert.match(template, /id="descricaoLancamento"[^>]*name="descricao"/);
  assert.match(template, /id="dataLancamento"[^>]*name="data"/);
  assert.match(template, /id="dueDateInput"[^>]*name="due_date"/);
  assert.match(template, /id="parcelada"[^>]*name="parcelada"/);
  assert.match(template, /id="parcelas"[^>]*name="parcelas"/);
  assert.match(template, /id="lancamentoFixo"[^>]*name="lancamento_fixo"/);
  assert.match(template, /id="recorrencia"[^>]*name="recorrencia"/);
  assert.match(template, /id="totalOccurrences"[^>]*name="total_occurrences"/);
  assert.match(template, /name="anotacoes"/);
  assert.match(template, /name="anexo"/);
  assert.match(template, /id="faturaSelect"[^>]*name="invoice_id"/);
});

test('conditional field hooks used by form-lancamento.js are present', () => {
  for (const id of [
    'origemPicker', 'destinoPicker', 'categoriaPicker', 'transferDirectionHint',
    'paymentScheduleNotice', 'faturaSelector', 'parcelasBox', 'recorrenciaBox',
    'totalOccurrencesBox', 'categoryLimitInfo', 'categoryReserveInfo',
    'boxOverdraftWarning', 'detalhesAdicionaisBody'
  ]) {
    assert.match(template, new RegExp(`id="${id}"`), `missing #${id}`);
  }
  assert.match(template, /id="transferDirectionHint"[^>]*hidden/);
  assert.match(template, /id="parcelasBox"[^>]*hidden/);
  assert.match(template, /id="recorrenciaBox"[^>]*hidden/);
  assert.match(template, /id="destinoPicker"[^>]*hidden/);
});

test('form data attributes for initial state and series scope are preserved', () => {
  for (const attr of [
    'data-series-edit', 'data-is-edit', 'data-initial-type',
    'data-initial-origin-id', 'data-initial-date', 'data-initial-status'
  ]) {
    assert.match(template, new RegExp(attr + '='), `missing ${attr}`);
  }
  assert.match(template, /data-form-scroll/);
  assert.match(template, /data-form-footer/);
  assert.match(template, /id="escopoEdicao"/);
  assert.match(template, /id="seriesScopeModal"/);
  assert.match(template, /id="seriesDeleteModal"/);
  assert.match(template, /data-series-delete-open/);
  assert.match(template, /data-series-scope-confirm/);
  assert.match(template, /data-series-delete-confirm/);
});

test('the footer is outside the scrolling body and uses a real submit button', () => {
  const scrollIndex = template.indexOf('data-form-scroll');
  const formEnd = template.indexOf('</form>');
  const footerIndex = template.indexOf('data-form-footer');
  assert.ok(scrollIndex > 0 && formEnd > scrollIndex && footerIndex > formEnd);
  assert.match(template, /id="lancamentoSubmit"[^>]*form="lancamento-form"/);
  assert.match(template, /id="lancamentoSubmit"[\s\S]*?type="submit"|type="submit"[^>]*id="lancamentoSubmit"/);
  assert.match(template, /data-onclick="closeBottomSheet">\s*Cancelar/);
});

test('tabs expose pressed state and the sheet has a single title hook', () => {
  const pressed = template.match(/data-type="[^"]+" aria-pressed="/g) || [];
  assert.equal(pressed.length, 3);
  assert.match(template, /id="lancamento-sheet-title"[^>]*data-sheet-title-mode=/);
});

test('auxiliary financial values opt into the existing privacy mask', () => {
  assert.match(template, /id="limitInfoText" data-sensitive/);
  assert.match(template, /id="reserveInfoText" data-sensitive/);
});

test('responsive sheet contracts keep anti-zoom, safe area and reduced motion', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', 'css', 'sheet-form.css'), 'utf8');
  assert.match(css, /@media \(max-width: 1023px\)[\s\S]*?font-size: 16px/);
  assert.match(css, /@media \(min-width: 768px\)[\s\S]*?max-height: min\(880px, calc\(100dvh - 48px\)\)/);
  assert.match(css, /env\(safe-area-inset-bottom/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(css, /\.sheet-form-footer \{[\s\S]*?border-top: 1px solid var\(--ui-border\)/);
  // Mobile keeps the amount and the icon-only status control on the same row.
  // The label spans both columns and the input/button share the control row.
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-amount-controls \{[\s\S]*?grid-template-columns: minmax\(0, 1fr\) auto;[\s\S]*?grid-template-rows: auto auto;[\s\S]*?column-gap: 10px;/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-amount-main \{ display: contents; \}/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-amount-label \{[\s\S]*?grid-column: 1 \/ -1;[\s\S]*?grid-row: 1;/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?#valor-input \{[\s\S]*?grid-column: 1;[\s\S]*?grid-row: 2;/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-status-field \{[\s\S]*?grid-column: 2;[\s\S]*?grid-row: 2;/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-status-caption \{ display: none; \}/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-status-btn \{[\s\S]*?width: 48px;[\s\S]*?min-width: 48px;[\s\S]*?height: 56px;/);
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.sheet-status-label \{ display: none; \}/);
});

test('payment status keeps one toggle with a dynamic accessible label', () => {
  assert.match(template, /id="statusPagamentoBtn"[\s\S]*?aria-label="\{\{if eq \.StatusInicial "pending"\}\}Pendente — toque para marcar como pago/);
  assert.match(template, /id="statusPagamentoBtn"[\s\S]*?aria-pressed="\{\{if eq \.StatusInicial "pending"\}\}false\{\{else\}\}true\{\{end\}\}"/);
  const js = fs.readFileSync(path.join(__dirname, 'form-lancamento.js'), 'utf8');
  assert.match(js, /btn\.setAttribute\('aria-label', isPaid \? 'Pago[^']*' : 'Pendente[^']*'\)/);
});

test('amount field and payment status are independent layout blocks', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', 'css', 'sheet-form.css'), 'utf8');
  assert.match(template, /class="sheet-amount-controls"/);
  assert.match(template, /id="valor-input" class="sheet-amount-input-shell"/);
  assert.match(template, /id="statusPagamentoField" class="sheet-status-field"/);
  assert.match(template, /class="sheet-status-caption">Situação</);

  // The status button must not live inside the input shell: a focus ring
  // around the amount can no longer wrap the payment control.
  const shell = template.match(/<div id="valor-input"[^>]*>([\s\S]*?)\n\s*<\/div>/);
  assert.ok(shell, 'missing amount input shell');
  assert.match(shell[1], /id="valor"/);
  assert.doesNotMatch(shell[1], /statusPagamentoBtn/);

  assert.match(css, /\.sheet-amount-controls \{[\s\S]*?grid-template-columns: minmax\(0, 1fr\) auto/);
  assert.match(css, /\.sheet-amount-input-shell:focus-within \{[\s\S]*?box-shadow:/);
  assert.doesNotMatch(css, /\.sheet-amount-field:focus-within/);
  assert.doesNotMatch(css, /\.sheet-status-btn\s*\{[^}]*position:\s*absolute/);
  assert.match(css, /\.sheet-status-btn \{[\s\S]*?height: 56px;[\s\S]*?min-width: 108px;/);
  assert.match(css, /\.sheet-status-btn \{[\s\S]*?flex-direction: row/);
});

test('single-instance functional ids are not duplicated', () => {
  for (const id of [
    'lancamento-form', 'lancamento-sheet-title', 'lancamento-sheet-context',
    'valor', 'descricaoLancamento', 'dataLancamento', 'origemPicker',
    'destinoPicker', 'categoriaPicker', 'faturaSelect', 'lancamentoSubmit',
    'seriesScopeModal', 'seriesDeleteModal'
  ]) {
    const occurrences = [...template.matchAll(new RegExp(`\\sid="${id}"`, 'g'))].length;
    assert.equal(occurrences, 1, `expected one #${id}, found ${occurrences}`);
  }
});

// --- Runtime helpers ------------------------------------------------------

function fakeElement(overrides = {}) {
  const classes = new Set((overrides.classes || []).filter(Boolean));
  const attrs = new Map(Object.entries(overrides.attrs || {}));
  const element = {
    dataset: Object.assign({}, overrides.dataset),
    tagName: overrides.tagName || 'DIV',
    value: overrides.value || '',
    textContent: overrides.textContent || '',
    checked: Boolean(overrides.checked),
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      toggle: (name, force) => {
        const next = typeof force === 'boolean' ? force : !classes.has(name);
        next ? classes.add(name) : classes.delete(name);
        return next;
      },
      contains: (name) => classes.has(name)
    },
    setAttribute: (name, value) => attrs.set(name, String(value)),
    getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
    removeAttribute: (name) => attrs.delete(name),
    addEventListener: () => {},
    focus: () => {},
    contains: () => false,
    querySelector: () => null,
    querySelectorAll: () => [],
    ...overrides
  };
  element.classes = classes;
  return element;
}

function runtime() {
  const listeners = new Map();
  const elements = new Map();
  const tabs = ['despesa', 'receita', 'transferencia'].map((type) => fakeElement({
    dataset: { type }
  }));
  const document = {
    activeElement: null,
    addEventListener: (name, fn) => {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(fn);
    },
    body: { addEventListener: () => {} },
    getElementById: (id) => elements.get(id) || null,
    querySelector: () => null,
    querySelectorAll: (selector) => (selector === '[data-type]' ? tabs : []),
    contains: () => false
  };
  const window = {
    addEventListener: () => {},
    dispatchEvent: () => {},
    setTimeout: (fn) => fn(),
    location: { origin: 'http://localhost' },
    closeBottomSheet: () => {}
  };
  const context = vm.createContext({
    window,
    document,
    console,
    URL,
    Intl,
    Number,
    CustomEvent: class CustomEvent {
      constructor(type, init) {
        this.type = type;
        this.detail = init && init.detail;
      }
    }
  });
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, 'form-lancamento.js'), 'utf8'),
    context,
    { filename: 'form-lancamento.js' }
  );
  return { ns: window.ContaBaseFormLancamento, window, document, elements, tabs };
}

test('money helpers preserve cents and documented legacy mask semantics', () => {
  const { ns } = runtime();
  assert.equal(ns.parseMoneyCents('R$ 99,99'), 9999);
  assert.equal(ns.parseMoneyCents('R$ 1.234,56'), 123456);
  assert.equal(ns.parseMoneyCents(''), 0);
  assert.equal(ns.parseMoneyCents('sem valor'), 0);
  // The legacy money mask treats raw digits as cents: typing 9999 becomes
  // R$ 99,99. This test documents the contract instead of assuming an error.
  assert.equal(ns.parseMoneyCents('9999'), 9999);
  assert.equal(ns.formatCurrencyCents(9999), '99,99');
  assert.equal(ns.formatCurrencyCents(123456), '1.234,56');
  assert.equal(ns.formatCurrencyCents(-123456), '-1.234,56');
  assert.equal(ns.formatCurrencyCents(5), '0,05');
});

test('transaction type changes keep the conditional field contract', () => {
  const { ns, elements, tabs } = runtime();
  const tipo = fakeElement({ value: 'despesa' });
  const origemLabel = fakeElement({ textContent: 'Conta de origem' });
  const categoria = fakeElement();
  const destino = fakeElement({ classes: ['hidden'] });
  const cartoes = fakeElement();
  const hint = fakeElement({ classes: ['hidden'] });
  const title = fakeElement({ attrs: { 'data-sheet-title-mode': 'create' }, textContent: 'Nova despesa' });
  elements.set('tipoLancamento', tipo);
  elements.set('origemLabel', origemLabel);
  elements.set('categoriaPicker', categoria);
  elements.set('destinoPicker', destino);
  elements.set('cartoesModalGroup', cartoes);
  elements.set('transferDirectionHint', hint);
  elements.set('lancamento-sheet-title', title);

  ns.applyTransactionType({ type: 'transferencia', currentOriginType: 'conta' }, {});
  assert.equal(tipo.value, 'transferencia');
  assert.equal(categoria.classList.contains('hidden'), true);
  assert.equal(destino.classList.contains('hidden'), false);
  assert.equal(hint.classList.contains('hidden'), false);
  assert.equal(cartoes.classList.contains('hidden'), true);
  assert.equal(title.textContent, 'Nova transferência');
  assert.equal(tabs[2].getAttribute('aria-pressed'), 'true');
  assert.equal(tabs[0].getAttribute('aria-pressed'), 'false');

  ns.applyTransactionType({ type: 'receita', currentOriginType: 'conta' }, {});
  assert.equal(categoria.classList.contains('hidden'), false);
  assert.equal(destino.classList.contains('hidden'), true);
  assert.equal(hint.classList.contains('hidden'), true);
  assert.equal(cartoes.classList.contains('hidden'), true);
  assert.equal(title.textContent, 'Nova receita');

  ns.applyTransactionType({ type: 'despesa', currentOriginType: 'conta' }, {});
  assert.equal(cartoes.classList.contains('hidden'), false);
  assert.equal(title.textContent, 'Nova despesa');
});

test('edit mode never rewrites the dialog title on type change', () => {
  const { ns, elements } = runtime();
  const title = fakeElement({ attrs: { 'data-sheet-title-mode': 'edit' }, textContent: 'Editar lançamento' });
  elements.set('lancamento-sheet-title', title);
  elements.set('tipoLancamento', fakeElement({ value: 'despesa' }));
  ns.applyTransactionType({ type: 'receita', currentOriginType: 'conta' }, {});
  assert.equal(title.textContent, 'Editar lançamento');
});

test('installment preview uses semantic hint classes instead of ad-hoc utilities', () => {
  const { ns, window, elements } = runtime();
  const toggle = fakeElement({ checked: false });
  const summary = fakeElement();
  const select = fakeElement({ value: '3' });
  const value = fakeElement({ value: 'R$ 100,00' });
  elements.set('parcelada', toggle);
  elements.set('parcelasResumo', summary);
  elements.set('parcelas', select);
  elements.set('valor', value);

  ns.updateInstallmentsSummary();
  assert.equal(summary.className, 'sheet-option__hint');

  toggle.checked = true;
  summary.className = '';
  window.ContaBaseInstallmentPreview = {
    describe: (cents, count) => `${count}x de ${ns.formatCurrencyCents(cents / count)}`
  };
  ns.updateInstallmentsSummary();
  assert.equal(summary.className, 'sheet-option__hint is-filled');
  assert.match(summary.textContent, /^3x de /);
});

// --- F8-01: transfers must use two different accounts ----------------------

test('validateTransferAccounts blocks same-account transfers with a visible message', () => {
  const { ns, elements } = runtime();
  const tipo = fakeElement({ value: 'transferencia' });
  const origem = fakeElement({ value: 'acc-1' });
  const destino = fakeElement({ value: 'acc-1' });
  const errorBox = fakeElement({ classes: ['hidden'] });
  elements.set('tipoLancamento', tipo);
  elements.set('origemContaId', origem);
  elements.set('destinoContaId', destino);
  elements.set('lancamento-form-error', errorBox);

  assert.equal(ns.validateTransferAccounts(), false);
  assert.equal(errorBox.classList.contains('hidden'), false);
  assert.match(
    errorBox.textContent,
    /conta de origem e a conta de destino devem ser diferentes/i
  );

  destino.value = 'acc-2';
  assert.equal(ns.validateTransferAccounts(), true);
});

test('validateTransferAccounts requires an explicit destination for transfers', () => {
  const { ns, elements } = runtime();
  elements.set('tipoLancamento', fakeElement({ value: 'transferencia' }));
  elements.set('origemContaId', fakeElement({ value: 'acc-1' }));
  const destino = fakeElement({ value: '' });
  const errorBox = fakeElement({ classes: ['hidden'] });
  elements.set('destinoContaId', destino);
  elements.set('lancamento-form-error', errorBox);

  assert.equal(ns.validateTransferAccounts(), false);
  assert.match(errorBox.textContent, /selecione a conta de destino/i);
});

test('validateTransferAccounts ignores non-transfer types even with mirrored ids', () => {
  const { ns, elements } = runtime();
  elements.set('tipoLancamento', fakeElement({ value: 'despesa' }));
  elements.set('origemContaId', fakeElement({ value: 'acc-1' }));
  elements.set('destinoContaId', fakeElement({ value: 'acc-1' }));
  assert.equal(ns.validateTransferAccounts(), true);
});

test('switching to transferencia resets a destination that mirrors the origin', () => {
  const { ns, elements } = runtime();
  elements.set('tipoLancamento', fakeElement({ value: 'despesa' }));
  elements.set('origemContaId', fakeElement({ value: 'acc-1' }));
  const destino = fakeElement({ value: 'acc-1' });
  const destinoNome = fakeElement({ textContent: 'Conta Um' });
  const destinoIcon = fakeElement({ classes: ['hidden'] });
  const destinoIconText = fakeElement({ classes: ['hidden'] });
  const destinoIconImage = fakeElement({ classes: ['hidden'] });
  const destinoIconWrap = fakeElement();
  elements.set('destinoContaId', destino);
  elements.set('destinoNome', destinoNome);
  elements.set('destinoIcon', destinoIcon);
  elements.set('destinoIconText', destinoIconText);
  elements.set('destinoIconImage', destinoIconImage);
  elements.set('destinoIconWrap', destinoIconWrap);
  elements.set('categoriaPicker', fakeElement());
  elements.set('destinoPicker', fakeElement({ classes: ['hidden'] }));
  elements.set('cartoesModalGroup', fakeElement());
  elements.set('transferDirectionHint', fakeElement({ classes: ['hidden'] }));
  elements.set('lancamento-sheet-title', fakeElement({
    attrs: { 'data-sheet-title-mode': 'create' },
    textContent: 'Nova despesa'
  }));

  ns.applyTransactionType({ type: 'transferencia', currentOriginType: 'conta' }, {});

  assert.equal(destino.value, '', 'mirrored destination must be cleared');
  assert.equal(destinoNome.textContent, 'Conta de destino');
  assert.equal(destinoIcon.classList.contains('hidden'), false);
  assert.equal(destinoIcon.getAttribute('data-lucide'), 'wallet');
});

test('changing the origin to the destination account clears the destination', () => {
  const { ns, elements } = runtime();
  elements.set('tipoLancamento', fakeElement({ value: 'transferencia' }));
  elements.set('origemContaId', fakeElement({ value: 'acc-1' }));
  const destino = fakeElement({ value: 'acc-2' });
  const destinoNome = fakeElement({ textContent: 'Conta Dois' });
  const destinoIcon = fakeElement({ classes: ['hidden'] });
  const destinoIconText = fakeElement({ classes: ['hidden'] });
  const destinoIconImage = fakeElement({ classes: ['hidden'] });
  const destinoIconWrap = fakeElement();
  elements.set('destinoContaId', destino);
  elements.set('destinoNome', destinoNome);
  elements.set('destinoIcon', destinoIcon);
  elements.set('destinoIconText', destinoIconText);
  elements.set('destinoIconImage', destinoIconImage);
  elements.set('destinoIconWrap', destinoIconWrap);
  elements.set('origemIconWrap', fakeElement());
  elements.set('origemIcon', fakeElement());
  elements.set('origemIconText', fakeElement());
  elements.set('origemIconImage', fakeElement());
  elements.set('origemTipo', fakeElement({ value: 'conta' }));
  elements.set('origemLabel', fakeElement());
  elements.set('origemNome', fakeElement());

  // User moves the origin onto the current destination account (acc-2).
  ns.applyOriginSelection({ mode: 'origin', id: 'acc-2', kind: 'conta', name: 'Conta Dois' }, {});

  assert.equal(destino.value, '', 'destination equal to the new origin must be cleared');
  assert.equal(destinoNome.textContent, 'Conta de destino');
  assert.equal(destinoIcon.getAttribute('data-lucide'), 'wallet');
});

test('changing the origin to a different account keeps the destination', () => {
  const { ns, elements } = runtime();
  elements.set('tipoLancamento', fakeElement({ value: 'transferencia' }));
  elements.set('origemContaId', fakeElement({ value: 'acc-1' }));
  const destino = fakeElement({ value: 'acc-2' });
  const destinoIcon = fakeElement({ classes: ['hidden'] });
  const destinoIconText = fakeElement({ classes: ['hidden'] });
  const destinoIconImage = fakeElement({ classes: ['hidden'] });
  elements.set('destinoContaId', destino);
  elements.set('destinoNome', fakeElement({ textContent: 'Conta Dois' }));
  elements.set('destinoIcon', destinoIcon);
  elements.set('destinoIconText', destinoIconText);
  elements.set('destinoIconImage', destinoIconImage);
  elements.set('destinoIconWrap', fakeElement());
  elements.set('origemIconWrap', fakeElement());
  elements.set('origemIcon', fakeElement());
  elements.set('origemIconText', fakeElement());
  elements.set('origemIconImage', fakeElement());
  elements.set('origemTipo', fakeElement({ value: 'conta' }));
  elements.set('origemLabel', fakeElement());
  elements.set('origemNome', fakeElement());

  ns.applyOriginSelection({ mode: 'origin', id: 'acc-3', kind: 'conta', name: 'Conta Tres' }, {});

  assert.equal(destino.value, 'acc-2', 'a distinct destination must survive');
});
