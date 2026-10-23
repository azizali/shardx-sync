-- ShardX Sync schema for Neon (Data API + Managed Better Auth).
-- Run on the branch's neondb database (Neon Console > SQL Editor, or `neon psql -f`).
-- Safe to run again on an existing database: it adds what is missing and replaces the functions.
--
-- Everyone who can sign in is on one team. Sign-ups are disabled; accounts are created by an admin.
--   admin   sees and manages every profile, grants access, and is the only one who deletes for good.
--   member  sees only the profiles granted to them, directly or through a folder. Deleting hides a
--           profile until an admin restores it (a soft delete).
--
-- Everything uploaded belongs to the workspace. When a member loses access to a profile, their app asks
-- profile_verdicts() and deletes its local copy. Profiles never uploaded are never touched.
-- Offboarding: set_member_status(user, 'removed'), wait for wiped_at, then delete the account.
--
-- First run only: sign in to the app once (that records you), then in the SQL Editor run
--   select public.bootstrap_admin('you@example.com');

create table if not exists public.synced_profiles (
  id              uuid primary key,          -- ShardX profile id
  name            text not null,
  folder          text,
  version         integer not null default 0,
  parts           integer not null default 0,
  size_bytes      bigint,
  sha256          text,
  blob_prefix     text,                       -- storage folder of the current version
  updated_at      timestamptz,
  updated_by      uuid,
  updated_by_email text,
  updated_from    text,                       -- computer name
  updated_os      text,                       -- darwin | win32 | linux
  locked_by       uuid,
  locked_by_email text,
  locked_from     text,
  locked_at       timestamptz
);
alter table public.synced_profiles add column if not exists deleted_at timestamptz;
alter table public.synced_profiles add column if not exists deleted_by_email text;

create table if not exists public.team_settings (
  key   text primary key,
  value text not null
);

create table if not exists public.team_members (
  user_id  uuid primary key,
  email    text not null,
  role     text not null default 'member' check (role in ('admin', 'member')),
  added_at timestamptz not null default now()
);
alter table public.team_members add column if not exists status text not null default 'active';
alter table public.team_members add column if not exists wiped_at timestamptz;
do $$ begin
  alter table public.team_members add constraint team_members_status_check check (status in ('active', 'removed'));
exception when duplicate_object then null; end $$;

-- Profiles an admin deleted for good. Lets a member's app tell "deleted" apart from "never uploaded".
create table if not exists public.profile_tombstones (
  id         uuid primary key,
  deleted_at timestamptz not null default now()
);

create table if not exists public.profile_grants (
  profile_id uuid not null references public.synced_profiles(id) on delete cascade,
  user_id    uuid not null references public.team_members(user_id) on delete cascade,
  primary key (profile_id, user_id)
);

create table if not exists public.folder_grants (
  folder  text not null check (folder <> ''),
  user_id uuid not null references public.team_members(user_id) on delete cascade,
  primary key (folder, user_id)
);

alter table public.synced_profiles enable row level security;
alter table public.team_settings   enable row level security;
alter table public.team_members    enable row level security;
alter table public.profile_grants  enable row level security;
alter table public.folder_grants   enable row level security;
alter table public.profile_tombstones enable row level security;

-- Helpers. Security definer, so they can read the grant tables that members cannot read directly.
-- A removed member keeps their sign-in only so their app can wipe its copies. They get no access.
create or replace function public.is_active() returns boolean
language sql stable security definer set search_path = public as $$
  select exists (select 1 from team_members where user_id = auth.uid() and status = 'active');
$$;

create or replace function public.is_admin() returns boolean
language sql stable security definer set search_path = public as $$
  select exists (select 1 from team_members where user_id = auth.uid() and role = 'admin' and status = 'active');
$$;

