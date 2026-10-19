import { useState } from "react";
import { useCreateToken, useDeleteToken, useTokens, useTokenSelfTest } from "../lib/api/hooks";
import { Button, Card, Input } from "../components/ui";
import { formatDate } from "../lib/format";
import { copyToClipboard } from "../lib/clipboard";
import { toast } from "../lib/toast";
import { useT } from "../lib/i18n/context";
import { translateApiError } from "../lib/i18n";

// buildSharexConfig produces a ShareX custom-uploader (.sxcu) for this token.
function buildSharexConfig(name: string, token: string): string {
  return JSON.stringify(
    {
      Version: "16.0.0",
      Name: `Uploader – ${name}`,
      DestinationType: "ImageUploader, FileUploader, TextUploader",
      RequestMethod: "POST",
      RequestURL: `${window.location.origin}/api/v1/upload`,
      Headers: { Authorization: `Bearer ${token}` },
      Body: "MultipartFormData",
      FileFormName: "file",
      URL: "{json:url}",
      ThumbnailURL: "{json:thumb}",
      DeletionURL: "{json:delete}",
    },
    null,
    2,
  );
}

function download(filename: string, content: string) {
  const blob = new Blob([content], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

export function TokensPage() {
  const t = useT();
  const { data: tokens } = useTokens();
  const create = useCreateToken();
  const del = useDeleteToken();
  const [name, setName] = useState("");
  const [fresh, setFresh] = useState<{ name: string; token: string } | null>(null);
  const test = useTokenSelfTest();

  const onCreate = (e: React.FormEvent) => {
    e.preventDefault();
    const tokenName = name || "token";
    create.mutate(tokenName, {
      onSuccess: (res) => {
        setFresh(res);
        setName("");
        test.reset();
      },
    });
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <h1 className="text-lg font-semibold">{t("tokens.title")}</h1>

      <Card className="p-4">
        <form onSubmit={onCreate} className="flex gap-2">
          <Input
            placeholder={t("tokens.namePlaceholder")}
            aria-label={t("tokens.nameLabel")}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" disabled={create.isPending}>
            {t("common.create")}
          </Button>
        </form>
        {create.isError && (
          <p className="mt-2 text-sm text-[var(--color-danger)]">
            {t("tokens.createError", { error: translateApiError(create.error) })}
          </p>
        )}
      </Card>

      {fresh && (
        <Card className="space-y-3 border-[var(--color-accent)] p-4">
          <p className="text-sm">{t("tokens.copyNow")}</p>
          <code className="block break-all rounded-lg border border-[var(--color-border)] p-2 text-xs">
            {fresh.token}
          </code>
          <div className="flex gap-2">
            <Button
              variant="ghost"
              onClick={async () => {
                const ok = await copyToClipboard(fresh.token);
                toast(ok ? t("tokens.tokenCopied") : t("common.copyFailed"), ok ? "success" : "error");
              }}
            >
              {t("tokens.copyToken")}
            </Button>
            <Button
              onClick={() =>
                download(`uploader-${fresh.name}.sxcu`, buildSharexConfig(fresh.name, fresh.token))
              }
            >
              {t("tokens.downloadSharex")}
            </Button>
            <Button variant="ghost" disabled={test.isPending} onClick={() => test.mutate(fresh.token)}>
              {test.isPending ? t("tokens.testing") : t("tokens.testToken")}
            </Button>
            <Button variant="ghost" onClick={() => setFresh(null)}>
              {t("common.done")}
            </Button>
          </div>
          {test.isSuccess && (
            <p className="text-sm text-[var(--color-success)]">
              {t("tokens.tokenWorks")}{" "}
              <a className="underline" href={test.data.url} target="_blank" rel="noreferrer">
                {test.data.url}
              </a>
            </p>
          )}
          {test.isError && (
            <p className="text-sm text-[var(--color-danger)]">
              {t("tokens.selfTestFailed", { error: translateApiError(test.error) })}
            </p>
          )}
        </Card>
      )}

      {del.isError && (
        <p className="text-sm text-[var(--color-danger)]">
          {t("tokens.revokeError", { error: translateApiError(del.error) })}
        </p>
      )}

      <div className="space-y-2">
        {(tokens ?? []).map((tok) => (
          <Card key={tok.id} className="flex items-center justify-between p-3 text-sm">
            <div>
              <div className="font-medium">{tok.name}</div>
              <div className="text-xs text-[var(--color-muted)]">
                {tok.prefix}… · {t("tokens.created", { date: formatDate(tok.created_at) })}
                {tok.last_used_at
                  ? ` · ${t("tokens.lastUsed", { date: formatDate(tok.last_used_at) })}`
                  : ` · ${t("tokens.neverUsed")}`}
              </div>
            </div>
            <Button
              variant="danger"
              onClick={() => {
                if (confirm(t("tokens.confirmRevoke", { name: tok.name }))) {
                  del.mutate(tok.id);
                }
              }}
              disabled={del.isPending}
            >
              {t("common.revoke")}
            </Button>
          </Card>
        ))}
        {tokens && tokens.length === 0 && (
          <p className="text-sm text-[var(--color-muted)]">{t("tokens.empty")}</p>
        )}
      </div>
    </div>
  );
}
