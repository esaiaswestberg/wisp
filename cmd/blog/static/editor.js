(() => {
  const editor = document.querySelector('#editor');
  const form = document.querySelector('#post-form');
  if (!editor || !form) return;
  document.querySelectorAll('[data-command]').forEach((button) => {
    button.addEventListener('click', () => {
      const value = button.dataset.value || null;
      if (button.dataset.command === 'createLink') {
        const url = window.prompt('URL');
        if (url) document.execCommand('createLink', false, url);
      } else {
        document.execCommand(button.dataset.command, false, value);
      }
      editor.focus();
    });
  });
  form.addEventListener('submit', () => {
    document.querySelector('#body-html').value = editor.innerHTML;
  });
})();
