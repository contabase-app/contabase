const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function runtime(initial = [], help = []) {
    const listeners = new Map();
    const document = {
        addEventListener(name,fn) { if (!listeners.has(name)) listeners.set(name,[]); listeners.get(name).push(fn); },
        querySelectorAll(selector) { return selector === '[data-investment-money]' ? initial : selector === '[data-investment-dial-help]' ? help : []; }, querySelector() { return null; },
    };
    vm.runInNewContext(fs.readFileSync(require.resolve('./investment-create.js'),'utf8'),{document});
    function field(mode = 'positive', dial = true) {
        return { value:'', selectionStart:0, selectionEnd:0, dataset:{investmentMoney:mode},
            matches: s => s === '[data-investment-money]' || dial && s === '[data-investment-money-dial]',
            hasAttribute: s => dial && s === 'data-investment-money-dial',
            setCustomValidity(message) { this.message = message; },
            setSelectionRange(start,end) { this.selectionStart=start; this.selectionEnd=end; },
        };
    }
    function send(el, value, inputType = 'insertFromPaste', data = null, cursor = value.length) {
        for (const fn of listeners.get('beforeinput')) fn({target:el,inputType,data});
        el.value=value; el.selectionStart=cursor; el.selectionEnd=cursor;
        for (const fn of listeners.get('input')) fn({target:el,inputType,data});
        return el.value;
    }
    function type(el,chars) {
        for (const char of chars) {
            const start=el.selectionStart, end=el.selectionEnd;
            send(el,el.value.slice(0,start)+char+el.value.slice(end),'insertText',char,start+1);
        }
        return el.value;
    }
    function blur(el) { for (const fn of listeners.get('blur')) fn({target:el}); return el.value; }
    function remove(el, forward = false) {
        let start=el.selectionStart, end=el.selectionEnd;
        if (start===end) { if (forward) end=Math.min(end+1,el.value.length); else start=Math.max(0,start-1); }
        return send(el,el.value.slice(0,start)+el.value.slice(end),forward?'deleteContentForward':'deleteContentBackward',null,start);
    }
    function focus(el) { for (const fn of listeners.get('focus') || []) fn({target:el}); }
    return {field,send,type,blur,remove,focus};
}

test('progressive digits dial cents and the displayed POST text matches shared Go samples',()=>{
    const samples=require('./testdata/investment-money-b7.json');
    const r=runtime(), progressive=r.field();
    for (const [digit,want] of [['1','R$ 0,01'],['2','R$ 0,12'],['3','R$ 1,23'],['4','R$ 12,34']]) {
        assert.equal(r.type(progressive,digit),want);
        assert.equal(progressive.message,'');
        assert.equal(progressive.selectionStart,want.length);
    }
    for (const sample of samples) {
        for (const mode of ['positive','zero']) {
            const el=r.field(mode);
            assert.equal(r.type(el,sample.typed),sample.display);
            const payload=new URLSearchParams({principal_amount:el.value,amount:el.value});
            assert.equal(payload.get('principal_amount'),sample.display);
            assert.equal(payload.get('amount'),sample.display);
            for(let n=0;n<3;n++) { r.focus(el); assert.equal(r.blur(el),sample.display); }
            assert.equal(el.message,'');
        }
    }
});

test('explicit BR paste, autofill and restored values keep their reais meaning',()=>{
    const r=runtime();
    for (const origin of ['insertFromPaste','insertFromDrop','insertReplacementText','historyRedo',undefined]) {
        for (const [raw,want] of [['3000','R$ 3.000,00'],['300,00','R$ 300,00'],['16.440,41','R$ 16.440,41'],['R$ 16.440,41','R$ 16.440,41'],['R$\u00a016.440,41','R$ 16.440,41'],['300,0','R$ 300,00'],['0,01','R$ 0,01'],['300','R$ 300,00'],['300.001','R$ 300.001,00']]) {
            const el=r.field(); r.send(el,raw,origin); assert.equal(r.blur(el),want); assert.equal(el.message,'');
            assert.equal(r.blur(el),want);
        }
    }
    const restored=r.field(); restored.value='3000';
    runtime([restored]); assert.equal(restored.value,'R$ 3.000,00');
    runtime([restored]); assert.equal(restored.value,'R$ 3.000,00');
});

test('invalid external formats stay invalid through input, deletion and blur',()=>{
    const r=runtime();
    for (const raw of ['300,001','300.00','1,234.56','1.23,45','1.23,','-300','1e3','abc0','1..234','R$ R$ 10,00','1.644,041,00','92233720368547758,08']) {
        const el=r.field(); assert.equal(r.send(el,raw),raw); assert.ok(el.message); assert.equal(r.blur(el),raw); assert.ok(el.message);
        // Adding a third digit repairs the BR thousands group in 300.00.
        if (raw === '300.00') {
            assert.equal(r.send(el,raw+'1','insertText','1'),'R$ 300.001,00');
            assert.equal(el.message,'');
            assert.equal(r.blur(el),'R$ 300.001,00');
        } else {
            assert.equal(r.send(el,raw+'1','insertText','1'),raw+'1'); assert.ok(el.message);
        }
    }
    const el=r.field(); r.send(el,'1.23,45');
    el.selectionStart=0;el.selectionEnd=el.value.length;
    assert.equal(r.type(el,'1'),'R$ 0,01'); assert.equal(el.message,'');
});

