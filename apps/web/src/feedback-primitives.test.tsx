// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Session, User } from "@teamie/api-client";
import { AuthProvider } from "./auth";
import { PasswordField } from "./password-field";
import { Join, Login, normalizeInviteInput } from "./public/auth-views";
import { chatStore } from "./store";
import { TOAST_DURATION, Toaster, toast } from "./toast";
import { Button, Field, Loading, Modal, Notice, SwitchField, Tabs } from "./ui";

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(async () => root.unmount());
  act(() => toast.clear());
  host.remove();
  client.clear();
  chatStore.clear();
  api.csrfToken = "";
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(node: React.ReactNode) {
  await act(async () => root.render(node));
}
async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}
function find<T extends HTMLElement>(selector: string): T {
  const node = host.querySelector<T>(selector);
  if (!node) throw new Error(`Missing ${selector}`);
  return node;
}
function buttonByText(text: string) {
  const button = Array.from(host.querySelectorAll("button")).find(
    (element) => element.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Missing button ${text}`);
  return button;
}
async function typeInto(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!
      .set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function escape() {
  await act(async () => {
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
  });
}

describe("Toaster", () => {
  it("announces success politely and errors assertively", async () => {
    await render(<Toaster />);
    await act(async () => {
      toast.success("已保存");
      toast.error("保存失败");
    });
    const polite = find('[role="status"]');
    const assertive = find('[role="alert"]');
    expect(polite.getAttribute("aria-live")).toBe("polite");
    expect(polite.textContent).toContain("已保存");
    expect(polite.textContent).not.toContain("保存失败");
    expect(assertive.textContent).toContain("保存失败");
  });

  it("auto-dismisses after its duration, longer for errors, and pauses while focused", async () => {
    vi.useFakeTimers();
    await render(<Toaster />);
    await act(async () => {
      toast.success("成功提示");
      toast.error("错误提示");
    });
    await act(async () => {
      vi.advanceTimersByTime(TOAST_DURATION.success + 10);
    });
    expect(host.textContent).not.toContain("成功提示");
    expect(host.textContent).toContain("错误提示");
    await act(async () => {
      vi.advanceTimersByTime(TOAST_DURATION.error - TOAST_DURATION.success);
    });
    expect(host.textContent).not.toContain("错误提示");

    await act(async () => {
      toast.info("可暂停");
    });
    await act(async () => {
      vi.advanceTimersByTime(1000);
      find<HTMLButtonElement>(".toast-close").focus();
    });
    await act(async () => {
      vi.advanceTimersByTime(TOAST_DURATION.info * 3);
    });
    expect(host.textContent).toContain("可暂停");
    await act(async () => {
      find<HTMLButtonElement>(".toast-close").blur();
    });
    await act(async () => {
      vi.advanceTimersByTime(TOAST_DURATION.info);
    });
    expect(host.textContent).not.toContain("可暂停");
  });

  it("runs an action once, closes manually and keeps at most three toasts", async () => {
    const undo = vi.fn();
    await render(<Toaster />);
    await act(async () => {
      toast.success("已删除", { action: { label: "撤销", onClick: undo } });
    });
    await act(async () => buttonByText("撤销").click());
    expect(undo).toHaveBeenCalledTimes(1);
    expect(host.textContent).not.toContain("已删除");

    await act(async () => {
      for (const n of [1, 2, 3, 4]) toast.info(`提示 ${n}`);
    });
    expect(host.querySelectorAll(".toast")).toHaveLength(3);
    expect(host.textContent).not.toContain("提示 1");
    await act(async () =>
      find<HTMLButtonElement>('button[aria-label="关闭通知"]').click(),
    );
    expect(host.querySelectorAll(".toast")).toHaveLength(2);
  });

  it("deduplicates identical active toast messages, supports persistent duration: 0, and keeps button text out of live regions", async () => {
    vi.useFakeTimers();
    await render(<Toaster />);
    await act(async () => {
      toast.error("网络异常", { duration: 0 });
      toast.error("网络异常", { duration: 0 });
      toast.success("已复制", { action: { label: "查看", onClick: () => {} } });
    });
    expect(host.querySelectorAll(".toast.error")).toHaveLength(1);
    const assertive = find('[role="alert"]');
    const polite = find('[role="status"]');
    expect(assertive.textContent).toBe("网络异常");
    expect(assertive.textContent).not.toContain("关闭通知");
    expect(polite.textContent).toBe("已复制");
    expect(polite.textContent).not.toContain("查看");
    expect(polite.textContent).not.toContain("关闭通知");

    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    expect(host.querySelectorAll(".toast.error")).toHaveLength(1);
  });
});

describe("Modal dismissal", () => {
  it("routes the close button through busy blocking and dirty confirmation", async () => {
    const close = vi.fn();
    function Harness({ busy, dirty }: { busy: boolean; dirty: boolean }) {
      return (
        <Modal title="编辑" onClose={close} busy={busy} dirty={dirty}>
          <input name="name" />
        </Modal>
      );
    }
    await render(<Harness busy dirty={false} />);
    await act(async () => find<HTMLButtonElement>(".modal-close").click());
    await escape();
    expect(close).not.toHaveBeenCalled();
    expect(find(".modal-close").getAttribute("aria-disabled")).toBe("true");
    expect(find(".modal").classList.contains("dismiss-blocked")).toBe(true);

    await render(<Harness busy={false} dirty />);
    await act(async () => find<HTMLButtonElement>(".modal-close").click());
    expect(close).not.toHaveBeenCalled();
    expect(host.textContent).toContain("放弃未保存的更改？");
    expect(document.activeElement?.textContent?.trim()).toBe("保留编辑");
    await act(async () => buttonByText("保留编辑").click());
    expect(host.textContent).not.toContain("放弃未保存的更改？");

    await act(async () =>
      find(".modal-backdrop").dispatchEvent(new MouseEvent("mousedown", { bubbles: true })),
    );
    expect(host.textContent).toContain("放弃未保存的更改？");
    await escape();
    expect(host.textContent).not.toContain("放弃未保存的更改？");
    expect(close).not.toHaveBeenCalled();

    await escape();
    await act(async () => buttonByText("放弃").click());
    expect(close).toHaveBeenCalledTimes(1);
  });

  it("closes a clean modal from the close button", async () => {
    const close = vi.fn();
    await render(<Modal title="T" onClose={close}><p>内容</p></Modal>);
    await act(async () => find<HTMLButtonElement>(".modal-close").click());
    expect(close).toHaveBeenCalledTimes(1);
  });

  it("lets only the top-most overlay handle Escape and respects claimed Escape", async () => {
    const outerClose = vi.fn();
    function Nested() {
      const [inner, setInner] = useState(true);
      return (
        <Modal title="外层" onClose={outerClose}>
          <input
            name="outer"
            onKeyDown={(event) => {
              if (event.key === "Escape") event.preventDefault();
            }}
          />
          {inner && (
            <Modal title="内层" onClose={() => setInner(false)}>
              <p>内层内容</p>
            </Modal>
          )}
        </Modal>
      );
    }
    await render(<Nested />);
    expect(host.querySelectorAll('[role="dialog"]')).toHaveLength(2);
    await escape();
    expect(host.querySelectorAll('[role="dialog"]')).toHaveLength(1);
    expect(outerClose).not.toHaveBeenCalled();

    // A nested control (e.g. mention popover) that claims Escape keeps the modal.
    const input = find<HTMLInputElement>('input[name="outer"]');
    await act(async () => {
      input.focus();
      input.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
      );
    });
    expect(outerClose).not.toHaveBeenCalled();
    await escape();
    expect(outerClose).toHaveBeenCalledTimes(1);
  });

  it("focuses [data-autofocus] first, otherwise the first form field instead of the close button", async () => {
    await render(
      <Modal title="T" onClose={() => {}}>
        <p>说明</p>
        <select name="kind"><option>a</option></select>
        <input name="second" />
      </Modal>,
    );
    expect(document.activeElement).toBe(find('select[name="kind"]'));
    await render(<></>);
    await render(
      <Modal title="删除？" onClose={() => {}}>
        <input name="field" />
        <Button variant="secondary" data-autofocus>取消</Button>
        <Button variant="danger">删除</Button>
      </Modal>,
    );
    expect(document.activeElement?.textContent?.trim()).toBe("取消");
  });
});

describe("Button", () => {
  it("exposes busy state accessibly and maps variant/size to existing classes", async () => {
    await render(
      <>
        <Button busy variant="danger" size="sm" className="extra">删除</Button>
        <Button variant="ghost">更多</Button>
        <Button size="lg">主行动</Button>
      </>,
    );
    const [busy, ghost, large] = Array.from(host.querySelectorAll("button"));
    expect(busy.getAttribute("aria-busy")).toBe("true");
    expect(busy.disabled).toBe(true);
    expect(busy.querySelector(".sr-only")?.textContent).toBe("处理中…");
    expect(busy.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(busy.className.split(" ")).toEqual(
      expect.arrayContaining(["button", "danger", "small-button", "extra"]),
    );
    expect(ghost.hasAttribute("aria-busy")).toBe(false);
    expect(ghost.className.split(" ")).toEqual(["button", "ghost"]);
    expect(large.className.split(" ")).toEqual(["button", "large-button"]);
  });

  it("renders accessible Field, Tabs (arrow-key roving focus), SwitchField (role=switch), Notice (warning + dismiss), and Loading (role=status)", async () => {
    const onTab = vi.fn();
    const onSwitch = vi.fn();
    const onDismiss = vi.fn();
    await render(
      <>
        <Field label="空间名称" hint="最多 40 个字" error="请填写空间名称。" htmlFor="ws-name">
          <input id="ws-name" />
        </Field>
        <Tabs
          ariaLabel="管理分区"
          value="a"
          onChange={onTab}
          items={[
            { value: "a", label: "分区一" },
            { value: "b", label: "分区二" },
          ]}
        />
        <SwitchField checked={false} title="桌面通知" onChange={onSwitch} />
        <Notice tone="warning" onDismiss={onDismiss}>请注意保存密钥</Notice>
        <Loading label="正在准备…" />
      </>,
    );
    expect(find(".field .field-error").getAttribute("role")).toBe("alert");
    const tabs = Array.from(host.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(tabs[0].tabIndex).toBe(0);
    expect(tabs[1].tabIndex).toBe(-1);
    await act(async () => {
      tabs[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    });
    expect(onTab).toHaveBeenCalledWith("b");
    expect(document.activeElement).toBe(tabs[1]);

    const sw = find<HTMLInputElement>('input[role="switch"]');
    expect(sw.type).toBe("checkbox");
    expect(find(".notice.warning").textContent).toContain("请注意保存密钥");
    await act(async () => find<HTMLButtonElement>(".notice-dismiss").click());
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(find(".loading-screen").getAttribute("role")).toBe("status");
  });
});

describe("PasswordField", () => {
  it("toggles visibility, checks rules by code points and UTF-8 bytes and warns on Caps Lock", async () => {
    await render(
      <form>
        <PasswordField name="pw" label="新密码" autoComplete="new-password" showRules />
      </form>,
    );
    const input = find<HTMLInputElement>('input[name="pw"]');
    const toggle = find<HTMLButtonElement>(".password-toggle");
    expect(input.type).toBe("password");
    expect(toggle.getAttribute("aria-label")).toBe("显示密码");
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    await act(async () => toggle.click());
    expect(input.type).toBe("text");
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(toggle.title).toBe("隐藏密码");

    const rules = find(".password-rules");
    expect(rules.getAttribute("aria-live")).toBe("polite");
    expect(input.getAttribute("aria-describedby")).toContain(rules.id);
    // Empty input stays neutral ("待输入") rather than falsely marking <= 72 bytes as met.
    let items = Array.from(rules.querySelectorAll("li"));
    expect(items.map((item) => item.classList.contains("met"))).toEqual([false, false]);
    expect(items[1].textContent).toContain("待输入");
    await typeInto(input, "🔑".repeat(12));
    items = Array.from(rules.querySelectorAll("li"));
    expect(items.map((item) => item.classList.contains("met"))).toEqual([true, true]);
    await typeInto(input, "密".repeat(25));
    items = Array.from(rules.querySelectorAll("li"));
    expect(items.map((item) => item.classList.contains("met"))).toEqual([true, false]);
    expect(items[1].textContent).toContain("未满足");

    await act(async () => {
      const event = new KeyboardEvent("keydown", { key: "A", bubbles: true });
      Object.defineProperty(event, "getModifierState", {
        value: (key: string) => key === "CapsLock",
      });
      input.dispatchEvent(event);
    });
    expect(host.textContent).toContain("已开启大写锁定");
  });

  it("checks a confirmation mismatch on blur and clears it once it matches", async () => {
    await render(
      <form>
        <PasswordField name="pw" label="密码" autoComplete="new-password" />
        <PasswordField name="confirm" label="确认密码" autoComplete="new-password" confirmOf="pw" />
      </form>,
    );
    const password = find<HTMLInputElement>('input[name="pw"]');
    const confirm = find<HTMLInputElement>('input[name="confirm"]');
    password.value = "example-password-a";
    await typeInto(confirm, "example-password-b");
    expect(host.textContent).not.toContain("两次输入的密码不一致。");
    await act(async () => {
      confirm.focus();
      confirm.blur();
    });
    expect(confirm.getAttribute("aria-invalid")).toBe("true");
    expect(document.getElementById(confirm.getAttribute("aria-describedby")!)?.textContent)
      .toContain("两次输入的密码不一致。");
    await typeInto(confirm, "example-password-a");
    expect(confirm.hasAttribute("aria-invalid")).toBe(false);
  });
});

const member: User = {
  id: "feedback-user",
  kind: "human",
  platformRole: "user",
  displayName: "反馈伙伴",
  email: "feedback@example.test",
  createdAt: "2026-10-17T00:00:00Z",
};
const session: Session = { user: member, csrfToken: "example-feedback-session" };
function Location() {
  const location = useLocation();
  return <output data-location>{location.pathname + location.search}</output>;
}
async function mountApp(path: string) {
  await settle(() =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          <AuthProvider>
            <Routes>
              <Route path="/login" element={<Login />} />
              <Route path="/join/:inviteCode" element={<Join />} />
              <Route path="/app" element={<p>工作空间</p>} />
            </Routes>
            <Location />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await settle();
}

describe("auth forms", () => {
  beforeEach(() => {
    vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "unauthorized", "unauthorized"));
    vi.spyOn(api, "registration").mockResolvedValue({ mode: "invite", bootstrapRequired: false });
    vi.spyOn(api, "register").mockResolvedValue(session);
    vi.spyOn(api, "login").mockResolvedValue(session);
  });

  it("uses noValidate and shows linked inline errors, revalidating the field in error", async () => {
    await mountApp("/login");
    const form = find<HTMLFormElement>(".auth-form form");
    expect(form.noValidate).toBe(true);
    await settle(() =>
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
    );
    expect(api.login).not.toHaveBeenCalled();
    const email = find<HTMLInputElement>('input[name="email"]');
    expect(document.activeElement).toBe(email);
    expect(email.getAttribute("aria-invalid")).toBe("true");
    const inline = document.getElementById(email.getAttribute("aria-describedby")!);
    expect(inline?.classList.contains("field-error")).toBe(true);
    expect(inline?.textContent).toContain("请填写邮箱地址。");
    expect(find('[role="alert"]').textContent).toContain("请填写邮箱地址。");

    await typeInto(email, "not-an-email");
    expect(inline?.isConnected && inline.textContent).toContain("请输入有效的邮箱地址。");
    await typeInto(email, member.email);
    expect(email.hasAttribute("aria-invalid")).toBe(false);
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("puts the registration invite first and validates it first", async () => {
    await mountApp("/login?mode=register");
    const fields = Array.from(
      host.querySelectorAll<HTMLInputElement>(".auth-form form input"),
    ).map((input) => input.name);
    expect(fields[0]).toBe("inviteCode");
    await settle(() =>
      find<HTMLFormElement>(".auth-form form").dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      ),
    );
    expect(document.activeElement).toBe(find('input[name="inviteCode"]'));
    expect(host.textContent).toContain("请填写注册邀请码。");
    expect(api.register).not.toHaveBeenCalled();
  });

  it("prefills an invite from the URL as a read-only summary that can be edited", async () => {
    await mountApp("/login?mode=register&inviteCode=example-url-invite");
    const summary = find(".invite-summary");
    expect(summary.textContent).toContain("example-url-invite");
    const hidden = find<HTMLInputElement>('input[name="inviteCode"]');
    expect(hidden.type).toBe("hidden");
    expect(hidden.value).toBe("example-url-invite");
    await settle(() => find<HTMLButtonElement>(".invite-summary-edit").click());
    const editable = find<HTMLInputElement>('input[name="inviteCode"]');
    expect(editable.type).toBe("text");
    expect(editable.value).toBe("example-url-invite");
    expect(document.activeElement).toBe(editable);

    expect(
      normalizeInviteInput("https://teamie.example/login?mode=register&inviteCode=extracted-code"),
    ).toEqual({ code: "extracted-code", isWorkspaceJoinLink: false });
    expect(
      normalizeInviteInput("https://teamie.example/join/workspace-token"),
    ).toEqual({
      code: "https://teamie.example/join/workspace-token",
      isWorkspaceJoinLink: true,
    });
    await typeInto(editable, "https://teamie.example/join/workspace-token");
    expect(host.textContent).toContain("这是工作空间邀请链接");
  });

  it("keeps inputs focusable but read-only while submitting", async () => {
    let finish!: (value: Session) => void;
    vi.mocked(api.login).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    await mountApp("/login");
    find<HTMLInputElement>('input[name="email"]').value = member.email;
    find<HTMLInputElement>('input[name="password"]').value = "example-password";
    await settle(() =>
      find<HTMLFormElement>(".auth-form form").dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      ),
    );
    const inputs = Array.from(host.querySelectorAll<HTMLInputElement>(".auth-form input"));
    expect(inputs.every((input) => !input.disabled && input.readOnly)).toBe(true);
    await settle(() => finish(session));
    expect(find("[data-location]").textContent).toBe("/app");
  });
});

describe("Join page", () => {
  const invite = {
    workspace: { id: "w1", name: "产品空间" },
    expiresAt: new Date(Date.now() + 86_400_000).toISOString(),
  };
  beforeEach(() => {
    vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "unauthorized", "unauthorized"));
  });

  it("explains an invalid or expired invitation without offering a retry", async () => {
    vi.spyOn(api, "invite").mockRejectedValue(new ApiError(410, "invite_expired", "gone"));
    await mountApp("/join/expired-code");
    expect(find("h1").textContent).toBe("邀请已失效");
    expect(host.textContent).not.toContain("重新加载邀请");
  });

  it("distinguishes network failures and retries", async () => {
    const lookup = vi
      .spyOn(api, "invite")
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValue(invite as Awaited<ReturnType<typeof api.invite>>);
    await mountApp("/join/flaky-code");
    expect(find("h1").textContent).toBe("暂时无法打开邀请");
    await settle(() => buttonByText("重新加载邀请").click());
    await settle();
    expect(lookup).toHaveBeenCalledTimes(2);
    expect(find("h1").textContent).toBe("加入「产品空间」");
  });

  it("tells signed-out visitors an account is required and links registration with next", async () => {
    vi.spyOn(api, "invite").mockResolvedValue(invite as Awaited<ReturnType<typeof api.invite>>);
    await mountApp("/join/team-code");
    expect(host.textContent).toContain("加入工作空间需要 Teamie 账号");
    const register = Array.from(host.querySelectorAll("a")).find((a) =>
      a.textContent?.includes("还没有账号？使用注册邀请码注册"),
    )!;
    expect(register.getAttribute("href")).toBe(
      `/login?mode=register&next=${encodeURIComponent("/join/team-code")}`,
    );
  });
});