create or replace function public.can_access(p_id uuid, p_folder text) returns boolean
language sql stable security definer set search_path = public as $$
  select public.is_admin()
      or (public.is_active() and (
        exists (select 1 from profile_grants where profile_id = p_id and user_id = auth.uid())
        or (coalesce(p_folder, '') <> '' and exists (select 1 from folder_grants where folder = p_folder and user_id = auth.uid()))));
$$;

create or replace function public.folder_granted(p_folder text) returns boolean
language sql stable security definer set search_path = public as $$
  select public.is_admin()
      or (public.is_active() and coalesce(p_folder, '') <> ''
        and exists (select 1 from folder_grants where folder = p_folder and user_id = auth.uid()));
$$;

-- Row access. Members never get a delete policy, so a direct DELETE through the Data API is refused.
drop policy if exists "team read profiles" on public.synced_profiles;
drop policy if exists "team delete profiles" on public.synced_profiles;
drop policy if exists "members read granted profiles" on public.synced_profiles;
create policy "members read granted profiles" on public.synced_profiles for select to authenticated
  using (public.can_access(id, folder) and (deleted_at is null or public.is_admin()));

drop policy if exists "team read settings" on public.team_settings;
create policy "team read settings" on public.team_settings for select to authenticated using (true);
drop policy if exists "team insert settings" on public.team_settings;
create policy "team insert settings" on public.team_settings for insert to authenticated with check (true);

drop policy if exists "admins read members" on public.team_members;
create policy "admins read members" on public.team_members for select to authenticated using (public.is_admin());
drop policy if exists "admins read profile grants" on public.profile_grants;
create policy "admins read profile grants" on public.profile_grants for select to authenticated using (public.is_admin());
drop policy if exists "admins read folder grants" on public.folder_grants;
create policy "admins read folder grants" on public.folder_grants for select to authenticated using (public.is_admin());

-- Records the signed-in user on the team list. A new user is a member; an existing role is kept.
-- Returns 'admin', 'member', or 'removed' for an offboarded member.
create or replace function public.register_me() returns text
language plpgsql security definer set search_path = public as $$
declare me uuid := auth.uid(); r text;
begin
  if me is null then raise exception 'not signed in'; end if;
  insert into team_members (user_id, email, role) values (me, coalesce(auth.jwt() ->> 'email', ''), 'member')
    on conflict (user_id) do update set email = excluded.email;
  select case when status = 'removed' then 'removed' else role end into r from team_members where user_id = me;
  return r;
end $$;

create or replace function public.my_role() returns text
language sql stable security definer set search_path = public as $$
  select coalesce((select case when status = 'removed' then 'removed' else role end from team_members where user_id = auth.uid()), 'member');
$$;

-- Run by hand in the SQL Editor (the owner role), never from the app.
create or replace function public.bootstrap_admin(p_email text) returns void
language sql security definer set search_path = public as $$
  update team_members set role = 'admin' where email = p_email;
$$;

-- Take the lock on a profile (so only one person uses it at a time).
create or replace function public.claim_lock(p_id uuid, p_machine text, p_force boolean default false)
returns public.synced_profiles
language plpgsql security definer set search_path = public as $$
declare r public.synced_profiles; me uuid := auth.uid(); my_email text := auth.jwt() ->> 'email';
begin
  if me is null then raise exception 'not signed in'; end if;
  select * into r from synced_profiles where id = p_id for update;
  if not found then raise exception 'profile is not in the cloud'; end if;
  if not public.can_access(r.id, r.folder) then raise exception 'NO_ACCESS: you do not have access to this profile'; end if;
  if r.deleted_at is not null then raise exception 'DELETED: this profile was deleted'; end if;
  if r.locked_by is not null and not (r.locked_by = me and r.locked_from = p_machine) and not p_force then
    raise exception 'LOCKED:% on %', r.locked_by_email, r.locked_from;
  end if;
  update synced_profiles set locked_by = me, locked_by_email = my_email, locked_from = p_machine, locked_at = now()
    where id = p_id returning * into r;
  return r;
