const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Contract for the profile password area: one listener per element, one
// afterSwap listener, aria-expanded synced, focus moved into the form and back
// to the trigger on cancel, and safe re-execution after HTMX swaps.
const source = fs.readFileSync(path.join(__dirname, 'perfil-ui.js'), 'utf8');

function classList(initial = []) {
    const values = new Set(initial);
    return {
        add: (key) => values.add(key),
        remove: (key) => values.delete(key),
        contains: (key) => values.has(key),
        toggle: (key, force) => {
            const next = force === undefined ? !values.has(key) : Boolean(force);
            if (next) values.add(key);
            else values.delete(key);
            return next;
        },
    };
}

function element(id, initialClasses = []) {
    const attrs = new Map();
    const listeners = new Map();
    const node = {
        id,
        classList: classList(initialClasses),
        setAttribute: (key, value) => attrs.set(key, String(value)),
        getAttribute: (key) => (attrs.has(key) ? attrs.get(key) : null),
        removeAttribute: (key) => attrs.delete(key),
        addEventListener(name, fn) {
            if (!listeners.has(name)) listeners.set(name, []);
            if (!listeners.get(name).includes(fn)) listeners.get(name).push(fn);
        },
        removeEventListener(name, fn) {
            const list = listeners.get(name) || [];
            const index = list.indexOf(fn);
            if (index >= 0) list.splice(index, 1);
        },
        click() {
            (listeners.get('click') || []).forEach((fn) => fn({ preventDefault() {} }));
        },
        listenerCount(name) {
            return (listeners.get(name) || []).length;
        },
        focusCount: 0,
        resetCount: 0,
        focus() {
            this.focusCount += 1;
        },
        reset() {
            this.resetCount += 1;
        },
    };
    return node;
}

function createRuntime() {
    const firstInput = element('current_password');
    const formArea = element('passwordFormArea', ['hidden']);
    formArea.querySelector = (selector) => {
        if (selector === 'input[name="current_password"]') return firstInput;
        if (selector === 'form') return form;
        return null;
    };
    const toggleBtn = element('togglePasswordForm');
    const cancelBtn = element('cancelPasswordForm');
    const form = element('passwordChangeForm');
    const payload = element('settings-dynamic-payload');

    const bodyListeners = new Map();
    let feedback = null;
    const document = {
        readyState: 'complete',
        getElementById(id) {
            return { togglePasswordForm: toggleBtn, cancelPasswordForm: cancelBtn, passwordFormArea: formArea, passwordChangeForm: form }[id] || null;
        },
        querySelector(selector) {
            return selector === '#passwordFormArea [data-password-feedback]' ? feedback : null;
        },
        addEventListener() {},
        body: {
            addEventListener(name, fn) {
                if (!bodyListeners.has(name)) bodyListeners.set(name, []);
                if (!bodyListeners.get(name).includes(fn)) bodyListeners.get(name).push(fn);
            },
            emit(name, event = {}) {
                (bodyListeners.get(name) || []).forEach((fn) => fn(event));
            },
        },
    };
    const window = {};
    const context = vm.createContext({ document, window, console });

    function load() {
        vm.runInContext(source, context, { filename: 'perfil-ui.js' });
    }

    return {
        context,
        window,
        document,
        firstInput,
        formArea,
        toggleBtn,
        cancelBtn,
        form,
        payload,
        bodyListeners,
        load,
        setFeedback(node) {
            feedback = node;
        },
    };
}

test('initializes the password toggle and exposes closed aria state', () => {
    const rt = createRuntime();
    rt.load();

    assert.equal(rt.toggleBtn.getAttribute('aria-expanded'), 'false');
    assert.equal(rt.toggleBtn.listenerCount('click'), 1);
    assert.equal(rt.cancelBtn.listenerCount('click'), 1);
});

test('opens the form, moves focus into it and exposes expanded state', () => {
    const rt = createRuntime();
    rt.load();

    rt.toggleBtn.click();

    assert.equal(rt.formArea.classList.contains('hidden'), false);
    assert.equal(rt.toggleBtn.getAttribute('aria-expanded'), 'true');
    assert.equal(rt.firstInput.focusCount, 1);
});

test('cancel closes, resets and returns focus to the trigger', () => {
    const rt = createRuntime();
    rt.load();
    rt.toggleBtn.click();

    rt.cancelBtn.click();

    assert.equal(rt.formArea.classList.contains('hidden'), true);
    assert.equal(rt.toggleBtn.getAttribute('aria-expanded'), 'false');
    assert.equal(rt.form.resetCount, 1);
    assert.equal(rt.toggleBtn.focusCount, 1);
});

test('HTMX afterSwap rebinds without duplicating listeners', () => {
    const rt = createRuntime();
    rt.load();
    rt.document.body.emit('htmx:afterSwap', { detail: { target: rt.payload } });
    rt.document.body.emit('htmx:afterSwap', { detail: { target: rt.formArea } });

    assert.equal(rt.toggleBtn.listenerCount('click'), 1);
    assert.equal(rt.cancelBtn.listenerCount('click'), 1);
    assert.equal(rt.bodyListeners.get('htmx:afterSwap').length, 1);
});

test('re-execution is guarded and keeps a single afterSwap listener', () => {
    const rt = createRuntime();
    rt.load();
    rt.load();

    assert.equal(rt.window.__contabasePerfilUIInitialized, true);
    assert.equal(rt.bodyListeners.get('htmx:afterSwap').length, 1);
    assert.equal(rt.toggleBtn.listenerCount('click'), 1);
});

test('reopening after a successful swap keeps aria and focus coherent', () => {
    const rt = createRuntime();
    rt.load();
    rt.toggleBtn.click();
    rt.formArea.classList.add('hidden');
    rt.document.body.emit('htmx:afterSwap', { detail: { target: rt.formArea } });

    assert.equal(rt.toggleBtn.getAttribute('aria-expanded'), 'false');
    rt.toggleBtn.click();
    assert.equal(rt.formArea.classList.contains('hidden'), false);
    assert.equal(rt.toggleBtn.getAttribute('aria-expanded'), 'true');
});

test('afterSwap moves focus to the rendered password feedback', () => {
    const rt = createRuntime();
    rt.load();
    const feedback = element('passwordFormError');
    rt.setFeedback(feedback);

    rt.document.body.emit('htmx:afterSwap', { detail: { target: rt.formArea } });

    assert.equal(feedback.focusCount, 1);
    assert.equal(rt.firstInput.focusCount, 0, 'feedback focus must not move into the first field');
});

test('swaps without feedback keep the current focus', () => {
    const rt = createRuntime();
    rt.load();
    rt.setFeedback(null);

    rt.document.body.emit('htmx:afterSwap', { detail: { target: rt.payload } });

    assert.equal(rt.firstInput.focusCount, 0);
    assert.equal(rt.toggleBtn.focusCount, 0);
});
