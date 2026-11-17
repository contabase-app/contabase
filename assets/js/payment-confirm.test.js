const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Locks the shared confirmation modal contract for TRANSFER: no payment mode
// choices, transfer-specific date label and submit action, and origin/destination
// rendered from the row while the existing status endpoint stays in charge.
const source = fs.readFileSync(path.join(__dirname, 'payment-confirm.js'), 'utf8');

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

function element(initial = {}) {
    const attributes = new Map(Object.entries(initial.attrs || {}));
    return {
        id: initial.id || '',
        value: initial.value || '',
        checked: !!initial.checked,
        textContent: initial.textContent || '',
        innerHTML: '',
        disabled: false,
        required: false,
        focused: false,
        dataset: initial.dataset || {},
        classList: classList(initial.classes || []),
        children: [],
        getAttribute: (name) => (attributes.has(name) ? attributes.get(name) : null),
        setAttribute: (name, value) => attributes.set(name, String(value)),
        removeAttribute: (name) => attributes.delete(name),
        appendChild(child) {
            this.children.push(child);
            if (child && typeof child.textContent === 'string') this.textContent += child.textContent;
            return child;
        },
        focus() { this.focused = true; },
        setCustomValidity(message) { this.customValidity = message; },
        reportValidity() { this.reported = true; },
        querySelector: () => null,
        querySelectorAll: () => [],
    };
}

function world(overrides = {}) {
    const paidRadio = element({ attrs: { value: 'paid' }, checked: true });
    const form = element({ id: 'payment-confirm-form' });
    form.querySelector = (selector) => {
        if (selector.includes('payment_action') && (selector.includes(':checked') || selector.includes('[value="paid"]'))) return paidRadio;
        return null;
    };
    form.querySelectorAll = () => [];
    const modal = element({ id: 'payment-confirm-modal', classes: ['hidden'] });
    const labels = [element({ textContent: 'Conta de pagamento' }), element({ textContent: 'Conta de pagamento' })];
    const ids = {
        'payment-confirm-modal': modal,
        'payment-confirm-form': form,
        'payment-confirm-date': element({ id: 'payment-confirm-date' }),
        'payment-scheduled-date': element({ id: 'payment-scheduled-date' }),
        'payment-paid-date-row': element({ id: 'payment-paid-date-row' }),
        'payment-paid-date-label': element({ id: 'payment-paid-date-label', textContent: 'Data de pagamento' }),
        'payment-scheduled-date-row': element({ id: 'payment-scheduled-date-row', classes: ['hidden'] }),
        'payment-mode-group': element({ id: 'payment-mode-group' }),
        'payment-clear-schedule-option': element({ id: 'payment-clear-schedule-option', classes: ['hidden'] }),
        'payment-confirm-context': element({ id: 'payment-confirm-context' }),
        'payment-confirm-title': element({ id: 'payment-confirm-title', textContent: 'Confirmar pagamento' }),
        'payment-confirm-description': element({ id: 'payment-confirm-description' }),
        'payment-confirm-summary': element({ id: 'payment-confirm-summary' }),
        'payment-confirm-submit': element({ id: 'payment-confirm-submit' }),
        'payment-account-readonly-row': element({ id: 'payment-account-readonly-row' }),
        'payment-account-readonly': element({ id: 'payment-account-readonly' }),
        'payment-account-readonly-effect': element({ id: 'payment-account-readonly-effect' }),
        'payment-account-hint': element({ id: 'payment-account-hint' }),
        'payment-transfer-destination-row': element({ id: 'payment-transfer-destination-row', classes: ['hidden'] }),
        'payment-transfer-destination': element({ id: 'payment-transfer-destination' }),
        'payment-account-select-row': element({ id: 'payment-account-select-row', classes: ['hidden'] }),
        'payment-account-select': element({ id: 'payment-account-select' }),
        'payment-account-select-effect': element({ id: 'payment-account-select-effect' }),
        ...overrides,
    };
    const document = {
        body: { addEventListener() {} },
        addEventListener() {},
        getElementById: (id) => ids[id] || null,
        querySelectorAll: (selector) => (selector === '[data-payment-account-label]' ? labels : []),
        createElement: (tag) => element({ tag }),
        createTextNode: (text) => ({ nodeType: 3, textContent: text }),
    };
    const window = {
        refreshIcons() {},
        overlayDidOpen() {},
    };
    const context = { console, document, window };
    vm.createContext(context);
    vm.runInContext(source, context);
    return { context, ids, labels, form, modal };
}