end $$;

create or replace function public.release_lock(p_id uuid, p_machine text, p_force boolean default false)
returns public.synced_profiles
language plpgsql security definer set search_path = public as $$
declare r public.synced_profiles; me uuid := auth.uid();
begin
  if me is null then raise exception 'not signed in'; end if;
  select * into r from synced_profiles where id = p_id for update;
  if not found then raise exception 'profile is not in the cloud'; end if;
  if not public.can_access(r.id, r.folder) then raise exception 'NO_ACCESS: you do not have access to this profile'; end if;
  if r.locked_by is not null and not (r.locked_by = me and r.locked_from = p_machine) and not p_force then
    raise exception 'LOCKED:% on %', r.locked_by_email, r.locked_from;
  end if;
  update synced_profiles set locked_by = null, locked_by_email = null, locked_from = null, locked_at = null
    where id = p_id returning * into r;
  return r;
end $$;

-- Publish a new version after its files are uploaded. Fails if someone else
-- published in the meantime (p_expected is the version you started from).
create or replace function public.commit_version(
  p_id uuid, p_expected integer, p_name text, p_folder text, p_parts integer,
  p_size bigint, p_sha text, p_prefix text, p_machine text, p_os text, p_keep_lock boolean)
returns public.synced_profiles
language plpgsql security definer set search_path = public as $$
declare r public.synced_profiles; me uuid := auth.uid(); my_email text := auth.jwt() ->> 'email';
begin
  if me is null then raise exception 'not signed in'; end if;
  if exists (select 1 from profile_tombstones where id = p_id) then
    raise exception 'DELETED: this profile was deleted for good';
  end if;
  if not public.is_active() then raise exception 'NO_ACCESS: your access was removed'; end if;
  select * into r from synced_profiles where id = p_id for update;
  if not found then
    if p_expected <> 0 then raise exception 'CONFLICT:0'; end if;
    insert into synced_profiles (id, name, folder, version, parts, size_bytes, sha256, blob_prefix, updated_at, updated_by,
      updated_by_email, updated_from, updated_os, locked_by, locked_by_email, locked_from, locked_at)
    values (p_id, p_name, p_folder, 1, p_parts, p_size, p_sha, p_prefix, now(), me, my_email, p_machine, p_os,
      case when p_keep_lock then me end, case when p_keep_lock then my_email end,
      case when p_keep_lock then p_machine end, case when p_keep_lock then now() end)
    returning * into r;
    -- The uploader can always see what they uploaded.
    insert into profile_grants (profile_id, user_id) values (p_id, me) on conflict do nothing;
    return r;
  end if;
  if not public.can_access(r.id, r.folder) then raise exception 'NO_ACCESS: you do not have access to this profile'; end if;
  if r.deleted_at is not null then raise exception 'DELETED: this profile was deleted'; end if;
  if r.version <> p_expected then raise exception 'CONFLICT:%', r.version; end if;
  if r.locked_by is not null and not (r.locked_by = me and r.locked_from = p_machine) then
    raise exception 'LOCKED:% on %', r.locked_by_email, r.locked_from;
  end if;
  -- A member may only move a profile to a folder they are granted, so it does not disappear from their list.
  if coalesce(p_folder, '') <> coalesce(r.folder, '') and not public.folder_granted(p_folder)
     and not (coalesce(p_folder, '') = '' and exists (select 1 from profile_grants where profile_id = p_id and user_id = me)) then
    raise exception 'NO_ACCESS: you can only move a profile into a folder you have access to';
  end if;
  update synced_profiles set version = r.version + 1, name = p_name, folder = p_folder, parts = p_parts,
    size_bytes = p_size, sha256 = p_sha, blob_prefix = p_prefix, updated_at = now(), updated_by = me, updated_by_email = my_email,
    updated_from = p_machine, updated_os = p_os,
    locked_by = case when p_keep_lock then me end, locked_by_email = case when p_keep_lock then my_email end,
    locked_from = case when p_keep_lock then p_machine end, locked_at = case when p_keep_lock then now() end
  where id = p_id returning * into r;
  return r;
