export function resetPageScroll() {
  if (typeof window === "undefined") return;
  const scrolled =
    window.scrollY !== 0 ||
    document.documentElement.scrollTop !== 0 ||
    document.body.scrollTop !== 0;
  document.documentElement.scrollTop = 0;
  document.body.scrollTop = 0;
  if (scrolled && typeof window.scrollTo === "function") {
    window.scrollTo({ top: 0, left: 0, behavior: "auto" });
  }
}
