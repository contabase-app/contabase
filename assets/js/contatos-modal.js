// Contatos Modal + Input Masks
(function() {
    var lastTrigger = null;

    window.openContatosModal = function(event) {
        if (event) event.stopPropagation();
        var modal = document.getElementById('contatos-modal');
        if (!modal) return;
        lastTrigger = event && event.currentTarget ? event.currentTarget : document.activeElement;
        var error = document.getElementById('contato-form-error');
        if (error) {
            error.textContent = '';
            error.classList.add('hidden');
        }
        modal.classList.remove('hidden');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('contatos-modal');
        if (typeof window.refreshIcons === 'function') window.refreshIcons();
        window.requestAnimationFrame(function() {
            var firstField = modal.querySelector('input[name="name"]');
            if (firstField) firstField.focus();
        });
    };

    window.closeContatosModal = function(event) {
        var modal = document.getElementById('contatos-modal');
        if (!modal) return;
        if (!modal.classList.contains('hidden')) {
            modal.classList.add('hidden');
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
            if (lastTrigger && typeof lastTrigger.focus === 'function' && document.contains(lastTrigger)) {
                lastTrigger.focus();
            }
            lastTrigger = null;
        }
        if (event) event.stopPropagation();
    };

    document.body.addEventListener('htmx:responseError', function(event) {
        var detail = event.detail || {};
        var form = detail.elt;
        var xhr = detail.xhr;
        if (!form || !form.matches || !form.matches('.cb-v2-contatos-form') || !xhr || xhr.status !== 422) return;
        event.stopImmediatePropagation();

        var response = document.createElement('div');
        response.innerHTML = xhr.responseText || '';
        var message = (response.textContent || '').trim() || 'Não foi possível salvar o contato.';
        var modal = form.closest('#contatos-modal');
        if (modal) {
            var modalError = document.getElementById('contato-form-error');
            if (modalError) {
                modalError.textContent = message;
                modalError.classList.remove('hidden');
                modalError.focus({ preventScroll: true });
            }
            return;
        }

        var inlineError = form.querySelector('.cb-contact-inline-error');
        if (!inlineError) {
            inlineError = document.createElement('div');
            inlineError.className = 'cb-contact-form-error cb-contact-inline-error';
            inlineError.setAttribute('role', 'alert');
            form.prepend(inlineError);
        }
        inlineError.textContent = message;
        inlineError.focus({ preventScroll: true });
    });

    function sanitizeNoAccent(value) {
        return (value || '').normalize('NFD').replace(/[\u0300-\u036f]/g, '');
    }

    window.maskCnpjCpfContato = function(input) {
        var clean = sanitizeNoAccent(input.value).replace(/\D/g, '').substring(0, 14);
        if (clean.length <= 11) {
            clean = clean.replace(/(\d{3})(\d)/, '$1.$2').replace(/(\d{3})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d{1,2})$/, '.$1-$2');
        } else {
            clean = clean.replace(/^(\d{2})(\d)/, '$1.$2').replace(/^(\d{2})\.(\d{3})(\d)/, '$1.$2.$3').replace(/\.(\d{3})(\d)/, '.$1/$2').replace(/(\d{4})(\d{1,2})$/, '$1-$2');
        }
        input.value = clean;
    };

    window.maskPhoneContato = function(input) {
        var clean = sanitizeNoAccent(input.value).replace(/\D/g, '').substring(0, 11);
        if (clean.length <= 10) {
            clean = clean.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{4})(\d{1,4})$/, '$1-$2');
        } else {
            clean = clean.replace(/^(\d{2})(\d)/, '($1) $2').replace(/(\d{5})(\d{1,4})$/, '$1-$2');
        }
        input.value = clean;
    };
})();
