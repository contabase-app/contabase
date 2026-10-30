const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Contratos do formulário de Metas (M6):
// - o segmented usa is-active + aria-pressed (sem classes Tailwind de cor);
// - trocar a aba atualiza formulário, required, aria, label, título e ícone;
// - a validação client-side cobre obrigatoriedade e valor > 0 sem duplicar
//   regras financeiras;
// - erros ficam junto ao campo, com aria-invalid e foco;
// - o erro devolvido pelo servidor recebe foco uma única vez;
// - selecionar/remover categoria limpa o estado de erro.
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

function makeForm(id, nameInput, amountInput) {
    const form = control({ id, classes: [] });
    form.querySelector = (selector) => {
        if (selector === '[name="name"]') return nameInput;
        if (selector === '[name="max_amount_monthly"]') return amountInput;
        return null;
    };
    return form;
}

function runtime() {
    const limitName = null;
    const limitAmount = control({ id: 'limiteMaxAmount', value: '' });
    const reserveName = control({ id: 'caixinhaName', value: '' });
    const limitForm = makeForm('meta-limite-form', limitName, limitAmount);
    const reserveForm = makeForm('meta-caixinha-form', reserveName, null);

    const limitTab = control({ id: 'tab-limite', classes: ['ui-tab', 'metas-form__tab', 'is-active'] });
    limitTab.dataset.metaTab = 'limite';
    const reserveTab = control({ id: 'tab-caixinha', classes: ['ui-tab', 'metas-form__tab'] });
    reserveTab.dataset.metaTab = 'caixinha';

    const submit = control({ id: 'meta-create-submit' });
    const submitLabel = control({ id: 'meta-submit-label', textContent: 'Criar limite' });
    const sheetIcon = control({ id: 'meta-sheet-icon' });
    const closeButton = control({ id: 'meta-sheet-close', classes: ['metas-sheet__close'] });
    const eyebrow = control({ id: 'meta-sheet-eyebrow', textContent: 'Limites' });
    const root = control({ id: 'meta-form-root' });
    root.dataset = {
        labelLimite: 'Criar limite',
        labelCaixinha: 'Criar reserva',
        titleLimite: 'Novo limite',
        titleCaixinha: 'Nova reserva',
        descriptionLimite: 'Defina o teto mensal de uma categoria.',
        descriptionCaixinha: 'Planeje um valor sem mover o dinheiro das contas.',
        eyebrowLimite: 'Limites',
        eyebrowCaixinha: 'Reservas',
    };
    root.querySelector = (selector) => {
        if (selector === '[data-meta-create-submit]') return submit;
        if (selector === '[data-meta-submit-label]') return submitLabel;
        if (selector === '[data-meta-sheet-icon]') return sheetIcon;
        if (selector === '.metas-sheet__close') return closeButton;
        if (selector === '[data-meta-sheet-eyebrow]') return eyebrow;
        return null;
    };
    root.querySelectorAll = (selector) => (selector === '[data-meta-tab]:not([data-meta-tab-ready])' ? [limitTab, reserveTab] : []);

    const ids = {
        'meta-limite-form': limitForm,
        'meta-caixinha-form': reserveForm,
        'tab-limite': limitTab,
        'tab-caixinha': reserveTab,
        'limiteMaxAmount': limitAmount,
        'caixinhaName': reserveName,
        'categoriaId_limite': control({ id: 'categoriaId_limite' }),
        'categoriaId_caixinha': control({ id: 'categoriaId_caixinha' }),
        'categoriaNome_limite': control({ id: 'categoriaNome_limite', innerText: 'Buscar categoria...' }),
        'categoriaNome_caixinha': control({ id: 'categoriaNome_caixinha', innerText: 'Sem categoria' }),
        'categoriaIcon_limite': control({ id: 'categoriaIcon_limite' }),
        'categoriaIcon_caixinha': control({ id: 'categoriaIcon_caixinha' }),
        'categoriaIconWrap_limite': control({ id: 'categoriaIconWrap_limite' }),
        'categoriaIconWrap_caixinha': control({ id: 'categoriaIconWrap_caixinha' }),
        'categoria-error-limite': control({ id: 'categoria-error-limite', classes: ['hidden'] }),
        'categoria-error-caixinha': control({ id: 'categoria-error-caixinha', classes: ['hidden'] }),
        'limite-amount-error': control({ id: 'limite-amount-error', classes: ['hidden'] }),
        'caixinha-name-error': control({ id: 'caixinha-name-error', classes: ['hidden'] }),
        'limite-form-error': control({ id: 'limite-form-error', classes: ['hidden'] }),
        'caixinha-form-error': control({ id: 'caixinha-form-error', classes: ['hidden'] }),
        'meta-sheet-title': control({ id: 'meta-sheet-title', textContent: 'Novo limite' }),
        'meta-sheet-description': control({ id: 'meta-sheet-description', textContent: 'Defina o teto mensal de uma categoria.' }),
        'targetMonthInput': control({ id: 'targetMonthInput' }),
        'bottom-sheet-container': control({ id: 'bottom-sheet-container' }),
    };
    ids['bottom-sheet-container'].innerHTML = '';

    const pickerLimit = control({ id: 'picker-limite', attrs: { 'aria-invalid': 'false' } });
    pickerLimit.closest = (selector) => (selector.indexOf('[data-meta-category-picker]') >= 0 ? pickerLimit : null);
    const pickerReserve = control({ id: 'picker-caixinha', attrs: { 'aria-invalid': 'false' } });
    pickerReserve.closest = (selector) => (selector.indexOf('[data-meta-category-picker]') >= 0 ? pickerReserve : null);
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
            if (selector === '[data-meta-form-root]') return root;
            if (selector === '#categoriaPicker_limite [data-meta-category-picker]') return pickerLimit;
            if (selector === '#categoriaPicker_caixinha [data-meta-category-picker]') return pickerReserve;
            if (selector === '#limite-form-error:not(.hidden), #caixinha-form-error:not(.hidden)') {
                const error = ids['limite-form-error'].classList.contains('hidden') ? ids['caixinha-form-error'] : ids['limite-form-error'];
                return error.classList.contains('hidden') ? null : error;
            }
            return null;
        },
        querySelectorAll() {
            return [];
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
        root,
        limitForm,
        reserveForm,
        limitTab,
        reserveTab,
        submit,
        submitLabel,
        sheetIcon,
        closeButton,
        eyebrow,
        pickerLimit,
        pickerReserve,
        documentListeners,
        bodyListeners,
        pickerCloseCalls: () => pickerCloseCalls,
        submitEvent(form) {
            let prevented = false;
            const event = {
                target: form,
                preventDefault() { prevented = true; },
                stopImmediatePropagation() {},
            };
            (documentListeners.submit || []).forEach((fn) => fn(event));
            return prevented;
        },
        inputEvent(target) {
            (documentListeners.input || []).forEach((fn) => fn({ target }));
        },
        settle() {
            (bodyListeners['htmx:afterSettle'] || []).forEach((fn) => fn({}));
        },
        initSheet() {
            ids['bottom-sheet-container'].innerHTML = '<div data-meta-form-root></div>';
            (bodyListeners['htmx:afterSwap'] || []).forEach((fn) => fn({
                detail: {
                    target: {
                        id: 'bottom-sheet-container',
                        querySelector: () => root,
                    },
                },
            }));
        },
    };
}

