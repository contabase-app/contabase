(function() {
  function initDropzones() {
    document.querySelectorAll('[data-backup-dropzone]').forEach(function(form) {
      var label = form.querySelector('[data-backup-dropzone-label]');
      var input = form.querySelector('input[type="file"][name="backup_file"]');
      var fileNameEl = form.querySelector('[data-backup-filename]');
      var fileWrap = form.querySelector('[data-backup-file-wrap]');
      var dragTexts = form.querySelectorAll('[data-backup-drag-text]');

      if (!label || !input) return;

      function setDragging(isDragging) {
        if (isDragging) {
          label.classList.add('border-indigo-500', 'bg-indigo-50/20', 'dark:border-indigo-400', 'dark:bg-indigo-950/20');
          label.classList.remove('border-zinc-350', 'bg-zinc-50/10', 'hover:border-zinc-400', 'hover:bg-zinc-100/30', 'dark:border-zinc-700', 'dark:bg-zinc-950/10', 'dark:hover:border-zinc-650', 'dark:hover:bg-zinc-900/10');
        } else {
          label.classList.remove('border-indigo-500', 'bg-indigo-50/20', 'dark:border-indigo-400', 'dark:bg-indigo-950/20');
          label.classList.add('border-zinc-350', 'bg-zinc-50/10', 'hover:border-zinc-400', 'hover:bg-zinc-100/30', 'dark:border-zinc-700', 'dark:bg-zinc-950/10', 'dark:hover:border-zinc-650', 'dark:hover:bg-zinc-900/10');
        }
      }

      label.addEventListener('dragover', function(e) {
        e.preventDefault();
        setDragging(true);
      });

      label.addEventListener('dragleave', function(e) {
        e.preventDefault();
        setDragging(false);
      });

      label.addEventListener('drop', function(e) {
        e.preventDefault();
        setDragging(false);
        if (e.dataTransfer.files.length > 0) {
          var file = e.dataTransfer.files[0];
          if (file.name.toLowerCase().endsWith('.db')) {
            input.files = e.dataTransfer.files;
            input.dispatchEvent(new Event('change', { bubbles: true }));
          }
        }
      });

      input.addEventListener('change', function() {
        var name = input.files[0] ? input.files[0].name : '';
        if (fileNameEl) fileNameEl.textContent = name;
        if (fileWrap) fileWrap.classList.toggle('hidden', !name);
        dragTexts.forEach(function(el) { el.classList.toggle('hidden', !!name); });
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initDropzones);
  } else {
    initDropzones();
  }

  document.body.addEventListener('htmx:afterSettle', function() {
    initDropzones();
  });
})();
