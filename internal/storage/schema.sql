create table if not exists events(
 id text primary key,
 event_type text not null,
 payload text not null,
 created_at integer not null
);

create table if not exists delivery_jobs(
 delivery_id text primary key,
 event_id text not null references events(id),
 status text not null check (status in ('pending','processing','delivered','dead')),
 target_url text not null,
 attempts integer not null default 0,
 next_attempt_at integer,
 lease_owner text,
 lease_expires_at integer,
 last_error text,
 created_at integer not null,
 updated_at integer not null
);

create index if not exists idx_delivery_jobs_due on delivery_jobs(status, next_attempt_at);
create index if not exists idx_delivery_jobs_lease on delivery_jobs(status, lease_expires_at);
create index if not exists idx_delivery_jobs_event on delivery_jobs(event_id);

create table if not exists delivery_attempts(
 id text primary key,
 delivery_id text not null references delivery_jobs(delivery_id) on delete cascade,
 attempt_number integer not null,
 started_at integer not null,
 finished_at integer not null,
 duration_ms integer not null,
 status_code integer,
 success integer not null,
 error text,
 next_attempt_at integer
);

create index if not exists idx_delivery_attempts_delivery on delivery_attempts(delivery_id, attempt_number);
