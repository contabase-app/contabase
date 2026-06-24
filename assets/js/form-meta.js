// Form Meta — extracted from templates/components/form_meta.html (Fase C2)
// CSP-safe external JS: no inline scripts, uses data-* attributes and event delegation.
(function() {
    'use strict';

    window.toggleMetaTab = function(tab) {
        var isCaixinha = tab === 'caixinha';
        var caixinhaForm = document.getElementById('meta-caixinha-form');
        var limiteForm = document.getElementById('meta-limite-form');
        var caixinhaTab = document.getElementById('tab-caixinha');
        var limiteTab = document.getElementById('tab-limite');
        if (!caixinhaForm || !limiteForm || !caixinhaTab || !limiteTab) return;
        caixinhaForm.classList.toggle('hidden', !isCaixinha);
        limiteForm.classList.toggle('hidden', isCaixinha);
        var caixinhaName = caixinhaForm.querySelector('[name="name"]');
        var limiteAmount = limiteForm.querySelector('[name="max_amount_monthly"]');
        if (caixinhaName) caixinhaName.required = isCaixinha;
        if (limiteAmount) limiteAmount.required = !isCaixinha;
        var root = document.querySelector('[data-meta-form-root]');
        if (!root) return;
        var createSubmit = root.querySelector('[data-meta-create-submit]');
        if (createSubmit) {
            createSubmit.setAttribute('form', 'meta-' + tab + '-form');
        }
        caixinhaTab.className = 'h-10 rounded-lg text-xs font-semibold transition-all ' + (isCaixinha ? 'bg-violet-500/20 text-violet-200' : 'text-zinc-500');
        limiteTab.className = 'h-10 rounded-lg text-xs font-semibold transition-all ' + (!isCaixinha ? 'bg-orange-500/20 text-orange-200' : 'text-zinc-500');
    };

    function initMetaSelectPickerItem() {
        window.selectPickerItem = function(id, name, icon, color) {
            var caixinhaForm = document.getElementById('meta-caixinha-form');
            // Only handle if meta forms are in DOM; otherwise let other forms' selectPickerItem handle it.
            if (!caixinhaForm) return;

            var isCaixinha = !caixinhaForm.classList.contains('hidden');
            var suffix = isCaixinha ? '_caixinha' : '_limite';

            var catIdEl = document.getElementById('categoriaId' + suffix);
            if (!catIdEl) return;
            catIdEl.value = id;

            var nomeEl = document.getElementById('categoriaNome' + suffix);
            if (nomeEl) nomeEl.innerText = name;

            var iconEl = document.getElementById('categoriaIcon' + suffix);
            if (iconEl) {
                iconEl.setAttribute('data-lucide', icon);
                iconEl.style.color = color;
            }

            var wrap = document.getElementById('categoriaIconWrap' + suffix);
            if (wrap) {
                wrap.style.borderColor = color;
                wrap.style.backgroundColor = color + '22';
            }

            if (typeof window.refreshIcons === 'function') window.refreshIcons();
        };
    }

    function initMonthPicker() {
        var monthInput = document.getElementById('targetMonthInput');
        if (!monthInput || monthInput.dataset.metaMonthPickerReady) return;
        monthInput.dataset.metaMonthPickerReady = 'true';
        monthInput.addEventListener('click', function() {
            if (typeof monthInput.showPicker === 'function') monthInput.showPicker();
        });
    }

    function isMetaFormInDOM() {
        return !!document.querySelector('[data-meta-form-root]');
    }

    function initMetaForm() {
        if (!isMetaFormInDOM()) return;

        initMetaSelectPickerItem();
        initMonthPicker();

        var article = document.querySelector('[data-meta-form-root]');
        var openTab = article ? article.getAttribute('data-open-tab') : null;
        window.toggleMetaTab(openTab || 'limite');
    }

    // Initialize when meta form is swapped into bottom sheet
    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (e.detail.target && (e.detail.target.id === 'bottom-sheet-container' || e.detail.target.querySelector('[data-meta-form-root]'))) {
            initMetaForm();
        }
    });

    // Run on initial page load if meta form already in DOM
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function() {
            var bs = document.getElementById('bottom-sheet-container');
            if (bs && bs.innerHTML.trim() !== '') {
                initMetaForm();
            }
        });
    } else {
        var bs = document.getElementById('bottom-sheet-container');
        if (bs && bs.innerHTML.trim() !== '') {
            initMetaForm();
        }
    }
})();