end $$;

-- Admins delete for good. Everyone else soft-deletes: the profile is hidden until an admin restores it.
create or replace function public.delete_profile(p_id uuid) returns void
language plpgsql security definer set search_path = public as $$
declare r public.synced_profiles; me uuid := auth.uid(); my_email text := auth.jwt() ->> 'email';
begin
  if me is null then raise exception 'not signed in'; end if;
  select * into r from synced_profiles where id = p_id for update;
  if not found then raise exception 'profile is not in the cloud'; end if;
  if not public.can_access(r.id, r.folder) then raise exception 'NO_ACCESS: you do not have access to this profile'; end if;
  if public.is_admin() then
    insert into profile_tombstones (id) values (p_id) on conflict do nothing;
    delete from synced_profiles where id = p_id;
  elsif r.deleted_at is null then
    update synced_profiles set deleted_at = now(), deleted_by_email = my_email,
      locked_by = null, locked_by_email = null, locked_from = null, locked_at = null
    where id = p_id;
  end if;
end $$;

create or replace function public.restore_profile(p_id uuid) returns void
language plpgsql security definer set search_path = public as $$
begin
  if not public.is_admin() then raise exception 'ADMIN_ONLY: only an admin can do this'; end if;
  update synced_profiles set deleted_at = null, deleted_by_email = null where id = p_id;
  if not found then raise exception 'profile is not in the cloud'; end if;
end $$;

-- Admin-only management. Each one replaces the list it is given.
create or replace function public.set_profile_access(p_id uuid, p_user_ids uuid[]) returns void
language plpgsql security definer set search_path = public as $$
begin
  if not public.is_admin() then raise exception 'ADMIN_ONLY: only an admin can do this'; end if;
  delete from profile_grants where profile_id = p_id and not (user_id = any (p_user_ids));
  insert into profile_grants (profile_id, user_id)
    select p_id, u from unnest(p_user_ids) as u where exists (select 1 from team_members where user_id = u)
    on conflict do nothing;
end $$;

create or replace function public.set_folder_access(p_folder text, p_user_ids uuid[]) returns void
language plpgsql security definer set search_path = public as $$
begin
  if not public.is_admin() then raise exception 'ADMIN_ONLY: only an admin can do this'; end if;
  if coalesce(p_folder, '') = '' then raise exception 'Unfiled profiles are not shared by folder.'; end if;
  delete from folder_grants where folder = p_folder and not (user_id = any (p_user_ids));
  insert into folder_grants (folder, user_id)
    select p_folder, u from unnest(p_user_ids) as u where exists (select 1 from team_members where user_id = u)
    on conflict do nothing;
end $$;

create or replace function public.set_member_role(p_user_id uuid, p_role text) returns void
language plpgsql security definer set search_path = public as $$
begin
  if not public.is_admin() then raise exception 'ADMIN_ONLY: only an admin can do this'; end if;
  if p_role not in ('admin', 'member') then raise exception 'Role must be admin or member.'; end if;
  if p_role <> 'admin' and not exists (select 1 from team_members where role = 'admin' and status = 'active' and user_id <> p_user_id) then
    raise exception 'There must be at least one admin.';
  end if;
  update team_members set role = p_role where user_id = p_user_id;
end $$;

-- Offboarding. 'removed' drops every grant; the member's app then wipes its copies and calls report_wipe().
create or replace function public.set_member_status(p_user_id uuid, p_status text) returns void
language plpgsql security definer set search_path = public as $$
begin
  if not public.is_admin() then raise exception 'ADMIN_ONLY: only an admin can do this'; end if;
  if p_status not in ('active', 'removed') then raise exception 'Status must be active or removed.'; end if;
  if p_status = 'removed' and not exists (
    select 1 from team_members where role = 'admin' and status = 'active' and user_id <> p_user_id) then
    raise exception 'There must be at least one admin.';
  end if;
  update team_members set status = p_status, wiped_at = null where user_id = p_user_id;
  if p_status = 'removed' then
    delete from profile_grants where user_id = p_user_id;
    delete from folder_grants where user_id = p_user_id;
  end if;
