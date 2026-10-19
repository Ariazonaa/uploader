import { useEffect, useId, useState } from "react";
import { useSettings, useUpdateSettings } from "../lib/api/hooks";
import { Button, Card, Input, Select } from "../components/ui";
import type { Settings } from "../lib/api/types";
import type { MessageKey } from "../lib/i18n/types";
import { useT } from "../lib/i18n/context";
import { tGlobal } from "../lib/i18n";

type Field = {
  key: keyof Settings;
  labelKey: MessageKey;
  hintKey?: MessageKey;
  multiline?: boolean;
  numeric?: boolean;
  // Upper bound of a numeric field; must match the server-side clamp in
  // backend/internal/settings/settings.go.
  max?: number;
  bool?: boolean;
  options?: { value: string; labelKey: MessageKey }[];
};

// Labels/hints are message keys (resolved in SettingField); the field metadata
// itself (multiline/numeric/bool/options) stays language-neutral.
const FIELDS: Field[] = [
  { key: "app_name", labelKey: "settings.field.app_name.label" },
  {
    key: "public_url",
    labelKey: "settings.field.public_url.label",
    hintKey: "settings.field.public_url.hint",
  },
  {
    key: "app_logo_url",
    labelKey: "settings.field.app_logo_url.label",
    hintKey: "settings.field.app_logo_url.hint",
  },
  {
    key: "login_heading",
    labelKey: "settings.field.login_heading.label",
    hintKey: "settings.field.login_heading.hint",
  },
  { key: "login_subtext", labelKey: "settings.field.login_subtext.label" },
  { key: "login_bg_url", labelKey: "settings.field.login_bg_url.label" },
  {
    key: "embed_color",
    labelKey: "settings.field.embed_color.label",
    hintKey: "settings.field.embed_color.hint",
  },
  {
    key: "embed_site_name",
    labelKey: "settings.field.embed_site_name.label",
    hintKey: "settings.field.embed_site_name.hint",
  },
  {
    key: "embed_title",
    labelKey: "settings.field.embed_title.label",
    hintKey: "settings.field.embed_title.hint",
  },
  {
    key: "embed_description",
    labelKey: "settings.field.embed_description.label",
    multiline: true,
    hintKey: "settings.field.embed_description.hint",
  },
  {
    key: "embed_author_name",
    labelKey: "settings.field.embed_author_name.label",
    hintKey: "settings.field.embed_author_name.hint",
  },
  {
    key: "embed_author_url",
    labelKey: "settings.field.embed_author_url.label",
    hintKey: "settings.field.embed_author_url.hint",
  },
  {
    key: "hide_powered_by",
    labelKey: "settings.field.hide_powered_by.label",
    bool: true,
    hintKey: "settings.field.hide_powered_by.hint",
  },
  {
    key: "legal_notice",
    labelKey: "settings.field.legal_notice.label",
    multiline: true,
    hintKey: "settings.field.legal_notice.hint",
  },
  {
    key: "blocked_mime",
    labelKey: "settings.field.blocked_mime.label",
    multiline: true,
    hintKey: "settings.field.blocked_mime.hint",
  },
  {
    key: "max_upload_mib",
    labelKey: "settings.field.max_upload_mib.label",
    numeric: true,
    max: 1048576,
    hintKey: "settings.field.max_upload_mib.hint",
  },
  {
    key: "strip_exif",
    labelKey: "settings.field.strip_exif.label",
    bool: true,
    hintKey: "settings.field.strip_exif.hint",
  },
  {
    key: "encrypt_default",
    labelKey: "settings.field.encrypt_default.label",
    bool: true,
    hintKey: "settings.field.encrypt_default.hint",
  },
  {
    key: "slug_strategy",
    labelKey: "settings.field.slug_strategy.label",
    options: [
      { value: "random", labelKey: "settings.field.slug_strategy.random" },
      { value: "gfycat", labelKey: "settings.field.slug_strategy.gfycat" },
      { value: "date", labelKey: "settings.field.slug_strategy.date" },
    ],
    hintKey: "settings.field.slug_strategy.hint",
  },
  {
    key: "retention_days",
    labelKey: "settings.field.retention_days.label",
    numeric: true,
    max: 36500,
    hintKey: "settings.field.retention_days.hint",
  },
  {
    key: "trash_retention_days",
    labelKey: "settings.field.trash_retention_days.label",
    numeric: true,
    max: 36500,
    hintKey: "settings.field.trash_retention_days.hint",
  },
  {
    key: "audit_retention_days",
    labelKey: "settings.field.audit_retention_days.label",
    numeric: true,
    max: 36500,
    hintKey: "settings.field.audit_retention_days.hint",
  },
];

