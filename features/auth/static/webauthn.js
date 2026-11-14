// webauthn.js: 管理者認証の画面だけで読み込む、承認済みの唯一のJavaScriptです。
// ブラウザのWebAuthn APIを呼び、結果をサーバーへ渡すだけで、判定はすべてサーバーが行います。
// 端末へのパスキーの状態の知らせ（Signal API）も、サーバーが許した値と場面でだけ行います。
"use strict";

(function () {
  var status = document.getElementById("webauthn-status");

  function show(message) {
    if (status) {
      status.textContent = message;
    }
  }

  function toBuffer(b64url) {
    var b64 = b64url.replace(/-/g, "+").replace(/_/g, "/");
    while (b64.length % 4 !== 0) {
      b64 += "=";
    }
    var bin = atob(b64);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) {
      bytes[i] = bin.charCodeAt(i);
    }
    return bytes.buffer;
  }

  function toB64url(buffer) {
    var bytes = new Uint8Array(buffer);
    var bin = "";
    for (var i = 0; i < bytes.length; i++) {
      bin += String.fromCharCode(bytes[i]);
    }
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  async function post(url, body, csrfToken) {
    var headers = { "Content-Type": "application/json" };
    if (csrfToken) {
      headers["X-CSRF-Token"] = csrfToken;
    }
    var res = await fetch(url, {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
      body: JSON.stringify(body)
    });
    var data = {};
    try {
      data = await res.json();
    } catch (e) {
      data = {};
    }
    if (!res.ok) {
      var err = new Error(data.message || "処理に失敗しました。もう一度お試しください。");
      // 知らせるかは、状態コードではなく本文の符号で判定する（前段の404で外させないため）
      err.code = typeof data.error === "string" ? data.error : "";
      throw err;
    }
    return data;
  }

  // --- 端末へのパスキーの状態の知らせ（Signal API） ---
  // 関数が無いブラウザでは何もしない。失敗しても画面の動きを変えない。

  function isB64url(value) {
    return typeof value === "string" && /^[A-Za-z0-9_-]+$/.test(value);
  }

  function isRpId(value) {
    return typeof value === "string" && /^[A-Za-z0-9.-]+$/.test(value);
  }

  async function settle(promise) {
    // 知らせの完了を待ちすぎて、移動を止めない
    var timer;
    var timeout = new Promise(function (resolve) {
      timer = setTimeout(resolve, 1000);
    });
    try {
      await Promise.race([promise, timeout]);
    } catch (e) {
      // 握りつぶす
    }
    clearTimeout(timer);
  }

  async function signalUnknown(rpId, credentialId) {
    if (typeof PublicKeyCredential.signalUnknownCredential !== "function" || !isRpId(rpId) || !isB64url(credentialId)) {
      return;
    }
    try {
      await settle(PublicKeyCredential.signalUnknownCredential({ rpId: rpId, credentialId: credentialId }));
    } catch (e) {
      // 握りつぶす
    }
  }

  // signalAccepted は、本人の有効なパスキーの一覧を知らせる。空の一覧は「全部外せ」と同じなので、決して渡さない
  async function signalAccepted(accepted) {
    if (typeof PublicKeyCredential.signalAllAcceptedCredentials !== "function" || !accepted) {
      return;
    }
    var ids = accepted.credentialIds;
    if (!isRpId(accepted.rpId) || !isB64url(accepted.userId) ||
        !Array.isArray(ids) || ids.length === 0 || !ids.every(isB64url)) {
      return;
    }
    try {
      await settle(PublicKeyCredential.signalAllAcceptedCredentials({
        rpId: accepted.rpId,
        userId: accepted.userId,
        allAcceptedCredentialIds: ids
      }));
    } catch (e) {
      // 握りつぶす
    }
  }

  function browserError(err) {
    if (err && err.name === "NotAllowedError") {
      return "操作が取り消されたか、時間切れになりました。もう一度お試しください。";
    }
    if (err && err.name === "InvalidStateError") {
      return "この認証器は登録済みです。";
    }
    return (err && err.message) || "処理に失敗しました。もう一度お試しください。";
  }

  if (!window.PublicKeyCredential || !navigator.credentials) {
    show("このブラウザはパスキーに対応していません。最新のブラウザで開いてください。");
    return;
  }

  // 登録: 初回・復旧は登録トークン、ログイン中の追加はCSRFトークンで認可される
  async function register(token, csrfToken, label) {
    show("認証器の操作を待っています…");
    var options = await post("/admin/webauthn/register/begin", { token: token }, csrfToken);
    if (options.accountName) {
      show("「" + options.accountName + "」のパスキーを登録します。認証器の操作を待っています…");
    }
    options.challenge = toBuffer(options.challenge);
    options.user.id = toBuffer(options.user.id);
    options.excludeCredentials = options.excludeCredentials.map(function (c) {
      return { type: c.type, id: toBuffer(c.id) };
    });
    var mayForget = options.signalUnknown === true;
    delete options.signalUnknown;
    var credential = await navigator.credentials.create({ publicKey: options });
    var response = credential.response;
    if (typeof response.getPublicKey !== "function" || typeof response.getAuthenticatorData !== "function") {
      throw new Error("このブラウザは必要な機能に対応していません。最新のブラウザで開いてください。");
    }
    var publicKey = response.getPublicKey();
    if (!publicKey) {
      throw new Error("この認証器の鍵の種類には対応していません。別の認証器を使ってください。");
    }
    var rawId = toB64url(credential.rawId);
    var result;
    try {
      result = await post("/admin/webauthn/register/finish", {
        token: token,
        label: label,
        rawId: rawId,
        clientDataJSON: toB64url(response.clientDataJSON),
        authenticatorData: toB64url(response.getAuthenticatorData()),
        publicKey: toB64url(publicKey),
        publicKeyAlgorithm: response.getPublicKeyAlgorithm()
      }, csrfToken);
    } catch (err) {
      // 同期型を断ったときだけ、端末に残ったパスキーを外させる。
      // credential_exists（登録済みの認証器）では、本物を外させるので決して知らせない
      if (mayForget && err.code === "synced_credential") {
        await signalUnknown(options.rp.id, rawId);
      }
      throw err;
    }
    location.href = result.next;
  }

  // 登録画面: トークンはURLの#以降から読み、すぐにアドレスバーと履歴から消す
  var setupForm = document.getElementById("setup-form");
  if (setupForm) {
    var match = location.hash.match(/^#token=([A-Za-z0-9_-]+)$/);
    var token = match ? match[1] : "";
    history.replaceState(null, "", location.pathname);
    if (!token) {
      show("登録用のURLが正しくありません。受け取ったURLをそのまま開いてください。使えない場合は、管理者に登録URLを発行し直してもらってください。");
      return;
    }
    setupForm.hidden = false;
    setupForm.addEventListener("submit", async function (event) {
      event.preventDefault();
      try {
        await register(token, "", document.getElementById("passkey-label").value);
      } catch (err) {
        show(browserError(err));
      }
    });
  }

  // パスキー管理画面: ログイン中にパスキーを追加する
  var addForm = document.getElementById("passkey-add-form");
  if (addForm) {
    // サーバーが埋め込んだときだけ、本人の有効なパスキーの一覧を端末へ知らせる
    if (addForm.dataset.credentialIds) {
      signalAccepted({
        rpId: addForm.dataset.rpId,
        userId: addForm.dataset.userId,
        credentialIds: addForm.dataset.credentialIds.split(" ")
      });
    }
    addForm.hidden = false;
    addForm.addEventListener("submit", async function (event) {
      event.preventDefault();
      try {
        await register("", addForm.elements.csrf_token.value, document.getElementById("passkey-label").value);
      } catch (err) {
        show(browserError(err));
      }
    });
  }

  // ログイン画面
  var loginButton = document.getElementById("login-button");
  if (loginButton) {
    loginButton.hidden = false;
    loginButton.addEventListener("click", async function () {
      try {
        show("認証器の操作を待っています…");
        var options = await post("/admin/webauthn/login/begin", {});
        options.challenge = toBuffer(options.challenge);
        var credential = await navigator.credentials.get({ publicKey: options });
        var response = credential.response;
        var rawId = toB64url(credential.rawId);
        var result;
        try {
          result = await post("/admin/webauthn/login/finish", {
            rawId: rawId,
            clientDataJSON: toB64url(response.clientDataJSON),
            authenticatorData: toB64url(response.authenticatorData),
            signature: toB64url(response.signature),
            userHandle: response.userHandle ? toB64url(response.userHandle) : null
          });
        } catch (err) {
          // サーバーが「無い」と答えたときだけ、端末にそのパスキーを外させる
          if (err.code === "credential_unknown") {
            await signalUnknown(options.rpId, rawId);
          }
          throw err;
        }
        await signalAccepted(result.accepted);
        location.href = result.next;
      } catch (err) {
        show(browserError(err));
      }
    });
  }
})();
