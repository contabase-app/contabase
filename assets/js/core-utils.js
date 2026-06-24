function getCookieValue(name) {
    var escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    var match = document.cookie.match(new RegExp('(?:^|; )' + escaped + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : '';
}

function csrfToken() {
    return getCookieValue('contabase_csrf');
}

function centerActiveMonth() {
    document.querySelectorAll('[data-month-active="true"]').forEach(function(active) {
        active.scrollIntoView({ behavior: 'auto', block: 'nearest', inline: 'center' });
    });
}

function showGlobalFeedback(message, tone) {
    var box = document.getElementById('global-feedback');
    if (!box) return;
    box.classList.remove('hidden');
    if (tone === 'success') {
        box.className = box.className.replace(/border-rose-[^ ]+/g, '').replace(/bg-rose-[^ ]+/g, '').replace(/text-rose-[^ ]+/g, '');
        box.classList.add('border-emerald-400/30', 'bg-emerald-500/15', 'text-emerald-50');
    } else {
        box.className = box.className.replace(/border-emerald-[^ ]+/g, '').replace(/bg-emerald-[^ ]+/g, '').replace(/text-emerald-[^ ]+/g, '');
        box.classList.add('border-rose-400/30', 'bg-rose-500/15', 'text-rose-50');
    }
    box.textContent = message;
    window.setTimeout(function() {
        if (box.textContent === message) box.classList.add('hidden');
    }, 2600);
}

window.maskMoneyRealTime = function(v) {
    if (!v) return '';
    var val = v.toString().replace(/\D/g, '');
    if (!val) return '';
    var floatVal = parseFloat(val) / 100;
    return floatVal.toLocaleString('pt-BR', { style: 'currency', currency: 'BRL' });
};
