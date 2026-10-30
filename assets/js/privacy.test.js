const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// The privacy state is global and shared across pages. These tests pin the
// contract used by Dashboard and Lançamentos: one sessionStorage key, one body
// class, one delegated click listener, and an accessible toggle state that is
// re-applied after HTMX swaps.
function runtime(stored = '0') {
    const bodyClasses = new Set();
    const buttons = [];
    const icons = [];
    const documentListeners = [];
    let refreshCount = 0;

    function makeButton(attributes = {}) {
        const attrs = new Map(Object.entries(attributes));
        const button = {
            dataset: {},
            setAttribute: (name, value) => attrs.set(name, String(value)),
            getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
            hasAttribute: (name) => attrs.has(name),
            removeAttribute: (name) => attrs.delete(name),
            closest: (selector) => (selector.indexOf('data-privacy-toggle') !== -1 && attrs.has('data-privacy-toggle') ? button : null),
        };
        buttons.push(button);
        return button;
    }

    function makeIcon() {
        const attrs = new Map();
        const icon = {
            setAttribute: (name, value) => attrs.set(name, String(value)),
            getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
        };
        icons.push(icon);
        return icon;
    }

    const document = {
        body: {
            classList: {
                toggle: (name, enabled) => {
                    if (enabled) bodyClasses.add(name);
                    else bodyClasses.delete(name);
                },
                contains: (name) => bodyClasses.has(name),
            },
            addEventListener: (name, fn) => documentListeners.push({ name, fn }),
        },
        querySelectorAll: (selector) => {
            if (selector.indexOf('data-privacy-icon') !== -1) return icons;
            if (selector.indexOf('data-privacy-toggle') !== -1) return buttons;
            return [];
        },
        addEventListener: (name, fn) => documentListeners.push({ name, fn }),
    };

    const store = new Map([['contabasePrivacy', stored]]);
    const sessionStorage = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => store.set(key, String(value)),
    };

    const window = {
        refreshIcons: () => {
            refreshCount += 1;
        },
    };

    const context = vm.createContext({ document, sessionStorage, window, console });
    const source = fs.readFileSync(require.resolve('./privacy.js'), 'utf8');
    vm.runInContext(
        source + '\nthis.__privacy = { applyPrivacyState, togglePrivacyState, bindPrivacyDelegation, initPrivacyToggle };',
        context,
        { filename: 'privacy.js' }
    );

    return {
        api: context.__privacy,
        buttons,
        icons,
        makeButton,
        makeIcon,
        documentListeners,
        bodyClasses,
        store,
        refreshCount: () => refreshCount,
    };
}

test('applies the shared privacy state to values and toggles', () => {
    const rt = runtime('1');
    const button = rt.makeButton({ 'data-privacy-toggle': '', 'data-tooltip-hover': '', 'data-tooltip-text': 'Ocultar valores financeiros' });
    const icon = rt.makeIcon();

    rt.api.applyPrivacyState();

    assert.equal(rt.bodyClasses.has('privacy-enabled'), true);
    assert.equal(icon.getAttribute('data-lucide'), 'eye-off');
    assert.equal(button.getAttribute('aria-pressed'), 'true');
    assert.equal(button.getAttribute('aria-label'), 'Mostrar valores financeiros');
    assert.equal(button.getAttribute('data-tooltip-text'), 'Mostrar valores financeiros');
    assert.equal(button.hasAttribute('title'), false);
    assert.equal(rt.refreshCount(), 1);
});

test('restores the visible state without changing the storage format', () => {
    const rt = runtime('0');
    const button = rt.makeButton({ 'data-privacy-toggle': '' });
    const icon = rt.makeIcon();

    rt.api.applyPrivacyState();

    assert.equal(rt.bodyClasses.has('privacy-enabled'), false);
    assert.equal(icon.getAttribute('data-lucide'), 'eye');
    assert.equal(button.getAttribute('aria-pressed'), 'false');
    assert.equal(button.getAttribute('aria-label'), 'Ocultar valores financeiros');
    assert.equal(rt.store.get('contabasePrivacy'), '0');
});

test('toggle persists the existing sessionStorage preference', () => {
    const rt = runtime('0');
    rt.makeButton({ 'data-privacy-toggle': '' });
    rt.makeIcon();

    rt.api.togglePrivacyState();
    assert.equal(rt.store.get('contabasePrivacy'), '1');
    assert.equal(rt.bodyClasses.has('privacy-enabled'), true);

    rt.api.togglePrivacyState();
    assert.equal(rt.store.get('contabasePrivacy'), '0');
    assert.equal(rt.bodyClasses.has('privacy-enabled'), false);
});

test('binds the delegated click listener exactly once', () => {
    const rt = runtime('0');
    const button = rt.makeButton({ 'data-privacy-toggle': '' });
    rt.makeIcon();

    rt.api.bindPrivacyDelegation();
    rt.api.bindPrivacyDelegation();
    rt.api.initPrivacyToggle();
    rt.api.initPrivacyToggle();

    const clicks = rt.documentListeners.filter((entry) => entry.name === 'click');
    assert.equal(clicks.length, 1);

    let prevented = false;
    clicks[0].fn({ target: button, preventDefault: () => { prevented = true; } });
    assert.equal(prevented, true);
    assert.equal(rt.store.get('contabasePrivacy'), '1');
    assert.equal(rt.bodyClasses.has('privacy-enabled'), true);
});

test('re-applies the current state to controls inserted by HTMX', () => {
    const rt = runtime('1');
    rt.makeIcon();
    const swappedButton = rt.makeButton({ 'data-privacy-toggle': '' });

    rt.api.initPrivacyToggle();

    assert.equal(swappedButton.getAttribute('aria-pressed'), 'true');
    assert.equal(swappedButton.dataset.privacyInit, 'true');
    assert.equal(rt.bodyClasses.has('privacy-enabled'), true);
});
