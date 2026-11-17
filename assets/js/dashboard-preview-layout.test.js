const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// Regressão das duas ordens responsivas do Dashboard Preview (R1.3-B10.1).
// O DOM é renderizado na ordem canônica mobile; no desktop o script monta as
// regiões usando os mesmos nós. Estes testes fixam as duas ordens e a
// restauração de foco quando o navegador limpa o foco na troca de breakpoint.

const CANONICAL = ['balance', 'finance', 'cashflow', 'accounts', 'commitments', 'cards', 'categories', 'goals'];
const DESKTOP = ['balance', 'accounts', 'cards', 'commitments', 'finance', 'cashflow', 'categories', 'goals'];
const SCRIPT = fs.readFileSync(require.resolve('./dashboard-preview-layout.js'), 'utf8');

function makeClassList(node) {
    const set = new Set();
    return {
        add(name) { set.add(name); },
        remove(name) { set.delete(name); },
        contains(name) { return set.has(name); },
        get value() { return [...set].join(' '); }
    };
}

function makeNode(tagName, attrs, document) {
    const node = {
        tagName,
        attrs: Object.assign({}, attrs),
        children: [],
        parentNode: null,
        classList: null,
        listeners: {},
        getAttribute(name) { return this.attrs[name] === undefined ? null : this.attrs[name]; },
        setAttribute(name, value) { this.attrs[name] = String(value); },
        removeAttribute(name) { delete this.attrs[name]; },
        addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
        removeEventListener(type, fn) {
            this.listeners[type] = (this.listeners[type] || []).filter(item => item !== fn);
        },
        appendChild(child) { return this.insertBefore(child, null); },
        insertBefore(child, ref) {
            if (child.parentNode) {
                child.parentNode.children = child.parentNode.children.filter(c => c !== child);
            }
            const index = ref ? this.children.indexOf(ref) : -1;
            if (index >= 0) {
                this.children.splice(index, 0, child);
            } else {
                this.children.push(child);
            }
            child.parentNode = this;
            if (document && document.activeElement && child.contains(document.activeElement)) {
                document.activeElement = document.body;
            }
            return child;
        },
        contains(candidate) {
            if (candidate === this) {
                return true;
            }
            let current = candidate ? candidate.parentNode : null;
            while (current) {
                if (current === this) {
                    return true;
                }
                current = current.parentNode;
            }
            return false;
        },
        querySelector(selector) {
            return this.querySelectorAll(selector)[0] || null;
        },
        querySelectorAll(selector) {
            const found = [];
            const match = node => {
                if (selector.startsWith('[') && selector.endsWith(']')) {
                    const inner = selector.slice(1, -1);
                    const equal = inner.indexOf('=');
                    if (equal > 0) {
                        const key = inner.slice(0, equal);
                        const value = inner.slice(equal + 1).replace(/^"|"$/g, '');
                        return node.attrs[key] === value;
                    }
                    return node.attrs[inner] !== undefined;
                }
                return false;
            };
            const walk = node => {
                for (const child of node.children) {
                    if (match(child)) {
                        found.push(child);
                    }
                    walk(child);
                }
            };
            walk(this);
            return found;
        },
        getBoundingClientRect() {
            const height = this.__height || 0;
            return { height, width: 0, top: 0, left: 0, bottom: height, right: 0 };
        },
        focus() {
            if (document) {
                document.activeElement = this;
                let current = this.parentNode;
                while (current) {
                    (current.listeners.focusin || []).forEach(fn => fn({ target: this }));
                    current = current.parentNode;
                }
            }
        }
    };
    node.classList = makeClassList(node);
    return node;
}

