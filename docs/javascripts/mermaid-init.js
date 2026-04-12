document.addEventListener("DOMContentLoaded", () => {
  if (!window.mermaid) {
    return;
  }
  document.querySelectorAll("pre.mermaid").forEach((block) => {
    const div = document.createElement("div");
    div.className = "mermaid";
    div.textContent = block.textContent ?? "";
    block.replaceWith(div);
  });
  document.querySelectorAll("code.mermaid").forEach((block) => {
    const div = document.createElement("div");
    div.className = "mermaid";
    div.textContent = block.textContent ?? "";
    block.replaceWith(div);
  });
  window.mermaid.initialize({
    startOnLoad: true,
    securityLevel: "loose",
  });
});
