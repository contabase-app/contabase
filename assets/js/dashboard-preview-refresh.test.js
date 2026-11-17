const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// Regressão do contrato canônico de atualização do Dashboard V2 (R1.3-D3-B1).
// O grid escuta `refreshFinancials`, busca o próprio endpoint com
// `hx-select="#dashboard-content"` e precisa: (a) preservar mês/faixa vindos da
// URL viva; (b) devolver foco e rolagem após a troca outerHTML; (c) não roubar
// foco de fora do grid.

const SCRIPT = fs.readFileSync(require.resolve('./dashboard-preview-refresh.js'), 'utf8');

function matches(node, selector) {
    return selector.split(',').map(part => part.trim()).filter(Boolean).some(part => {
        if (part.startsWith('[') && part.endsWith(']')) {
            const inner = part.slice(1, -1);
            const equal = inner.indexOf('=');
            if (equal > 0) {
                const key = inner.slice(0, equal);
                const value = inner.slice(equal + 1).replace(/^"|"$/g, '');
                return node.attrs[key] === value;
            }
            return node.attrs[inner] !== undefined;
        }
        return node.tagName.toLowerCase() === part.toLowerCase();
    });
}

function makeClassList() {
    const set = new Set();
    return {
        add(name) { set.add(name); },
        remove(name) { set.delete(name); },
        contains(name) { return set.has(name); },
        get value() { return [...set].join(' '); }
    };
}

function makeNode(tagName, attrs, doc) {
    const node = {
        tagName: tagName.toUpperCase(),
        attrs: Object.assign({}, attrs || {}),
        children: [],
        parentNode: null,
        textContent: '',
        listeners: {},
        getAttribute(name) { return this.attrs[name] === undefined ? null : this.attrs[name]; },
        setAttribute(name, value) {
            this.attrs[name] = String(value);
            if (name === 'id') this.id = String(value);
        },
        hasAttribute(name) { return this.attrs[name] !== undefined; },
        addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
        appendChild(child) {
            child.parentNode = this;
            this.children.push(child);
            return child;
        },
        contains(candidate) {
            if (candidate === this) return true;
            let current = candidate ? candidate.parentNode : null;
            while (current) {
                if (current === this) return true;
                current = current.parentNode;
            }
            return false;
        },
        closest(selector) {
            let current = this;
            while (current) {
                if (matches(current, selector)) return current;
                current = current.parentNode;
            }
            return null;
        },
        querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
        querySelectorAll(selector) {
            const found = [];
            const walk = node => {
                for (const child of node.children) {
                    if (matches(child, selector)) found.push(child);
                    walk(child);
                }
            };
            walk(this);
            return found;
        },
        focus() { doc.activeElement = this; }
    };
    node.classList = makeClassList();
    if (attrs && attrs.class) {
        String(attrs.class).split(/\s+/).filter(Boolean).forEach(name => node.classList.add(name));
    }
    if (attrs && attrs.id) node.id = attrs.id;
    return node;
}

function buildGrid(doc) {
    const grid = makeNode('div', {
        id: 'dashboard-content',
        'data-cbp-refresh-root': '',
        'hx-get': '/?fluxo=6m'
    }, doc);
    const section = makeNode('section', { 'data-cbp-section': 'cashflow' }, doc);
    grid.appendChild(section);
    const link = makeNode('a', { 'aria-controls': 'cbp-flow-card', href: '/?fluxo=12m' }, doc);
    link.textContent = '12M';
    section.appendChild(link);
    return { grid, section, link };
}

function boot(options) {
    const document = {
        activeElement: null,
        listeners: {},
        addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
        dispatch(type, event) { (this.listeners[type] || []).forEach(fn => fn(event || {})); },
        getElementById(id) { return this.root.querySelectorAll('[id]').find(n => n.attrs.id === id) || null; }
    };
    const html = makeNode('html', {}, document);
    const body = makeNode('body', {}, document);
    html.appendChild(body);
    document.documentElement = html;
    document.body = body;
    document.root = html;
    document.contains = node => {
        const walk = current => current.children && current.children.some(child => child === node || walk(child));
        return node ? walk(html) : false;
    };
    document.activeElement = body;

    const window = {
        location: { pathname: options.path || '/', search: options.search || '' },
        scrollX: options.scrollX || 0,
        scrollY: options.scrollY || 0,
        scrollCalls: [],
        scrollTo(x, y) { this.scrollX = x; this.scrollY = y; this.scrollCalls.push([x, y]); },
        setTimeout(fn) { fn(); return 0; },
        clearTimeout() {}
    };

    const initial = buildGrid(document);
    body.appendChild(initial.grid);

    vm.runInNewContext(SCRIPT, { window, document, URLSearchParams, console: { log() {}, warn() {}, error() {} } });

    return { document, body, window, initial };
}