function boot(context) {
    const document = {
        activeElement: null,
        listeners: {},
        addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
        removeEventListener(type, fn) {
            this.listeners[type] = (this.listeners[type] || []).filter(item => item !== fn);
        },
        dispatch(type, event) { (this.listeners[type] || []).forEach(fn => fn(event || { target: null })); }
    };
    const root = makeNode('html', {}, document);
    const body = makeNode('body', {}, document);
    root.appendChild(body);
    document.body = body;
    document.documentElement = root;
    document.contains = node => root.contains(node);
    document.getElementById = id => body.querySelectorAll('[id]').find(n => n.attrs.id === id) || null;

    const grid = makeNode('div', { id: 'dashboard-content' }, document);
    grid.classList.add('cbp-grid');
    if (context.historical !== true) {
        grid.classList.add('cbp-grid--contract');
        grid.classList.add('is-layout-pending');
    }
    body.appendChild(grid);

    const left = makeNode('div', { 'data-cbp-region': 'left' }, document);
    const right = makeNode('div', { 'data-cbp-region': 'right' }, document);
    const pair = makeNode('div', { 'data-cbp-region-pair': '' }, document);
    grid.appendChild(left);
    grid.appendChild(right);
    right.appendChild(pair);

    const cards = {};
    for (const key of CANONICAL) {
        const card = makeNode('section', { 'data-cbp-section': key }, document);
        card.__height = context.heights && context.heights[key] ? context.heights[key] : 0;
        cards[key] = card;
        grid.appendChild(card);
    }

    const media = {
        matches: context.desktop,
        handlers: [],
        addEventListener(type, fn) { if (type === 'change') this.handlers.push(fn); },
        removeEventListener(type, fn) { this.handlers = this.handlers.filter(h => h !== fn); },
        setMatches(value) {
            this.matches = value;
            this.handlers.forEach(fn => fn({ matches: value }));
        }
    };
    const window = {
        matchMedia: () => media,
        requestAnimationFrame: fn => fn(),
        setTimeout: fn => { fn(); return 0; },
        addEventListener() {},
        removeEventListener() {}
    };
    function CSSStyleSheet() {
        this.cssText = '';
    }
    CSSStyleSheet.prototype.replaceSync = function (text) {
        this.cssText = text;
    };
    document.adoptedStyleSheets = [];
    const sandbox = { window, document, CSSStyleSheet, console: { log() {}, warn() {}, error() {} }, CustomEvent: function () {} };
    document.activeElement = body;

    vm.runInNewContext(SCRIPT, sandbox);

    return { document, grid, left, right, pair, cards, media };
}

const sections = node => node.querySelectorAll('[data-cbp-section]').map(card => card.getAttribute('data-cbp-section'));
const topPairRule = document => document.adoptedStyleSheets.map(sheet => sheet.cssText).join('');
const cardSection = node => node.querySelectorAll('[data-cbp-section]');

test('desktop monta as regiões na ordem de leitura, sem duplicar cards', () => {
    const { grid, left, right, pair, cards } = boot({ desktop: true });

    assert.deepEqual(sections(left), ['balance', 'accounts', 'cards', 'commitments']);
    assert.deepEqual(sections(right), ['finance', 'cashflow', 'categories', 'goals']);
    assert.deepEqual(sections(pair), ['categories', 'goals']);
    assert.deepEqual(sections(grid), DESKTOP);
    assert.equal(grid.classList.contains('is-regions'), true);
    assert.equal(grid.classList.contains('is-layout-pending'), false);

    for (const key of CANONICAL) {
        assert.equal(cards[key].parentNode !== null, true);
        assert.equal(grid.querySelectorAll(`[data-cbp-section="${key}"]`).length, 1);
    }
});

test('mobile mantém o DOM canônico, sem montar regiões', () => {
    const { grid, left, right, pair } = boot({ desktop: false });

    assert.deepEqual(sections(grid), CANONICAL);
    assert.equal(sections(left).length, 0);
    assert.equal(sections(right).length, 0);
    assert.equal(sections(pair).length, 0);
    assert.equal(grid.classList.contains('is-regions'), false);
    assert.equal(grid.classList.contains('is-layout-pending'), false);
});

