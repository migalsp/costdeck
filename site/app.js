(() => {
  "use strict";

  const root = document.documentElement;
  const storageKey = "costdeck-theme";
  const themeColors = { light: "#ffffff", dark: "#0b1120" };
  const systemDark = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  const readStoredTheme = () => {
    try {
      const value = window.localStorage.getItem(storageKey);
      return value === "light" || value === "dark" ? value : null;
    } catch {
      return null;
    }
  };

  const storeTheme = (theme) => {
    try {
      window.localStorage.setItem(storageKey, theme);
    } catch {
      return;
    }
  };

  const systemTheme = () => (systemDark && systemDark.matches ? "dark" : "light");

  const currentTheme = () => readStoredTheme() || systemTheme();

  const applyStoredThemeToRoot = () => {
    const stored = readStoredTheme();
    if (stored) {
      root.dataset.theme = stored;
    } else {
      delete root.dataset.theme;
    }
    return stored;
  };

  applyStoredThemeToRoot();

  const syncDarkImageSources = (stored) => {
    const media = stored === "dark" ? "all" : stored === "light" ? "not all" : "(prefers-color-scheme: dark)";
    document.querySelectorAll("source[data-dark-source]").forEach((source) => {
      if (source.media !== media) {
        source.media = media;
      }
    });
  };

  const syncThemeColorMeta = (stored) => {
    document.querySelectorAll('meta[name="theme-color"]').forEach((meta) => {
      const scheme = (meta.media || "").includes("dark") ? "dark" : "light";
      meta.content = themeColors[stored || scheme];
    });
  };

  const syncToggle = (toggle) => {
    const theme = currentTheme();
    const next = theme === "dark" ? "light" : "dark";
    toggle.dataset.current = theme;
    toggle.setAttribute("aria-label", `Switch to ${next} theme`);
    toggle.title = `Switch to ${next} theme`;
  };

  const renderTheme = (toggle) => {
    const stored = applyStoredThemeToRoot();
    syncDarkImageSources(stored);
    syncThemeColorMeta(stored);
    if (toggle) {
      syncToggle(toggle);
    }
  };

  const setupThemeToggle = () => {
    const toggle = document.querySelector(".theme-toggle");
    if (!toggle) {
      return;
    }
    toggle.hidden = false;
    toggle.addEventListener("click", () => {
      storeTheme(currentTheme() === "dark" ? "light" : "dark");
      renderTheme(toggle);
    });
    if (systemDark && systemDark.addEventListener) {
      systemDark.addEventListener("change", () => renderTheme(toggle));
    }
    renderTheme(toggle);
  };

  const writeToClipboard = async (text) => {
    if (!navigator.clipboard || !window.isSecureContext) {
      return false;
    }
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      return false;
    }
  };

  const selectContents = (element) => {
    const selection = window.getSelection();
    if (!selection) {
      return;
    }
    const range = document.createRange();
    range.selectNodeContents(element);
    selection.removeAllRanges();
    selection.addRange(range);
  };

  const setupCopyButtons = () => {
    const status = document.getElementById("copy-status");
    document.querySelectorAll("[data-copy-target]").forEach((button) => {
      const code = document.getElementById(button.dataset.copyTarget);
      if (!code) {
        return;
      }
      const idleLabel = button.textContent;
      let resetTimer = 0;
      button.hidden = false;
      button.setAttribute("aria-label", "Copy command");
      button.addEventListener("click", async () => {
        const copied = await writeToClipboard(code.textContent);
        if (!copied) {
          selectContents(code);
        }
        button.textContent = copied ? "Copied" : "Selected";
        button.dataset.state = copied ? "copied" : "selected";
        if (status) {
          status.textContent = copied
            ? "Command copied to the clipboard."
            : "Command selected. Press Control+C or Command+C to copy it.";
        }
        window.clearTimeout(resetTimer);
        resetTimer = window.setTimeout(() => {
          button.textContent = idleLabel;
          delete button.dataset.state;
          if (status) {
            status.textContent = "";
          }
        }, 2400);
      });
    });
  };

  const start = () => {
    setupThemeToggle();
    setupCopyButtons();
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
