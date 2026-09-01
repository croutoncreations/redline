'use strict';

const button = document.querySelector('#pair-button');
const status = document.querySelector('#pair-status');
const fragment = new URLSearchParams(location.hash.slice(1));
const pairingToken = fragment.get('pairing_token') || '';

if (!pairingToken) {
  button.disabled = true;
  status.classList.add('error');
  status.textContent = 'This pairing link is incomplete. Generate a new QR code on your Mac.';
}

button.addEventListener('click', async () => {
  button.disabled = true;
  status.classList.remove('error');
  status.textContent = 'Pairing…';
  try {
    const response = await fetch('/v1/pairing/redeem', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', Accept: 'application/json'},
      body: JSON.stringify({pairing_token: pairingToken}),
    });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      throw new Error(data.error || `HTTP ${response.status}`);
    }
    history.replaceState(null, '', '/pair');
    location.replace('/m');
  } catch (error) {
    status.classList.add('error');
    status.textContent = error.message;
    button.disabled = false;
  }
});