test('o refresh usa a URL viva, preservando mês e faixa escolhidos', () => {
    const { document, window, initial } = boot({ path: '/', search: '?mes=2026-10&fluxo=12m' });
    const detail = { elt: initial.grid, path: '/?fluxo=6m' };
    document.dispatch('htmx:configRequest', { detail });

    assert.equal(detail.path, '/?mes=2026-10&fluxo=12m');
    assert.equal(window.location.search, '?mes=2026-10&fluxo=12m');
});

test('outros elementos não têm a URL reescrita', () => {
    const { document, initial } = boot({ search: '?fluxo=12m' });
    const detail = { elt: initial.link, path: '/faturas' };
    document.dispatch('htmx:configRequest', { detail });
    assert.equal(detail.path, '/faturas');
});

test('foco e rolagem voltam para o card equivalente após a troca do grid', () => {
    const { document, body, window, initial } = boot({ scrollX: 0, scrollY: 640 });
    initial.link.focus();
    assert.equal(document.activeElement, initial.link);

    document.dispatch('htmx:beforeSwap', { detail: { target: initial.grid } });

    // Simula a troca outerHTML: o grid antigo sai e um novo entra. O Chrome
    // mantém o foco no nó destacado até o próximo frame.
    body.children = [];
    const next = buildGrid(document);
    body.appendChild(next.grid);

    document.dispatch('htmx:afterSwap', { detail: { target: next.grid } });

    assert.equal(document.activeElement, next.link);
    assert.equal(next.link.getAttribute('aria-controls'), 'cbp-flow-card');
    assert.equal(window.scrollY, 640);
});

test('foco fora do grid não é sequestrado pelo refresh', () => {
    const { document, body, initial } = boot({});
    const outside = makeNode('input', { id: 'busca' }, document);
    body.appendChild(outside);
    outside.focus();

    document.dispatch('htmx:beforeSwap', { detail: { target: initial.grid } });

    body.children = [];
    const next = buildGrid(document);
    body.appendChild(next.grid);
    outside.parentNode = null;
    body.appendChild(outside);
    document.activeElement = outside;

    document.dispatch('htmx:afterSwap', { detail: { target: next.grid } });

    assert.equal(document.activeElement, outside);
});

test('refresh nunca mantém o gate anti-flash do primeiro paint', () => {
    const { document, initial } = boot({});
    initial.grid.classList.add('is-layout-pending');
    document.dispatch('htmx:afterSettle', { detail: { target: initial.grid } });
    assert.equal(initial.grid.classList.contains('is-layout-pending'), false);
});

test('swap que não é do grid não captura nem restaura foco', () => {
    const { document, initial } = boot({ scrollY: 100 });
    initial.link.focus();
    const other = { id: 'global-feedback' };

    document.dispatch('htmx:beforeSwap', { detail: { target: other } });
    document.activeElement = document.body;
    document.dispatch('htmx:afterSwap', { detail: { target: other } });

    assert.equal(document.activeElement, document.body);
});


test('navegação mensal mantém a faixa alterada por HTMX fora do grid', () => {
    const { document, body, window } = boot({ search: '?fluxo=12m' });
    const previous = makeNode('a', { 'data-cbp-month-link': '', href: '/?mes=2026-10' }, document);
    const current = makeNode('a', { 'data-cbp-month-link': '', href: '/' }, document);
    body.appendChild(previous);
    body.appendChild(current);

    document.dispatch('htmx:pushedIntoHistory');
    assert.equal(previous.getAttribute('href'), '/?mes=2026-10&fluxo=12m');
    assert.equal(current.getAttribute('href'), '/?fluxo=12m');

    window.location.search = '?mes=2026-10';
    document.dispatch('htmx:pushedIntoHistory');
    assert.equal(previous.getAttribute('href'), '/?mes=2026-10');
    assert.equal(current.getAttribute('href'), '/');
    assert.equal(document.listeners['htmx:pushedIntoHistory'].length, 1);
});