function sourceRow(attributes) {
    const values = new Map(Object.entries(attributes));
    return {
        getAttribute: (name) => (values.has(name) ? values.get(name) : null),
        closest(selector) {
            return selector === '[data-payment-id]' ? this : null;
        },
    };
}

test('transfer confirmation reuses the shared modal with transfer-specific copy', () => {
    const ui = world();
    const row = sourceRow({
        'data-payment-id': 'tr-1',
        'data-payment-context': 'contas',
        'data-payment-post-url': '/transacoes/tr-1/status-pagamento',
        'data-payment-description': 'Transferência para reserva',
        'data-payment-amount': 'R$ 123,45',
        'data-payment-type': 'TRANSFER',
        'data-payment-account-id': 'checking-test',
        'data-payment-account-name': 'Conta Teste',
        'data-payment-destination-account-name': 'Conta Extra',
        'data-payment-account-change-allowed': 'false',
    });

    ui.context.window.openPaymentConfirmModalFromElement(row);

    assert.equal(ui.ids['payment-confirm-title'].textContent, 'Confirmar transferência');
    assert.equal(ui.ids['payment-paid-date-label'].textContent, 'Data da transferência');
    assert.equal(ui.ids['payment-mode-group'].classList.contains('hidden'), true);
    assert.match(ui.ids['payment-confirm-submit'].innerHTML, /Transferir agora/);
    assert.doesNotMatch(ui.ids['payment-confirm-submit'].innerHTML, /Pagar agora/);
    assert.match(ui.ids['payment-confirm-summary'].textContent, /A transferência será concluída em \d{2}\/\d{2}\/\d{4}\./);
    assert.equal(ui.ids['payment-account-readonly'].textContent, 'Conta Teste');
    assert.equal(ui.ids['payment-transfer-destination'].textContent, 'Conta Extra');
    assert.equal(ui.ids['payment-transfer-destination-row'].classList.contains('hidden'), false);
    assert.equal(ui.ids['payment-account-hint'].classList.contains('hidden'), true);
    assert.deepEqual(ui.labels.map((label) => label.textContent), ['Conta de origem', 'Conta de origem']);
    assert.equal(ui.form.getAttribute('hx-post'), '/transacoes/tr-1/status-pagamento');
    assert.equal(ui.ids['payment-confirm-context'].value, 'contas');
    assert.equal(ui.modal.classList.contains('hidden'), false);
    assert.equal(ui.modal.classList.contains('flex'), true);
});

test('regular payment keeps the payment mode and account labels', () => {
    const ui = world();
    const row = sourceRow({
        'data-payment-id': 'tx-1',
        'data-payment-context': 'contas',
        'data-payment-post-url': '/transacoes/tx-1/status-pagamento',
        'data-payment-description': 'Despesa pendente',
        'data-payment-amount': '- R$ 100,00',
        'data-payment-type': 'EXPENSE',
        'data-payment-account-id': 'checking-test',
        'data-payment-account-name': 'Conta Teste',
        'data-payment-account-change-allowed': 'false',
    });

    ui.context.window.openPaymentConfirmModalFromElement(row);

    assert.equal(ui.ids['payment-confirm-title'].textContent, 'Confirmar pagamento');
    assert.equal(ui.ids['payment-paid-date-label'].textContent, 'Data de pagamento');
    assert.equal(ui.ids['payment-mode-group'].classList.contains('hidden'), false);
    assert.match(ui.ids['payment-confirm-submit'].innerHTML, /Pagar agora/);
    assert.equal(ui.ids['payment-transfer-destination-row'].classList.contains('hidden'), true);
    assert.deepEqual(ui.labels.map((label) => label.textContent), ['Conta de pagamento', 'Conta de pagamento']);
});
