var predictiveDescriptionTimer = null;
var predictiveDescriptionAbort = null;

function normalizeSearchText(value) {
    return (value || '').normalize("NFD").replace(/[\u0300-\u036f]/g, '').toLowerCase();
}

function clearPredictiveDescriptionSuggestions() {
    window.clearTimeout(predictiveDescriptionTimer);
    var container = document.getElementById('sugestoes-container');
    if (container) container.innerHTML = '';
}

function loadPredictiveDescriptionSuggestions(input) {
    var container = document.getElementById('sugestoes-container');
    if (!input || !container) return;
    var q = input.value.trim();
    if (q.length < 2) {
        clearPredictiveDescriptionSuggestions();
        return;
    }
    if (predictiveDescriptionAbort) {
        predictiveDescriptionAbort.abort();
    }
    predictiveDescriptionAbort = new AbortController();
    var normalizedQ = normalizeSearchText(q);
    fetch('/transacoes/preditiva?q=' + encodeURIComponent(normalizedQ), {
        headers: { 'HX-Request': 'true', 'X-CSRF-Token': csrfToken() },
        signal: predictiveDescriptionAbort.signal
    })
        .then(function (response) {
            if (!response.ok) throw new Error('predictive request failed');
            return response.text();
        })
        .then(function (html) {
            container.innerHTML = html;
            if (window.htmx) htmx.process(container);
        })
        .catch(function (error) {
            if (error.name !== 'AbortError') clearPredictiveDescriptionSuggestions();
        });
}

function schedulePredictiveDescriptionSuggestions(input) {
    if (input && input.closest && input.closest('#lancamento-form')) return;
    window.clearTimeout(predictiveDescriptionTimer);
    predictiveDescriptionTimer = window.setTimeout(function () {
        loadPredictiveDescriptionSuggestions(input);
    }, 300);
}

// --- Predictive helpers for lancamento form context (Phase D.5.3.4) ---

window.__predictiveFormApplyFlag = false;
var __predictiveFormTimer = null;
var __predictiveFormAbort = null;

window.__clearPredictiveForm = function() {
    window.clearTimeout(__predictiveFormTimer);
    var container = document.getElementById('sugestoes-container');
    if (container) container.innerHTML = '';
};

window.__loadPredictiveForm = function(input) {
    var container = document.getElementById('sugestoes-container');
    if (!input || !container) return;
    var q = input.value.trim();
    if (q.length < 2) {
        window.__clearPredictiveForm();
        return;
    }
    if (__predictiveFormAbort) __predictiveFormAbort.abort();
    __predictiveFormAbort = new AbortController();
    var tipoInput = document.getElementById('tipoLancamento');
    var tipo = tipoInput ? tipoInput.value : '';
    fetch('/transacoes/preditiva?q=' + encodeURIComponent(q) + (tipo ? '&tipo=' + encodeURIComponent(tipo) : ''), {
        headers: { 'HX-Request': 'true' },
        signal: __predictiveFormAbort.signal
    })
        .then(function(response) {
            if (!response.ok) throw new Error('predictive request failed');
            return response.text();
        })
        .then(function(html) {
            container.innerHTML = html;
            if (window.htmx) htmx.process(container);
        })
        .catch(function(error) {
            if (error.name !== 'AbortError') window.__clearPredictiveForm();
        });
};

window.__schedulePredictiveForm = function(input) {
    if (window.__predictiveFormApplyFlag) return;
    window.clearTimeout(__predictiveFormTimer);
    __predictiveFormTimer = window.setTimeout(function() {
        window.__loadPredictiveForm(input);
    }, 300);
};

// Click-away handler for lancamento form (idempotent, Phase D.5.3.4)
if (!window.__lancamentoPredictiveClickAwayHandlerRegistered) {
    window.__lancamentoPredictiveClickAwayHandlerRegistered = true;
    window.__lancamentoPredictiveClickAwayHandler = function(event) {
        var descriptionInput = document.getElementById('descricaoLancamento');
        var container = document.getElementById('sugestoes-container');
        if (!descriptionInput || !container) return;
        if (descriptionInput.contains(event.target) || container.contains(event.target)) return;
        window.__clearPredictiveForm();
    };
    document.addEventListener('click', window.__lancamentoPredictiveClickAwayHandler);
}

// --- Apply suggestion ---

window.applyPredictiveSuggestionFromEvent = function (e) {
    var el = e.target.closest('[data-description]');
    if (el && typeof window.applyPredictiveSuggestion === 'function') {
        window.applyPredictiveSuggestion(el);
    }
};

window.applyPredictiveSuggestion = window.applyPredictiveSuggestion || function (item) {
    var description = document.getElementById('descricaoLancamento');
    var typeInput = document.getElementById('tipoLancamento');
    var originID = document.getElementById('origemContaId');
    var originType = document.getElementById('origemTipo');
    var originLabel = document.getElementById('origemLabel');
    var originName = document.getElementById('origemNome');
    var categoryID = document.getElementById('categoriaId');
    var categoryType = document.getElementById('categoriaType');
    var categoryName = document.getElementById('categoriaNome');
    var originOption = document.querySelector('.origin-option[data-origin-id="' + item.dataset.accountId + '"][data-origin-kind="' + item.dataset.accountKind + '"]');
    var categoryOption = document.querySelector('.category-option[data-category-id="' + item.dataset.categoryId + '"]');

    if (description) description.value = item.dataset.description || '';
    if (typeInput && item.dataset.type) typeInput.value = item.dataset.type;
    if (originID) originID.value = item.dataset.accountId || '';
    if (originType) originType.value = item.dataset.accountKind || 'conta';
    if (originName && originOption) originName.textContent = originOption.dataset.originName || 'Conta de origem';
    if (originLabel) originLabel.textContent = item.dataset.accountKind === 'cartao' ? 'Cartao de credito' : item.dataset.type === 'receita' ? 'Conta de entrada' : 'Conta de origem';
    if (categoryID) categoryID.value = item.dataset.categoryId || '';
    if (categoryType && categoryOption) categoryType.value = categoryOption.dataset.categoryType || '';
    if (categoryName && categoryOption) categoryName.textContent = categoryOption.dataset.categoryName || 'Categoria';

    clearPredictiveDescriptionSuggestions();
};

// Event listeners for description input — routes to form or external handler
document.addEventListener('input', function(event) {
    if (event.target && event.target.id === 'descricaoLancamento') {
        if (event.target.closest && event.target.closest('#lancamento-form')) {
            window.__schedulePredictiveForm && window.__schedulePredictiveForm(event.target);
        } else {
            schedulePredictiveDescriptionSuggestions(event.target);
        }
    }
});

document.addEventListener('focusin', function(event) {
    if (event.target && event.target.id === 'descricaoLancamento') {
        if (event.target.closest && event.target.closest('#lancamento-form')) {
            window.__schedulePredictiveForm && window.__schedulePredictiveForm(event.target);
        } else {
            schedulePredictiveDescriptionSuggestions(event.target);
        }
    }
});