test('segmented contract uses is-active and aria-pressed without hardcoded palette', () => {
    assert.match(source, /classList\.toggle\('is-active'/);
    assert.match(source, /setAttribute\('aria-pressed'/);
    assert.doesNotMatch(source, /bg-violet|text-violet|bg-orange|text-orange|text-zinc|bg-zinc/);
    assert.doesNotMatch(source, /className\s*=/);
});

test('toggleMetaTab switches forms, required state, ARIA, submit target and context', () => {
    const rt = runtime();
    rt.window.toggleMetaTab('caixinha');

    assert.equal(rt.reserveForm.classList.contains('hidden'), false);
    assert.equal(rt.limitForm.classList.contains('hidden'), true);
    assert.equal(rt.ids.caixinhaName.required, true);
    assert.equal(rt.ids.caixinhaName.getAttribute('aria-required'), 'true');
    assert.equal(rt.ids.limiteMaxAmount.required, false);
    assert.equal(rt.ids.limiteMaxAmount.getAttribute('aria-required'), 'false');
    assert.equal(rt.reserveTab.getAttribute('aria-pressed'), 'true');
    assert.equal(rt.limitTab.getAttribute('aria-pressed'), 'false');
    assert.equal(rt.reserveTab.classList.contains('is-active'), true);
    assert.equal(rt.limitTab.classList.contains('is-active'), false);
    assert.equal(rt.submit.getAttribute('form'), 'meta-caixinha-form');
    assert.equal(rt.submitLabel.textContent, 'Criar reserva');
    assert.equal(rt.ids['meta-sheet-title'].textContent, 'Nova reserva');
    assert.equal(rt.ids['meta-sheet-description'].textContent, 'Planeje um valor sem mover o dinheiro das contas.');
    assert.equal(rt.eyebrow.textContent, 'Reservas');
    assert.equal(rt.sheetIcon.getAttribute('data-lucide'), 'piggy-bank');
    assert.equal(rt.closeButton.getAttribute('aria-label'), 'Fechar formulário de reserva');
    assert.equal(rt.root.dataset.openTab, 'caixinha');

    rt.window.toggleMetaTab('limite');
    assert.equal(rt.limitForm.classList.contains('hidden'), false);
    assert.equal(rt.reserveForm.classList.contains('hidden'), true);
    assert.equal(rt.ids.limiteMaxAmount.required, true);
    assert.equal(rt.ids.caixinhaName.required, false);
    assert.equal(rt.submit.getAttribute('form'), 'meta-limite-form');
    assert.equal(rt.submitLabel.textContent, 'Criar limite');
    assert.equal(rt.ids['meta-sheet-title'].textContent, 'Novo limite');
    assert.equal(rt.eyebrow.textContent, 'Limites');
    assert.equal(rt.sheetIcon.getAttribute('data-lucide'), 'gauge');
    assert.equal(rt.closeButton.getAttribute('aria-label'), 'Fechar formulário de limite');
});

test('limit validation blocks empty category and zero amount with field errors', () => {
    const rt = runtime();

    assert.equal(rt.submitEvent(rt.limitForm), true);
    assert.equal(rt.ids['categoria-error-limite'].classList.contains('hidden'), false);
    assert.equal(rt.ids['categoria-error-limite'].textContent, 'Selecione uma categoria.');
    assert.equal(rt.pickerLimit.getAttribute('aria-invalid'), 'true');
    assert.equal(rt.pickerLimit.focusCalls(), 1);
    assert.equal(rt.document.activeElement, rt.pickerLimit);

    rt.ids.categoriaId_limite.value = 'cat-1';
    rt.ids.limiteMaxAmount.value = 'R$ 0,00';
    assert.equal(rt.submitEvent(rt.limitForm), true);
    assert.equal(rt.ids['limite-amount-error'].classList.contains('hidden'), false);
    assert.equal(rt.ids['limite-amount-error'].textContent, 'Informe um valor maior que zero.');
    assert.equal(rt.ids.limiteMaxAmount.getAttribute('aria-invalid'), 'true');
    assert.equal(rt.ids.limiteMaxAmount.focusCalls(), 1);

    rt.ids.limiteMaxAmount.value = 'R$ 1.234,56';
    assert.equal(rt.submitEvent(rt.limitForm), false);
    assert.equal(rt.ids['limite-amount-error'].classList.contains('hidden'), true);
    assert.equal(rt.ids.limiteMaxAmount.getAttribute('aria-invalid'), 'false');
});

test('reserve validation requires only the reserve name and clears on input', () => {
    const rt = runtime();
    rt.window.toggleMetaTab('caixinha');

    assert.equal(rt.submitEvent(rt.reserveForm), true);
    assert.equal(rt.ids['caixinha-name-error'].classList.contains('hidden'), false);
    assert.equal(rt.ids.caixinhaName.getAttribute('aria-invalid'), 'true');
    assert.equal(rt.ids.caixinhaName.focusCalls(), 1);

    rt.ids.caixinhaName.value = 'Aposentadoria';
    rt.inputEvent(rt.ids.caixinhaName);
    assert.equal(rt.ids['caixinha-name-error'].classList.contains('hidden'), true);
    assert.equal(rt.ids.caixinhaName.getAttribute('aria-invalid'), 'false');
    assert.equal(rt.submitEvent(rt.reserveForm), false);
});

test('category selection and removal clear the category error', () => {
    const rt = runtime();
    rt.initSheet();
    rt.submitEvent(rt.limitForm);
    assert.equal(rt.pickerLimit.getAttribute('aria-invalid'), 'true');

    rt.window.selectPickerItem('cat-1', 'Alimentação', 'utensils', '#8b5cf6');
    assert.equal(rt.ids['categoria-error-limite'].classList.contains('hidden'), true);
    assert.equal(rt.pickerLimit.getAttribute('aria-invalid'), 'false');
    assert.equal(rt.ids.categoriaNome_limite.innerText, 'Alimentação');
    assert.equal(rt.ids.categoriaId_limite.value, 'cat-1');
});

test('server error receives focus once after htmx settle', () => {
    const rt = runtime();
    const error = rt.ids['limite-form-error'];
    error.classList.remove('hidden');
    error.textContent = 'dados inválidos';

    rt.settle();
    assert.equal(error.getAttribute('tabindex'), '-1');
    assert.equal(error.focusCalls(), 1);
    assert.equal(rt.document.activeElement, error);
    assert.equal(error.dataset.metaErrorFocused, 'true');

    rt.settle();
    assert.equal(error.focusCalls(), 1);
});

test('initMetaForm wires tabs once and applies the opened tab', () => {
    const rt = runtime();
    rt.ids['bottom-sheet-container'].innerHTML = '<div data-meta-form-root></div>';
    vm.runInNewContext(source, vm.createContext({ document: rt.document, window: rt.window, console }), { filename: 'form-meta.js' });

    assert.equal(rt.limitTab.listeners.click.length, 1);
    assert.equal(rt.reserveTab.listeners.click.length, 1);
    assert.equal(rt.limitTab.dataset.metaTabReady, 'true');
    assert.equal(rt.reserveTab.dataset.metaTabReady, 'true');

    rt.limitTab.listeners.click[0]();
    assert.equal(rt.limitForm.classList.contains('hidden'), false);
    rt.reserveTab.listeners.click[0]();
    assert.equal(rt.reserveForm.classList.contains('hidden'), false);
});

test('category picker returns focus to its trigger when the picker closes', () => {
    const rt = runtime();
    const clickCapture = (rt.documentListeners.click || [])[0];
    clickCapture({ target: rt.pickerLimit });

    rt.window.closePickerModal();
    assert.equal(rt.pickerCloseCalls(), 1);
    assert.equal(rt.pickerLimit.focusCalls(), 1);
    assert.equal(rt.document.activeElement, rt.pickerLimit);
});

test('category picker receives focus on the search field after HTMX swap', () => {
    const rt = runtime();
    const search = control({ id: 'picker-search' });
    const picker = {
        querySelector: (selector) => (selector === '[data-picker-search]' ? search : null),
    };
    const target = {
        id: 'picker-modal-container',
        querySelector: (selector) => (selector === '#modalPickerBackdrop' ? picker : null),
    };

    (rt.bodyListeners['htmx:afterSwap'] || []).forEach((fn) => fn({ detail: { target } }));
    assert.equal(search.focusCalls(), 1);
    assert.equal(rt.document.activeElement, search);
});
