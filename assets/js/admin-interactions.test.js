const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// ADM2 — contrato da camada interativa administrativa restaurada:
//   - admin-user-card: Editar/Cancelar alternam view/form e mantêm aria-expanded;
//   - admin-recovery: ações de ADMIN revelam painel + form correspondente,
//     trocam de ação, Cancelar/Escape fecham e limpam confirm_email;
//   - admin-user-workspace-roles: checkbox habilita/desabilita o select de papel;
//   - admin-workspace: tipo Business revela/oculta blocos e limpa campos,
//     Editar/Cancelar alternam o card; máscaras CNPJ/telefone via delegação
//     defensiva (sem window.mask* não quebra);
//   - recarregar os módulos não duplica listeners (guard de documento).

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

function assignOwnerDocument(node, doc) {
    node.ownerDocument = doc;
    node.children.forEach((child) => assignOwnerDocument(child, doc));
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
        defaultValue: '',
        hidden: false,
        checked: false,
        defaultChecked: false,
        disabled: false,
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
            assignOwnerDocument(child, element.ownerDocument);
            element.children.push(child);
            return child;
        },
        addEventListener(name, fn) {
            if (!element._listeners.has(name)) element._listeners.set(name, []);
            if (!element._listeners.get(name).includes(fn)) element._listeners.get(name).push(fn);
        },
        focus() {
            if (element.ownerDocument && element.ownerDocument.activeElement) {
                element.ownerDocument.activeElement.focused = false;
            }
            if (element.ownerDocument) element.ownerDocument.activeElement = element;
            element.focused = true;
        },
        reset() {
            ['input', 'select', 'textarea'].forEach((selector) => {
                element.querySelectorAll(selector).forEach((field) => {
                    const type = (field.getAttribute('type') || '').toLowerCase();
                    if (type === 'checkbox' || type === 'radio') field.checked = Boolean(field.defaultChecked);
                    else field.value = field.defaultValue;
                });
            });
        },
        blur() {
            element.focused = false;
            if (element.ownerDocument) element.ownerDocument.activeElement = element.ownerDocument.body;
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
                    let matches = parts.every((part, index) => {
                        if (!matchesPart(child, part)) return false;
                        let parent = child.parent;
                        for (let i = index - 1; i >= 0; i--) {
                            let ancestorOk = false;
                            while (parent) {
                                if (matchesPart(parent, parts[i])) { ancestorOk = true; break; }
                                parent = parent.parent;
                            }
                            if (!ancestorOk) return false;
                            parent = parent ? parent.parent : null;
                        }
                        return true;
                    });
                    if (matches) found.push(child);
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
                });
            }
            return element._dataset;
        },
    };
    return element;
}

function createRuntime(scripts) {
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

    const sources = scripts.map((file) => fs.readFileSync(require.resolve(file), 'utf8'));
    const load = () => sources.forEach((source, i) => vm.runInContext(source, context, { filename: scripts[i] }));
    load();

    const fireDocument = (name, extra = {}) => {
        const event = Object.assign({ target: body, key: '', preventDefault() {} }, extra);
        documentListeners.filter((l) => l.name === name).forEach((l) => l.fn(event));
        return event;
    };
    const fireAfterSwap = () => body.fire('htmx:afterSwap');

    return {
        window,
        body,
        document,
        documentListeners,
        fireDocument,
        fireAfterSwap,
        loadAgain: load,
        mount(node) {
            body.appendChild(node);
        },
    };
}

const USERS_SCRIPTS = ['./admin-user-card.js', './admin-recovery.js', './admin-user-workspace-roles.js'];
const WS_SCRIPTS = ['./admin-workspace.js', './config-masks.js'];

