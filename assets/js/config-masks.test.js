const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// CFG5 — as máscaras do Perfil corporativo são carregadas apenas na página do
// workspace e chamadas por delegação (data-oninput). O módulo não registra
// listeners, então reexecutar/receber HTMX não duplica nada.
function runtime() {
    const window = {};
    const context = vm.createContext({ window, console });
    const source = fs.readFileSync(require.resolve('./config-masks.js'), 'utf8');
    const load = () => vm.runInContext(source, context, { filename: 'config-masks.js' });
    load();
    return { window, load };
}

function input(value) {
    return { value };
}

test('formats CNPJ and CPF while preserving the backend payload', () => {
    const { window } = runtime();

    const cnpj = input('12345678000195');
    window.maskCnpjCpf(cnpj);
    assert.equal(cnpj.value, '12.345.678/0001-95');

    const pasted = input('12.345.678/0001-95');
    window.maskCnpjCpf(pasted);
    assert.equal(pasted.value, '12.345.678/0001-95');

    const cpf = input('12345678901');
    window.maskCnpjCpf(cpf);
    assert.equal(cpf.value, '123.456.789-01');

    const cleared = input('');
    window.maskCnpjCpf(cleared);
    assert.equal(cleared.value, '');
});

test('formats landline and mobile phone numbers', () => {
    const { window } = runtime();

    const landline = input('1132654321');
    window.maskPhone(landline);
    assert.equal(landline.value, '(11) 3265-4321');

    const mobile = input('11987654321');
    window.maskPhone(mobile);
    assert.equal(mobile.value, '(11) 98765-4321');

    const pasted = input('(11) 98765-4321');
    window.maskPhone(pasted);
    assert.equal(pasted.value, '(11) 98765-4321');

    const cleared = input('');
    window.maskPhone(cleared);
    assert.equal(cleared.value, '');
});

test('strips non-digits instead of trusting the visual mask', () => {
    const { window } = runtime();
    const value = input('12a34b56c78d0001-95');
    window.maskCnpjCpf(value);
    assert.equal(value.value, '12.345.678/0001-95');
});

test('re-execution keeps the masks and does not resurrect the photo auto-submit', () => {
    const rt = runtime();
    rt.load();
    const value = input('11987654321');
    rt.window.maskPhone(value);
    assert.equal(value.value, '(11) 98765-4321');
    assert.equal(typeof rt.window.submitProfilePhoto, 'undefined');
});
