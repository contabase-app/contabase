const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// CFG2 — contrato da camada interativa de Categorias restaurada:
//   - abas Despesas/Receitas alternam os painéis (inclusive repetidamente);
//   - busca filtra a árvore sem perder a categoria correspondente;
//   - combobox de categoria pai abre, filtra por tipo e devolve o ID ao hidden;
//   - trocar de aba reseta o pai para não enviar um pai de outro tipo;
//   - grupos macro oferecidos são apenas os válidos para o tipo;
//   - reinicialização após HTMX liga apenas o novo payload;
//   - recarregar o módulo não duplica listeners.
//
// O runtime abaixo é um DOM mínimo: atributos, classes, dataset, árvore e
// eventos. Ele é suficiente para exercitar o módulo real sem dependências.

let activeDocument = null;

function kebab(name) {
    return name.replace(/([A-Z])/g, '-$1').toLowerCase();
}

function parsePart(part) {
    let negated = null;
    const notMatch = part.match(/:not\(([^)]+)\)/);
    if (notMatch) {
        negated = parsePart(notMatch[1]);
        part = part.replace(notMatch[0], '');
    }

    const attrs = [];
    part = part.replace(/\[([\w-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/g, (_, name, quoted, single, raw) => {
        attrs.push({ name, value: quoted !== undefined ? quoted : single !== undefined ? single : raw });
        return '';
    });

    const classes = [];
    part = part.replace(/\.([\w-]+)/g, (_, name) => {
        classes.push(name);
        return '';
    });

    const tagMatch = part.trim().match(/^[a-zA-Z][\w-]*/);
    return {
        tag: tagMatch ? tagMatch[0].toLowerCase() : null,
        classes,
        attrs,
        negated,
    };
}

function matchesPart(element, part) {
    if (part.negated && matchesPart(element, part.negated)) return false;
    if (part.tag && element.tag !== part.tag) return false;
    for (const name of part.classes) {
        if (!element._classes.has(name)) return false;
    }
    for (const attr of part.attrs) {
        if (!element._attrs.has(attr.name)) return false;
        if (attr.value !== undefined && attr.value !== '' && element._attrs.get(attr.name) !== attr.value) return false;
    }
    return true;
}

function matchesSelector(element, selector) {
    return selector.trim().split(/\s+/).every((part) => matchesPart(element, parsePart(part)));
}

function makeElement(tag, attributes = {}, classes = []) {
    const attrs = new Map(Object.entries(attributes).map(([key, value]) => [key, String(value)]));
    const element = {
        tag: tag.toLowerCase(),
        parent: null,
        children: [],
        ownerDocument: null,
        _attrs: attrs,
        _classes: new Set(classes),
        _listeners: new Map(),
        value: '',
        hidden: false,
        textContent: '',
        focused: false,
        classList: {
            add: (...names) => names.forEach((name) => element._classes.add(name)),
            remove: (...names) => names.forEach((name) => element._classes.delete(name)),
            contains: (name) => element._classes.has(name),
            toggle: (name, force) => {
                const next = force === undefined ? !element._classes.has(name) : Boolean(force);
                if (next) element._classes.add(name);
                else element._classes.delete(name);
                return next;
            },
        },
        setAttribute(name, value) {
            attrs.set(name, String(value));
        },
        getAttribute(name) {
            return attrs.has(name) ? attrs.get(name) : null;
        },
        hasAttribute(name) {
            return attrs.has(name);
        },
        removeAttribute(name) {
            attrs.delete(name);
        },
        appendChild(child) {
            child.parent = element;
            child.ownerDocument = element.ownerDocument;
            element.children.push(child);
            return child;
        },
        addEventListener(name, fn) {
            if (!element._listeners.has(name)) element._listeners.set(name, []);
            if (!element._listeners.get(name).includes(fn)) element._listeners.get(name).push(fn);
        },
        listenerCount(name) {
            return (element._listeners.get(name) || []).length;
        },
        fire(name, extra = {}) {
            const event = Object.assign({
                target: element,
                key: '',
                defaultPrevented: false,
                preventDefault() { this.defaultPrevented = true; },
            }, extra);
            (element._listeners.get(name) || []).forEach((fn) => fn(event));
            return event;
        },
        focus() {
            element.focused = true;
            if (activeDocument) activeDocument.activeElement = element;
        },
        select() {},
        closest(selector) {
            let node = element;
            while (node) {
                if (matchesSelector(node, selector)) return node;
                node = node.parent;
            }
            return null;
        },
        querySelector(selector) {
            return element.querySelectorAll(selector)[0] || null;
        },
        querySelectorAll(selector) {
            const parts = selector.trim().split(/\s+/).map(parsePart);
            const found = [];
            const walk = (node) => {
                node.children.forEach((child) => {
                    if (matchesChain(child, parts, parts.length - 1)) found.push(child);
                    walk(child);
                });
            };
            walk(element);
            return found;
        },
        get dataset() {
            if (!element._dataset) {
                element._dataset = new Proxy({}, {
                    get(_, prop) {
                        if (typeof prop !== 'string') return undefined;
                        const key = 'data-' + kebab(prop);
                        return attrs.has(key) ? attrs.get(key) : undefined;
                    },
                    set(_, prop, value) {
                        attrs.set('data-' + kebab(prop), String(value));
                        return true;
                    },
                    deleteProperty(_, prop) {
                        attrs.delete('data-' + kebab(prop));
                        return true;
                    },
                    has(_, prop) {
                        return attrs.has('data-' + kebab(prop));
                    },
                });
            }
            return element._dataset;
        },
    };
    return element;
}

function matchesChain(node, parts, index) {
    if (!matchesPart(node, parts[index])) return false;
    if (index === 0) return true;
    let parent = node.parent;
    while (parent) {
        if (matchesChain(parent, parts, index - 1)) return true;
        parent = parent.parent;
    }
    return false;
}

function makeSelect(attributes, options) {
    const select = makeElement('select', attributes);
    select.options = options.map((option) => {
        const node = makeElement('option', option.attrs || {}, option.classes || []);
        node.value = option.value;
        node.hidden = Boolean(option.hidden);
        return node;
    });
    return select;
}

// Sanitized fixture mirroring templates/pages/configuracoes_categorias.html.
function buildFixture() {
    const root = makeElement('div', { 'data-cat-root': 'true', 'data-cat-default-tab': 'EXPENSE', 'data-cat-default-macro': 'Essencial', 'data-cat-is-business': 'false' });

    const tabExpense = root.appendChild(makeElement('button', { 'data-cat-tab': 'EXPENSE', 'aria-pressed': 'true' }, ['border-transparent']));
    tabExpense.appendChild(makeElement('span', { 'data-cat-tab-icon': 'true' }));
    const tabIncome = root.appendChild(makeElement('button', { 'data-cat-tab': 'INCOME', 'aria-pressed': 'false' }, ['border-transparent']));
    tabIncome.appendChild(makeElement('span', { 'data-cat-tab-icon': 'true' }));

    const panelExpense = root.appendChild(makeElement('div', { 'data-cat-tab-panel': 'EXPENSE' }));
    const panelIncome = root.appendChild(makeElement('div', { 'data-cat-tab-panel': 'INCOME' }, ['hidden']));

    const parentExpense = panelExpense.appendChild(makeElement('section', { 'data-cat-parent-block': 'true' }));
    const headerExpense = parentExpense.appendChild(makeElement('div', { 'data-cat-parent-header': 'true' }));
    const expenseName = headerExpense.appendChild(makeElement('p', { 'data-cat-name': 'true' }));
    expenseName.textContent = 'Pessoal';
    const toggleExpense = headerExpense.appendChild(makeElement('button', { 'data-cat-toggle': 'true', 'aria-expanded': 'false' }));
    toggleExpense.appendChild(makeElement('span', { 'data-cat-chevron': 'true' }, ['-rotate-90']));
    const childrenExpense = parentExpense.appendChild(makeElement('div', { 'data-cat-children': 'true' }, ['hidden']));
    const rowMercado = childrenExpense.appendChild(makeElement('article', { 'data-cat-name': 'true' }));
    rowMercado.textContent = 'Mercado';
    const rowFarmacia = childrenExpense.appendChild(makeElement('article', { 'data-cat-name': 'true' }));
    rowFarmacia.textContent = 'Farmácia';

    const parentIncome = panelIncome.appendChild(makeElement('section', { 'data-cat-parent-block': 'true' }));
    const headerIncome = parentIncome.appendChild(makeElement('div', { 'data-cat-parent-header': 'true' }));
    const incomeName = headerIncome.appendChild(makeElement('p', { 'data-cat-name': 'true' }));
    incomeName.textContent = 'Salário';
    const toggleIncome = headerIncome.appendChild(makeElement('button', { 'data-cat-toggle': 'true', 'aria-expanded': 'false' }));
    toggleIncome.appendChild(makeElement('span', { 'data-cat-chevron': 'true' }, ['-rotate-90']));
    const childrenIncome = parentIncome.appendChild(makeElement('div', { 'data-cat-children': 'true' }, ['hidden']));
    const rowBonus = childrenIncome.appendChild(makeElement('article', { 'data-cat-name': 'true' }));
    rowBonus.textContent = 'Bônus';

    const typeSelect = makeSelect({ 'data-cat-type-select': 'true' }, [
        { value: 'EXPENSE' },
        { value: 'INCOME' },
    ]);
    typeSelect.value = 'EXPENSE';
    root.appendChild(typeSelect);

    const combobox = root.appendChild(makeElement('div', { 'data-cat-combobox': 'true' }));
    const toggle = combobox.appendChild(makeElement('button', { 'data-cat-combobox-toggle': 'true', 'aria-haspopup': 'listbox', 'aria-expanded': 'false' }));
    const label = toggle.appendChild(makeElement('span', { 'data-cat-combobox-label': 'true' }));
    label.textContent = 'Nenhuma (categoria raiz)';
    const panel = combobox.appendChild(makeElement('div', { 'data-cat-combobox-panel': 'true' }, ['hidden']));
    const search = panel.appendChild(makeElement('input', { 'data-cat-combobox-search': 'true' }));
    const list = panel.appendChild(makeElement('ul'));
    const optionRoot = list.appendChild(makeElement('li', {
        'data-cat-combobox-option': 'true',
        'data-cat-option-value': '',
        'data-cat-option-name': 'Nenhuma (categoria raiz)',
    }));
    const optionExpense = list.appendChild(makeElement('li', {
        'data-cat-combobox-option': 'true',
        'data-cat-option-value': 'cat-exp',
        'data-cat-option-name': 'Pessoal',
        'data-cat-option-type': 'EXPENSE',
    }));
    const optionIncome = list.appendChild(makeElement('li', {
        'data-cat-combobox-option': 'true',
        'data-cat-option-value': 'cat-inc',
        'data-cat-option-name': 'Salário',
        'data-cat-option-type': 'INCOME',
    }));
    const hidden = combobox.appendChild(makeElement('input', { 'data-cat-combobox-hidden': 'true', name: 'parent_id' }));

    const macroSection = root.appendChild(makeElement('div', { 'data-cat-macro-section': 'true' }));
    const macroSelect = makeSelect({ 'data-cat-macro-select': 'true' }, [
        { value: '', attrs: { 'data-cat-macro-for-type': 'EXPENSE,INCOME' } },
        { value: 'Essencial', attrs: { 'data-cat-macro-for-type': 'EXPENSE' } },
        { value: 'Estilo de Vida', attrs: { 'data-cat-macro-for-type': 'EXPENSE' } },
        { value: 'Receitas', attrs: { 'data-cat-macro-for-type': 'INCOME' } },
    ]);
    macroSection.appendChild(macroSelect);
    const subInfo = root.appendChild(makeElement('div', { 'data-cat-sub-info': 'true' }, ['hidden']));

    const searchCategories = root.appendChild(makeElement('input', { 'data-cat-search': 'true' }));
    searchCategories.value = '';

    return {
        root,
        tabExpense,
        tabIncome,
        panelExpense,
        panelIncome,
        parentExpense,
        parentIncome,
        rowMercado,
        rowFarmacia,
        childrenExpense,
        toggleExpense,
        combobox,
        toggle,
        label,
        panel,
        search,
        optionRoot,
        optionExpense,
        optionIncome,
        hidden,
        macroSelect,
        macroSection,
        subInfo,
        searchCategories,
        typeSelect,
    };
}

function createRuntime() {
    const body = makeElement('body');
    const documentListeners = [];
    const documentElement = makeElement('html');
    documentElement.ownerDocument = null;
    documentElement.appendChild(body);

    const document = {
        readyState: 'complete',
        activeElement: body,
        body,
        documentElement,
        addEventListener(name, fn) {
            documentListeners.push({ name, fn });
        },
        querySelectorAll(selector) {
            return documentElement.querySelectorAll(selector);
        },
        querySelector(selector) {
            return documentElement.querySelector(selector);
        },
    };
    documentElement.ownerDocument = document;
    body.ownerDocument = document;
    activeDocument = document;

    const window = {};
    const context = vm.createContext({
        document,
        window,
        console,
        requestAnimationFrame: (fn) => fn(),
        setTimeout: (fn) => fn(),
        Array,
    });

    const source = fs.readFileSync(require.resolve('./categorias-ui.js'), 'utf8');
    const load = () => vm.runInContext(source, context, { filename: 'categorias-ui.js' });
    load();

    return {
        fixture: buildFixture(),
        body,
        document,
        documentListeners,
        loadAgain: load,
        mount(node) {
            body.appendChild(node);
        },
    };
}

function mountFixture() {
    const runtime = createRuntime();
    runtime.mount(runtime.fixture.root);
    // O módulo já executou antes do fixture existir; htmx:afterSwap é o mesmo
    // caminho usado quando o payload chega por HTMX.
    const afterSwap = runtime.body._listeners.get('htmx:afterSwap') || [];
    assert.equal(afterSwap.length, 1, 'one htmx:afterSwap listener');
    afterSwap[0]({ detail: { target: runtime.fixture.root } });
    return runtime;
}

test('switching Despesas/Receitas reveals the matching panel repeatedly', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    assert.equal(fixture.root.dataset.catReady, 'true');
    assert.equal(fixture.panelExpense.classList.contains('hidden'), false);
    assert.equal(fixture.panelIncome.classList.contains('hidden'), true);
    assert.equal(fixture.tabExpense.getAttribute('aria-pressed'), 'true');
    assert.equal(fixture.tabIncome.getAttribute('aria-pressed'), 'false');

    for (let i = 0; i < 3; i += 1) {
        fixture.tabIncome.fire('click');
        assert.equal(fixture.panelIncome.classList.contains('hidden'), false, 'income visible');
        assert.equal(fixture.panelExpense.classList.contains('hidden'), true, 'expense hidden');
        assert.equal(fixture.tabIncome.getAttribute('aria-pressed'), 'true');
        assert.equal(fixture.tabExpense.getAttribute('aria-pressed'), 'false');

        fixture.tabExpense.fire('click');
        assert.equal(fixture.panelExpense.classList.contains('hidden'), false);
        assert.equal(fixture.panelIncome.classList.contains('hidden'), true);
    }
});

test('search filters the category tree and expands matching parents', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    assert.equal(fixture.childrenExpense.classList.contains('hidden'), true, 'parents start collapsed');

    fixture.searchCategories.value = 'merc';
    fixture.searchCategories.fire('input');
    assert.equal(fixture.parentExpense.classList.contains('hidden'), false);
    assert.equal(fixture.rowMercado.classList.contains('hidden'), false);
    assert.equal(fixture.rowFarmacia.classList.contains('hidden'), true);
    assert.equal(fixture.childrenExpense.classList.contains('hidden'), false, 'match expands children');

    fixture.searchCategories.value = '';
    fixture.searchCategories.fire('input');
    assert.equal(fixture.childrenExpense.classList.contains('hidden'), true, 'clearing collapses again');
    assert.equal(fixture.rowMercado.classList.contains('hidden'), false);
});

