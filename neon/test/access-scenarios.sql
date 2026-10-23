-- Access-rule scenarios for neon/setup.sql. Run against a plain Postgres (no Neon):
--   cat neon/test/auth-stub.sql neon/setup.sql neon/test/access-scenarios.sql | psql -v ON_ERROR_STOP=1 -q
-- Each step asserts one rule and prints ALL SCENARIOS PASSED at the end.
\set ON_ERROR_STOP on
-- Sign a user in: sets their JWT claims for this session.
create or replace function pg_temp.as_user(uid text, email text) returns void language sql as $$
  select set_config('request.jwt.claim.sub', uid, false), set_config('request.jwt.claims', json_build_object('sub', uid, 'email', email)::text, false);
$$;
-- Run a statement as a user through the API role (RLS and grants apply).
create or replace function pg_temp.as_api(q text) returns setof record language plpgsql as $$
begin return query execute q; end $$;
create or replace function pg_temp.expect_error(q text, must text) returns void language plpgsql as $$
begin
  execute 'set local role authenticated';
  execute q;
  execute 'reset role';
  raise exception 'FAIL: expected error containing "%" but the statement succeeded: %', must, q;
exception when others then
  execute 'reset role';
  if sqlerrm like '%FAIL:%' or sqlerrm not like '%' || must || '%' then raise exception 'FAIL: wanted "%", got "%"', must, sqlerrm; end if;
  raise notice 'ok (refused): %', sqlerrm;
end $$;
create or replace function pg_temp.expect_count(q text, n int, label text) returns void language plpgsql as $$
declare c int;
begin
  execute 'set local role authenticated';
  execute 'select count(*) from (' || q || ') s' into c;
  execute 'reset role';
  if c <> n then raise exception 'FAIL %: expected % rows, got %', label, n, c; end if;
  raise notice 'ok: % = %', label, c;
end $$;

-- Users: alice is the first admin, bob and carol are members.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000a', 'alice@team');
set role authenticated; select public.register_me(); reset role;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
set role authenticated; select public.register_me(); reset role;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000c', 'carol@team');
set role authenticated; select public.register_me(); reset role;
select public.bootstrap_admin('alice@team');
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
set role authenticated; select public.my_role(); reset role;
do $$ begin assert (select role from team_members where email='bob@team') = 'member'; end $$;

-- Alice (admin) uploads three profiles: Clients, Acme, unfiled.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000a', 'alice@team');
set role authenticated;
select public.commit_version('11111111-1111-1111-1111-111111111111', 0, 'Shop A', 'Clients', 1, 10, 'x', 'p1/', 'mac-a', 'darwin', false);
select public.commit_version('22222222-2222-2222-2222-222222222222', 0, 'Acme 1', 'Acme', 1, 10, 'x', 'p2/', 'mac-a', 'darwin', false);
select public.commit_version('33333333-3333-3333-3333-333333333333', 0, 'Loose', '', 1, 10, 'x', 'p3/', 'mac-a', 'darwin', false);
reset role;
select public.set_folder_access('Acme', array['00000000-0000-0000-0000-00000000000b'::uuid]);
select public.set_profile_access('33333333-3333-3333-3333-333333333333', array['00000000-0000-0000-0000-00000000000c'::uuid]);

-- Bob (member): sees only the Acme folder profile.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
select pg_temp.expect_count('select id from synced_profiles', 1, 'bob sees Acme only');
set role authenticated; select id from synced_profiles; reset role;
-- Carol (member): sees only the profile granted to her directly.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000c', 'carol@team');
select pg_temp.expect_count('select id from synced_profiles', 1, 'carol sees her one profile');
-- Members cannot read the grant tables or the member list.
select pg_temp.expect_count('select 1 from team_members', 0, 'carol cannot list members');
-- Members cannot delete rows directly.
select pg_temp.expect_error('delete from synced_profiles', 'permission denied');
-- Members cannot grant access.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
select pg_temp.expect_error($q$select public.set_profile_access('22222222-2222-2222-2222-222222222222', array[]::uuid[])$q$, 'ADMIN_ONLY');
-- Bob cannot touch Clients (no grant) and cannot move Acme into a folder he is not granted.
select pg_temp.expect_error($q$select public.claim_lock('11111111-1111-1111-1111-111111111111', 'pc', false)$q$, 'NO_ACCESS');
select pg_temp.expect_error($q$select public.commit_version('22222222-2222-2222-2222-222222222222', 1, 'Acme 1', 'Clients', 1, 10, 'x', 'p2b/', 'mac-b', 'darwin', false)$q$, 'NO_ACCESS');
-- Bob soft-deletes Acme 1: it disappears for him, and the row stays for admins.
select public.delete_profile('22222222-2222-2222-2222-222222222222');
select pg_temp.expect_count('select id from synced_profiles', 0, 'bob no longer sees the soft-deleted profile');
select pg_temp.expect_error($q$select public.commit_version('22222222-2222-2222-2222-222222222222', 1, 'Acme 1', 'Acme', 1, 10, 'x', 'p2c/', 'mac-b', 'darwin', false)$q$, 'DELETED');
select pg_temp.expect_error($q$select public.restore_profile('22222222-2222-2222-2222-222222222222')$q$, 'ADMIN_ONLY');
-- Alice (admin) sees it marked deleted, restores it, and bob sees it again.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000a', 'alice@team');
select pg_temp.expect_count('select id from synced_profiles where deleted_at is not null', 1, 'admin sees the soft-deleted profile');
set role authenticated; select public.restore_profile('22222222-2222-2222-2222-222222222222'); reset role;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
select pg_temp.expect_count('select id from synced_profiles', 1, 'bob sees Acme again after restore');
-- Alice cannot demote herself while she is the only admin.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000a', 'alice@team');
select pg_temp.expect_error($q$select public.set_member_role('00000000-0000-0000-0000-00000000000a', 'member')$q$, 'at least one admin');
-- Alice makes bob an admin; bob can then grant access and delete for good; alice is demoted.
set role authenticated; select public.set_member_role('00000000-0000-0000-0000-00000000000b', 'admin'); reset role;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
set role authenticated; select public.my_role(); select public.set_profile_access('33333333-3333-3333-3333-333333333333', array['00000000-0000-0000-0000-00000000000c'::uuid]); reset role;
select pg_temp.expect_count('select id from synced_profiles', 3, 'admin bob sees every profile');
set role authenticated; select public.delete_profile('22222222-2222-2222-2222-222222222222'); reset role;
select pg_temp.expect_count('select id from synced_profiles', 2, 'admin delete removed the row for good');
do $$ begin assert (select count(*) from profile_grants where profile_id='22222222-2222-2222-2222-222222222222') = 0, 'grants should cascade'; end $$;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000a', 'alice@team');
set role authenticated; select public.set_member_role('00000000-0000-0000-0000-00000000000a', 'member'); reset role;
do $$ begin assert (select role from team_members where email='alice@team') = 'member', 'alice should be a member now'; end $$;
select 'ALL SCENARIOS PASSED' as result;

