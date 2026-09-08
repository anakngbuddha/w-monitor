// Included only in the generated, same-origin dashboard.js. No bearer is kept
// in URLs, storage, chart state or reusable request headers.
let sessionReady = false;
let sessionLabel = '';
let requestGeneration = 0;
let activeController = null;
let loginPending = false;

try { localStorage.removeItem('wmonitor_api_key'); } catch (_) {}
const cleanLocation = new URL(window.location.href);
cleanLocation.searchParams.delete('api_key');
cleanLocation.searchParams.delete('read_token');
cleanLocation.searchParams.delete('token');
history.replaceState(null, '', cleanLocation.pathname + cleanLocation.search);

function invalidateRequests() {
  requestGeneration++;
  if (activeController) activeController.abort();
  activeController = null;
}

function clearDashboard() {
  for (const chart of [cpuChart, memChart, diskChart, netChart, iopsChart, usersChart]) {
    chart.data.labels = [];
    chart.data.datasets = [];
    chart.update('none');
  }
  document.querySelectorAll('.kpi-value,.kpi-sub,.kpi-avg-peak span,.spec-item span,.chart-badge').forEach(el => { el.textContent = 'No current data'; });
  document.querySelectorAll('.kpi-bar-fill').forEach(el => { el.style.width = '0%'; });
  document.getElementById('procContainer').textContent = '';
  document.getElementById('lastUpdated').textContent = 'No current data';
  document.getElementById('footerStats').textContent = '';
}

function resetSessionView() {
  sessionReady = false;
  sessionLabel = '';
  invalidateRequests();
  clearDashboard();
  currentServer = '';
  const select = document.getElementById('serverSelect');
  select.replaceChildren(new Option('All Servers', ''));
  document.getElementById('apiKeyInput').value = '';
  document.getElementById('statusLabel').textContent = 'Signed out';
  updateTenantBadge();
}

function showAuthModal(message) {
  document.getElementById('authModal').style.display = 'flex';
  const error = document.getElementById('authErrorMsg');
  error.textContent = message || '';
  error.style.display = message ? 'block' : 'none';
  document.getElementById('apiKeyInput').focus();
}

function hideAuthModal() {
  document.getElementById('authModal').style.display = 'none';
  updateTenantBadge();
}

function updateTenantBadge() {
  document.getElementById('tenantBadge').style.display = sessionLabel ? 'flex' : 'none';
  document.getElementById('tenantKeyPreview').textContent = sessionLabel;
}

async function submitAuthKey() {
  if (loginPending) return;
  const input = document.getElementById('apiKeyInput');
  if (!input.value) { showAuthModal('Enter a dashboard read token.'); return; }
  if (location.protocol !== 'https:' && !['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)) {
    input.value = '';
    showAuthModal('HTTPS is required before sending a read token.');
    return;
  }
  loginPending = true;
  resetSessionView();
  // resetSessionView clears the input, so capture the submission separately in
  // the click handler below before clearing the previous tenant's view.
  try {
    const body = JSON.stringify({read_token: submittedToken});
    submittedToken = '';
    const response = await fetch('/api/session', {method: 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error', headers: {'Content-Type': 'application/json'}, body});
    if (!response.ok) throw new Error('Login failed. Check the read token, HTTPS configuration, or rate limit.');
    const session = await response.json();
    sessionReady = true;
    sessionLabel = session.client_name || session.tenant_id || 'Signed in';
    hideAuthModal();
    fetchData();
  } catch (error) {
    showAuthModal(error.message || 'Login failed.');
  } finally {
    submittedToken = '';
    input.value = '';
    loginPending = false;
  }
}

let submittedToken = '';
function submitToken() {
  if (loginPending) return;
  submittedToken = document.getElementById('apiKeyInput').value;
  submitAuthKey();
}

async function logoutApiKey() {
  resetSessionView();
  try {
    const response = await fetch('/api/session', {method: 'DELETE', credentials: 'same-origin', cache: 'no-store', redirect: 'error'});
    if (!response.ok) throw new Error('Logout was not confirmed by the server. Retry before leaving this device.');
    showAuthModal();
    if (logoutChannel) logoutChannel.postMessage('logout');
  } catch (_) {
    showAuthModal('Server logout could not be confirmed. Retry Switch Key when connectivity returns.');
    document.getElementById('tenantBadge').style.display = 'flex';
  }
}

const logoutChannel = typeof BroadcastChannel === 'function' ? new BroadcastChannel('wmonitor-session') : null;
if (logoutChannel) logoutChannel.onmessage = event => { if (event.data === 'logout') { resetSessionView(); showAuthModal(); } };

async function authFetch(url, options = {}) {
  const target = new URL(url, location.origin);
  if (target.origin !== location.origin) throw new Error('Cross-origin request refused');
  const response = await fetch(target, {...options, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: options.signal || activeController?.signal});
  if (response.status === 401 || response.status === 403) {
    resetSessionView();
    showAuthModal('Your session expired or access was revoked. Sign in again.');
    throw new Error('Session unavailable');
  }
  return response;
}

async function bootstrapSession() {
  clearDashboard();
  try {
    const response = await fetch('/api/session', {credentials: 'same-origin', cache: 'no-store', redirect: 'error'});
    if (response.ok) {
      const session = await response.json();
      sessionReady = true;
      sessionLabel = session.client_name || session.tenant_id || 'Signed in';
      hideAuthModal();
      fetchData();
      return;
    }
    const health = await fetch('/api/health', {cache: 'no-store', redirect: 'error'});
    if (health.ok && (await health.json()).hub_mode === false) {
      sessionReady = true;
      fetchData();
      return;
    }
    showAuthModal();
  } catch (_) { showAuthModal('Unable to check the session. Retry when the hub is reachable.'); }
}

document.querySelector('.btn-login').addEventListener('click', submitToken);
document.getElementById('apiKeyInput').addEventListener('keydown', event => { if (event.key === 'Enter') submitToken(); });
document.querySelector('.btn-logout').addEventListener('click', logoutApiKey);
document.getElementById('serverSelect').addEventListener('change', onServerChange);
document.querySelector('.btn-export').addEventListener('click', exportCSV);
for (const range of ['24h', '7d', '30d']) document.getElementById('btn'+range).addEventListener('click', () => setRange(range));
