(function() {
  function initSetupPage() {
    var tab = 'new';
    var newBtn = document.getElementById('setup-tab-new');
    var restoreBtn = document.getElementById('setup-tab-restore');
    var newPanel = document.getElementById('setup-panel-new');
    var restorePanel = document.getElementById('setup-panel-restore');
    var fileInput = document.getElementById('setup-backup-file');
    var fileNameEl = document.getElementById('setup-file-name');
    var fileWrap = document.getElementById('setup-file-wrap');

    if (!newBtn || !restoreBtn || !newPanel || !restorePanel) return;

    function update() {
      var isNew = tab === 'new';

      newPanel.classList.toggle('hidden', !isNew);
      restorePanel.classList.toggle('hidden', isNew);

      // New button classes
      if (isNew) {
        newBtn.classList.remove('border-zinc-800', 'bg-zinc-950/50', 'hover:border-zinc-700', 'hover:bg-zinc-950/80');
        newBtn.classList.add('border-violet-500/60', 'bg-violet-500/10', 'shadow-lg', 'shadow-violet-500/5');
      } else {
        newBtn.classList.remove('border-violet-500/60', 'bg-violet-500/10', 'shadow-lg', 'shadow-violet-500/5');
        newBtn.classList.add('border-zinc-800', 'bg-zinc-950/50', 'hover:border-zinc-700', 'hover:bg-zinc-950/80');
      }

      var newIcon = newBtn.querySelector('span > span');
      if (newIcon) {
        if (isNew) {
          newIcon.classList.remove('bg-zinc-900', 'text-zinc-500');
          newIcon.classList.add('bg-violet-500/20', 'text-violet-300');
        } else {
          newIcon.classList.remove('bg-violet-500/20', 'text-violet-300');
          newIcon.classList.add('bg-zinc-900', 'text-zinc-500');
        }
      }

      var newTitle = newBtn.querySelector('p.text-sm');
      if (newTitle) {
        newTitle.classList.toggle('text-white', isNew);
        newTitle.classList.toggle('text-zinc-400', !isNew);
      }

      var newDesc = newBtn.querySelector('p.text-xs');
      if (newDesc) {
        newDesc.classList.toggle('text-zinc-300', isNew);
        newDesc.classList.toggle('text-zinc-500', !isNew);
      }

      // Restore button classes
      if (!isNew) {
        restoreBtn.classList.remove('border-zinc-800', 'bg-zinc-950/50', 'hover:border-zinc-700', 'hover:bg-zinc-950/80');
        restoreBtn.classList.add('border-indigo-500/60', 'bg-indigo-500/10', 'shadow-lg', 'shadow-indigo-500/5');
      } else {
        restoreBtn.classList.remove('border-indigo-500/60', 'bg-indigo-500/10', 'shadow-lg', 'shadow-indigo-500/5');
        restoreBtn.classList.add('border-zinc-800', 'bg-zinc-950/50', 'hover:border-zinc-700', 'hover:bg-zinc-950/80');
      }

      var restoreIcon = restoreBtn.querySelector('span > span');
      if (restoreIcon) {
        if (!isNew) {
          restoreIcon.classList.remove('bg-zinc-900', 'text-zinc-500');
          restoreIcon.classList.add('bg-indigo-500/20', 'text-indigo-300');
        } else {
          restoreIcon.classList.remove('bg-indigo-500/20', 'text-indigo-300');
          restoreIcon.classList.add('bg-zinc-900', 'text-zinc-500');
        }
      }

      var restoreTitle = restoreBtn.querySelector('p.text-sm');
      if (restoreTitle) {
        restoreTitle.classList.toggle('text-white', !isNew);
        restoreTitle.classList.toggle('text-zinc-400', isNew);
      }

      var restoreDesc = restoreBtn.querySelector('p.text-xs');
      if (restoreDesc) {
        restoreDesc.classList.toggle('text-zinc-300', !isNew);
        restoreDesc.classList.toggle('text-zinc-500', isNew);
      }
    }

    newBtn.addEventListener('click', function() { tab = 'new'; update(); });
    restoreBtn.addEventListener('click', function() { tab = 'restore'; update(); });

    if (fileInput && fileNameEl && fileWrap) {
      fileInput.addEventListener('change', function() {
        var name = fileInput.files.length ? fileInput.files[0].name : '';
        fileNameEl.textContent = name;
        fileWrap.classList.toggle('hidden', !name);
      });
    }

    update();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initSetupPage);
  } else {
    initSetupPage();
  }
})();