test('parent combobox opens, filters by type and returns a valid hidden value', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    assert.equal(fixture.optionIncome.classList.contains('hidden'), true, 'INCOME parent hidden while Despesas is active');
    assert.equal(fixture.optionExpense.classList.contains('hidden'), false);

    fixture.toggle.fire('click');
    assert.equal(fixture.panel.classList.contains('hidden'), false);
    assert.equal(fixture.toggle.getAttribute('aria-expanded'), 'true');

    fixture.optionExpense.fire('click');
    assert.equal(fixture.hidden.value, 'cat-exp');
    assert.equal(fixture.label.textContent, 'Pessoal');
    assert.equal(fixture.panel.classList.contains('hidden'), true);
    assert.equal(fixture.macroSection.classList.contains('hidden'), true, 'child inherits macro');
    assert.equal(fixture.subInfo.classList.contains('hidden'), false);

    fixture.toggle.fire('click');
    fixture.search.value = 'sal';
    fixture.search.fire('input');
    assert.equal(fixture.optionIncome.classList.contains('hidden'), true, 'search never reveals a parent of the other type');
    assert.equal(fixture.optionExpense.classList.contains('hidden'), true);
});

test('switching tab resets a parent selected for the previous type', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    fixture.optionExpense.fire('click');
    assert.equal(fixture.hidden.value, 'cat-exp');

    fixture.tabIncome.fire('click');
    assert.equal(fixture.hidden.value, '', 'parent reset when type changes');
    assert.equal(fixture.label.textContent, 'Nenhuma (categoria raiz)');
    assert.equal(fixture.macroSection.classList.contains('hidden'), false);
    assert.equal(fixture.subInfo.classList.contains('hidden'), true);
});

