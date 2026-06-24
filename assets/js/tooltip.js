function ensureGlobalTooltip() {
    var tip = document.getElementById('global-tooltip');
    if (tip) return tip;
    tip = document.createElement('div');
    tip.id = 'global-tooltip';
    tip.className = 'fixed z-[100] hidden max-w-[18rem] rounded-lg border border-zinc-200 bg-white px-2 py-1.5 text-[0.68rem] leading-snug text-zinc-600 shadow-2xl dark:border-white/10 dark:bg-[#141820] dark:text-white/75 dark:shadow-black/60';
    document.body.appendChild(tip);
    return tip;
}

function hideGlobalTooltip() {
    var tip = document.getElementById('global-tooltip');
    if (!tip) return;
    tip.classList.add('hidden');
    tip.textContent = '';
}

function showGlobalTooltip(trigger, text) {
    if (!trigger || !text) return;
    var tip = ensureGlobalTooltip();
    tip.textContent = text;
    tip.classList.remove('hidden');
    var rect = trigger.getBoundingClientRect();
    var top = rect.bottom + 10;
    var left = rect.left;
    var maxLeft = window.innerWidth - tip.offsetWidth - 10;
    if (left > maxLeft) left = maxLeft;
    if (left < 10) left = 10;
    if (top + tip.offsetHeight > window.innerHeight - 8) {
        top = Math.max(10, rect.top - tip.offsetHeight - 10);
    }
    tip.style.top = top + 'px';
    tip.style.left = left + 'px';
}

function resolveTooltipText(btn) {
    if (btn.dataset.tooltipText) return btn.dataset.tooltipText;
    if (btn.getAttribute('title')) return btn.getAttribute('title');
    var scope = btn.closest('article, section, div') || btn.parentElement;
    var local = (scope ? scope.querySelector('[data-tooltip]') : null) || btn.nextElementSibling;
    if (local && local.textContent) return local.textContent.trim();
    return '';
}

function initClickTooltips(root) {
    (root || document).querySelectorAll('[data-tooltip-trigger]').forEach(function(btn) {
        if (btn.dataset.tooltipInit === 'true') return;
        btn.dataset.tooltipInit = 'true';
        btn.addEventListener('click', function(event) {
            event.stopPropagation();
            var text = resolveTooltipText(btn);
            var tip = document.getElementById('global-tooltip');
            if (tip && !tip.classList.contains('hidden') && tip.textContent === text) {
                hideGlobalTooltip();
                return;
            }
            showGlobalTooltip(btn, text);
        });
    });
}
