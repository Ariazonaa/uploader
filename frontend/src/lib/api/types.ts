export interface User {
  id: string;
  username: string;
  email: string;
  is_admin: boolean;
  totp_enabled: boolean;
  created_at: string;
}

export interface TotpChallenge {
  totp_required: true;
  pending: string;
}

export interface PublicSettings {
  app_name: string;
  max_upload_bytes: number;
  registration_enabled: boolean;
  needs_setup: boolean;
  app_logo_url: string;
  login_heading: string;
  login_subtext: string;
  login_bg_url: string;
  legal_notice: string;
  push_enabled: boolean;
  vapid_public_key: string;
}

// UploadDefaults mirrors GET /api/v1/upload/defaults.
export interface UploadDefaults {
  encrypt_default: boolean;
  encryption_available: boolean;
}

export interface DayCount {
  day: string;
  count: number;
}

export interface InviteCode {
  code: string;
  used: boolean;
  expires_at: string | null;
  used_at: string | null;
  created_at: string;
}

export interface FileItem {
  id: string;
  slug: string;
  name: string;
  mime_type: string;
  size_bytes: number;
  width?: number;
  height?: number;
  duration_ms?: number;
  views: number;
  created_at: string;
  deleted_at?: string;
  url: string;
  raw: string;
  thumb?: string;
  encrypted?: boolean;
}

export interface ApiToken {
  id: string;
  name: string;
  prefix: string;
  last_used_at: string | null;
  created_at: string;
}

export interface Stats {
  file_count: number;
  total_bytes: number;
  total_views: number;
}

export interface SessionItem {
  id: string;
  ip: string;
  user_agent: string;
  current: boolean;
  created_at: string;
  expires_at: string;
}

export interface AuditEvent {
  id: string;
  created_at: string;
  user_id: string | null;
  actor_label: string;
  event: string;
  target: string;
  ip: string;
  user_agent: string;
  metadata: Record<string, unknown>;
}

export interface AuditPage {
  events: AuditEvent[];
  total: number;
  limit: number;
  offset: number;
}

export interface Settings {
  app_name: string;
  public_url: string;
  embed_site_name: string;
  embed_description: string;
  embed_color: string;
  embed_title: string;
  embed_author_name: string;
  embed_author_url: string;
  app_logo_url: string;
  login_heading: string;
  login_subtext: string;
  login_bg_url: string;
  hide_powered_by: string;
  legal_notice: string;
  blocked_mime: string;
  retention_days: string;
  max_upload_mib: string;
  strip_exif: string;
  trash_retention_days: string;
  encrypt_default: string;
  slug_strategy: string;
  audit_retention_days: string;
}
