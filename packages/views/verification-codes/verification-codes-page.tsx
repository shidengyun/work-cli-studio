"use client";

import { useMemo } from "react";
import { Copy, KeyRound, RefreshCw } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { verificationCodeListOptions } from "@multica/core/verification-codes";
import type { VerificationCode } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { copyText } from "@multica/ui/lib/clipboard";
import { toast } from "sonner";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

const LIMIT = 20;
const EMPTY_CODES: VerificationCode[] = [];

type VerificationCodeStatus = "active" | "used" | "expired" | "locked";

function getStatus(code: VerificationCode, now: number): VerificationCodeStatus {
  if (code.used) return "used";
  const expiresAt = Date.parse(code.expires_at);
  if (Number.isFinite(expiresAt) && expiresAt <= now) return "expired";
  if (code.attempts >= 5) return "locked";
  return "active";
}

export function VerificationCodesPage() {
  const { t } = useT("verification-codes");
  const query = useQuery(verificationCodeListOptions(LIMIT));
  const codes = query.data?.codes ?? EMPTY_CODES;
  const now = Date.now();

  const dateFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "medium",
      }),
    [],
  );

  const statusLabels: Record<VerificationCodeStatus, string> = {
    active: t(($) => $.status.active),
    used: t(($) => $.status.used),
    expired: t(($) => $.status.expired),
    locked: t(($) => $.status.locked),
  };

  const statusClasses: Record<VerificationCodeStatus, string> = {
    active: "border-primary/20 bg-primary/10 text-primary",
    used: "border-secondary bg-secondary text-secondary-foreground",
    expired: "border-muted bg-muted text-muted-foreground",
    locked: "border-destructive/20 bg-destructive/10 text-destructive",
  };

  const formatDate = (value: string) => {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return t(($) => $.table.empty_value);
    return dateFormatter.format(date);
  };

  const handleCopy = async (code: string) => {
    if (await copyText(code)) {
      toast.success(t(($) => $.toast.copied));
    }
  };

  const disabled = query.error instanceof ApiError && query.error.status === 403;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <KeyRound className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-sm font-semibold">{t(($) => $.title)}</h1>
        </div>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
                aria-label={t(($) => $.actions.refresh)}
              >
                <RefreshCw
                  className={query.isFetching ? "size-4 animate-spin" : "size-4"}
                />
              </Button>
            }
          />
          <TooltipContent>{t(($) => $.actions.refresh)}</TooltipContent>
        </Tooltip>
      </PageHeader>

      <main className="min-h-0 flex-1 overflow-auto p-4">
        {disabled ? (
          <Card>
            <CardContent className="flex min-h-40 flex-col justify-center gap-2">
              <h2 className="text-sm font-semibold">{t(($) => $.disabled.title)}</h2>
              <p className="max-w-xl text-sm text-muted-foreground">
                {t(($) => $.disabled.description)}
              </p>
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
                <div className="min-w-0">
                  <h2 className="text-sm font-semibold">{t(($) => $.table.title)}</h2>
                  <p className="text-xs text-muted-foreground">
                    {t(($) => $.table.subtitle, { count: LIMIT })}
                  </p>
                </div>
                <Badge variant="outline">
                  {t(($) => $.table.count, { count: codes.length })}
                </Badge>
              </div>

              {query.isPending ? (
                <div className="space-y-3 p-4">
                  {Array.from({ length: 6 }).map((_, index) => (
                    <div
                      key={index}
                      className="grid gap-3 md:grid-cols-[1.2fr_0.7fr_0.8fr_0.8fr_1fr]"
                    >
                      <Skeleton className="h-5" />
                      <Skeleton className="h-5" />
                      <Skeleton className="h-5" />
                      <Skeleton className="h-5" />
                      <Skeleton className="h-5" />
                    </div>
                  ))}
                </div>
              ) : query.isError ? (
                <div className="flex min-h-40 flex-col justify-center gap-2 px-4 py-8">
                  <h2 className="text-sm font-semibold">{t(($) => $.error.title)}</h2>
                  <p className="max-w-xl text-sm text-muted-foreground">
                    {query.error instanceof Error
                      ? query.error.message
                      : t(($) => $.error.description)}
                  </p>
                </div>
              ) : codes.length === 0 ? (
                <div className="flex min-h-40 flex-col justify-center gap-2 px-4 py-8">
                  <h2 className="text-sm font-semibold">{t(($) => $.empty.title)}</h2>
                  <p className="text-sm text-muted-foreground">{t(($) => $.empty.description)}</p>
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t(($) => $.table.email)}</TableHead>
                      <TableHead>{t(($) => $.table.code)}</TableHead>
                      <TableHead>{t(($) => $.table.status)}</TableHead>
                      <TableHead>{t(($) => $.table.attempts)}</TableHead>
                      <TableHead>{t(($) => $.table.created_at)}</TableHead>
                      <TableHead>{t(($) => $.table.expires_at)}</TableHead>
                      <TableHead>{t(($) => $.table.id)}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {codes.map((row) => {
                      const status = getStatus(row, now);
                      return (
                        <TableRow key={row.id}>
                          <TableCell className="max-w-[220px] truncate font-medium">
                            {row.email}
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-1.5">
                              <code className="rounded border bg-muted/50 px-2 py-1 font-mono text-xs">
                                {row.code}
                              </code>
                              <Tooltip>
                                <TooltipTrigger
                                  render={
                                    <Button
                                      type="button"
                                      variant="ghost"
                                      size="icon-sm"
                                      onClick={() => void handleCopy(row.code)}
                                      aria-label={t(($) => $.actions.copy_code)}
                                    >
                                      <Copy className="size-3.5" />
                                    </Button>
                                  }
                                />
                                <TooltipContent>{t(($) => $.actions.copy_code)}</TooltipContent>
                              </Tooltip>
                            </div>
                          </TableCell>
                          <TableCell>
                            <Badge variant="outline" className={statusClasses[status]}>
                              {statusLabels[status]}
                            </Badge>
                          </TableCell>
                          <TableCell>{row.attempts}</TableCell>
                          <TableCell>{formatDate(row.created_at)}</TableCell>
                          <TableCell>{formatDate(row.expires_at)}</TableCell>
                          <TableCell className="max-w-[220px] truncate font-mono text-xs text-muted-foreground">
                            {row.id}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        )}
      </main>
    </div>
  );
}
