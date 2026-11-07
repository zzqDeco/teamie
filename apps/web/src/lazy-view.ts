import { lazy } from "react";

/**
 * Wraps `React.lazy` so production and dev builds split each dynamic `import()`
 * into an on-demand chunk, while Vitest (`MODE === "test"`) warms the shared
 * loader promise once with a teardown-safe catch handler so synchronous `act()`
 * tests never stall on cold `vite-node` module transforms.
 */
export const lazyView: typeof lazy = (loader) => {
  if (import.meta.env.MODE === "test") {
    let inflight: ReturnType<typeof loader> | undefined;
    const cachedLoader: typeof loader = () => {
      inflight ??= loader();
      return inflight;
    };
    void cachedLoader().catch(() => {});
    return lazy(cachedLoader);
  }
  return lazy(loader);
};
