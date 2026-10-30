const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// C4 — o controller do sheet de Contatos convive com o overlay global. Estes
// testes fixam os contratos que não são cobertos pelo render Go:
//   - carregar o script duas vezes não duplica listeners nem embrulha
//     closeBottomSheet outra vez;
//   - as máscaras continuam globais (o picker inline de Lançamentos usa);
//   - 422 mantém o sheet aberto, escreve a mensagem e foca o erro.
function runtime() {
    const bodyListeners = [];
    const documentListeners = [];
    let closeCalls = 0;

    const documentStub = {
        readyState: 'complete',
        body: {
            addEventListener: (name, fn) => bodyListeners.push({ name, fn }),
        },
        addEventListener: (name, fn) => documentListeners.push({ name, fn }),
        createElement: () => ({
            innerHTML: '',
            get textContent() {
                return this.innerHTML.replace(/<[^>]*>/g, '');
            },
        }),
        querySelector: () => null,
        querySelectorAll: () => [],
        getElementById: () => null,
        contains: () => false,
    };

    const windowStub = {
        closeBottomSheet: () => {
            closeCalls += 1;
        },
    };

    const context = vm.createContext({
        document: documentStub,
        window: windowStub,
        console,
        URL,
        CSS: { escape: (value) => value },
    });

    const source = fs.readFileSync(require.resolve('./contatos-modal.js'), 'utf8');
    vm.runInContext(source, context, { filename: 'contatos-modal.js' });

    return {
        window: windowStub,
        bodyListeners,
        documentListeners,
        closeCalls: () => closeCalls,
        loadAgain: () => vm.runInContext(source, context, { filename: 'contatos-modal.js' }),
    };
}

function listenerOf(rt, name) {
    const matches = rt.bodyListeners.filter((entry) => entry.name === name);
    assert.equal(matches.length, 1, `expected exactly one ${name} listener`);
    return matches[0].fn;
}

test('loading the controller twice keeps a single listener set and a single close wrapper', () => {
    const rt = runtime();
    const countAfterFirst = rt.bodyListeners.length;
    rt.loadAgain();

    assert.equal(rt.bodyListeners.length, countAfterFirst, 'listeners must not be duplicated');
    assert.equal(rt.window.__contatosSheetController, true);

    rt.window.closeBottomSheet();
    assert.equal(rt.closeCalls(), 1, 'closeBottomSheet must be wrapped exactly once');
});

test('contact masks remain global for the inline picker', () => {
    const rt = runtime();

    const documentInput = { value: '12345678900' };
    rt.window.maskCnpjCpfContato(documentInput);
    assert.equal(documentInput.value, '123.456.789-00');

    const cnpjInput = { value: '12345678000190' };
    rt.window.maskCnpjCpfContato(cnpjInput);
    assert.equal(cnpjInput.value, '12.345.678/0001-90');

    const phoneInput = { value: '11999998888' };
    rt.window.maskPhoneContato(phoneInput);
    assert.equal(phoneInput.value, '(11) 99999-8888');

    const landline = { value: '1133334444' };
    rt.window.maskPhoneContato(landline);
    assert.equal(landline.value, '(11) 3333-4444');
});

test('422 keeps the sheet open, reports the message and focuses the error region', () => {
    const rt = runtime();
    const handler = listenerOf(rt, 'htmx:responseError');

    const removed = [];
    let focused = false;
    const error = {
        textContent: '',
        classList: { remove: (name) => removed.push(name) },
        focus: () => {
            focused = true;
        },
    };
    const sheet = { querySelector: (selector) => (selector === '#contato-form-error' ? error : null) };
    const form = {
        matches: (selector) => selector === '.cb-v2-contatos-form',
        closest: (selector) => (selector === '[data-contatos-sheet]' ? sheet : null),
    };

    let stopped = false;
    handler({
        detail: { elt: form, xhr: { status: 422, responseText: '<div class="cb-contact-response-error">nome e tipo são obrigatórios</div>' } },
        stopImmediatePropagation: () => {
            stopped = true;
        },
    });

    assert.equal(stopped, true, 'the global 422 handler must not swap the page');
    assert.equal(error.textContent, 'nome e tipo são obrigatórios');
    assert.deepEqual(removed, ['hidden']);
    assert.equal(focused, true);
});

test('non-422 responses are left to the global HTMX handling', () => {
    const rt = runtime();
    const handler = listenerOf(rt, 'htmx:responseError');

    let stopped = false;
    let sheetLookups = 0;
    const form = {
        matches: () => true,
        closest: () => {
            sheetLookups += 1;
            return null;
        },
    };

    handler({
        detail: { elt: form, xhr: { status: 500, responseText: 'erro' } },
        stopImmediatePropagation: () => {
            stopped = true;
        },
    });

    assert.equal(stopped, false, 'handler must not swallow non-422 errors');
    assert.equal(sheetLookups, 0, 'handler must not touch the sheet for non-422 errors');
});
