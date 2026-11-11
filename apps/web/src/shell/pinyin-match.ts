import { useEffect, useState } from "react";

/** Romanized forms of a label used for pinyin / initials matching. */
export type PinyinForms = { full: string; initials: string };
export type PinyinConverter = (text: string) => PinyinForms;

const CJK = /[\u3400-\u9fff]/u;
let converter: PinyinConverter | null = null;
let pending: Promise<PinyinConverter | null> | null = null;

/** Lazily loads `pinyin-pro` once; resolves to null when it cannot load. */
export function loadPinyin(): Promise<PinyinConverter | null> {
  if (converter) return Promise.resolve(converter);
  pending ??= import("pinyin-pro")
    .then(({ pinyin }) => {
      const cache = new Map<string, PinyinForms>();
      converter = (text: string) => {
        const cached = cache.get(text);
        if (cached) return cached;
        const syllables = CJK.test(text)
          ? pinyin(text, { toneType: "none", type: "array", v: true })
              .map((part) => part.trim().toLocaleLowerCase())
              .filter(Boolean)
          : [];
        const forms = {
          full: syllables.join(""),
          initials: syllables.map((part) => part.charAt(0)).join(""),
        };
        cache.set(text, forms);
        return forms;
      };
      return converter;
    })
    .catch(() => {
      pending = null;
      return null;
    });
  return pending;
}

/** The pinyin converter once loaded (null until then or when unavailable). */
export function usePinyin(enabled = true) {
  const [ready, setReady] = useState<PinyinConverter | null>(() => converter);
  useEffect(() => {
    if (!enabled || ready) return;
    let active = true;
    void loadPinyin().then((loaded) => {
      if (active && loaded) setReady(() => loaded);
    });
    return () => {
      active = false;
    };
  }, [enabled, ready]);
  return ready;
}
