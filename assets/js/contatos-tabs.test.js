const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// C5 — regressões identificadas na revisão de scripts:
//   - carregar o controller de tabs duas vezes (navegação HTMX) não pode
//     duplicar listeners;
//   - o teclado mantém o contrato aprovado: setas/Home/End movem o foco com
//     roving tabindex, sem ativar a aba (ativação por Enter/Espaço é nativa).
function runtime(tabIDs = ['t1', 't2', 't3']) {
    const documentListeners = [];
    const bodyListeners = [];
    const tabs = tabIDs.map((id) => ({
        id,
        tabIndex: id === tabIDs[0] ? 0 : -1,
        focused: false,
        attributes: {},
        setAttribute(name, value) {
            this.attributes[name] = String(value);
            if (name === 'tabindex') this.tabIndex = Number(value);
        },
        focus() {
            tabs.forEach((tab) => { tab.focused = tab === this; });
            documentStub.activeElement = this;
        },
        closest(selector) {
            return selector.indexOf('[role="tab"]') !== -1 ? this : null;
        },
    }));

    const documentStub = {
        readyState: 'complete',
        activeElement: tabs[0],
        body: { addEventListener: (name, fn) => bodyListeners.push({ name, fn }) },
        addEventListener: (name, fn) => documentListeners.push({ name, fn }),
        querySelectorAll: (selector) => (selector.indexOf('[role="tab"]') !== -1 ? tabs : []),
        querySelector: () => null,
        getElementById: () => null,
        contains: () => false,
    };

    const context = vm.createContext({ document: documentStub, window: {}, console });
    const source = fs.readFileSync(require.resolve('./contatos-tabs.js'), 'utf8');
    vm.runInContext(source, context, { filename: 'contatos-tabs.js' });

    return {
        tabs,
        documentListeners,
        bodyListeners,
        loadAgain: () => vm.runInContext(source, context, { filename: 'contatos-tabs.js' }),
        keydown: () => documentListeners.find((entry) => entry.name === 'keydown').fn,
    };
}

function keyEvent(key, target) {
    return {
        key,
        target,
        ctrlKey: false,
        metaKey: false,
        altKey: false,
        prevented: false,
        preventDefault() { this.prevented = true; },
    };
}

test('loading the tabs controller twice does not duplicate listeners', () => {
    const rt = runtime();
    const before = rt.documentListeners.length;
    rt.loadAgain();

    assert.equal(rt.documentListeners.length, before);
    assert.equal(rt.documentListeners.filter((entry) => entry.name === 'keydown').length, 1);
    assert.equal(rt.bodyListeners.filter((entry) => entry.name === 'htmx:afterSettle').length, 1);
});

test('arrow, Home and End move focus with roving tabindex without activating', () => {
    const rt = runtime();
    const keydown = rt.keydown();

    rt.tabs[0].focus();
    const right = keyEvent('ArrowRight', rt.tabs[0]);
    keydown(right);
    assert.equal(right.prevented, true);
    assert.equal(rt.tabs[1].focused, true);
    assert.deepEqual(rt.tabs.map((tab) => tab.tabIndex), [-1, 0, -1]);

    const end = keyEvent('End', rt.tabs[1]);
    keydown(end);
    assert.equal(rt.tabs[2].focused, true);
    assert.deepEqual(rt.tabs.map((tab) => tab.tabIndex), [-1, -1, 0]);

    const home = keyEvent('Home', rt.tabs[2]);
    keydown(home);
    assert.equal(rt.tabs[0].focused, true);

    const left = keyEvent('ArrowLeft', rt.tabs[0]);
    keydown(left);
    assert.equal(rt.tabs[2].focused, true, 'ArrowLeft wraps to the last tab');

    const enter = keyEvent('Enter', rt.tabs[2]);
    keydown(enter);
    assert.equal(enter.prevented, false, 'Enter stays native (button activation)');
});