test('troca de breakpoint reconstrói e devolve a ordem canônica', () => {
    const { grid, left, right, media } = boot({ desktop: true });
    assert.deepEqual(sections(grid), DESKTOP);

    media.setMatches(false);
    assert.deepEqual(sections(grid), CANONICAL);
    assert.equal(sections(left).length, 0);
    assert.equal(sections(right).length, 0);
    assert.equal(grid.classList.contains('is-regions'), false);

    media.setMatches(true);
    assert.deepEqual(sections(grid), DESKTOP);
    assert.deepEqual(sections(left), ['balance', 'accounts', 'cards', 'commitments']);
    assert.deepEqual(sections(right), ['finance', 'cashflow', 'categories', 'goals']);
});

test('foco ativo é preservado quando o navegador o limpa na troca de breakpoint', () => {
    const { document, grid, cards, media } = boot({ desktop: true });
    const button = makeNode('button', { 'data-privacy-toggle': '' }, document);
    cards.balance.appendChild(button);
    button.focus();
    assert.equal(document.activeElement, button);
    assert.equal(grid.classList.contains('is-regions'), true);

    // Simula o navegador limpando o foco antes de disparar o change.
    document.activeElement = document.body;
    media.setMatches(false);
    assert.equal(document.activeElement, button, 'o foco deve voltar ao controle do card movido');

    document.activeElement = document.body;
    media.setMatches(true);
    assert.equal(document.activeElement, button, 'o foco deve sobreviver ao retorno ao desktop');
});

test('desktop iguala a altura de Saldo e Visão financeira pelo card mais alto', () => {
    const { document } = boot({ desktop: true, heights: { balance: 250, finance: 375.7 } });
    assert.match(topPairRule(document), /#cbp-hero\{min-height:375\.7px\}/);
});

test('desktop iguala pelo Saldo quando ele é o mais alto', () => {
    const { document } = boot({ desktop: true, heights: { balance: 500, finance: 375.7 } });
    assert.match(topPairRule(document), /#cbp-finance\{min-height:500px\}/);
});

test('desktop não aplica min-height quando as alturas já coincidem', () => {
    const { document } = boot({ desktop: true, heights: { balance: 375.7, finance: 375.7 } });
    assert.equal(topPairRule(document), '');
});

test('mobile não aplica min-height e o desktop limpa ao sair', () => {
    const mobile = boot({ desktop: false, heights: { balance: 250, finance: 375.7 } });
    assert.equal(topPairRule(mobile.document), '');

    const { document, media } = boot({ desktop: true, heights: { balance: 250, finance: 375.7 } });
    assert.match(topPairRule(document), /#cbp-hero\{min-height:375\.7px\}/);
    media.setMatches(false);
    assert.equal(topPairRule(document), '');
    media.setMatches(true);
    assert.match(topPairRule(document), /#cbp-hero\{min-height:375\.7px\}/);
});

test('não monta regiões quando o mês é histórico', () => {
    const { grid, left, right } = boot({ desktop: true, historical: true });
    assert.equal(grid.classList.contains('is-regions'), false);
    assert.equal(sections(left).length, 0);
    assert.equal(sections(right).length, 0);
    assert.deepEqual(sections(grid), CANONICAL);
});

test('refresh consecutivo não acumula listeners e recompõe as regiões', () => {
    const { document, grid, media } = boot({ desktop: true });
    assert.equal(media.handlers.length, 1);
    assert.equal(grid.classList.contains('is-regions'), true);

    document.dispatch('htmx:afterSwap', {});
    document.dispatch('htmx:afterSettle', {});
    assert.equal(media.handlers.length, 1, 'eventos repetidos não podem duplicar o listener de mídia');
    assert.equal(grid.classList.contains('is-regions'), true);

    // Simula o settle do HTMX sobrescrevendo a class list do grid.
    grid.classList.remove('is-regions');
    grid.classList.add('is-layout-pending');
    document.dispatch('htmx:afterSettle', {});

    assert.equal(media.handlers.length, 1, 'a recomposição deve remover o listener anterior antes de religar');
    assert.equal(grid.classList.contains('is-regions'), true);
    assert.equal(grid.classList.contains('is-layout-pending'), false);
    assert.deepEqual(sections(grid), DESKTOP);
});