test('macro groups offered are only the valid ones for the active type', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    const visible = () => fixture.macroSelect.options.filter((option) => !option.hidden).map((option) => option.value);
    assert.deepEqual(visible(), ['', 'Essencial', 'Estilo de Vida']);
    assert.equal(fixture.macroSelect.options.some((option) => option.value === 'custom'), false);

    fixture.tabIncome.fire('click');
    assert.deepEqual(visible(), ['', 'Receitas']);
    assert.equal(fixture.macroSelect.value, 'Receitas');
});

test('combobox works from the keyboard', () => {
    const rt = mountFixture();
    const { fixture } = rt;

    fixture.toggle.fire('click');
    const searchEvent = fixture.search.fire('keydown', { key: 'ArrowDown' });
    assert.equal(searchEvent.defaultPrevented, true);
    assert.equal(fixture.optionRoot.focused, true, 'ArrowDown focuses the first visible option');

    fixture.optionExpense.fire('keydown', { key: 'Enter' });
    assert.equal(fixture.hidden.value, 'cat-exp');
    assert.equal(fixture.toggle.focused, true, 'focus returns to the trigger');
});

test('HTMX swap initializes only the replacement payload', () => {
    const rt = mountFixture();
    const firstListeners = rt.fixture.tabIncome.listenerCount('click');

    const replacement = buildFixture();
    rt.mount(replacement.root);
    const afterSwap = rt.body._listeners.get('htmx:afterSwap');
    (rt.body._listeners.get('htmx:afterSwap') || []).forEach((fn) => fn({ detail: { target: replacement.root } }));

    assert.equal(replacement.root.dataset.catReady, 'true');
    assert.equal(replacement.tabIncome.listenerCount('click'), 1);
    replacement.tabIncome.fire('click');
    assert.equal(replacement.panelIncome.classList.contains('hidden'), false);

    assert.equal(rt.fixture.tabIncome.listenerCount('click'), firstListeners, 'old payload keeps its single listener');
});

test('reloading the module does not duplicate listeners', () => {
    const rt = mountFixture();
    const documentClick = rt.documentListeners.filter((entry) => entry.name === 'click').length;
    const documentKeydown = rt.documentListeners.filter((entry) => entry.name === 'keydown').length;
    const tabListeners = rt.fixture.tabIncome.listenerCount('click');

    rt.loadAgain();

    assert.equal(rt.documentListeners.filter((entry) => entry.name === 'click').length, documentClick);
    assert.equal(rt.documentListeners.filter((entry) => entry.name === 'keydown').length, documentKeydown);
    assert.equal(rt.fixture.tabIncome.listenerCount('click'), tabListeners);
});
