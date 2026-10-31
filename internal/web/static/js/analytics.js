// アクセス解析の同意。同意するまで gtag.js を読み込まない。JSが動かなければ案内も計測も無い。
(function () {
  "use strict";
  var script = document.currentScript;
  var id = script && script.dataset.ga4Id;
  var banner = document.getElementById("analytics-consent");
  if (!id || !banner) {
    return;
  }
  var key = "mwk-analytics-consent";
  var status = document.getElementById("analytics-consent-status");
  var settings = document.querySelector("[data-analytics-settings]");
  var loaded = false;
  var opener = null;

  function read() {
    try {
      var value = window.localStorage.getItem(key);
      return value === "granted" || value === "denied" ? value : "";
    } catch (e) {
      return null;
    }
  }

  function save(value) {
    try {
      window.localStorage.setItem(key, value);
      return window.localStorage.getItem(key) === value;
    } catch (e) {
      return false;
    }
  }

  function load() {
    window["ga-disable-" + id] = false;
    if (loaded) {
      return;
    }
    loaded = true;
    window.dataLayer = window.dataLayer || [];
    window.gtag = function () {
      window.dataLayer.push(arguments);
    };
    window.gtag("js", new Date());
    window.gtag("config", id, {
      allow_google_signals: false,
      allow_ad_personalization_signals: false,
      cookie_domain: "none"
    });
    var tag = document.createElement("script");
    tag.async = true;
    tag.src = "https://www.googletagmanager.com/gtag/js?id=" + encodeURIComponent(id);
    document.head.appendChild(tag);
  }

  // 計測を止めてCookieを消す。cookie_domain は "none" なので、Domain を付けずに消す。
  // ブラウザ側で Domain が付いた場合に備えて、このホストの指定でも消す
  function stop() {
    window["ga-disable-" + id] = true;
    document.cookie.split(";").forEach(function (item) {
      var name = item.split("=")[0].trim();
      if (name.indexOf("_ga") === 0) {
        var expired = name + "=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/";
        document.cookie = expired + "; domain=" + window.location.hostname;
        document.cookie = expired;
      }
    });
  }

  function hide() {
    var inside = banner.contains(document.activeElement);
    banner.hidden = true;
    if (opener) {
      opener.focus();
      opener = null;
    } else if (inside) {
      var main = document.getElementById("main-content");
      if (main) {
        main.focus();
      }
    }
  }

  function show(moveFocus) {
    var current = read();
    status.textContent = current === "granted" ? "今は同意しています。" : current === "denied" ? "今は同意していません。" : "";
    banner.hidden = false;
    if (moveFocus) {
      banner.focus();
    }
  }

  function apply(value) {
    if (value === "granted") {
      if (!save(value)) {
        status.textContent = "設定を保存できなかったので、計測は始めません。";
        return;
      }
      hide();
      load();
      return;
    }
    // 取り消しは、保存できなくても、このページの計測を先に止める
    var active = loaded || read() === "granted";
    if (active) {
      stop();
    }
    if (!save(value)) {
      status.textContent = "計測は止めましたが、設定を保存できませんでした。次に開いたときに計測が再開することがあります。";
      return;
    }
    if (active) {
      window.location.reload();
    } else {
      hide();
    }
  }

  // 保存された値に画面を合わせる。他のタブの回答、値の削除、戻るの操作のときも、ここを通る
  function sync() {
    var current = read();
    if (current === "granted") {
      if (!banner.hidden) {
        hide();
      }
      load();
    } else if (loaded) {
      stop();
      window.location.reload();
    } else if (current === "denied") {
      window["ga-disable-" + id] = true;
      if (!banner.hidden) {
        hide();
      }
    } else if (current === "") {
      show(false);
    } else if (!banner.hidden) {
      // null は保存領域が使えない。案内を出しても回答を残せないので、出さず、計測もしない
      hide();
    }
  }

  banner.addEventListener("click", function (event) {
    var value = event.target.getAttribute && event.target.getAttribute("data-analytics-consent");
    if (value) {
      apply(value);
    }
  });
  if (settings && read() !== null) {
    settings.hidden = false;
    settings.addEventListener("click", function () {
      opener = settings;
      show(true);
    });
  }
  // 他のタブの回答を受ける。イベントの値は信用せず、今の保存値を読み直す
  window.addEventListener("storage", function (event) {
    if (event.key === key || event.key === null) {
      sync();
    }
  });
  // 戻るの操作でページが復元されたとき、他の画面の変更を反映する
  window.addEventListener("pageshow", function (event) {
    if (event.persisted) {
      sync();
    }
  });
  sync();
})();
