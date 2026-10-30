// Contatos — controller do bottom sheet Premium, exclusão segura e máscaras.
//
// O sheet em si é o global (#bottom-sheet-container + overlay-stack +
// closeBottomSheet). Este arquivo cuida apenas do escopo da página: foco
// inicial, focus trap, restauração de foco, confirmação de exclusão e o
// tratamento 422 que mantém o formulário aberto.
(function () {
    if (window.__contatosSheetController) return;
    window.__contatosSheetController = true;

    var sheetTrigger = null;
    var pendingCardID = null;
    var pendingListFocus = false;
    var sheetWasOpen = false;

    function sheetContainer() {
        return document.getElementById('bottom-sheet-container');
    }

    function contatosSheet() {
        return document.querySelector('#bottom-sheet-container [data-contatos-sheet]');
    }

    function focusables(root) {
        if (!root || typeof root.querySelectorAll !== 'function') return [];
        return Array.prototype.slice.call(root.querySelectorAll(
            'a[href], button:not([disabled]), select:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        )).filter(function (el) {
            return el.offsetWidth > 0 || el.offsetHeight > 0 || el === document.activeElement;
        });
    }

    function visibleCreateTrigger() {
        var candidates = document.querySelectorAll('[data-contact-create]');
        for (var i = 0; i < candidates.length; i++) {
            if (candidates[i].offsetParent !== null) return candidates[i];
        }
        return null;
    }

    function focusIfVisible(el) {
        if (!el || typeof el.focus !== 'function' || !document.contains(el)) return false;
        if (el.offsetParent === null) return false;
        el.focus({ preventScroll: true });
        return true;
    }

    function restoreFocus() {
        var card = null;
        if (pendingCardID) {
            var id = window.CSS && CSS.escape ? CSS.escape(pendingCardID) : pendingCardID;
            card = document.querySelector('#contato-' + id + ' .cb-contact-action') || document.querySelector('#contato-' + id);
        }
        if (sheetTrigger && focusIfVisible(sheetTrigger)) {
            pendingCardID = null;
            sheetTrigger = null;
            return;
        }
        if (card && focusIfVisible(card)) {
            pendingCardID = null;
            sheetTrigger = null;
            return;
        }
        pendingCardID = null;
        sheetTrigger = null;
        if (focusIfVisible(visibleCreateTrigger())) return;
        focusIfVisible(document.querySelector('.ui-input--search'));
    }

    function focusInitialField() {
        var root = contatosSheet();
        if (!root) return;
        var initial = root.querySelector('[data-contato-sheet-initial]') || root.querySelector('input, select, button');
        if (initial && typeof initial.focus === 'function') initial.focus({ preventScroll: true });
    }

    // --- Fechamento: botão, Escape (overlay-stack) e OOB de sucesso ----------

    function installCloseWrapper() {
        if (window.__contatosSheetCloseWrapped || typeof window.closeBottomSheet !== 'function') return;
        window.__contatosSheetCloseWrapped = true;
        var originalCloseBottomSheet = window.closeBottomSheet;
        window.closeBottomSheet = function () {
            var hadSheet = !!contatosSheet();
            var result = originalCloseBottomSheet.apply(this, arguments);
            if (hadSheet) {
                sheetWasOpen = false;
                restoreFocus();
            }
            return result;
        };
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', installCloseWrapper);
    } else {
        installCloseWrapper();
    }

    document.body.addEventListener('htmx:afterSwap', function (event) {
        installCloseWrapper();
        var target = event.detail && event.detail.target;
        if (target && target.id === 'bottom-sheet-container' && contatosSheet()) {
            sheetWasOpen = true;
            focusInitialField();
        }
    });

    document.body.addEventListener('htmx:afterSettle', function (event) {
        var target = event.detail && event.detail.target;
        if (sheetWasOpen && !contatosSheet()) {
            sheetWasOpen = false;
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
            restoreFocus();
        }
        if (pendingListFocus && target && (target.id === 'contatos-list' || target.id === 'contatos-grid')) {
            pendingListFocus = false;
            if (!focusIfVisible(visibleCreateTrigger())) {
                focusIfVisible(document.querySelector('.ui-input--search'));
            }
        }
    });

    // --- Trigger: guarda o elemento de retorno --------------------------------

    document.addEventListener('click', function (event) {
        var trigger = event.target.closest ? event.target.closest('[hx-target="#bottom-sheet-container"][hx-get]') : null;
        if (!trigger) return;
        var path = (trigger.getAttribute('hx-get') || '').split('?')[0];
        if (path !== '/contatos/novo' && !/^\/contatos\/[^/]+$/.test(path)) return;
        var active = document.activeElement;
        sheetTrigger = active && active !== document.body ? active : trigger;
        pendingCardID = null;
        if (path !== '/contatos/novo') {
            var card = trigger.closest('[id^="contato-"]');
            if (card) pendingCardID = card.id.replace(/^contato-/, '');
        }
    }, true);

    // --- Focus trap local (o overlay-stack segue dono de ESC/histórico) -------

    document.addEventListener('keydown', function (event) {
        if (event.key !== 'Tab') return;
        var root = contatosSheet();
        if (!root) return;
        var confirm = document.getElementById('global-confirm-modal');
        if (confirm && !confirm.classList.contains('hidden')) return;
        var dialog = root.querySelector('[role="dialog"]') || root;
        var items = focusables(dialog);
        if (!items.length) return;
        var first = items[0];
        var last = items[items.length - 1];
        var active = document.activeElement;
        if (event.shiftKey && (active === first || !dialog.contains(active))) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && (active === last || !dialog.contains(active))) {
            event.preventDefault();
            first.focus();
        }
    });

    // --- 422: mantém o sheet aberto e preserva os valores digitados -----------

    document.body.addEventListener('htmx:responseError', function (event) {
        var detail = event.detail || {};
        var form = detail.elt;
        var xhr = detail.xhr;
        if (!form || !form.matches || !form.matches('.cb-v2-contatos-form') || !xhr || xhr.status !== 422) return;
        event.stopImmediatePropagation();

        var response = document.createElement('div');
        response.innerHTML = xhr.responseText || '';
        var message = (response.textContent || '').trim() || 'Não foi possível salvar o contato.';
        var sheet = form.closest('[data-contatos-sheet]');
        var error = sheet ? sheet.querySelector('#contato-form-error') : null;
        if (!error) return;
        error.textContent = message;
        error.classList.remove('hidden');
        error.focus();
    });

    // --- Exclusão: confirmação global + refresh da lista filtrada -------------

    function refreshList() {
        if (typeof htmx === 'undefined') return;
        var url = new URL(window.location.href);
        url.searchParams.set('partial', 'lista');
        htmx.ajax('GET', url.pathname + '?' + url.searchParams.toString(), {
            target: '#contatos-list',
            swap: 'outerHTML'
        });
    }

    function deleteContact(id) {
        if (typeof htmx === 'undefined') return;
        var expected = '/contatos/' + id;
        htmx.ajax('DELETE', expected, { swap: 'none' });
        var handler = function (event) {
            var info = event.detail && event.detail.pathInfo;
            if (!info) return;
            if (info.finalPath !== expected && info.requestPath !== expected) return;
            document.body.removeEventListener('htmx:afterRequest', handler);
            if (!event.detail.successful) return;
            pendingListFocus = true;
            if (typeof window.closeBottomSheet === 'function') window.closeBottomSheet();
            refreshList();
        };
        document.body.addEventListener('htmx:afterRequest', handler);
    }

    document.addEventListener('click', function (event) {
        var button = event.target.closest ? event.target.closest('[data-contato-delete]') : null;
        if (!button) return;
        var id = button.getAttribute('data-contato-delete');
        var name = button.getAttribute('data-contato-name') || 'este contato';
        if (!id || typeof window.openGlobalConfirm !== 'function') return;

        window.openGlobalConfirm('Excluir o contato “' + name + '”? Esta ação não pode ser desfeita.', function () {
            deleteContact(id);
        }, true, 'Excluir contato');

        var confirmButton = document.getElementById('global-confirm-btn');
        if (confirmButton) {
            window.requestAnimationFrame(function () {
                confirmButton.focus({ preventScroll: true });
            });
        }
        document.addEventListener('click', function restore(ev) {
            if (!ev.target.closest || !ev.target.closest('#global-confirm-modal')) return;
            document.removeEventListener('click', restore, true);
            window.requestAnimationFrame(function () {
                focusIfVisible(button);
            });
        }, true);
    });

    // --- Máscaras compartilhadas (também usadas pelo picker de Lançamentos) ---

    function sanitizeNoAccent(value) {
        return (value || '').normalize('NFD').replace(/[\u0300-\u036f]/g, '');
    }

    window.maskCnpjCpfContato = function (input) {
        var clean = sanitizeNoAccent(input.value).replace(/\D/g, '').substring(0, 14);
        if (clean.length <= 11) {
            clean = clean.replace(/(\d{3})(\d)/, '$1.$2').replace(/(\d{3})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d{1,2})$/, '.$1-$2');
        } else {
            clean = clean.replace(/^(\d{2})(\d)/, '$1.$2').replace(/^(\d{2})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d)/, '.$1/$2').replace(/(\d{4})(\d{1,2})$/, '$1-$2');
        }
        input.value = clean;
    };

    window.maskPhoneContato = function (input) {
        var clean = sanitizeNoAccent(input.value).replace(/\D/g, '').substring(0, 11);
        if (clean.length <= 10) {
            clean = clean.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{4})(\d{1,4})$/, '$1-$2');
        } else {
            clean = clean.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{5})(\d{1,4})$/, '$1-$2');
        }
        input.value = clean;
    };
}());