end $$;

create or replace function public.report_wipe() returns void
language sql security definer set search_path = public as $$
  update team_members set wiped_at = now() where user_id = auth.uid() and status = 'removed';
$$;

-- What should this computer do with each profile it holds? 'keep', 'remove' (access ended, deleted, or the
-- member was removed), or 'unknown' (never uploaded, so it belongs to whoever made it). Admins keep everything.
create or replace function public.profile_verdicts(p_ids uuid[]) returns table (id uuid, verdict text)
language sql stable security definer set search_path = public as $$
  select i.id,
    case
      when s.id is null and t.id is null then 'unknown'
      when public.is_admin() then 'keep'
      when t.id is not null or s.deleted_at is not null then 'remove'
      when public.can_access(s.id, s.folder) then 'keep'
      else 'remove'
    end
  from unnest(p_ids) as i(id)
  left join synced_profiles s on s.id = i.id
  left join profile_tombstones t on t.id = i.id;
$$;

-- Grants: the app talks to these tables through the functions above, not directly.
grant usage on schema public to authenticated;
grant select on public.synced_profiles to authenticated;
revoke insert, update, delete on public.synced_profiles from authenticated;
grant select, insert on public.team_settings to authenticated;
grant select on public.team_members, public.profile_grants, public.folder_grants to authenticated;
revoke all on public.synced_profiles, public.team_settings, public.team_members, public.profile_grants, public.folder_grants from anonymous;
revoke all on public.profile_tombstones from anonymous, authenticated;

revoke all on function public.claim_lock(uuid, text, boolean), public.release_lock(uuid, text, boolean),
  public.commit_version(uuid, integer, text, text, integer, bigint, text, text, text, text, boolean),
  public.delete_profile(uuid), public.restore_profile(uuid), public.register_me(), public.my_role(),
  public.set_profile_access(uuid, uuid[]), public.set_folder_access(text, uuid[]), public.set_member_role(uuid, text),
  public.set_member_status(uuid, text), public.report_wipe(), public.profile_verdicts(uuid[]),
  public.bootstrap_admin(text) from public, anonymous;
grant execute on function public.claim_lock(uuid, text, boolean), public.release_lock(uuid, text, boolean),
  public.commit_version(uuid, integer, text, text, integer, bigint, text, text, text, text, boolean),
  public.delete_profile(uuid), public.restore_profile(uuid), public.register_me(), public.my_role(),
  public.set_profile_access(uuid, uuid[]), public.set_folder_access(text, uuid[]), public.set_member_role(uuid, text),
  public.set_member_status(uuid, text), public.report_wipe(), public.profile_verdicts(uuid[])
  to authenticated;
revoke all on function public.bootstrap_admin(text) from authenticated;

-- Storage gate: may this user write (or clean up) files under a profile? A profile that does not exist
-- yet may be uploaded to. An admin may always clean up. Everyone else needs live access.
create or replace function public.can_upload(p_id uuid) returns boolean
language sql stable security definer set search_path = public as $$
  select (not exists (select 1 from synced_profiles where id = p_id)
          and not exists (select 1 from profile_tombstones where id = p_id) and public.is_active())
      or exists (select 1 from synced_profiles s where s.id = p_id and public.is_admin())
      or exists (select 1 from synced_profiles s where s.id = p_id and s.deleted_at is null and public.can_access(s.id, s.folder));
$$;
revoke all on function public.can_upload(uuid) from public, anonymous;
grant execute on function public.can_upload(uuid) to authenticated;
