(() => {
  const editor = document.querySelector('#editor');
  const form = document.querySelector('#post-form');
  if (!editor || !form) return;
  const imageButton = document.querySelector('#image-button');
  const imageInput = document.querySelector('#image-input');
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
  if (imageButton && imageInput) {
    imageButton.addEventListener('click', () => imageInput.click());
    imageInput.addEventListener('change', async () => {
      const file = imageInput.files[0];
      if (!file) return;
      imageButton.disabled = true;
      try {
        const csrf = document.querySelector('input[name="csrf"]').value;
        const response = await fetch('/admin/uploads/presign', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf},
          body: JSON.stringify({filename: file.name, content_type: file.type})
        });
        if (!response.ok) throw new Error(await response.text());
        const upload = await response.json();
        const fields = new FormData();
        Object.entries(upload.fields).forEach(([key, value]) => fields.append(key, value));
        fields.append('file', file);
        const uploaded = await fetch(upload.url, {method: 'POST', body: fields});
        if (!uploaded.ok) throw new Error('upload failed');
        document.execCommand('insertHTML', false, `<img src="${upload.public_url}" alt="">`);
        editor.focus();
      } catch (error) {
        window.alert(`Image upload failed: ${error.message}`);
      } finally {
        imageButton.disabled = false;
        imageInput.value = '';
      }
    });
  }
  form.addEventListener('submit', () => {
    document.querySelector('#body-html').value = editor.innerHTML;
  });
})();