// Variables that can be used in any embed text field; resolved per file by the
// server when a link is unfurled. Keep in sync with newEmbedReplacer in
// backend/internal/api/embed.go ({originalname} is an alias of {filename}).
// The token stays literal; only its description is translated (descKey).
const VARIABLES: { token: string; descKey: MessageKey }[] = [
  { token: "{filename}", descKey: "settings.var.filename" },
  { token: "{slug}", descKey: "settings.var.slug" },
  { token: "{ext}", descKey: "settings.var.ext" },
  { token: "{mimetype}", descKey: "settings.var.mimetype" },
  { token: "{size}", descKey: "settings.var.size" },
  { token: "{size.bytes}", descKey: "settings.var.sizeBytes" },
  { token: "{width}", descKey: "settings.var.width" },
  { token: "{height}", descKey: "settings.var.height" },
  { token: "{duration}", descKey: "settings.var.duration" },
  { token: "{uploader}", descKey: "settings.var.uploader" },
  { token: "{date}", descKey: "settings.var.date" },
  { token: "{date.relative}", descKey: "settings.var.dateRelative" },
  { token: "{time}", descKey: "settings.var.time" },
  { token: "{views}", descKey: "settings.var.views" },
  { token: "{random.emoji}", descKey: "settings.var.randomEmoji" },
  { token: "{random.word}", descKey: "settings.var.randomWord" },
];

// Best-effort client-side preview of the placeholder expansion using example
// values; the server resolves the real values per file.
function previewSample(s: string): string {
  return s
    .replaceAll("{filename}", "screenshot.png")
    .replaceAll("{originalname}", "screenshot.png")
    .replaceAll("{slug}", "aB3xZ9")
    .replaceAll("{ext}", "png")
    .replaceAll("{mimetype}", "image/png")
    .replaceAll("{size}", "1.2 MiB")
    .replaceAll("{size.bytes}", "1258291")
    .replaceAll("{width}", "1920")
    .replaceAll("{height}", "1080")
    .replaceAll("{duration}", "0:42")
    .replaceAll("{uploader}", "admin")
    .replaceAll("{date}", "2026-06-17")
    .replaceAll("{date.relative}", "2m ago")
    .replaceAll("{time}", "14:03")
    .replaceAll("{views}", "42")
    .replaceAll("{random.emoji}", "✨")
    .replaceAll("{random.word}", "shiny");
}

// rawTags shows the OG/Twitter meta tags the server would emit (sample image),
// a lightweight "embed linter" so you can see exactly what crawlers parse.
function rawTags(form: Settings): string {
  const color = /^#[0-9a-fA-F]{3,8}$/.test(form.embed_color)
    ? form.embed_color
    : tGlobal("settings.colorRandom");
  const title = previewSample(form.embed_title) || "screenshot.png";
  const lines = [
    `<title>${title}</title>`,
    `<meta name="theme-color" content="${color}">`,
    `<meta property="og:title" content="${title}">`,
  ];
  if (form.embed_site_name)
    lines.push(`<meta property="og:site_name" content="${previewSample(form.embed_site_name)}">`);
  if (form.embed_description)
    lines.push(`<meta property="og:description" content="${previewSample(form.embed_description)}">`);
  lines.push(
    `<meta property="og:image" content="https://…/raw/abc123">`,
    `<meta name="twitter:card" content="summary_large_image">`,
  );
  if (form.embed_author_name) lines.push(`oEmbed author_name: ${previewSample(form.embed_author_name)}`);
  return lines.join("\n");
}

