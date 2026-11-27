import { useState } from "react";
import { Download, Share, Smartphone, X } from "lucide-react";
import { ICON } from "../icons";
import { Button, IconButton } from "../ui";
import { usePwaInstall } from "./install";
import "../styles/pwa.css";

/**
 * Restrained, permanently dismissible install banner shown only on mobile
 * browsers running outside standalone PWA mode.
 */
export function PwaInstallBanner({ mobile }: { mobile: boolean }) {
  const {
    isStandalone,
    canPromptInstall,
    showIosGuide,
    bannerDismissed,
    promptInstall,
    dismissBanner,
  } = usePwaInstall();
  const [busy, setBusy] = useState(false);

  if (!mobile || isStandalone || bannerDismissed) return null;
  if (!canPromptInstall && !showIosGuide) return null;

  async function install() {
    setBusy(true);
    try {
      await promptInstall();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="pwa-install-banner"
      role="region"
      aria-label="安装应用到主屏幕"
    >
      <Smartphone size={ICON.md} aria-hidden="true" />
      <div className="pwa-install-banner-text">
        {canPromptInstall ? (
          <>
            <b>安装 Teamie 到主屏幕</b>
            <span>全屏打开、系统角标与离线消息浏览</span>
          </>
        ) : (
          <>
            <b>添加到主屏幕</b>
            <span>
              点击 Safari 底部分享按钮 <Share size={ICON.xs} aria-hidden="true" />{" "}
              ，选择「添加到主屏幕」
            </span>
          </>
        )}
      </div>
      <div className="pwa-install-banner-actions">
        {canPromptInstall && (
          <Button
            type="button"
            size="sm"
            busy={busy}
            onClick={() => void install()}
          >
            安装
          </Button>
        )}
        <IconButton label="不再提示安装" onClick={dismissBanner}>
          <X size={ICON.sm} />
        </IconButton>
      </div>
    </div>
  );
}

/**
 * Account-settings card allowing desktop & mobile users to install Teamie as a
 * standalone PWA or view platform-specific installation instructions.
 */
export function PwaInstallSettingsCard() {
  const { isStandalone, canPromptInstall, showIosGuide, promptInstall } =
    usePwaInstall();
  const [busy, setBusy] = useState(false);

  async function install() {
    setBusy(true);
    try {
      await promptInstall();
    } finally {
      setBusy(false);
    }
  }

  return (
    <section
      className="settings-form pwa-install-card"
      aria-label="安装到桌面或主屏幕"
    >
      <h3>安装应用</h3>
      <p className="field-help">
        将 Teamie 安装到桌面或手机主屏幕，获得独立窗口、应用图标角标与断网只读浏览体验。
      </p>
      {isStandalone ? (
        <p className="pwa-install-status" role="status">
          <Smartphone size={ICON.sm} aria-hidden="true" />
          当前已作为独立应用运行。
        </p>
      ) : canPromptInstall ? (
        <Button
          type="button"
          variant="secondary"
          busy={busy}
          onClick={() => void install()}
        >
          <Download size={ICON.sm} aria-hidden="true" />
          安装到桌面 / 主屏幕
        </Button>
      ) : showIosGuide ? (
        <p className="pwa-install-steps">
          在 Safari 底部工具栏点击分享按钮{" "}
          <Share size={ICON.xs} aria-hidden="true" />，选择「添加到主屏幕」即可安装。
        </p>
      ) : (
        <p className="pwa-install-steps">
          可通过浏览器地址栏或菜单中的「安装应用 / 添加到主屏幕」将 Teamie 安装为独立应用。
        </p>
      )}
    </section>
  );
}