test('selection, middle edits, deletion and cursor remain natural',()=>{
    const r=runtime(), el=r.field(); r.type(el,'1234');
    assert.equal(el.value,'R$ 12,34'); assert.equal(el.selectionStart,8);
    el.selectionStart=4; el.selectionEnd=4; r.type(el,'9'); assert.equal(el.value,'R$ 192,34'); assert.equal(el.selectionStart,5);
    r.remove(el); assert.equal(el.value,'R$ 12,34'); assert.equal(el.selectionStart,4);
    r.remove(el,true); assert.equal(el.value,'R$ 1,34'); assert.equal(el.selectionStart,4);
    el.selectionStart=0;el.selectionEnd=el.value.length;r.type(el,'300');assert.equal(el.value,'R$ 3,00');
    r.send(el,'','deleteContentBackward');assert.equal(el.value,'');assert.equal(el.message,'');
    r.send(el,'   ');assert.equal(el.value,'');
});

test('deleting generated separators does not trap the caret; selections replace exact digits',()=>{
    const r=runtime(), el=r.field(); r.type(el,'123456');
    assert.equal(el.value,'R$ 1.234,56');
    el.selectionStart=4;el.selectionEnd=4;
    r.remove(el,true); assert.equal(el.value,'R$ 1.234,56'); assert.equal(el.selectionStart,5);
    r.remove(el,true); assert.equal(el.value,'R$ 134,56');
    r.send(el,'1.234,56'); el.selectionStart=5;el.selectionEnd=8;
    r.type(el,'9'); assert.equal(el.value,'R$ 19,56');
    el.selectionStart=3;el.selectionEnd=el.value.length;r.remove(el);assert.equal(el.value,'');
    assert.equal(r.type(el,'1'),'R$ 0,01');
});

test('backspace can empty the dial, including formatted zero; swapped fields start clean',()=>{
    const r=runtime(), el=r.field();r.type(el,'1234');
    for(const want of ['R$ 1,23','R$ 0,12','R$ 0,01','']) assert.equal(r.remove(el),want);
    assert.equal(el.message,'');
    const zero=r.field('zero');r.type(zero,'0');assert.equal(r.remove(zero),'');
    r.send(el,'1.23,45');assert.ok(el.message);
    const swapped=r.field();assert.equal(r.type(swapped,'1'),'R$ 0,01');assert.equal(swapped.message,'');
    r.type(swapped,',');assert.equal(swapped.value,'R$ 0,01,');assert.ok(swapped.message);
    assert.equal(r.remove(swapped),'R$ 0,01');assert.equal(swapped.message,'');
});

test('zero is allowed only for position/final value; max int64 stays exact',()=>{
    const r=runtime();
    const principal=r.field(), final=r.field('zero');
    r.send(principal,'0,00');assert.ok(principal.message);
    r.send(final,'0,00');assert.equal(final.value,'R$ 0,00');assert.equal(final.message,'');
    r.send(final,'92.233.720.368.547.758,07');assert.equal(final.value,'R$ 92.233.720.368.547.758,07');assert.equal(final.message,'');
    r.send(final,'0001234,56');assert.equal(final.value,'R$ 1.234,56');assert.equal(final.message,'');
    const typed=r.field('zero'); r.type(typed,'9223372036854775807');
    assert.equal(typed.value,'R$ 92.233.720.368.547.758,07');assert.equal(typed.message,'');
    r.type(typed,'8');assert.ok(typed.message);r.remove(typed);assert.equal(typed.message,'');
    const zero=r.field();r.type(zero,'0');assert.equal(zero.value,'R$ 0,00');assert.ok(zero.message);
    r.type(zero,'1');assert.equal(zero.value,'R$ 0,01');assert.equal(zero.message,'');
});

test('only opted-in creation, purchase and valuation fields dial cents; legacy consumers keep reais',()=>{
    const r=runtime(), legacy=r.field('zero',false);
    assert.equal(r.type(legacy,'3000'),'R$ 3.000');
    assert.equal(r.type(legacy,',56'),'R$ 3.000,56');
    assert.equal(r.blur(legacy),'R$ 3.000,56');
    for(const name of ['investimento_form.html','investimento_valuation_form.html']) {
        const html=fs.readFileSync(require('node:path').join(__dirname,'../../templates/components',name),'utf8');
        assert.equal((html.match(/data-investment-money-dial/g)||[]).length,name==='investimento_form.html'?2:1);
        assert.match(html,/aria-describedby="[^"]+"/);
        assert.match(html,/Digite em reais/);
        assert.doesNotMatch(html,/name="(?:principal_cents|amount_cents)"/);
    }
    const help={dataset:{investmentDialHelp:'positive'},textContent:''};runtime([],[help]);
    assert.match(help.textContent,/dois últimos números são centavos/);
    assert.match(help.textContent,/valor deve ser positivo/);
});
