const menuButton = document.querySelector("[data-menu-button]");
const menu = document.querySelector("[data-menu]");

function setMenu(open) {
  if (!menuButton || !menu) return;
  menuButton.setAttribute("aria-expanded", String(open));
  menuButton.setAttribute("aria-label", open ? "Close navigation" : "Open navigation");
  menu.classList.toggle("open", open);
  document.body.classList.toggle("menu-open", open);
  const icon = menuButton.querySelector("svg");
  if (icon) icon.setAttribute("data-lucide", open ? "x" : "menu");
  if (window.lucide) window.lucide.createIcons();
}

menuButton?.addEventListener("click", () => {
  setMenu(menuButton.getAttribute("aria-expanded") !== "true");
});

menu?.querySelectorAll("a").forEach((link) => {
  link.addEventListener("click", () => setMenu(false));
});

window.addEventListener("resize", () => {
  if (window.innerWidth > 960) setMenu(false);
});

function wireTabs(tabSelector, panelSelector, dataKey) {
  const tabs = [...document.querySelectorAll(tabSelector)];
  const panels = [...document.querySelectorAll(panelSelector)];

  tabs.forEach((tab, index) => {
    tab.addEventListener("click", () => {
      const selected = tab.dataset[dataKey];
      tabs.forEach((item) => item.setAttribute("aria-selected", String(item === tab)));
      panels.forEach((panel) => {
        panel.hidden = panel.dataset[dataKey.replace("Tab", "Panel")] !== selected;
      });
    });

    tab.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const direction = event.key === "ArrowRight" ? 1 : -1;
      tabs[(index + direction + tabs.length) % tabs.length].focus();
    });
  });
}

wireTabs("[data-mode-tab]", "[data-mode-panel]", "modeTab");
wireTabs("[data-deploy-tab]", "[data-deploy-panel]", "deployTab");

document.querySelectorAll("[data-copy-target]").forEach((button) => {
  button.addEventListener("click", async () => {
    const target = document.getElementById(button.dataset.copyTarget);
    if (!target) return;

    try {
      await navigator.clipboard.writeText(target.textContent);
      button.setAttribute("aria-label", "Commands copied");
      button.title = "Copied";
      button.innerHTML = '<i data-lucide="check" aria-hidden="true"></i>';
      if (window.lucide) window.lucide.createIcons();
      window.setTimeout(() => {
        button.setAttribute("aria-label", "Copy commands");
        button.title = "Copy commands";
        button.innerHTML = '<i data-lucide="copy" aria-hidden="true"></i>';
        if (window.lucide) window.lucide.createIcons();
      }, 1800);
    } catch {
      button.setAttribute("aria-label", "Copy failed");
      button.title = "Copy failed";
    }
  });
});

window.addEventListener("DOMContentLoaded", () => {
  if (window.lucide) window.lucide.createIcons();
});
