"use client";

import { DashboardLayout } from "@multica/views/layout";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { paths } from "@multica/core/paths";
import { SearchCommand, SearchTrigger } from "@multica/views/search";
import { FloatingChat } from "@multica/views/chat";
import { WebNotificationBridge } from "@/components/web-notification-bridge";

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <DashboardLayout
      loadingIndicator={<MulticaIcon className="size-6" />}
      searchSlot={<SearchTrigger />}
      verificationCodesHref={paths.verificationCodes()}
      extra={
        <>
          <SearchCommand />
          <WebNotificationBridge />
          <FloatingChat />
        </>
      }
    >
      {children}
    </DashboardLayout>
  );
}