function EmbedPreview({ form }: { form: Settings }) {
  const t = useT();
  const color = /^#[0-9a-fA-F]{3,8}$/.test(form.embed_color) ? form.embed_color : "#5865F2";
  const title = previewSample(form.embed_title) || "screenshot.png";
  return (
    <div className="rounded-md bg-[#313338] p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-[#949ba4]">
        {t("settings.discordPreview")}
      </div>
      <div className="mt-2 flex">
        <div className="w-1 rounded-l" style={{ backgroundColor: color }} />
        <div className="flex-1 rounded-r bg-[#2b2d31] p-3">
          {previewSample(form.embed_author_name) && (
            <div className="text-xs font-medium text-[#dbdee1]">{previewSample(form.embed_author_name)}</div>
          )}
          {previewSample(form.embed_site_name) && (
            <div className="text-xs text-[#949ba4]">{previewSample(form.embed_site_name)}</div>
          )}
          <div className="text-sm font-semibold text-[#00a8fc]">{title}</div>
          {previewSample(form.embed_description) && (
            <div className="mt-1 whitespace-pre-line text-sm text-[#dbdee1]">
              {previewSample(form.embed_description)}
            </div>
          )}
          <div className="mt-2 flex h-24 items-center justify-center rounded bg-black/40 text-xs text-[#949ba4]">
            {t("settings.imageVideoPreview")}
          </div>
        </div>
      </div>
      <details className="mt-3 text-xs">
        <summary className="cursor-pointer text-[var(--color-muted)]">{t("settings.showRawTags")}</summary>
        <pre className="mt-2 overflow-x-auto whitespace-pre-wrap rounded bg-black/40 p-2">
          {rawTags(form)}
        </pre>
      </details>
    </div>
  );
}

// SettingField renders one labelled control. A per-field useId ties the visible
// <label>, the control, and the optional hint together (htmlFor + aria-describedby)
// so assistive tech announces them as a unit.
function SettingField({
  field,
  value,
  onChange,
}: {
  field: Field;
  value: string;
  onChange: (v: string) => void;
}) {
  const t = useT();
  const id = useId();
  const hintId = field.hintKey ? `${id}-hint` : undefined;
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-sm">
        {t(field.labelKey)}
      </label>
      {field.options ? (
        <Select
          id={id}
          aria-describedby={hintId}
          className="max-w-xs"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        >
          {field.options.map((o) => (
            <option key={o.value} value={o.value}>
              {t(o.labelKey)}
            </option>
          ))}
        </Select>
      ) : field.bool ? (
        <label className="inline-flex cursor-pointer items-center gap-2 text-sm">
          <input
            id={id}
            type="checkbox"
            aria-describedby={hintId}
            className="h-4 w-4 accent-[var(--color-accent)]"
            checked={value === "true"}
            onChange={(e) => onChange(e.target.checked ? "true" : "false")}
          />
          <span className="text-[var(--color-muted)]">
            {value === "true" ? t("common.on") : t("common.off")}
          </span>
        </label>
      ) : field.multiline ? (
        <textarea
          id={id}
          aria-describedby={hintId}
          rows={3}
          className="w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm outline-none focus:border-[var(--color-accent)]"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : field.numeric ? (
        <Input
          id={id}
          aria-describedby={hintId}
          type="number"
          min={0}
          max={field.max}
          step={1}
          className="max-w-40"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <Input id={id} aria-describedby={hintId} value={value} onChange={(e) => onChange(e.target.value)} />
      )}
      {field.hintKey && (
        <p id={hintId} className="mt-1 text-xs text-[var(--color-muted)]">
          {t(field.hintKey)}
        </p>
      )}
    </div>
  );
}

export function SettingsPage() {
  const t = useT();
  const { data } = useSettings();
  const update = useUpdateSettings();
  const [form, setForm] = useState<Settings | null>(null);

  useEffect(() => {
    if (data) setForm(data);
  }, [data]);

  if (!form) return <div className="p-6 text-[var(--color-muted)]">{t("common.loading")}</div>;

  const onSave = (e: React.FormEvent) => {
    e.preventDefault();
    update.mutate(form);
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <h1 className="text-lg font-semibold">{t("settings.title")}</h1>

      <Card className="p-4">
        <EmbedPreview form={form} />
        <div className="mt-3">
          <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-muted)]">
            {t("settings.variablesHeading")}
          </div>
          <div className="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
            {VARIABLES.map((v) => (
              <div key={v.token} className="flex items-baseline gap-2">
                <code className="rounded border border-[var(--color-border)] px-1">{v.token}</code>
                <span className="text-[var(--color-muted)]">{t(v.descKey)}</span>
              </div>
            ))}
          </div>
        </div>
      </Card>

      <Card className="p-4">
        <form onSubmit={onSave} className="space-y-4">
          {FIELDS.map((f) => (
            <SettingField
              key={f.key}
              field={f}
              value={form[f.key] ?? ""}
              onChange={(v) => setForm({ ...form, [f.key]: v })}
            />
          ))}
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? t("common.saving") : t("common.save")}
            </Button>
            {update.isSuccess && (
              <span className="text-sm text-[var(--color-success)]">{t("common.saved")}</span>
            )}
          </div>
        </form>
      </Card>
    </div>
  );
}