-- Storage gate: members may upload only to profiles they can open; admins may clean up anything.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000c', 'carol@team');
set role authenticated;
do $$ begin assert public.can_upload('33333333-3333-3333-3333-333333333333') is true, 'carol: own profile'; end $$;
do $$ begin assert public.can_upload('11111111-1111-1111-1111-111111111111') is false, 'carol: profile she cannot see'; end $$;
do $$ begin assert public.can_upload('44444444-4444-4444-4444-444444444444') is true, 'carol: brand-new profile'; end $$;
reset role;
select 'STORAGE GATE OK' as result;

-- Removal verdicts. Now: bob is the only admin, carol has p3 directly, p2 was deleted for good by bob.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000c', 'carol@team');
set role authenticated;
do $$
declare v text;
begin
  select verdict into v from public.profile_verdicts(array['33333333-3333-3333-3333-333333333333']::uuid[]);
  assert v = 'keep', 'carol keeps her granted profile, got ' || v;
  select verdict into v from public.profile_verdicts(array['11111111-1111-1111-1111-111111111111']::uuid[]);
  assert v = 'remove', 'carol has no access to Clients, got ' || v;
  select verdict into v from public.profile_verdicts(array['22222222-2222-2222-2222-222222222222']::uuid[]);
  assert v = 'remove', 'deleted for good must be removed, got ' || v;
  select verdict into v from public.profile_verdicts(array['55555555-5555-5555-5555-555555555555']::uuid[]);
  assert v = 'unknown', 'never uploaded must be unknown, got ' || v;
end $$;
reset role;
-- A deleted-for-good profile cannot come back from a stale copy.
select pg_temp.expect_error($q$select public.commit_version('22222222-2222-2222-2222-222222222222', 0, 'Acme 1', 'Acme', 1, 10, 'x', 'p2z/', 'pc', 'darwin', false)$q$, 'DELETED');
-- The admin keeps everything they hold.
select pg_temp.as_user('00000000-0000-0000-0000-00000000000b', 'bob@team');
set role authenticated;
do $$ begin assert (select verdict from public.profile_verdicts(array['11111111-1111-1111-1111-111111111111']::uuid[])) = 'keep', 'admin keeps'; end $$;
reset role;
-- The last active admin cannot be removed.
select pg_temp.expect_error($q$select public.set_member_status('00000000-0000-0000-0000-00000000000b', 'removed')$q$, 'at least one admin');
-- Offboard carol: she loses every grant, gets 'remove' for her profile, and reports the wipe.
set role authenticated; select public.set_member_status('00000000-0000-0000-0000-00000000000c', 'removed'); reset role;
select pg_temp.as_user('00000000-0000-0000-0000-00000000000c', 'carol@team');
set role authenticated;
do $$ begin
  assert public.register_me() = 'removed', 'carol should be told she was removed';
  assert (select verdict from public.profile_verdicts(array['33333333-3333-3333-3333-333333333333']::uuid[])) = 'remove', 'removed member wipes';
  assert public.can_upload('66666666-6666-6666-6666-666666666666') is false, 'removed member cannot upload';
end $$;
select public.report_wipe();
reset role;
select pg_temp.expect_count('select 1 from synced_profiles', 0, 'removed member sees nothing');
do $$ begin assert (select wiped_at from team_members where email = 'carol@team') is not null, 'wipe recorded'; end $$;
select 'REMOVAL SCENARIOS PASSED' as result;
