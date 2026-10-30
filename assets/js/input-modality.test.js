const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'input-modality.js'), 'utf8');

function runtime() {
    const attrs = new Map();
    const documentListeners = new Map();
    const document = {
        documentElement: {
            getAttribute: (key) => (attrs.has(key) ? attrs.get(key) : null),
            setAttribute: (key, value) => attrs.set(key, String(value)),
        },
        addEventListener(name, fn, capture) {
            if (!documentListeners.has(name)) documentListeners.set(name, []);
            documentListeners.get(name).push({ fn, capture });
        },
        emit(name, event) {
            (documentListeners.get(name) || []).forEach((listener) => listener.fn(event));
        },
    };
    const window = {};
    const context = vm.createContext({ document, window, console });
    return {
        document,
        window,
        context,
        load() {
            vm.runInContext(source, context, { filename: 'input-modality.js' });
        },
        mode() {
            return document.documentElement.getAttribute('data-input-modality');
        },
        listenerCount(name) {
            return (documentListeners.get(name) || []).length;
        },
    };
}

test('starts in pointer modality', () => {
    const rt = runtime();
    rt.load();
    assert.equal(rt.mode(), 'pointer');
});

test('Tab switches to keyboard modality', () => {
    const rt = runtime();
    rt.load();
    rt.document.emit('keydown', { key: 'Tab' });
    assert.equal(rt.mode(), 'keyboard');
});

test('Enter, Space and Escape switch to keyboard modality', () => {
    for (const key of ['Enter', ' ', 'Spacebar', 'Escape']) {
        const rt = runtime();
        rt.load();
        rt.document.emit('keydown', { key });
        assert.equal(rt.mode(), 'keyboard', key);
    }
});

test('a pointer gesture returns to pointer modality', () => {
    const rt = runtime();
    rt.load();
    rt.document.emit('keydown', { key: 'Tab' });
    rt.document.emit('pointerdown', {});
    assert.equal(rt.mode(), 'pointer');
});

test('typing a regular character does not change pointer modality', () => {
    const rt = runtime();
    rt.load();
    rt.document.emit('keydown', { key: 'a' });
    assert.equal(rt.mode(), 'pointer');
});

test('reloading the module does not duplicate listeners', () => {
    const rt = runtime();
    rt.load();
    rt.load();
    assert.equal(rt.listenerCount('keydown'), 1);
    assert.equal(rt.listenerCount('pointerdown'), 1);
    rt.document.emit('keydown', { key: 'Tab' });
    assert.equal(rt.mode(), 'keyboard');
});
