// Contatos Modal + Input Masks
(function() {
    window.openContatosModal = function(event) {
        if (event) event.stopPropagation();
        var modal = document.getElementById('contatos-modal');
        if (!modal) return;
        modal.classList.remove('hidden');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('contatos-modal');
        if (typeof window.refreshIcons === 'function') window.refreshIcons();
    };

    window.closeContatosModal = function(event) {
        var modal = document.getElementById('contatos-modal');
        if (!modal) return;
        if (!modal.classList.contains('hidden')) {
            modal.classList.add('hidden');
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
        }
        if (event) event.stopPropagation();
    };

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
