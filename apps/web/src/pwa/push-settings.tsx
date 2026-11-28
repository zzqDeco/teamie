import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import type { Bootstrap } from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import { toast } from "../toast";
import { Notice, SwitchField } from "../ui";
import {
  getExistingPushSubscription,
  isIosSafariNotStandalone,
  isWebPushSupported,
  subscribeWebPush,
  unsubscribeWebPush,
} from "./push";

export function WebPushSettingsControl({
  notificationPermission,
}: {
  notificationPermission: NotificationPermission | "unsupported";
}) {
  const { user, offlineMode } = useAuth();
  const bootstrapQuery = useQuery<Bootstrap>({
    queryKey: ["bootstrap", user?.id],
    queryFn: () => api.bootstrap(),
    enabled: false,
  });
  const pushFlagEnabled = Boolean(
    bootstrapQuery.data?.capabilities?.["im.push.web"],
  );

  const configQuery = useQuery({
    queryKey: ["pushConfig", user?.id],
    queryFn: ({ signal }) => api.pushConfig(signal),
    enabled: Boolean(user && !offlineMode && pushFlagEnabled),
    staleTime: 60_000,
    retry: false,
  });

  const [subscribed, setSubscribed] = useState(false);
  const [checking, setChecking] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const activeConfig =
    pushFlagEnabled &&
    configQuery.data?.enabled === true &&
    Boolean(configQuery.data.vapidPublicKey);

  useEffect(() => {
    if (!activeConfig) {
      setChecking(false);
      return;
    }
    let cancelled = false;
    setChecking(true);
    void getExistingPushSubscription()
      .then((sub) => {
        if (!cancelled) {
          setSubscribed(Boolean(sub));
        }
      })
      .finally(() => {
        if (!cancelled) {
          setChecking(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [activeConfig, user?.id]);

  if (!activeConfig || !configQuery.data?.vapidPublicKey) {
    return null;
  }

  const vapidPublicKey = configQuery.data.vapidPublicKey;
  const iosNeedsHomeScreen = isIosSafariNotStandalone();
  const supported = isWebPushSupported();
  const permissionDenied = notificationPermission === "denied";
  const disabled =
    busy || checking || iosNeedsHomeScreen || !supported || permissionDenied;

  let description =
    "关闭页面或锁屏后也能收到新消息唤醒提醒（仅发送加密唤醒信号，不包含消息正文）。";
  if (iosNeedsHomeScreen) {
    description =
      "iOS Safari 需先点击「分享 → 添加到主屏幕」并从主屏幕图标打开后，才能开启后台推送。";
  } else if (!supported) {
    description = "当前浏览器不支持后台 Web Push 推送。";
  } else if (permissionDenied) {
    description =
      "浏览器通知权限已被阻止，请在浏览器设置中允许通知后再开启后台推送。";
  }

  async function toggle(checked: boolean) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      if (checked) {
        await subscribeWebPush(vapidPublicKey);
        setSubscribed(true);
        toast.success("已开启后台锁屏推送");
      } else {
        await unsubscribeWebPush();
        setSubscribed(false);
        toast.success("已关闭后台锁屏推送");
      }
    } catch (e) {
      setError(errorText(e));
      const latest = await getExistingPushSubscription();
      setSubscribed(Boolean(latest));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Notice kind="error">{error}</Notice>
      <SwitchField
        checked={subscribed}
        disabled={disabled}
        title="开启后台锁屏推送"
        description={description}
        onChange={(checked) => {
          void toggle(checked);
        }}
      />
    </>
  );
}
