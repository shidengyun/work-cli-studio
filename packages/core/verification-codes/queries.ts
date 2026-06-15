import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const verificationCodeKeys = {
  all: () => ["verification-codes"] as const,
  list: (limit: number) => [...verificationCodeKeys.all(), "list", limit] as const,
};

export function verificationCodeListOptions(limit = 20) {
  return queryOptions({
    queryKey: verificationCodeKeys.list(limit),
    queryFn: () => api.listVerificationCodes(limit),
    staleTime: 10 * 1000,
  });
}
