import { createContext, useContext, useEffect } from "react";
import { useLocation } from "react-router-dom";

type SetTitleDetail = (path: string, detail: string) => void;

const PageTitleContext = createContext<SetTitleDetail>(() => {});

/** Provided by the shell to collect detail-page names for `document.title`. */
export const PageTitleProvider = PageTitleContext.Provider;

/**
 * Reports the entity name of a detail page (for example an Agent or developer
 * app) so the document title reads「名称 · 个人 Agent · Teamie」. The detail is
 * bound to the current path and cleared when the page unmounts.
 */
export function usePageTitleDetail(detail: string | undefined) {
  const setTitleDetail = useContext(PageTitleContext);
  const { pathname } = useLocation();
  useEffect(() => {
    if (!detail) return;
    setTitleDetail(pathname, detail);
    return () => setTitleDetail(pathname, "");
  }, [setTitleDetail, pathname, detail]);
}
