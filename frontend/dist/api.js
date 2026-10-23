'use strict';
// Bridges the UI to the Go backend (Wails bindings). Every Go method returns {ok, data, error}.
(function () {
  const go = () => window.go.main.App;
  window.api = {
    state: () => go().State(),
    saveSettings: (s) => go().SaveSettings(s),
    pickFolder: () => go().PickFolder(),
    signIn: (email, password) => go().SignIn(email, password),
    sendCode: (email) => go().SendCode(email),
    resetPassword: (email, otp, password) => go().ResetPassword(email, otp, password),
    signOut: () => go().SignOut(),
    list: () => go().List(),
    push: (id, opts) => go().Push(id, opts || {}),
    pull: (id, opts) => go().Pull(id, { takeLock: true, ...(opts || {}) }),
    releaseLock: (id, force) => go().ReleaseLock(id, !!force),
    takeLock: (id, force) => go().TakeLock(id, !!force),
    deleteRemote: (id) => go().DeleteRemote(id),
    restoreRemote: (id) => go().RestoreRemote(id),
    team: () => go().Team(),
    setProfileAccess: (id, userIds) => go().SetProfileAccess(id, userIds),
    setFolderAccess: (folder, userIds) => go().SetFolderAccess(folder, userIds),
    setMemberRole: (userId, role) => go().SetMemberRole(userId, role),
    openBackups: () => go().OpenBackups(),
    openShardx: () => go().OpenShardx(),
    onProgress: (fn) => window.runtime.EventsOn('progress', fn),
  };
  // Wails injects window.go and window.runtime; wait for them before the UI starts.
  window.apiReady = new Promise((resolve) => {
    const check = () => (window.go && window.go.main && window.runtime ? resolve() : setTimeout(check, 20));
    check();
  });
})();