function buildUserCardFixture() {
    const card = makeElement('article', { 'data-admin-user-card': 'true' });
    const view = card.appendChild(makeElement('div', { 'data-admin-user-view': 'true' }));
    const form = card.appendChild(makeElement('div', { 'data-admin-user-form': 'true' }, ['hidden']));
    const editBtn = card.appendChild(makeElement('button', { 'data-admin-user-edit': 'true', 'aria-expanded': 'false' }));
    const cancelBtn = card.appendChild(makeElement('button', { 'data-admin-user-cancel': 'true' }));
    const nameInput = form.appendChild(makeElement('input', { name: 'name' }));
    nameInput.value = 'Usuária Sintética';
    nameInput.defaultValue = 'Usuária Sintética';
    return { card, view, form, editBtn, cancelBtn, nameInput };
}

test('admin-user-card: Editar abre o formulário e Cancelar restaura a visualização', () => {
    const runtime = createRuntime(USERS_SCRIPTS);
    const fixture = buildUserCardFixture();
    runtime.mount(fixture.card);
    runtime.fireAfterSwap();

    fixture.editBtn.fire('click');
    assert.equal(fixture.form.classList.contains('hidden'), false, 'form deve aparecer');
    assert.equal(fixture.view.classList.contains('hidden'), true, 'view deve sumir');
    assert.equal(fixture.card.classList.contains('ring-1'), true, 'card ganha destaque de edição');
    assert.equal(fixture.editBtn.getAttribute('aria-expanded'), 'true');
    assert.equal(runtime.document.activeElement, fixture.nameInput, 'foco vai para o primeiro campo');

    fixture.nameInput.value = 'Nome alterado sem salvar';
    fixture.cancelBtn.fire('click');
    assert.equal(fixture.form.classList.contains('hidden'), true);
    assert.equal(fixture.view.classList.contains('hidden'), false);
    assert.equal(fixture.editBtn.getAttribute('aria-expanded'), 'false');
    assert.equal(fixture.nameInput.value, 'Usuária Sintética', 'Cancelar descarta a edição');
    assert.equal(runtime.document.activeElement, fixture.editBtn, 'Cancelar devolve o foco ao Editar');

    // Reinicialização pós-HTMX não duplica listeners nem perde o estado.
    runtime.fireAfterSwap();
    runtime.fireAfterSwap();
    assert.equal(fixture.editBtn.listenerCount('click'), 1, 'listener de Editar deve ser único');
    fixture.editBtn.fire('click');
    assert.equal(fixture.form.classList.contains('hidden'), false, 'continua alternando após swaps');
});

test('admin-user-card: recarregar o módulo não duplica listeners de documento', () => {
    const runtime = createRuntime(USERS_SCRIPTS);
    const before = runtime.body.listenerCount('htmx:afterSwap');
    runtime.loadAgain();
    runtime.loadAgain();
    assert.equal(runtime.body.listenerCount('htmx:afterSwap'), before, 'guard de dupla inclusão');
});

function buildRecoveryFixture() {
    const root = makeElement('div', { 'data-admin-recovery-root': 'true', 'data-recovery-action': '' });
    const actionsWrap = root.appendChild(makeElement('div'));
    const btnPassword = actionsWrap.appendChild(makeElement('button', { 'data-admin-recovery-action': 'password', 'aria-expanded': 'false' }));
    const btnSessions = actionsWrap.appendChild(makeElement('button', { 'data-admin-recovery-action': 'sessions', 'aria-expanded': 'false' }));
    const btn2fa = actionsWrap.appendChild(makeElement('button', { 'data-admin-recovery-action': '2fa', 'aria-expanded': 'false' }));

    const panel = root.appendChild(makeElement('div', { 'data-admin-recovery-panel': 'true' }, ['hidden']));
    const infos = {
        password: panel.appendChild(makeElement('p', { 'data-admin-recovery-info': 'password' }, ['hidden'])),
        sessions: panel.appendChild(makeElement('p', { 'data-admin-recovery-info': 'sessions' }, ['hidden'])),
        twofa: panel.appendChild(makeElement('p', { 'data-admin-recovery-info': '2fa' }, ['hidden'])),
    };

    const forms = {};
    const confirms = {};
    for (const action of ['password', 'sessions', '2fa']) {
        const form = panel.appendChild(makeElement('form', { 'data-admin-recovery-form': action }, ['hidden']));
        form.appendChild(makeElement('input', { type: 'hidden', name: 'user_id' }));
        confirms[action] = form.appendChild(makeElement('input', { type: 'email', name: 'confirm_email' }));
        confirms[action].value = 'digitei@exemplo.com';
        form.appendChild(makeElement('button', { 'data-admin-recovery-cancel': 'true' }));
        forms[action] = form;
    }
    return { root, panel, btnPassword, btnSessions, btn2fa, forms, confirms, infos };
}

