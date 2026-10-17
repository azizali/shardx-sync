-- ShardX Sync: run this once in Supabase > SQL Editor > New query.
-- Everyone who can log in to this Supabase project is treated as one team.
-- Turn OFF public sign-ups (Authentication > Sign In / Providers) and invite teammates by email.

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

create table if not exists public.team_settings (
  key   text primary key,
  value text not null
);

alter table public.synced_profiles enable row level security;
alter table public.team_settings  enable row level security;

drop policy if exists "team read profiles" on public.synced_profiles;
create policy "team read profiles" on public.synced_profiles for select to authenticated using (true);
drop policy if exists "team delete profiles" on public.synced_profiles;
create policy "team delete profiles" on public.synced_profiles for delete to authenticated using (true);
drop policy if exists "team read settings" on public.team_settings;
create policy "team read settings" on public.team_settings for select to authenticated using (true);
drop policy if exists "team insert settings" on public.team_settings;
create policy "team insert settings" on public.team_settings for insert to authenticated with check (true);
-- Inserts/updates of synced_profiles only happen through the functions below.

-- Take the lock on a profile (so only one person uses it at a time).
create or replace function public.claim_lock(p_id uuid, p_machine text, p_force boolean default false)
returns public.synced_profiles
language plpgsql security definer set search_path = public as $$
declare r public.synced_profiles; me uuid := auth.uid(); my_email text := auth.jwt() ->> 'email';
begin
  if me is null then raise exception 'not signed in'; end if;
  select * into r from synced_profiles where id = p_id for update;
  if not found then raise exception 'profile is not in the cloud'; end if;
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
  select * into r from synced_profiles where id = p_id for update;
  if not found then
    if p_expected <> 0 then raise exception 'CONFLICT:0'; end if;
    insert into synced_profiles (id, name, folder, version, parts, size_bytes, sha256, blob_prefix, updated_at, updated_by,
      updated_by_email, updated_from, updated_os, locked_by, locked_by_email, locked_from, locked_at)
    values (p_id, p_name, p_folder, 1, p_parts, p_size, p_sha, p_prefix, now(), me, my_email, p_machine, p_os,
      case when p_keep_lock then me end, case when p_keep_lock then my_email end,
      case when p_keep_lock then p_machine end, case when p_keep_lock then now() end)
    returning * into r;
    return r;
  end if;
  if r.version <> p_expected then raise exception 'CONFLICT:%', r.version; end if;
  if r.locked_by is not null and not (r.locked_by = me and r.locked_from = p_machine) then
    raise exception 'LOCKED:% on %', r.locked_by_email, r.locked_from;
  end if;
  update synced_profiles set version = r.version + 1, name = p_name, folder = p_folder, parts = p_parts,
    size_bytes = p_size, sha256 = p_sha, blob_prefix = p_prefix, updated_at = now(), updated_by = me, updated_by_email = my_email,
    updated_from = p_machine, updated_os = p_os,
    locked_by = case when p_keep_lock then me end, locked_by_email = case when p_keep_lock then my_email end,
    locked_from = case when p_keep_lock then p_machine end, locked_at = case when p_keep_lock then now() end
  where id = p_id returning * into r;
  return r;
end $$;

revoke all on function public.claim_lock(uuid, text, boolean), public.release_lock(uuid, text, boolean),
  public.commit_version(uuid, integer, text, text, integer, bigint, text, text, text, text, boolean) from public, anon;
grant execute on function public.claim_lock(uuid, text, boolean), public.release_lock(uuid, text, boolean),
  public.commit_version(uuid, integer, text, text, integer, bigint, text, text, text, text, boolean) to authenticated;

-- Private storage bucket for the encrypted profile archives.
insert into storage.buckets (id, name, public) values ('profiles', 'profiles', false)
  on conflict (id) do nothing;

drop policy if exists "team read profile files" on storage.objects;
create policy "team read profile files" on storage.objects for select to authenticated using (bucket_id = 'profiles');
drop policy if exists "team write profile files" on storage.objects;
create policy "team write profile files" on storage.objects for insert to authenticated with check (bucket_id = 'profiles');
drop policy if exists "team update profile files" on storage.objects;
create policy "team update profile files" on storage.objects for update to authenticated using (bucket_id = 'profiles');
drop policy if exists "team delete profile files" on storage.objects;
create policy "team delete profile files" on storage.objects for delete to authenticated using (bucket_id = 'profiles');
