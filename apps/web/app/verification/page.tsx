import type { Metadata } from "next";
import { VerificationCodesPage } from "@multica/views/verification-codes";

export const metadata: Metadata = {
  title: "Verification Codes",
};

export default function Page() {
  return <VerificationCodesPage />;
}