test('admin-recovery: ações de ADMIN revelam o painel correto sem mutação', () => {
    const runtime = createRuntime(USERS_SCRIPTS);
    const fixture = buildRecoveryFixture();
    runtime.mount(fixture.root);
    runtime.fireAfterSwap();

    runtime.fireDocument('click', { target: fixture.btnPassword });
    assert.equal(fixture.panel.classList.contains('hidden'), false, 'painel aparece');
    assert.equal(fixture.forms.password.classList.contains('hidden'), false, 'form password aparece');
    assert.equal(fixture.forms.sessions.classList.contains('hidden'), true, 'form sessions fica oculto');
    assert.equal(fixture.forms['2fa'].classList.contains('hidden'), true, 'form 2fa fica oculto');
    assert.equal(fixture.infos.password.classList.contains('hidden'), false);
    assert.equal(fixture.infos.sessions.classList.contains('hidden'), true);
    assert.equal(fixture.btnPassword.getAttribute('aria-expanded'), 'true');
    assert.equal(fixture.btnSessions.getAttribute('aria-expanded'), 'false');
    assert.equal(runtime.document.activeElement, fixture.confirms.password, 'foco vai para confirm_email');

    runtime.fireDocument('click', { target: fixture.btnSessions });
    assert.equal(fixture.forms.sessions.classList.contains('hidden'), false);
    assert.equal(fixture.forms.password.classList.contains('hidden'), true);
    assert.equal(fixture.confirms.password.value, '', 'confirm_email do form anterior é limpo');
    assert.equal(fixture.btnSessions.getAttribute('aria-expanded'), 'true');
    assert.equal(fixture.btnPassword.getAttribute('aria-expanded'), 'false');

    runtime.fireDocument('click', { target: fixture.forms.sessions.querySelector('[data-admin-recovery-cancel]') });
    assert.equal(fixture.panel.classList.contains('hidden'), true, 'Cancelar fecha o painel');
    assert.equal(runtime.document.activeElement, fixture.btnSessions, 'Cancelar devolve o foco à ação');

    runtime.fireDocument('click', { target: fixture.btn2fa });
    assert.equal(fixture.panel.classList.contains('hidden'), false);
    assert.equal(runtime.document.activeElement, fixture.confirms['2fa']);

    runtime.window.hasOpenOverlay = () => true;
    runtime.fireDocument('keydown', { key: 'Escape' });
    assert.equal(fixture.panel.classList.contains('hidden'), false, 'Escape não fecha a camada inferior');
    assert.equal(runtime.document.activeElement, fixture.confirms['2fa'], 'foco permanece na camada superior');

    runtime.window.hasOpenOverlay = () => false;
    runtime.fireDocument('keydown', { key: 'Escape' });
    assert.equal(fixture.panel.classList.contains('hidden'), true, 'Escape fecha o painel sem overlay');
    assert.equal(runtime.document.activeElement, fixture.btn2fa, 'Escape devolve o foco à ação');
});

