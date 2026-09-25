function getAuthToken() {
  return sessionStorage.getItem('kube_token') || '';
}

function toggleAuthModal() {
  const modal = document.getElementById('auth-modal');
  const input = document.getElementById('token-input');
  const clearBtn = document.getElementById('btn-clear-token');
  const msg = document.getElementById('auth-msg');
  msg.className = 'modal-msg';
  msg.innerText = '';

  const token = getAuthToken();
  if (token) {
    input.value = token;
    clearBtn.style.display = 'inline-flex';
  } else {
    input.value = '';
    clearBtn.style.display = 'none';
  }
  modal.classList.add('active');
  input.focus();
}

function closeAuthModal() {
  document.getElementById('auth-modal').classList.remove('active');
}

async function submitToken() {
  const token = document.getElementById('token-input').value.trim();
  const msg = document.getElementById('auth-msg');
  if (!token) {
    msg.className = 'modal-msg error';
    msg.innerText = 'Please enter a token';
    return;
  }
  msg.className = 'modal-msg';
  msg.innerText = 'Validating token with cluster apiserver...';
  msg.style.display = 'block';

  try {
    const res = await fetch('/api/auth/verify', {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + token }
    });
    const data = await res.json();
    if (res.ok && data.authenticated) {
      sessionStorage.setItem('kube_token', token);
      msg.className = 'modal-msg success';
      msg.innerText = 'Token verified! Telemetry unlocked.';
      setTimeout(() => {
        closeAuthModal();
        if (typeof loadStatus === 'function') loadStatus();
      }, 500);
    } else {
      msg.className = 'modal-msg error';
      msg.innerText = 'Invalid token: ' + (data.reason || 'Permission denied');
    }
  } catch (err) {
    msg.className = 'modal-msg error';
    msg.innerText = 'Verification request failed: ' + err.message;
  }
}

function clearToken() {
  sessionStorage.removeItem('kube_token');
  document.getElementById('token-input').value = '';
  closeAuthModal();
  if (typeof loadStatus === 'function') loadStatus();
}
