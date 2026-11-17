const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function element(id) {
    return {
        id,
        hidden: false,
        textContent: '',
        attrs: {},
        listeners: {},
        disabled: false,
        addEventListener(name, fn) { this.listeners[name] = fn; },
        setAttribute(name, value) { this.attrs[name] = String(value); },
        removeAttribute(name) { delete this.attrs[name]; },
    };
}

function runtime(options = {}) {
    const ids = {
        'pwa-install-banner': element('pwa-install-banner'),
        'pwa-install-btn': element('pwa-install-btn'),
        'pwa-close-btn': element('pwa-close-btn'),
        'pwa-install-title': element('pwa-install-title'),
        'pwa-install-text': element('pwa-install-text'),
    };
    ids['pwa-install-banner'].hidden = true;

    const stored = new Map(Object.entries(options.stored || {}));
    const windowListeners = {};
    const windowStub = {
        localStorage: {
            getItem: (key) => (stored.has(key) ? stored.get(key) : null),
            setItem: (key, value) => stored.set(key, String(value)),
        },
        navigator: {
            userAgent: options.userAgent || 'Mozilla/5.0 (X11; Linux x86_64) Chrome/157',
            maxTouchPoints: options.maxTouchPoints || 0,
            serviceWorker: {
                register: () => Promise.resolve(),
            },
        },
        matchMedia: (query) => ({
            matches: query.indexOf('standalone') !== -1 ? Boolean(options.standalone) : false,
            addEventListener: () => {},
        }),
        addEventListener: (name, fn) => { windowListeners[name] = fn; },
        setTimeout: (fn) => { fn(); return 1; },
        contabaseDebugUI: () => {},
        __contabasePWAInstall: false,
    };

    const documentStub = {
        readyState: 'complete',
        getElementById: (id) => ids[id] || null,
        addEventListener: () => {},
    };

    const context = vm.createContext({ window: windowStub, document: documentStub, Promise });
    const source = fs.readFileSync(require.resolve('./pwa-install.js'), 'utf8');
    vm.runInContext(source, context, { filename: 'pwa-install.js' });

    return { window: windowStub, document: documentStub, ids, stored, windowListeners };
}

function promptEvent(outcome = 'accepted') {
    return {
        prevented: false,
        preventDefault() { this.prevented = true; },
        prompt() { return Promise.resolve(); },
        userChoice: Promise.resolve({ outcome }),
    };
}

test('does not invite an app running in standalone mode', () => {
    const rt = runtime({ standalone: true });
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(promptEvent());
    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
    assert.equal(rt.ids['pwa-install-banner'].attrs['data-pwa-mode'], undefined);
});

test('shows the native install button when beforeinstallprompt is available', () => {
    const rt = runtime();
    const event = promptEvent();
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(event);

    assert.equal(event.prevented, true);
    assert.equal(rt.ids['pwa-install-banner'].hidden, false);
    assert.equal(rt.ids['pwa-install-banner'].attrs['data-pwa-mode'], 'native');
    assert.equal(rt.ids['pwa-install-btn'].hidden, false);
});

test('honors a previous dismissal', () => {
    const rt = runtime({ stored: { pwa_banner_dismissed: 'true' } });
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(promptEvent());
    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
});

test('does not invent an invite when the browser has no native prompt', () => {
    const rt = runtime({ userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1' });
    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
});

test('native prompt acceptance calls prompt on click and records installation', async () => {
    const rt = runtime();
    let promptCalls = 0;
    const event = promptEvent('accepted');
    event.prompt = () => { promptCalls += 1; return Promise.resolve(); };
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(event);
    await rt.ids['pwa-install-btn'].listeners.click();

    assert.equal(promptCalls, 1);
    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
    assert.equal(rt.stored.get('pwa_installed'), 'true');
    assert.equal(rt.ids['pwa-install-btn'].disabled, false);
});

test('native prompt dismissal hides the invite without recording installation', async () => {
    const rt = runtime();
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(promptEvent('dismissed'));
    await rt.ids['pwa-install-btn'].listeners.click();

    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
    assert.equal(rt.stored.get('pwa_installed'), undefined);
});

test('appinstalled hides a visible invite', () => {
    const rt = runtime();
    rt.window.ContaBasePWA.handleBeforeInstallPrompt(promptEvent());
    assert.equal(rt.ids['pwa-install-banner'].hidden, false);
    rt.windowListeners.appinstalled();
    assert.equal(rt.ids['pwa-install-banner'].hidden, true);
});