test('admin-recovery: após swap HTMX o painel volta fechado e sem duplicar listeners', () => {
    const runtime = createRuntime(USERS_SCRIPTS);
    const fixture = buildRecoveryFixture();
    runtime.mount(fixture.root);
    runtime.fireAfterSwap();

    runtime.fireDocument('click', { target: fixture.btnPassword });
    assert.equal(fixture.panel.classList.contains('hidden'), false);
    runtime.fireAfterSwap();
    assert.equal(fixture.panel.classList.contains('hidden'), true, 'reset pós-swap');

    const clicks = runtime.documentListeners.filter((l) => l.name === 'click').length;
    const keys = runtime.documentListeners.filter((l) => l.name === 'keydown').length;
    runtime.loadAgain();
    assert.equal(runtime.documentListeners.filter((l) => l.name === 'click').length, clicks, 'guard de dupla inclusão (click)');
    assert.equal(runtime.documentListeners.filter((l) => l.name === 'keydown').length, keys, 'guard de dupla inclusão (keydown)');
});

test('admin-user-workspace-roles: checkbox habilita e desabilita o select de papel', () => {
    const runtime = createRuntime(USERS_SCRIPTS);
    const row = makeElement('div', { 'data-admin-user-workspace-row': 'true' });
    const toggle = row.appendChild(makeElement('input', { 'data-admin-user-workspace-toggle': 'true', type: 'checkbox' }));
    toggle.checked = true;
    const role = row.appendChild(makeElement('select', { 'data-admin-user-workspace-role': 'true', disabled: 'true' }));
    runtime.mount(row);
    runtime.fireAfterSwap();

    assert.equal(role.disabled, false, 'checkbox marcado habilita o papel');
    toggle.checked = false;
    toggle.fire('change');
    assert.equal(role.disabled, true, 'checkbox desmarcado desabilita o papel');
    toggle.checked = true;
    toggle.fire('change');
    assert.equal(role.disabled, false);

    runtime.fireAfterSwap();
    assert.equal(toggle.listenerCount('change'), 1, 'listener único após re-swap');
});

function buildWorkspaceFixtures() {
    const createForm = makeElement('form', { 'data-admin-ws-create': 'true' });
    const typeSelect = createForm.appendChild(makeElement('select', { 'data-admin-ws-type': 'true', name: 'type' }));
    typeSelect.value = 'personal';
    const businessProfile = createForm.appendChild(makeElement('div', { 'data-admin-ws-business': 'true' }, ['hidden']));
    const companyName = businessProfile.appendChild(makeElement('input', { name: 'company_name' }));
    companyName.value = 'Empresa Sintética LTDA';
    const cnpj = businessProfile.appendChild(makeElement('input', { name: 'cnpj_cpf', 'data-admin-mask': 'cnpj' }));
    const logos = createForm.appendChild(makeElement('div', { 'data-admin-ws-business': 'true' }, ['hidden']));

    const card = makeElement('article', { 'data-admin-ws-card': 'true' });
    const view = card.appendChild(makeElement('div', { 'data-admin-ws-view': 'true' }));
    const form = card.appendChild(makeElement('form', { 'data-admin-ws-form': 'true' }, ['hidden']));
    form.appendChild(makeElement('input', { name: 'workspace_id', type: 'hidden' }));
    const editName = form.appendChild(makeElement('input', { name: 'name', type: 'text' }));
    editName.value = 'Workspace Sintético';
    editName.defaultValue = 'Workspace Sintético';
    const editTypeSelect = form.appendChild(makeElement('select', { 'data-admin-ws-type': 'true', name: 'type' }));
    editTypeSelect.value = 'personal';
    editTypeSelect.defaultValue = 'personal';
    const editBusiness = form.appendChild(makeElement('div', { 'data-admin-ws-business': 'true' }, ['hidden']));
    const toggle = card.appendChild(makeElement('button', { 'data-admin-ws-toggle': 'true', 'aria-expanded': 'false' }));
    const toggleText = toggle.appendChild(makeElement('span', { 'data-admin-ws-toggle-text': 'true' }));

    return { createForm, typeSelect, businessProfile, logos, companyName, cnpj, card, view, form, toggle, toggleText, editName, editTypeSelect, editBusiness };
}

