create table if not exists events(
id text primary key,
event_type text not null,
payload text not null,
created_at text not null
);

create table if not exists delivery_jobs(
 delivery_id text primary key,
 event_id text not null references events(id),
 status text not null,
 target_url text not null,
 attempts integer not null default 0,
 next_attempt_at text
);