test('admin-workspace: tipo Business revela os blocos empresariais e Pessoal os oculta limpando os campos', () => {
    const runtime = createRuntime(WS_SCRIPTS);
    const fixture = buildWorkspaceFixtures();
    runtime.mount(fixture.createForm);
    runtime.fireAfterSwap();

    fixture.typeSelect.value = 'business';
    fixture.typeSelect.fire('change');
    assert.equal(fixture.businessProfile.classList.contains('hidden'), false, 'perfil corporativo aparece');
    assert.equal(fixture.logos.classList.contains('hidden'), false, 'logos aparecem');

    fixture.typeSelect.value = 'personal';
    fixture.typeSelect.fire('change');
    assert.equal(fixture.businessProfile.classList.contains('hidden'), true);
    assert.equal(fixture.logos.classList.contains('hidden'), true);
    assert.equal(fixture.companyName.value, '', 'campos empresariais são limpos ao voltar para Pessoal');
});

test('admin-workspace: Editar/Cancelar alternam o card do workspace', () => {
    const runtime = createRuntime(WS_SCRIPTS);
    const fixture = buildWorkspaceFixtures();
    runtime.mount(fixture.card);
    runtime.fireAfterSwap();

    fixture.toggle.fire('click');
    assert.equal(fixture.form.classList.contains('hidden'), false, 'form de edição aparece');
    assert.equal(fixture.view.classList.contains('hidden'), true);
    assert.equal(fixture.toggleText.textContent, 'Cancelar');
    assert.equal(fixture.toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(runtime.document.activeElement, fixture.editName, 'foco vai para o primeiro campo');

    fixture.editTypeSelect.value = 'business';
    fixture.editTypeSelect.fire('change');
    assert.equal(fixture.editBusiness.classList.contains('hidden'), false, 'Business revela o bloco no formulário');

    fixture.toggle.fire('click');
    assert.equal(fixture.form.classList.contains('hidden'), true);
    assert.equal(fixture.toggleText.textContent, 'Editar');
    assert.equal(fixture.toggle.getAttribute('aria-expanded'), 'false');
    assert.equal(fixture.editTypeSelect.value, 'personal', 'Cancelar restaura o tipo original');
    assert.equal(fixture.editBusiness.classList.contains('hidden'), true, 'Cancelar reoculta o bloco original');
    assert.equal(runtime.document.activeElement, fixture.toggle, 'Cancelar mantém o foco no controle');

    runtime.fireAfterSwap();
    assert.equal(fixture.toggle.listenerCount('click'), 1, 'listener único após re-swap');
});

test('admin-workspace: máscaras CNPJ e telefone aplicam via delegação e degradam sem window.mask*', () => {
    const runtime = createRuntime(WS_SCRIPTS);
    const fixture = buildWorkspaceFixtures();
    runtime.mount(fixture.createForm);
    runtime.fireAfterSwap();

    assert.equal(typeof runtime.window.maskCnpjCpf, 'function', 'config-masks expõe maskCnpjCpf');
    assert.equal(typeof runtime.window.maskPhone, 'function', 'config-masks expõe maskPhone');

    fixture.cnpj.value = '12345678000199';
    runtime.fireDocument('input', { target: fixture.cnpj });
    assert.equal(fixture.cnpj.value, '12.345.678/0001-99');

    const phone = fixture.businessProfile.appendChild(makeElement('input', { name: 'phone', 'data-admin-mask': 'phone' }));
    phone.value = '11999998888';
    runtime.fireDocument('input', { target: phone });
    assert.equal(phone.value, '(11) 99999-8888');

    // Sem a máscara global carregada o input não quebra (delegação defensiva).
    runtime.window.maskPhone = undefined;
    phone.value = '1188887777';
    runtime.fireDocument('input', { target: phone });
    assert.equal(phone.value, '1188887777', 'sem maskPhone o valor permanece cru');

    const before = runtime.documentListeners.filter((l) => l.name === 'input').length;
    runtime.loadAgain();
    assert.equal(runtime.documentListeners.filter((l) => l.name === 'input').length, before, 'guard de dupla inclusão (input)');
});
